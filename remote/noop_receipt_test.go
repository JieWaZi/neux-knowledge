package remote_test

import (
	"context"
	"encoding/json"
	"github.com/JieWaZi/neux-knowledge/core"
	"github.com/JieWaZi/neux-knowledge/remote"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestActualNoOpReceiptAndReconciliation(t *testing.T) {
	ctx := context.Background()
	e, l, _, _ := customFixture(t)
	rec, err := e.AcceptJob(ctx, core.AcceptJobInput{RequestID: "nothing-pending", LibraryID: l.ID, ExpectedVersion: l.Version, StructureVersion: l.StructureVersion, Permission: core.PermissionSnapshot{PolicyVersion: l.PolicyVersion, Operations: []string{"organize"}}, RulesVersion: "review", ConfigDigest: "review"})
	if err != nil {
		t.Fatal(err)
	}
	if rec.State != "no-op" {
		t.Fatal(rec)
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { json.NewEncoder(w).Encode(rec) }))
	defer s.Close()
	c, err := remote.NewCustom(remote.Options{BaseURL: s.URL, Token: "secret"}, remote.ResourceRef{Backend: "custom", ConnectionID: "review", KnowledgeBaseID: l.ID})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Organize(ctx, remote.OrganizeRequest{RequestID: rec.RequestID, LibraryID: l.ID, ProfileID: "p"})
	if err != nil {
		t.Errorf("genuine no-op receipt misclassified: %v", err)
	}
	_, err = c.GetReceipt(ctx, rec.RequestID)
	if err != nil {
		t.Errorf("genuine no-op reconciliation failed: %v", err)
	}
}
