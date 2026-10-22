// Keep library-scope isolation assertions with a real temporary SQLite host, excluding tenant and global activity interfaces absent locally.
package store

import (
	"context"
	"testing"

	types "github.com/JieWaZi/neux-knowledge/internal/domain"
)

func TestAuditLogRepositoryListFiltersKnowledgeBaseScope(t *testing.T) {
	s, _, _ := openActivityStore(t)
	ctx := context.Background()
	a, err := s.CreateLibrary(ctx, types.CreateLibraryInput{RequestID: "scope-a", Name: "作用域甲", Kind: "general", WikiLanguage: "zh-CN"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateLibrary(ctx, types.CreateLibraryInput{RequestID: "scope-b", Name: "作用域乙", Kind: "general", WikiLanguage: "zh-CN"})
	if err != nil {
		t.Fatal(err)
	}
	rows := []*types.AuditLog{
		{RequestID: "scope-unscoped", Operation: "test", ActorUserID: "scope-user", Action: types.AuditActionKBUpdated, Outcome: types.AuditOutcomeSuccess},
		{RequestID: "scope-in-a", Operation: "test", ActorUserID: "scope-user", Action: types.AuditActionKBUpdated, Outcome: types.AuditOutcomeSuccess, ScopeType: "knowledge_base", ScopeID: a.ResourceID},
		{RequestID: "scope-in-b", Operation: "test", ActorUserID: "scope-user", Action: types.AuditActionKnowledgeCreated, Outcome: types.AuditOutcomeSuccess, ScopeType: "knowledge_base", ScopeID: b.ResourceID},
	}
	for _, row := range rows {
		if err := s.db.Create(row).Error; err != nil {
			t.Fatalf("insert audit log: %v", err)
		}
	}
	got, err := s.ListLibraryActivity(ctx, a.ResourceID, &types.ActivityQuery{ActorUserID: "scope-user"})
	if err != nil {
		t.Fatalf("list audit logs: %v", err)
	}
	if len(got) != 1 || got[0].ScopeID != a.ResourceID {
		t.Fatalf("scope filter returned unexpected rows: %+v", got)
	}
}
