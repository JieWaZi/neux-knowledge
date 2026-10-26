package store

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	types "github.com/JieWaZi/neux-knowledge/internal/domain"
	"gorm.io/gorm"
)

func openActivityStore(t *testing.T) (*Store, string, string) {
	t.Helper()
	dir := t.TempDir()
	dbPath, rawPath := filepath.Join(dir, "knowledge.db"), filepath.Join(dir, "raw")
	s, err := Open(context.Background(), dbPath, rawPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, dbPath, rawPath
}

func TestAuditFailureRollsBackLibraryAndReceipt(t *testing.T) {
	s, _, _ := openActivityStore(t)
	if err := s.db.Exec("CREATE TRIGGER reject_activity BEFORE INSERT ON knowledge_activity_logs BEGIN SELECT RAISE(ABORT, 'activity test rejected'); END").Error; err != nil {
		t.Fatal(err)
	}
	_, err := s.CreateLibrary(context.Background(), types.CreateLibraryInput{RequestID: "atomic-activity", Name: "必须回滚", Kind: "general", WikiLanguage: "zh-CN"})
	if err == nil {
		t.Fatal("audit failure must not leave unaudited committed mutation")
	}
	for _, model := range []any{&types.Library{}, &types.Receipt{}, &types.AuditLog{}} {
		var count int64
		if err := s.db.Model(model).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("activity failure committed partial state: %T count=%d", model, count)
		}
	}
}

func TestActivitySanitizerOnlyKeepsBoundedNonSecretMetadata(t *testing.T) {
	s, _, _ := openActivityStore(t)
	err := s.transaction(context.Background(), func(tx *gorm.DB) error {
		return recordKBActivity(tx, "sanitized", "test.audit", "kb", types.ActivityActor{Role: "admin"}, types.AuditActionKnowledgeReparseStarted, "task", "task", types.AuditOutcomeAccepted, map[string]any{"task_id": "task", "count": 2, "title": strings.Repeat("界", 300), "token": "secret-token", "content": "secret-body", "config": map[string]any{"apiKey": "secret-key"}})
	})
	if err != nil {
		t.Fatal(err)
	}
	var row types.AuditLog
	if err = s.db.First(&row).Error; err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(row)
	if strings.Contains(string(raw), "secret-") || row.ActorUserID != "" || row.ActorRole != "" {
		t.Fatalf("secret or invented role persisted: %s", raw)
	}
	var details map[string]any
	if err = json.Unmarshal(row.Details, &details); err != nil {
		t.Fatal(err)
	}
	if len([]rune(details["title"].(string))) != 200 || details["processing_status"] != "pending" || details["trigger"] != "system" {
		t.Fatalf("metadata bound/accepted state lost: %#v", details)
	}
}

func TestAuditRepositoryCapsLimitAndUsesStableDescendingIDs(t *testing.T) {
	s, _, _ := openActivityStore(t)
	ctx := context.Background()
	rec, err := s.CreateLibrary(ctx, types.CreateLibraryInput{RequestID: "activity-many-create", Name: "有界活动", Kind: "general", WikiLanguage: "zh-CN"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 105; i++ {
		row := types.AuditLog{RequestID: strings.Repeat("x", i+1), Operation: "test", Action: types.AuditActionKBUpdated, Outcome: types.AuditOutcomeSuccess, ActorUserID: "filter-user", ScopeType: auditScopeKnowledgeBase, ScopeID: rec.ResourceID}
		if err = s.db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	rows, err := s.ListLibraryActivity(ctx, rec.ResourceID, &types.ActivityQuery{Limit: 1000, ActorUserID: "filter-user"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 100 {
		t.Fatalf("unbounded result: %d", len(rows))
	}
	for i := 1; i < len(rows); i++ {
		if rows[i-1].ID <= rows[i].ID {
			t.Fatal("unstable ID cursor order")
		}
	}
	rest, err := s.ListLibraryActivity(ctx, rec.ResourceID, &types.ActivityQuery{AfterID: rows[len(rows)-1].ID, ActorUserID: "filter-user"})
	if err != nil || len(rest) != 5 {
		t.Fatalf("cursor skipped or repeated: %d %v", len(rest), err)
	}
}

func TestMaintenanceActivityReadsLegacyIdentityWithoutChangingStoredHistory(t *testing.T) {
	s, _, _ := openActivityStore(t)
	ctx := context.Background()
	receipt, err := s.CreateLibrary(ctx, types.CreateLibraryInput{RequestID: "legacy-activity-library", Name: "历史维护活动", Kind: "general", WikiLanguage: "zh-CN"})
	if err != nil {
		t.Fatal(err)
	}
	job := types.Job{ID: "legacy-maintenance", RequestID: "legacy-job", LibraryID: receipt.ResourceID, State: "failed", Attempt: 1, Version: 1, Permission: types.PermissionSnapshot{Operations: []string{"maintain"}}}
	if err := s.db.Create(&job).Error; err != nil {
		t.Fatal(err)
	}
	legacy := types.AuditLog{RequestID: "legacy-event", Operation: "job.fail", Action: types.AuditActionWikiContentChanged, ScopeType: auditScopeKnowledgeBase, ScopeID: receipt.ResourceID, TargetType: "knowledge_task", TargetID: job.ID, Outcome: types.AuditOutcomeFailed, Details: types.JSON(`{"task_id":"legacy-maintenance","total":0,"attempt":1}`)}
	if err := s.db.Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListLibraryActivity(ctx, receipt.ResourceID, &types.ActivityQuery{Action: types.AuditAction("wiki.maintenance"), Outcome: types.AuditOutcomeFailed})
	if err != nil || len(rows) != 1 || rows[0].ID != legacy.ID || rows[0].TargetType != "wiki_maintenance_task" || rows[0].Action != "wiki.maintenance" {
		t.Fatalf("legacy maintenance lost from semantic filter: %+v %v", rows, err)
	}
	var details map[string]any
	if err := json.Unmarshal(rows[0].Details, &details); err != nil {
		t.Fatal(err)
	}
	if details["scope"] != "existing_wiki" || details["total"] != nil || details["changed_pages"] != nil {
		t.Fatalf("legacy activity invented a page count: %s", rows[0].Details)
	}
	changedRows, err := s.ListLibraryActivity(ctx, receipt.ResourceID, &types.ActivityQuery{Action: types.AuditActionWikiContentChanged})
	if err != nil || len(changedRows) != 0 {
		t.Fatalf("maintenance still classified as a content mutation: %+v %v", changedRows, err)
	}
	var stored types.AuditLog
	if err := s.db.Where("id = ?", legacy.ID).Take(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if string(stored.Details) != string(legacy.Details) || stored.Action != legacy.Action || stored.TargetType != legacy.TargetType {
		t.Fatalf("read rewrote immutable activity: %+v", stored)
	}
}

func TestMaintenanceActivityIdentityProjectionBatchesCurrentPage(t *testing.T) {
	s, _, _ := openActivityStore(t)
	ctx := context.Background()
	library, err := s.CreateLibrary(ctx, types.CreateLibraryInput{RequestID: "batch-activity-library", Name: "批量活动", Kind: "general", WikiLanguage: "zh-CN"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		id := fmt.Sprintf("maintenance-%03d", i)
		job := types.Job{ID: id, RequestID: id, LibraryID: library.ResourceID, State: "failed", Attempt: 1, Version: 1, Permission: types.PermissionSnapshot{Operations: []string{"maintain"}}}
		if err := s.db.Create(&job).Error; err != nil {
			t.Fatal(err)
		}
		row := types.AuditLog{RequestID: id, Operation: "job.fail", Action: types.AuditActionWikiContentChanged, ScopeType: auditScopeKnowledgeBase, ScopeID: library.ResourceID, TargetType: "knowledge_task", TargetID: id, Outcome: types.AuditOutcomeFailed, Details: types.JSON(`{"total":0}`)}
		if err := s.db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	jobQueries := 0
	if err := s.db.Callback().Query().Before("gorm:query").Register("maintenance_identity_queries", func(tx *gorm.DB) {
		if tx.Statement.Table == "knowledge_jobs" && !tx.DryRun {
			jobQueries++
			if strings.Join(tx.Statement.Selects, ",") != "id,source_version_ids,permission" {
				t.Errorf("identity projection loads unrelated job state: %v", tx.Statement.Selects)
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer s.db.Callback().Query().Remove("maintenance_identity_queries")
	for _, limit := range []int{1, 100} {
		jobQueries = 0
		rows, err := s.ListLibraryActivity(ctx, library.ResourceID, &types.ActivityQuery{Limit: limit, Action: types.AuditActionWikiMaintenance})
		if err != nil || len(rows) != limit || jobQueries != 1 {
			t.Fatalf("limit=%d rows=%d identity queries=%d err=%v", limit, len(rows), jobQueries, err)
		}
	}
	var plan []struct {
		Detail string // SQLite 返回的实际查询步骤。
	}
	err = s.db.Raw("EXPLAIN QUERY PLAN SELECT id FROM knowledge_activity_logs WHERE scope_type = ? AND scope_id = ? AND (action = ? OR (target_type = ? AND target_id IN (SELECT id FROM knowledge_jobs WHERE library_id = ? AND COALESCE(json_array_length(source_version_ids),0) = 0 AND json_array_length(json_extract(permission, '$.operations')) = 1 AND json_extract(permission, '$.operations[0]') = ?))) ORDER BY id DESC LIMIT 100", auditScopeKnowledgeBase, library.ResourceID, types.AuditActionWikiMaintenance, "knowledge_task", library.ResourceID, "maintain").Scan(&plan).Error
	if err != nil {
		t.Fatal(err)
	}
	details := []string{}
	for _, step := range plan {
		details = append(details, step.Detail)
	}
	joined := strings.Join(details, " | ")
	if !strings.Contains(joined, "idx_activity_scope_desc") || !strings.Contains(joined, "idx_knowledge_jobs_library_id") {
		t.Fatalf("activity and job candidate windows lack owning-library indexes: %s", joined)
	}
	t.Logf("1/100 activities each use one bounded job identity query; filter plan: %s", joined)
}
