package remote

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestCustomAuditAndExportOnlyForwardBoundReadRequests(t *testing.T) {
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.RequestURI())
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
		}
		switch r.URL.Path {
		case customPath + "/libraries/library/audit":
			_, _ = w.Write([]byte(`{"libraryId":"library","pageCount":1,"issues":[]}`))
		case customPath + "/libraries/library/markdown-export":
			_, _ = w.Write([]byte(`{"formatVersion":1,"libraryId":"library","files":[]}`))
		default:
			t.Errorf("unexpected route: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	client, err := NewCustom(Options{BaseURL: server.URL, Token: "secret"}, ResourceRef{Backend: "custom", ConnectionID: "connection", KnowledgeBaseID: "library"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client.LintLibrary(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err = client.ExportMarkdown(context.Background(), []string{"one", "two"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"GET " + customPath + "/libraries/library/audit", "GET " + customPath + "/libraries/library/markdown-export?pageId=one&pageId=two"}
	if !reflect.DeepEqual(requests, want) {
		t.Fatalf("requests=%v want=%v", requests, want)
	}
}
