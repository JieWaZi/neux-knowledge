package store

import (
	"context"
	"fmt"
	"strings"
	"testing"

	types "github.com/JieWaZi/neux-knowledge/internal/domain"
	sqlite3 "github.com/ncruces/go-sqlite3"
	sqlitedriver "github.com/ncruces/go-sqlite3/driver"
)

// TestSourceWorkMembershipIsAtomicWithAdmission 任何工作关系写入失败都须与接纳、重试及回执整体回滚。
func TestSourceWorkMembershipIsAtomicWithAdmission(t *testing.T) {
	ctx := context.Background()
	t.Run("accept", func(t *testing.T) {
		s, _, _, library := auditStore(t)
		raw := purgeStoreSource(t, s, library)
		input := types.AcceptJobInput{RequestID: "atomic-accept", LibraryID: library.ID, ExpectedVersion: library.Version, StructureVersion: library.StructureVersion, SourceVersionIDs: []string{raw.ID}, Permission: types.PermissionSnapshot{PolicyVersion: library.PolicyVersion, Operations: []string{"organize"}}, RulesVersion: "rules", ConfigDigest: "config"}
		if err := s.db.Exec("CREATE TRIGGER reject_work BEFORE INSERT ON knowledge_organize_source_work BEGIN SELECT RAISE(ABORT, 'controlled membership failure'); END").Error; err != nil {
			t.Fatal(err)
		}
		if _, err := s.AcceptJob(ctx, input); err == nil {
			t.Fatal("accepted a job without committing its source membership")
		}
		var count int64
		if err := s.db.Model(&types.Job{}).Where("library_id = ?", library.ID).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("failed membership left a job: count=%d err=%v", count, err)
		}
		if _, err := s.GetReceipt(ctx, input.RequestID); err == nil {
			t.Fatal("failed membership left an accepted receipt")
		}
		if err := s.db.Exec("DROP TRIGGER reject_work").Error; err != nil {
			t.Fatal(err)
		}
		rec, err := s.AcceptJob(ctx, input)
		if err != nil {
			t.Fatal(err)
		}
		work, err := s.ListOrganizeSourceWork(ctx, rec.JobID)
		if err != nil || len(work) != 1 || work[0].SourceVersionID != raw.ID || work[0].State != "pending" || work[0].Version != 1 || work[0].JobAttempt != 1 {
			t.Fatalf("queued job lacks durable membership before Run: work=%+v err=%v", work, err)
		}
	})
	t.Run("retry", func(t *testing.T) {
		s, _, source, job := sourceWorkFixture(t)
		if _, err := s.DeferOrganizeSourceWork(ctx, types.SourceWorkActionInput{RequestID: "prior-failure", JobID: job.ID, JobAttempt: 1, SourceVersionID: source.ID, ExpectedVersion: 1, Diagnostic: "旧尝试诊断", NotBefore: 1800000005000}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.FailJob(ctx, types.FailJobInput{JobActionInput: types.JobActionInput{RequestID: "fail-before-retry", JobID: job.ID, ExpectedVersion: job.Version}, Diagnostic: "失败", Sources: []types.SourceConclusion{{SourceVersionID: source.ID, State: "failed", Diagnostic: "来源原诊断"}}}); err != nil {
			t.Fatal(err)
		}
		job, err := s.GetJob(ctx, job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.db.Exec("CREATE TRIGGER reject_work BEFORE INSERT ON knowledge_organize_source_work WHEN NEW.job_attempt = 2 BEGIN SELECT RAISE(ABORT, 'controlled membership failure'); END").Error; err != nil {
			t.Fatal(err)
		}
		input := types.JobActionInput{RequestID: "atomic-retry", JobID: job.ID, ExpectedVersion: job.Version}
		if _, err := s.RetryJob(ctx, input); err == nil {
			t.Fatal("retried a job without committing the new attempt membership")
		}
		stored, err := s.GetJob(ctx, job.ID)
		if err != nil || stored.Attempt != 1 || stored.State != "failed" || stored.Version != job.Version {
			t.Fatalf("failed membership changed the job attempt: job=%+v err=%v", stored, err)
		}
		if _, err := s.GetReceipt(ctx, input.RequestID); err == nil {
			t.Fatal("failed retry membership left a receipt")
		}
		if err := s.db.Exec("DROP TRIGGER reject_work").Error; err != nil {
			t.Fatal(err)
		}
		if _, err := s.RetryJob(ctx, input); err != nil {
			t.Fatal(err)
		}
		work, err := s.ListOrganizeSourceWork(ctx, job.ID)
		if err != nil || len(work) != 1 || work[0].JobAttempt != 2 || work[0].State != "pending" || work[0].FailureCount != 0 || work[0].NotBefore != 0 || work[0].StartedAt != 0 || work[0].Version != 1 {
			t.Fatalf("retry membership not fresh before Run: work=%+v err=%v", work, err)
		}
		var prior types.OrganizeSourceWork
		if err := s.db.Where("job_id = ? AND job_attempt = 1", job.ID).Take(&prior).Error; err != nil || prior.FailureCount != 1 || prior.Diagnostic != "旧尝试诊断" || prior.NotBefore != 1800000005000 {
			t.Fatalf("retry discarded earlier audit: prior=%+v err=%v", prior, err)
		}
	})
}

// sourceQueryVMSteps 读取 SQLite 实际虚拟机步数，子查询内的 JSON 与任务遍历同样计入预算。
func sourceQueryVMSteps(t *testing.T, s *Store, query string, values []string) int {
	t.Helper()
	db, err := s.db.DB()
	if err != nil {
		t.Fatal(err)
	}
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	steps := 0
	if err := conn.Raw(func(driverConn any) error {
		stmt, _, err := driverConn.(sqlitedriver.Conn).Raw().Prepare(query)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for index, value := range values {
			if err := stmt.BindText(index+1, value); err != nil {
				return err
			}
		}
		for stmt.Step() {
		}
		steps = stmt.Status(sqlite3.STMTSTATUS_VM_STEP, false)
		return stmt.Err()
	}); err != nil {
		t.Fatal(err)
	}
	return steps
}

// TestSourceActivityDatabaseWorkDoesNotScaleWithUnrelatedMembership 覆盖未占用的整页，禁止把全库 JSON 扫描移入每个来源的子查询。
func TestSourceActivityDatabaseWorkDoesNotScaleWithUnrelatedMembership(t *testing.T) {
	ctx := context.Background()
	s, _, _, library := auditStore(t)
	targets := make([]types.SourceVersion, 100)
	values := []string{library.ID}
	for i := range targets {
		targets[i] = types.SourceVersion{ID: fmt.Sprintf("target-%03d", i), LibraryID: library.ID, SourceID: fmt.Sprintf("target-source-%03d", i), Version: 1, State: "pending", Filename: fmt.Sprintf("unreserved-%03d.md", i)}
		values = append(values, targets[i].ID)
	}
	if err := s.db.CreateInBatches(targets, 50).Error; err != nil {
		t.Fatal(err)
	}
	where := " FROM knowledge_source_versions WHERE library_id = ? AND id IN (" + strings.TrimSuffix(strings.Repeat("?,", len(targets)), ",") + ")"
	queries := []string{
		"SELECT id, " + sourceActivityStateSQL() + ", " + sourceOccupiedSQL() + where,
		"SELECT count(*)" + where + " AND CASE WHEN state IN ('processed','no-change') THEN state ELSE COALESCE(NULLIF(" + sourceActivityStateSQL() + ", ''), state) END = 'pending'",
	}
	before := []int{}
	for _, query := range queries {
		before = append(before, sourceQueryVMSteps(t, s, query, values))
	}
	var unrelated []types.SourceVersion
	var heads []types.SourceHead
	var ids []string
	for i := 0; i < 1000; i++ {
		id, sourceID := fmt.Sprintf("unrelated-%04d", i), fmt.Sprintf("unrelated-source-%04d", i)
		unrelated = append(unrelated, types.SourceVersion{ID: id, LibraryID: library.ID, SourceID: sourceID, Version: 1, State: "pending", Filename: "无关来源.md"})
		heads = append(heads, types.SourceHead{ID: sourceID, LibraryID: library.ID, VersionID: id, Version: 1})
		ids = append(ids, id)
	}
	if err := s.db.CreateInBatches(unrelated, 50).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.CreateInBatches(heads, 50).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcceptJob(ctx, types.AcceptJobInput{RequestID: "large-unrelated-queue", LibraryID: library.ID, ExpectedVersion: library.Version, StructureVersion: library.StructureVersion, SourceVersionIDs: ids, Permission: types.PermissionSnapshot{PolicyVersion: library.PolicyVersion, Operations: []string{"organize"}}, RulesVersion: "rules", ConfigDigest: "config"}); err != nil {
		t.Fatal(err)
	}
	for index, query := range queries {
		after := sourceQueryVMSteps(t, s, query, values)
		t.Logf("query=%d targets=100 unrelated-membership=1000 before=%d after=%d", index, before[index], after)
		// 允许来源主查询计划在额外1000行上的必要差异，禁止100个来源重复展开1000条冻结成员。
		if after > before[index]+15000 {
			t.Errorf("query work grew with unrelated frozen membership: query=%d before=%d after=%d", index, before[index], after)
		}
	}
	items, total, err := s.ListSourcesQuery(ctx, library.ID, types.SourceQuery{Query: "unreserved-", State: "pending", Limit: 100})
	if err != nil || len(items) != 100 || total != 100 {
		t.Fatalf("pending page/count changed: total=%d items=%d err=%v", total, len(items), err)
	}
	for _, item := range items {
		if item.State != "pending" || item.OrganizeBlocked {
			t.Fatalf("unreserved source changed: %+v", item)
		}
	}
	queued, count, err := s.ListSourcesQuery(ctx, library.ID, types.SourceQuery{State: "queued", Offset: 999, Limit: 1})
	if err != nil || count != 1000 || len(queued) != 1 || queued[0].State != "queued" || !queued[0].OrganizeBlocked {
		t.Fatalf("state filtering capped the admitted membership: count=%d items=%+v err=%v", count, queued, err)
	}
}

// TestEnsureSourceWorkDoesNotBackfillMissingAdmission 缺失接纳关系应明确失败，运行时不能另建历史补齐路径。
func TestEnsureSourceWorkDoesNotBackfillMissingAdmission(t *testing.T) {
	s, _, _, job := sourceWorkFixture(t)
	if err := s.db.Where("job_id = ?", job.ID).Delete(&types.OrganizeSourceWork{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureOrganizeSourceWork(context.Background(), job.ID); err == nil {
		t.Fatal("missing admission membership was silently restored")
	}
	var count int64
	if err := s.db.Model(&types.OrganizeSourceWork{}).Where("job_id = ?", job.ID).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("runtime validation wrote membership: count=%d err=%v", count, err)
	}
}
