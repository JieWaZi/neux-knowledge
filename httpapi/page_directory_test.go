package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JieWaZi/neux-knowledge/core"
	"github.com/JieWaZi/neux-knowledge/httpapi"
	"github.com/JieWaZi/neux-knowledge/remote"
)

func TestPageDirectoryHTTPAndSDKKeepBodySeparate(t *testing.T) {
	e, l, security := fixture(t)
	body := strings.Repeat("正文", 1000)
	receipt, err := e.WritePage(t.Context(), core.WritePageInput{RequestID: "light-page", LibraryID: l.ID, StructureVersion: l.StructureVersion, Page: core.Page{Slug: "concept/light", Title: "轻量页面", Summary: "导航简介", PageType: "concept", Content: body}})
	if err != nil {
		t.Fatal(err)
	}
	handler := httpapi.New(e, httpapi.Options{Security: security})
	path := "/libraries/" + l.ID + "/page-directory"
	w := request(t, handler, "GET", path+"?limit=1000", "secret", "192.0.2.1:1", "", nil)
	if w.Code != 200 || strings.Contains(w.Body.String(), `"content"`) || strings.Contains(w.Body.String(), body) {
		t.Fatal(w.Code, w.Body.String())
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	c, err := remote.NewCustom(remote.Options{BaseURL: server.URL, Token: "secret"}, remote.ResourceRef{Backend: "custom", ConnectionID: "c", KnowledgeBaseID: l.ID})
	if err != nil {
		t.Fatal(err)
	}
	result, err := c.ListPageDirectory(context.Background(), 0, 1000)
	if err != nil || result.Total != 1 || len(result.Items) != 1 || result.Items[0].ID != receipt.ResourceID || result.Items[0].Summary != "导航简介" {
		t.Fatal(result, err)
	}
	detail, err := c.GetPage(t.Context(), receipt.ResourceID)
	if err != nil || detail.Content != body {
		t.Fatal("detail body lost", err)
	}
	for _, query := range []string{"?limit=1001", "?offset=-1", "?limit=oops"} {
		w = request(t, handler, "GET", path+query, "secret", "192.0.2.1:1", "", nil)
		if w.Code != 400 {
			t.Fatalf("%s accepted: %d", query, w.Code)
		}
	}
	w = request(t, handler, "GET", path, "", "192.0.2.1:1", "", nil)
	if w.Code != 401 {
		t.Fatal("unauthenticated", w.Code)
	}
	w = request(t, handler, "GET", "/libraries/foreign/page-directory", "secret", "192.0.2.1:1", "", nil)
	if w.Code != 403 {
		t.Fatal("foreign scope", w.Code)
	}
	// 窄结果在序列化阶段也不包含大字段。
	raw, _ := json.Marshal(result.Items)
	if strings.Contains(string(raw), `"content"`) || strings.Contains(string(raw), `"evidence"`) {
		t.Fatal(string(raw))
	}
}
