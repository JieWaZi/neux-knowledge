package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/JieWaZi/neux-knowledge/core"
	"github.com/JieWaZi/neux-knowledge/httpapi"
)

func TestWikiMaintenanceDoesNotExposeIssueManagement(t *testing.T) {
	e, l, s := fixture(t)
	h := httpapi.New(e, httpapi.Options{Security: s})
	base := "/libraries/" + l.ID
	for _, route := range []struct {
		method string // Deprecated request method.
		path   string // Deprecated in-library question endpoint.
	}{
		{http.MethodGet, "/issues"},
		{http.MethodGet, "/issues/issue"},
		{http.MethodPost, "/issues"},
		{http.MethodPost, "/issues/issue/update"},
	} {
		w := request(t, h, route.method, base+route.path, "secret", "192.0.2.1:1", "", nil)
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s %s exposed: %d %s", route.method, route.path, w.Code, w.Body)
		}
	}
	w := request(t, h, http.MethodGet, base+"/capabilities", "secret", "192.0.2.1:1", "", nil)
	var caps httpapi.Capabilities
	if err := json.Unmarshal(w.Body.Bytes(), &caps); err != nil {
		t.Fatal(err)
	}
	for _, operation := range []string{"issuesRead", "issuesWrite"} {
		if _, ok := caps.Operations[operation]; ok {
			t.Fatalf("obsolete operation %s", operation)
		}
		if _, ok := caps.Details[operation]; ok {
			t.Fatalf("obsolete operation details %s", operation)
		}
	}
}

func TestWikiMaintenanceHTTP(t *testing.T) {
	e, l, s := fixture(t)
	h := httpapi.New(e, httpapi.Options{Security: s})
	base := "/libraries/" + l.ID
	in := core.WritePageInput{RequestID: "agent-http", LibraryID: l.ID, StructureVersion: l.StructureVersion, Page: core.Page{Slug: "concept/a", Title: "A", PageType: "concept", Content: "target"}}
	w := request(t, h, "POST", base+"/pages/agent-write", "secret", "192.0.2.1:1", "", in)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	var r core.Receipt
	if err := json.Unmarshal(w.Body.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	p, err := e.GetPage(context.Background(), l.ID, r.ResourceID)
	if err != nil || p.LastEditSource != "agent" {
		t.Fatalf("%+v %v", p, err)
	}
	w = request(t, h, "POST", base+"/pages/"+p.ID+"/replace-text", "secret", "192.0.2.1:1", "", core.ReplacePageTextInput{RequestID: "http-replace", LibraryID: l.ID, PageID: "foreign", ExpectedVersion: p.Version, StructureVersion: l.StructureVersion, OldText: "target", NewText: "new"})
	if w.Code != 403 {
		t.Fatal(w.Code, w.Body)
	}
}
