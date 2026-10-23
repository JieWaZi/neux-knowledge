package core_test

import (
	"context"
	"testing"

	"github.com/JieWaZi/neux-knowledge/core"
)

func TestGraphReadsOnlyRequestedLibraryAndReportsActualScope(t *testing.T) {
	e, _ := open(t)
	l := library(t, e, "graph")
	other := library(t, e, "other-graph")
	for _, lib := range []*core.Library{l, other} {
		_, err := e.WritePage(context.Background(), core.WritePageInput{RequestID: "page-" + lib.ID, LibraryID: lib.ID, StructureVersion: lib.StructureVersion, Page: core.Page{KnowledgeBaseID: lib.ID, Slug: "concept/one", Title: lib.Name, Content: "正文", PageType: "concept"}})
		if err != nil {
			t.Fatal(err)
		}
	}
	graph, err := e.Graph(context.Background(), core.GraphRequest{KnowledgeBaseID: l.ID, Mode: "overview", Limit: 10})
	if err != nil || len(graph.Nodes) != 1 || graph.Nodes[0].Title != "graph" || graph.Meta.Truncated {
		t.Fatal(graph, err)
	}
	if _, err := e.Graph(context.Background(), core.GraphRequest{KnowledgeBaseID: "missing", Mode: "overview", Limit: 10}); err == nil {
		t.Fatal("unknown library returned success")
	}
}
