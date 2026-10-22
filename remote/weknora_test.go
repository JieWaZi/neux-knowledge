package remote

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWeKnoraProfilesAndPagination(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != "test-key" {
			t.Error("missing scoped authentication")
		}
		switch r.URL.Path {
		case "/api/v1/knowledge-bases":
			w.Write([]byte(`{"success":true,"data":[{"id":"kb","name":"研究","type":"wiki"}]}`))
		case "/api/v1/knowledgebase/kb/wiki/pages":
			if r.URL.Query().Get("page") != "2" || r.URL.Query().Get("page_size") != "20" {
				t.Error("page contract lost")
			}
			w.Write([]byte(`{"pages":[{"id":"p","knowledge_base_id":"kb","slug":"概念/身份","title":"身份","version":3}],"total":21,"page":2,"page_size":20,"total_pages":2}`))
		case "/api/v1/knowledgebase/kb/wiki/pages/概念/身份":
			w.Write([]byte(`{"id":"p","knowledge_base_id":"kb","slug":"概念/身份","title":"身份","summary":"页面说明","content":"正文","version":3}`))
		case "/api/v1/knowledgebase/kb/wiki/revisions/概念/身份":
			w.Write([]byte(`{"revisions":[{"id":"r","page_id":"p","version":2,"summary":"旧说明","content":"旧正文"}],"total":1,"current_version":3}`))
		case "/api/v1/knowledgebase/kb/wiki/folders":
			w.Write([]byte(`{"parent_id":"","folders":[{"id":"f","knowledge_base_id":"kb","name":"概念","has_children":false,"page_count":1}]}`))
		default:
			t.Errorf("unexpected route %s", r.URL)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, err := NewWeKnora(Options{BaseURL: server.URL, Token: "test-key", AuthMode: "api_key"})
	if err != nil {
		t.Fatal(err)
	}
	libraries, err := client.ListLibraries(context.Background())
	if err != nil || len(libraries) != 1 {
		t.Fatalf("libraries %v %v", libraries, err)
	}
	pages, err := client.ListPages(context.Background(), "kb", PageOptions{Page: 2, PageSize: 20})
	if err != nil || pages.Total != 21 || pages.Pages[0].Version != 3 {
		t.Fatalf("pages %v %v", pages, err)
	}
	page, err := client.GetPage(context.Background(), "kb", "概念/身份")
	if err != nil || page.Content != "正文" || page.Summary != "页面说明" {
		t.Fatalf("page %v %v", page, err)
	}
	revisions, err := client.ListRevisions(context.Background(), "kb", "概念/身份", RevisionOptions{ContentVersion: 2})
	if err != nil || revisions.CurrentVersion != 3 || revisions.Revisions[0].Content != "旧正文" || revisions.Revisions[0].Summary != "旧说明" {
		t.Fatalf("revisions %v %v", revisions, err)
	}
	folders, err := client.ListFolders(context.Background(), "kb", "")
	if err != nil || len(folders.Folders) != 1 {
		t.Fatalf("folders %v %v", folders, err)
	}
	for _, op := range []string{"raw_upload", "organize", "archive", "atomic_restore", "versioned_edit", "software_rules"} {
		if client.Capabilities()[op].Supported {
			t.Errorf("unsupported claim %s", op)
		}
	}
}

func TestWeKnoraUnknownShapeIsNotEmpty(t *testing.T) {
	for _, body := range []string{`{}`, `{"data":[]}`, `{"pages":null}`, `{"pages":[],"total":0}`, `{"pages":[{}],"total":1,"page":1,"page_size":20,"total_pages":1}`, `{"pages":[],"total":0,"page":1,"page_size":20,"total_pages":0} trailing`} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
			defer server.Close()
			client, _ := NewWeKnora(Options{BaseURL: server.URL, Token: "key"})
			_, err := client.ListPages(context.Background(), "kb", PageOptions{})
			var remoteErr *Error
			if !errors.As(err, &remoteErr) || remoteErr.Code != "protocol_incompatible" {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestWeKnoraHTTPFailureAndRedirectDoNotLeakOrFallback(t *testing.T) {
	called := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer server.Close()
	client, _ := NewWeKnora(Options{BaseURL: server.URL, Token: "SECRET"})
	_, err := client.ListLibraries(context.Background())
	if err == nil || called {
		t.Fatalf("redirect followed %v %v", called, err)
	}
	for _, status := range []int{401, 403, 404, 409, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status); w.Write([]byte("SECRET")) }))
			defer s.Close()
			c, _ := NewWeKnora(Options{BaseURL: s.URL, Token: "SECRET"})
			_, err := c.ListLibraries(context.Background())
			var e *Error
			if !errors.As(err, &e) || e.Status != status {
				t.Fatalf("lost HTTP status %v", err)
			}
			if err.Error() == "SECRET" {
				t.Fatal("raw error leak")
			}
		})
	}
}

func TestWeKnoraBinaryAndPathSafety(t *testing.T) {
	raw := []byte{0, 1, 2, 255}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/knowledge/source/download" {
			t.Fatal(r.URL.Path)
		}
		w.Header().Set("Content-Disposition", `attachment; filename="raw.pdf"`)
		w.Write(raw)
	}))
	defer server.Close()
	client, _ := NewWeKnora(Options{BaseURL: server.URL, Token: "key"})
	file, err := client.DownloadSource(context.Background(), "source")
	if err != nil || string(file.Bytes) != string(raw) || file.Filename != "raw.pdf" {
		t.Fatalf("binary corrupted %v %v", file, err)
	}
	for _, slug := range []string{"../private", "a/../private", "/private", "a//b", "a\\b", "a/./b"} {
		if _, err := client.GetPage(context.Background(), "kb", slug); err == nil {
			t.Fatalf("accepted unsafe slug %q", slug)
		}
	}
}

func TestWeKnoraSearchGraphAndSourceContracts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/knowledgebase/kb/wiki/search":
			if r.URL.Query().Get("q") != "会话" {
				t.Error("query lost")
			}
			w.Write([]byte(`{"pages":[{"id":"p","knowledge_base_id":"kb","slug":"session","version":2}]}`))
		case "/api/v1/knowledgebase/kb/wiki/graph":
			w.Write([]byte(`{"nodes":[{"slug":"session","title":"会话","page_type":"concept"}],"edges":[],"meta":{"mode":"overview","total":2,"returned":1,"truncated":true}}`))
		case "/api/v1/knowledge-bases/kb/knowledge":
			w.Write([]byte(`{"success":true,"data":[{"id":"s","knowledge_base_id":"kb","file_name":"会话.pdf","file_type":"pdf","parse_status":"completed"}],"total":1,"page":1,"page_size":50}`))
		default:
			t.Error(r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	c, _ := NewWeKnora(Options{BaseURL: server.URL, Token: "key"})
	pages, err := c.Search(context.Background(), "kb", "会话", 10)
	if err != nil || len(pages) != 1 {
		t.Fatalf("search %v %v", pages, err)
	}
	graph, err := c.Graph(context.Background(), "kb", GraphOptions{})
	if err != nil || !graph.Meta.Truncated || graph.Meta.Total != 2 {
		t.Fatalf("graph %v %v", graph, err)
	}
	sources, err := c.ListSources(context.Background(), "kb", 1, 50)
	if err != nil || len(sources.Data) != 1 || sources.Data[0].Filename != "会话.pdf" {
		t.Fatalf("sources %v %v", sources, err)
	}
}

func TestWeKnoraSourceScopeBeforeDownload(t *testing.T) {
	downloads := 0
	kb := "other"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/knowledge/raw/download" {
			downloads++
			w.Write([]byte{0, 1, 2})
			return
		}
		if r.URL.Path != "/api/v1/knowledge/raw" {
			t.Errorf("unexpected route %s", r.URL.Path)
		}
		w.Write([]byte(`{"success":true,"data":{"id":"raw","knowledge_base_id":"` + kb + `","file_name":"test.pdf"}}`))
	}))
	defer server.Close()
	c, _ := NewWeKnora(Options{BaseURL: server.URL, Token: "token"})
	if _, err := c.DownloadSourceInLibrary(context.Background(), "kb", "raw"); err == nil || downloads != 0 {
		t.Fatal("wrong library reached download")
	}
	kb = "kb"
	v, err := c.DownloadSourceInLibrary(context.Background(), "kb", "raw")
	if err != nil || len(v.Bytes) != 3 || downloads != 1 {
		t.Fatalf("download %v %v", v, err)
	}
}

func TestWeKnoraRevisionRequiresRequestedContent(t *testing.T) {
	for _, tail := range []string{``, `,"content":null`, `,"content":""`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"revisions":[{"id":"r","page_id":"p","version":2` + tail + `}],"total":1,"current_version":3}`))
		}))
		c, _ := NewWeKnora(Options{BaseURL: server.URL, Token: "token"})
		_, err := c.ListRevisions(context.Background(), "kb", "page", RevisionOptions{ContentVersion: 2})
		server.Close()
		if (err == nil) != (tail == `,"content":""`) {
			t.Fatalf("content presence mismatch %s %v", tail, err)
		}
	}
}
