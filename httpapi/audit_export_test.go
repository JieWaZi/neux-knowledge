package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JieWaZi/neux-knowledge/core"
	"github.com/JieWaZi/neux-knowledge/httpapi"
)

func TestAuditAndMarkdownExportAreReadOnly(t *testing.T) {
	ctx := context.Background()
	e, err := core.Open(ctx, core.Options{DBPath: filepath.Join(t.TempDir(), "knowledge.db"), RawDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	l, err := e.CreateLibrary(ctx, core.CreateLibraryInput{RequestID: "audit-lib", Name: "Audit", Kind: "general", WikiLanguage: "zh-CN"})
	if err != nil {
		t.Fatal(err)
	}
	lib, err := e.GetLibrary(ctx, l.ResourceID)
	if err != nil {
		t.Fatal(err)
	}
	p, err := e.WritePage(ctx, core.WritePageInput{RequestID: "audit-page", LibraryID: lib.ID, StructureVersion: lib.StructureVersion, Page: core.Page{Slug: "guide/export", Title: "Export", PageType: "guide", Status: "published", Content: strings.Repeat("正文", 30)}})
	if err != nil {
		t.Fatal(err)
	}
	lib, _ = e.GetLibrary(ctx, l.ResourceID)
	policy, _ := httpapi.NewPolicyStore(httpapi.SecurityConfig{Tokens: map[string]httpapi.Principal{"reader": {Subject: "reader", Libraries: map[string]httpapi.LibraryGrant{lib.ID: {Operations: []string{"read"}, PolicyVersion: lib.PolicyVersion}}}}})
	h := httpapi.New(e, httpapi.Options{Security: policy})
	before, err := e.GetLibrary(ctx, lib.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/libraries/" + lib.ID + "/audit", "/libraries/" + lib.ID + "/markdown-export?pageId=" + p.ResourceID} {
		r := httptest.NewRequest(http.MethodGet, httpapi.BasePath+path, nil)
		r.Header.Set("Authorization", "Bearer reader")
		r.RemoteAddr = "192.0.2.1:1234"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "raw") || strings.Contains(w.Body.String(), "excerpt") {
			t.Fatalf("sensitive source content leaked: %s", w.Body.String())
		}
		if strings.Contains(path, "markdown-export") {
			var out core.MarkdownExport
			if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
			if len(out.Files) != 1 || out.Files[0].Metadata.PageID != p.ResourceID {
				t.Fatalf("unexpected export: %+v", out)
			}
		}
	}
	after, err := e.GetLibrary(ctx, lib.ID)
	if err != nil {
		t.Fatal(err)
	}
	if before.Version != after.Version || before.StructureVersion != after.StructureVersion {
		t.Fatal("read-only endpoint changed library versions")
	}
}
