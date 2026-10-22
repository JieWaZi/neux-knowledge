package remote_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/JieWaZi/neux-knowledge/core"
	"github.com/JieWaZi/neux-knowledge/remote"
)

func TestCustomSourceQueryRoundTrip(t *testing.T) {
	ctx := context.Background()
	e, l, c, _ := customFixture(t)
	for _, name := range []string{"alpha.md", "target_%.md", "target-other.md"} {
		_, err := e.ImportSource(ctx, core.ImportSourceInput{RequestID: name, LibraryID: l.ID, Filename: name, Bytes: []byte(name)})
		if err != nil {
			t.Fatal(err)
		}
	}
	page, err := c.ListSources(ctx, 0, 1, core.SourceQuery{Query: "TARGET_%", State: "pending", Sort: "oldest"})
	if err != nil || page.Total != 1 || len(page.Items) != 1 || page.Items[0].Filename != "target_%.md" {
		t.Fatalf("query roundtrip %+v %v", page, err)
	}
	_, err = c.ListSources(ctx, 0, 1, core.SourceQuery{State: "invalid"})
	var invalid *remote.Error
	if !errors.As(err, &invalid) || invalid.Status != http.StatusBadRequest {
		t.Fatalf("invalid state response: %v", err)
	}
	_, err = c.ListSources(ctx, 0, 1, core.SourceQuery{Sort: "invalid"})
	if !errors.As(err, &invalid) || invalid.Status != http.StatusBadRequest {
		t.Fatalf("invalid sort response: %v", err)
	}
}

func TestOfficialSourceQueryMapsOnlyExactStates(t *testing.T) {
	requests := 0
	expectedState := "processing"
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		q := r.URL.Query()
		if q.Get("keyword") != "literal_%" || q.Get("parse_status") != expectedState || q.Get("sort_by") != "created_at" || q.Get("sort_order") != "asc" || q.Get("page") != "2" || q.Get("page_size") != "10" {
			t.Errorf("query=%s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"success":true,"data":[],"total":0,"page":2,"page_size":10}`))
	}))
	defer s.Close()
	c, err := remote.NewWeKnora(remote.Options{BaseURL: s.URL, Token: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"pending", "processing", "failed", "cancelled"} {
		expectedState = state
		_, err = c.ListSources(context.Background(), "kb", 2, 10, core.SourceQuery{Query: "literal_%", State: state, Sort: "oldest"})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, state := range []string{"queued", "processed", "unchanged"} {
		_, err = c.ListSources(context.Background(), "kb", 1, 10, core.SourceQuery{State: state})
		var remoteErr *remote.Error
		if !errors.As(err, &remoteErr) || remoteErr.Code != "unsupported_source_state" {
			t.Fatalf("state=%s err=%v", state, err)
		}
	}
	if requests != 4 {
		t.Fatalf("unsupported state reached server: %d", requests)
	}
}

func TestCustomSourceActivityConsistentAcrossListAndDetail(t *testing.T) {
	ctx := context.Background()
	e, l, c, _ := customFixture(t)
	receipt, err := e.ImportSource(ctx, core.ImportSourceInput{RequestID: "activity", LibraryID: l.ID, Filename: "activity.md", Bytes: []byte("activity")})
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.AcceptJob(ctx, core.AcceptJobInput{RequestID: "organize", LibraryID: l.ID, ExpectedVersion: l.Version, StructureVersion: l.StructureVersion, SourceVersionIDs: []string{receipt.ResourceID}, Permission: core.PermissionSnapshot{PolicyVersion: l.PolicyVersion, Operations: []string{"organize"}}, RulesVersion: "rules", ConfigDigest: "config"})
	if err != nil {
		t.Fatal(err)
	}
	list, err := c.ListSources(ctx, 0, 10, core.SourceQuery{State: "queued"})
	if err != nil || list.Total != 1 || len(list.Items) != 1 || list.Items[0].State != "queued" {
		t.Fatalf("queued list=%+v %v", list, err)
	}
	detail, err := c.GetSource(ctx, receipt.ResourceID)
	if err != nil || detail.State != "queued" || detail.RawKey != "" {
		t.Fatalf("detail=%+v %v", detail, err)
	}
}
