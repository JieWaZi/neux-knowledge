package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	types "github.com/JieWaZi/neux-knowledge/internal/domain"
	"gorm.io/gorm"
)

func sourceWorkFixture(t *testing.T, extraSources ...string) (*Store, *types.Library, *types.SourceVersion, *types.Job) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(ctx, filepath.Join(dir, "work.db"), filepath.Join(dir, "raw"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	l, err := s.CreateLibrary(ctx, types.CreateLibraryInput{RequestID: "work-library", Name: "重排", Kind: "general", WikiLanguage: "zh-CN"})
	if err != nil {
		t.Fatal(err)
	}
	library, _ := s.GetLibrary(ctx, l.ResourceID)
	raw, err := s.ImportSource(ctx, types.ImportSourceInput{RequestID: "work-source", LibraryID: library.ID, Filename: "work.md", Bytes: []byte("实际来源")})
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{raw.ResourceID}
	for _, name := range extraSources {
		additional, err := s.ImportSource(ctx, types.ImportSourceInput{RequestID: "work-source-" + name, LibraryID: library.ID, Filename: name + ".md", Bytes: []byte("来源" + name)})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, additional.ResourceID)
	}
	job, err := s.AcceptJob(ctx, types.AcceptJobInput{RequestID: "work-job", LibraryID: library.ID, ExpectedVersion: library.Version, StructureVersion: library.StructureVersion, SourceVersionIDs: ids, Permission: types.PermissionSnapshot{PolicyVersion: library.PolicyVersion, Operations: []string{"organize"}}, RulesVersion: "rules", ConfigDigest: "config"})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.EnsureOrganizeSourceWork(ctx, job.JobID); err != nil {
		t.Fatal(err)
	}
	version, err := s.GetSource(ctx, library.ID, raw.ResourceID)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := s.GetJob(ctx, job.JobID)
	if err != nil {
		t.Fatal(err)
	}
	return s, library, version, accepted
}

func TestSourcePageDoesNotMaterializeOtherQueuedJobs(t *testing.T) {
	ctx := context.Background()
	s, library, source, _ := sourceWorkFixture(t)
	for index := 0; index < 32; index++ {
		name := fmt.Sprintf("unused-%02d", index)
		raw, err := s.ImportSource(ctx, types.ImportSourceInput{RequestID: name + "-raw", LibraryID: library.ID, Filename: name + ".md", Bytes: []byte(name)})
		if err != nil {
			t.Fatal(err)
		}
		_, err = s.AcceptJob(ctx, types.AcceptJobInput{RequestID: name + "-job", LibraryID: library.ID, ExpectedVersion: library.Version, StructureVersion: library.StructureVersion, SourceVersionIDs: []string{raw.ResourceID}, Permission: types.PermissionSnapshot{PolicyVersion: library.PolicyVersion, Operations: []string{"organize"}}, RulesVersion: "rules", ConfigDigest: "config"})
		if err != nil {
			t.Fatal(err)
		}
	}
	var materialized, sourceRows int64
	if err := s.db.Callback().Query().After("gorm:query").Register("source-page-row-budget", func(tx *gorm.DB) {
		if tx.Statement.Schema == nil {
			return
		}
		switch tx.Statement.Schema.Table {
		case "knowledge_jobs", "knowledge_organize_source_work":
			materialized += tx.RowsAffected
		case "knowledge_source_versions":
			sourceRows += tx.RowsAffected
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer s.db.Callback().Query().Remove("source-page-row-budget")
	for _, state := range []string{"", "queued"} {
		materialized, sourceRows = 0, 0
		items, total, err := s.ListSourcesQuery(ctx, library.ID, types.SourceQuery{Query: "work.md", State: state, Limit: 1})
		if err != nil || total != 1 || len(items) != 1 || !items[0].OrganizeBlocked || items[0].State != "queued" {
			t.Fatalf("source page lost actual activity: items=%+v total=%d err=%v", items, total, err)
		}
		// 来源行最多包含 count 标量、当前页和当前页投影，不允许把其他版本带回 Go。
		if materialized > 2 || sourceRows > 3 {
			t.Fatalf("one-source page materialized unrelated queue state: jobs/work=%d sourceRows=%d", materialized, sourceRows)
		}
		t.Logf("state=%q jobs/work=%d sourceRows=%d", state, materialized, sourceRows)
	}
	materialized, sourceRows = 0, 0
	items, total, err := s.ListSourcesQuery(ctx, library.ID, types.SourceQuery{State: "queued", Offset: 31, Limit: 1})
	if err != nil || total != 33 || len(items) != 1 || items[0].State != "queued" || !items[0].OrganizeBlocked || materialized > 2 || sourceRows > 3 {
		t.Fatalf("state count or page scope changed: items=%+v total=%d jobs/work=%d sourceRows=%d err=%v", items, total, materialized, sourceRows, err)
	}
	t.Logf("filtered total=%d page=%d jobs/work=%d sourceRows=%d", total, len(items), materialized, sourceRows)
	materialized, sourceRows = 0, 0
	items, err = s.SourceActivity(ctx, library.ID, []*types.SourceVersion{source})
	if err != nil || len(items) != 1 || items[0].State != "queued" || !items[0].OrganizeBlocked || materialized > 2 || sourceRows > 1 {
		t.Fatalf("direct projection escaped source scope: items=%+v jobs/work=%d sourceRows=%d err=%v", items, materialized, sourceRows, err)
	}
	t.Logf("direct projection jobs/work=%d sourceRows=%d", materialized, sourceRows)

}

func TestSourceWorkRetryBudgetUsesPersistentCASAndReceipts(t *testing.T) {
	ctx := context.Background()
	queue, _, raw, job := sourceWorkFixture(t)
	for count := 1; count <= 6; count++ {
		in := types.SourceWorkActionInput{JobAttempt: 1, RequestID: fmt.Sprintf("work-defer-%d", count), JobID: job.ID, SourceVersionID: raw.ID, ExpectedVersion: int64(count), Diagnostic: "reduce failed", NotBefore: 1800000000000 + int64(count)*5000}
		receipt, err := queue.DeferOrganizeSourceWork(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		replay, err := queue.DeferOrganizeSourceWork(ctx, in)
		if err != nil || replay.Version != receipt.Version {
			t.Fatalf("receipt replay=%+v %v", replay, err)
		}
		items, err := queue.ListOrganizeSourceWork(ctx, job.ID)
		wantState := "pending"
		if count == 6 {
			wantState = "dead"
		}
		if err != nil || len(items) != 1 || items[0].State != wantState || items[0].FailureCount != count || items[0].Version != int64(count+1) || items[0].NotBefore != in.NotBefore {
			t.Fatalf("retry queue=%+v %v", items, err)
		}
	}
}

func TestDeletingLibraryRemovesSourceWorkDiagnostics(t *testing.T) {
	ctx := context.Background()
	s, library, source, job := sourceWorkFixture(t)
	if _, err := s.DeferOrganizeSourceWork(ctx, types.SourceWorkActionInput{JobAttempt: 1, RequestID: "work-private-diagnostic", JobID: job.ID, SourceVersionID: source.ID, ExpectedVersion: 1, Diagnostic: "PRIVATE_DOCUMENT_COPY", NotBefore: 1800000005000}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FailJob(ctx, types.FailJobInput{JobActionInput: types.JobActionInput{RequestID: "fail-work-for-delete", JobID: job.ID, ExpectedVersion: job.Version}, Diagnostic: "failed", Sources: []types.SourceConclusion{{SourceVersionID: source.ID, State: "failed", Diagnostic: "failed"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteLibrary(ctx, types.DeleteLibraryInput{RequestID: "delete-work-library", LibraryID: library.ID, ExpectedVersion: library.Version}); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := s.db.Model(&types.OrganizeSourceWork{}).Where("library_id = ?", library.ID).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("deleted library retained source retry diagnostics: %d %v", count, err)
	}
}

func TestPurgingSourceRemovesSourceWorkInternalCopies(t *testing.T) {
	ctx := context.Background()
	s, library, source, job := sourceWorkFixture(t)
	if _, err := s.DeferOrganizeSourceWork(ctx, types.SourceWorkActionInput{JobAttempt: 1, RequestID: "work-private-diagnostic", JobID: job.ID, SourceVersionID: source.ID, ExpectedVersion: 1, Diagnostic: "PRIVATE_DOCUMENT_COPY", NotBefore: 1800000005000}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FailJob(ctx, types.FailJobInput{JobActionInput: types.JobActionInput{RequestID: "fail-work-for-purge", JobID: job.ID, ExpectedVersion: job.Version}, Diagnostic: "failed", Sources: []types.SourceConclusion{{SourceVersionID: source.ID, State: "failed", Diagnostic: "failed"}}}); err != nil {
		t.Fatal(err)
	}
	preview, err := s.PreviewSourcePurge(ctx, types.PurgePreviewInput{LibraryID: library.ID, SourceID: source.SourceID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ConfirmSourcePurge(ctx, types.ConfirmSourcePurgeInput{RequestID: "purge-source-work", LibraryID: library.ID, SourceID: source.SourceID, PreviewDigest: preview.Digest, Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err = s.db.Model(&types.OrganizeSourceWork{}).Where("library_id = ?", library.ID).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("purged source retained internal retry copies: %d %v", count, err)
	}
}

func TestSourceWorkRejectsLatePreviousJobAttempt(t *testing.T) {
	ctx := context.Background()
	s, _, source, job := sourceWorkFixture(t)
	if _, err := s.FailJob(ctx, types.FailJobInput{JobActionInput: types.JobActionInput{RequestID: "old-attempt-failure", JobID: job.ID, ExpectedVersion: job.Version}, Diagnostic: "failed", Sources: []types.SourceConclusion{{SourceVersionID: source.ID, State: "failed", Diagnostic: "failed"}}}); err != nil {
		t.Fatal(err)
	}
	job, err := s.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.RetryJob(ctx, types.JobActionInput{RequestID: "new-attempt", JobID: job.ID, ExpectedVersion: job.Version}); err != nil {
		t.Fatal(err)
	}
	if err = s.EnsureOrganizeSourceWork(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	for _, attempt := range []int{0, 1} {
		in := types.SourceWorkActionInput{JobAttempt: attempt, RequestID: fmt.Sprintf("late-old-attempt-%d", attempt), JobID: job.ID, SourceVersionID: source.ID, ExpectedVersion: 1, Diagnostic: "late failure", NotBefore: 1800000005000}
		_, err = s.DeferOrganizeSourceWork(ctx, in)
		var domainErr *types.Error
		if !errors.As(err, &domainErr) || domainErr.Code != types.ErrStaleAttempt {
			t.Fatalf("old attempt changed new document queue: %v", err)
		}
	}
	items, err := s.ListOrganizeSourceWork(ctx, job.ID)
	if err != nil || len(items) != 1 || items[0].JobAttempt != 2 || items[0].FailureCount != 0 || items[0].Version != 1 {
		t.Fatalf("late attempt changed queue=%+v %v", items, err)
	}
}

// TestLiteContributionReceiptRollsBackWithPage 防止页面与贡献回执分开提交后丢失恢复边界。
func TestLiteContributionReceiptRollsBackWithPage(t *testing.T) {
	ctx := context.Background()
	s, library, source, job := sourceWorkFixture(t)
	const key = "applied/source-page"
	if err := s.db.Exec(`CREATE TRIGGER reject_contribution BEFORE INSERT ON knowledge_receipts WHEN NEW.request_id='applied/source-page' BEGIN SELECT RAISE(ABORT,'controlled receipt failure'); END`).Error; err != nil {
		t.Fatal(err)
	}
	publication := &types.Publication{Pages: []types.PageMutation{{Page: types.WikiPage{KnowledgeBaseID: library.ID, Slug: "concept/atomic", Title: "原子贡献", PageType: "concept", Status: "published", Content: "真实来源支撑的页面。", Evidence: types.PageEvidence{Sources: []types.SourceRef{{SourceID: source.SourceID, SourceVersionID: source.ID, SHA256: source.SHA256, ClaimKind: "document_context"}}}}}}}
	if _, err := s.PublishLite(ctx, "atomic-publication", job.ID, job.Attempt, publication, key); err == nil {
		t.Fatal("controlled transaction failure did not fail")
	}
	if _, err := s.GetPageBySlug(ctx, library.ID, "concept/atomic"); err == nil {
		t.Fatal("page survived failed contribution transaction")
	}
	if _, err := s.GetReceipt(ctx, key); err == nil {
		t.Fatal("receipt survived failed contribution transaction")
	}
	if err := s.db.Exec("DROP TRIGGER reject_contribution").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.PublishLite(ctx, "atomic-publication", job.ID, job.Attempt, publication, key); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetReceipt(ctx, key); err != nil {
		t.Fatal(err)
	}
	page, err := s.GetPageBySlug(ctx, library.ID, "concept/atomic")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PublishLite(ctx, "atomic-publication", job.ID, job.Attempt, publication, key); err != nil {
		t.Fatal(err)
	}
	replay, _ := s.GetPageBySlug(ctx, library.ID, "concept/atomic")
	if replay.Version != page.Version {
		t.Fatal("receipt replay changed page version")
	}
}

// TestTransactionAdmissionCanBeCancelled 排队中的事务取消不应等待写入者结束或产生数据修改。
func TestTransactionAdmissionCanBeCancelled(t *testing.T) {
	s, _, _, _ := sourceWorkFixture(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		finished <- s.transaction(context.Background(), func(_ *gorm.DB) error { close(entered); <-release; return nil })
	}()
	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := make(chan error, 1)
	go func() {
		result <- s.transaction(ctx, func(_ *gorm.DB) error { t.Error("cancelled transaction entered callback"); return nil })
	}()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("cancel result=%v", err)
		}
	case <-time.After(time.Second):
		t.Error("cancelled admission waited for writer")
	}
	close(release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
}

// TestSourceActivityKeepsPersistentConclusionsAndCrossVersionReservations 覆盖取消中的任务、归档、当前尝试与旧版本占用。
func TestSourceActivityKeepsPersistentConclusionsAndCrossVersionReservations(t *testing.T) {
	ctx := context.Background()
	s, library, old, job := sourceWorkFixture(t)
	rec, err := s.ImportSource(ctx, types.ImportSourceInput{RequestID: "new-version", LibraryID: library.ID, SourceID: old.SourceID, ExpectedVersion: old.Version, Filename: old.Filename, Bytes: []byte("更新后的真实来源")})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&types.Job{}).Where("id = ?", job.ID).Update("state", "cancel_requested").Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(&types.OrganizeSourceWork{ID: "old-attempt", JobID: job.ID, JobAttempt: job.Attempt - 1, SourceVersionID: old.ID, State: "dead", Diagnostic: "旧尝试诊断", StartedAt: 10}).Error; err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name       string // 当前持久事实场景。
		rawState   string // 来源持久结论。
		archived   bool   // 来源归档状态。
		workState  string // 当前尝试的工作状态。
		startedAt  int64  // 当前尝试是否已实际开始。
		wantState  string // 请求范围中的最终展示状态。
		diagnostic string // 应保留的当前诊断。
	}{
		{"waiting", "pending", false, "pending", 0, "queued", ""},
		{"started", "failed", false, "pending", 10, "processing", ""},
		{"cancel-retry", "cancelled", false, "pending", 0, "queued", ""},
		{"processed", "processed", false, "pending", 0, "processed", "来源原诊断"},
		{"no-change", "no-change", false, "pending", 0, "no-change", "来源原诊断"},
		{"done", "failed", false, "done", 10, "failed", "来源原诊断"},
		{"dead", "pending", false, "dead", 10, "failed", "当前尝试诊断"},
		{"archived", "failed", true, "dead", 10, "failed", "来源原诊断"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := s.db.Model(&types.SourceVersion{}).Where("id = ?", old.ID).Updates(map[string]any{"state": tc.rawState, "archived": tc.archived, "diagnostic": "来源原诊断"}).Error; err != nil {
				t.Fatal(err)
			}
			if err := s.db.Model(&types.OrganizeSourceWork{}).Where("job_id = ? AND job_attempt = ?", job.ID, job.Attempt).Updates(map[string]any{"state": tc.workState, "started_at": tc.startedAt, "diagnostic": "当前尝试诊断"}).Error; err != nil {
				t.Fatal(err)
			}
			prior, err := s.GetSource(ctx, library.ID, old.ID)
			if err != nil {
				t.Fatal(err)
			}
			latest, err := s.GetSource(ctx, library.ID, rec.ResourceID)
			if err != nil {
				t.Fatal(err)
			}
			items, err := s.SourceActivity(ctx, library.ID, []*types.SourceVersion{prior, nil, latest})
			if err != nil || len(items) != 2 || items[0].State != tc.wantState || items[0].Diagnostic != tc.diagnostic || !items[0].OrganizeBlocked || items[1].State != "pending" || !items[1].OrganizeBlocked || prior.State != tc.rawState {
				t.Fatalf("cross-version activity changed: items=%+v prior=%+v err=%v", items, prior, err)
			}
			page, total, err := s.ListSourcesQuery(ctx, library.ID, types.SourceQuery{State: tc.wantState, Limit: 1})
			want := int64(1)
			if tc.archived {
				want = 0
			}
			if err != nil || total != want || int64(len(page)) != want || (want > 0 && (page[0].ID != old.ID || page[0].State != tc.wantState || !page[0].OrganizeBlocked)) {
				t.Fatalf("filtered count lost persistent state: total=%d want=%d page=%+v err=%v", total, want, page, err)
			}
		})
	}
	if err := s.db.Model(&types.Job{}).Where("id = ?", job.ID).Update("state", "cancelled").Error; err != nil {
		t.Fatal(err)
	}
	latest, err := s.GetSource(ctx, library.ID, rec.ResourceID)
	if err != nil {
		t.Fatal(err)
	}
	items, err := s.SourceActivity(ctx, library.ID, []*types.SourceVersion{latest})
	if err != nil || len(items) != 1 || items[0].OrganizeBlocked || items[0].State != "pending" {
		t.Fatalf("terminal job retained reservation: items=%+v err=%v", items, err)
	}
}

// TestSourceActivityQueryPlanUsesPersistentIndexes 核对当前尝试、来源身份及库范围沿现有索引查询，不增加连接或第二事实源。
func TestSourceActivityQueryPlanUsesPersistentIndexes(t *testing.T) {
	s, library, source, _ := sourceWorkFixture(t)
	queries := []string{
		"SELECT id, " + sourceActivityStateSQL() + ", " + sourceOccupiedSQL() + " FROM knowledge_source_versions WHERE library_id = ? AND id = ?",
		"SELECT count(*) FROM knowledge_source_versions WHERE library_id = ? AND archived = false AND CASE WHEN state IN ('processed','no-change') THEN state ELSE COALESCE(NULLIF(" + sourceActivityStateSQL() + ", ''), state) END = ?",
	}
	for index, query := range queries {
		args := []any{library.ID, source.ID}
		if index == 1 {
			args[1] = "queued"
		}
		var plan []struct {
			ID     int    // 查询步骤标识。
			Parent int    // 上级步骤标识。
			Unused int    // SQLite 保留列。
			Detail string // SQLite 实际查询计划。
		}
		if err := s.db.Raw("EXPLAIN QUERY PLAN "+query, args...).Scan(&plan).Error; err != nil {
			t.Fatal(err)
		}
		details := []string{}
		for _, step := range plan {
			details = append(details, step.Detail)
		}
		text := strings.Join(details, " | ")
		if strings.Contains(text, "json_each") || strings.Contains(text, "idx_knowledge_jobs_library_id") || strings.Contains(text, "SCAN source_work") {
			t.Fatalf("source lookup scanned the library job membership: %s", text)
		}
		if !strings.Contains(text, "idx_source_work_version_job") || !strings.Contains(text, "sqlite_autoindex_knowledge_jobs_1") || (index == 0 && !strings.Contains(text, "idx_source_identity_version")) {
			t.Fatalf("persistent lookup index missing: %s", text)
		}
		t.Logf("query=%d plan=%s", index, text)
	}
}
