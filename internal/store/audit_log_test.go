package store

import (
	"context"
	"encoding/json"
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
