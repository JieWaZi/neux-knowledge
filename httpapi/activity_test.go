package httpapi_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/JieWaZi/neux-knowledge/core"
	"github.com/JieWaZi/neux-knowledge/httpapi"
)

func TestLibraryActivityReturnsUpstreamCursorAndFilters(t *testing.T) {
	e, l, s := fixture(t)
	h := httpapi.New(e, httpapi.Options{Security: s})
	w := request(t, h, "GET", "/libraries/"+l.ID+"/activity?after_id=bad&limit=bad&action=kb.created&outcome=success", "secret", "192.0.2.1:1", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("activity route failed: %d %s", w.Code, w.Body)
	}
	var body struct {
		// Success indicates a successful query of actual persisted events.
		Success bool `json:"success"`
		// Data holds the actual activities on this page.
		Data []*core.AuditLog `json:"data"`
		// NextCursor is the last activity ID on this page.
		NextCursor uint64 `json:"next_cursor"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Success || len(body.Data) != 1 || body.Data[0].ScopeID != l.ID || body.NextCursor != body.Data[0].ID {
		t.Fatalf("cursor envelope changed: %s", w.Body)
	}
	w = request(t, h, "GET", "/libraries/"+l.ID+"/activity?actor=unknown", "secret", "192.0.2.1:1", "", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"next_cursor":0`) || !strings.Contains(w.Body.String(), `"data":[]`) {
		t.Fatalf("empty cursor incorrect: %d %s", w.Code, w.Body)
	}
}

func TestReadOnlySharedGrantCannotInspectActivity(t *testing.T) {
	e, l, s := fixture(t)
	err := s.Replace(httpapi.SecurityConfig{Tokens: map[string]httpapi.Principal{"reader": {Subject: "reader", Libraries: map[string]httpapi.LibraryGrant{l.ID: {Operations: []string{"read"}, PolicyVersion: l.PolicyVersion}}}}})
	if err != nil {
		t.Fatal(err)
	}
	h := httpapi.New(e, httpapi.Options{Security: s})
	w := request(t, h, "GET", "/libraries/"+l.ID+"/activity", "reader", "192.0.2.1:1", "", nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("read-only principal inspected configuration history: %d %s", w.Code, w.Body)
	}
}
