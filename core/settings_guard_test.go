package core_test

import (
	"context"
	"testing"

	"github.com/JieWaZi/neux-knowledge/core"
)

func TestEmbeddingModelChangeRequiresEmptyLibrary(t *testing.T) {
	e, _ := open(t)
	ctx := context.Background()
	l := library(t, e, "model-guard")
	first := "model-a"
	_, err := e.UpdateLibrary(ctx, core.UpdateLibraryInput{RequestID: "initial-model", LibraryID: l.ID, ExpectedVersion: l.Version, Name: l.Name, PolicyVersion: l.PolicyVersion, EmbeddingModelID: &first})
	if err != nil {
		t.Fatal(err)
	}
	l, _ = e.GetLibrary(ctx, l.ID)
	source(t, e, l, "guard-content")
	next := "model-b"
	_, err = e.UpdateLibrary(ctx, core.UpdateLibraryInput{RequestID: "model-with-content", LibraryID: l.ID, ExpectedVersion: l.Version, Name: l.Name, PolicyVersion: l.PolicyVersion, EmbeddingModelID: &next})
	code(t, err, core.ErrInvalid)
}

func TestEmbeddingModelGuardKeepsUpstreamFirstConfigurationAndSameValue(t *testing.T) {
	e, _ := open(t)
	ctx := context.Background()
	l := library(t, e, "model-first-content")
	source(t, e, l, "first-content")
	first := "model-a"
	_, err := e.UpdateLibrary(ctx, core.UpdateLibraryInput{RequestID: "first-after-content", LibraryID: l.ID, ExpectedVersion: l.Version, Name: l.Name, PolicyVersion: l.PolicyVersion, EmbeddingModelID: &first})
	if err != nil {
		t.Fatal(err)
	}
	l, _ = e.GetLibrary(ctx, l.ID)
	_, err = e.UpdateLibrary(ctx, core.UpdateLibraryInput{RequestID: "same-after-content", LibraryID: l.ID, ExpectedVersion: l.Version, Name: l.Name, PolicyVersion: l.PolicyVersion, EmbeddingModelID: &first})
	if err != nil {
		t.Fatal(err)
	}
	l, _ = e.GetLibrary(ctx, l.ID)
	empty := ""
	_, err = e.UpdateLibrary(ctx, core.UpdateLibraryInput{RequestID: "clear-model", LibraryID: l.ID, ExpectedVersion: l.Version, Name: l.Name, PolicyVersion: l.PolicyVersion, EmbeddingModelID: &empty})
	code(t, err, core.ErrInvalid)
}

func TestEmbeddingModelChangeGuardsWikiContentAndAllowsEmptyLibrary(t *testing.T) {
	e, _ := open(t)
	ctx := context.Background()
	l := library(t, e, "wiki-model-guard")
	first := "model-a"
	_, err := e.UpdateLibrary(ctx, core.UpdateLibraryInput{RequestID: "wiki-model-first", LibraryID: l.ID, ExpectedVersion: l.Version, Name: l.Name, PolicyVersion: l.PolicyVersion, EmbeddingModelID: &first})
	if err != nil {
		t.Fatal(err)
	}
	l, _ = e.GetLibrary(ctx, l.ID)
	second := "model-b"
	_, err = e.UpdateLibrary(ctx, core.UpdateLibraryInput{RequestID: "empty-model-switch", LibraryID: l.ID, ExpectedVersion: l.Version, Name: l.Name, PolicyVersion: l.PolicyVersion, EmbeddingModelID: &second})
	if err != nil {
		t.Fatal(err)
	}
	l, _ = e.GetLibrary(ctx, l.ID)
	page(t, e, l, "model-wiki")
	l, _ = e.GetLibrary(ctx, l.ID)
	_, err = e.UpdateLibrary(ctx, core.UpdateLibraryInput{RequestID: "wiki-model-switch", LibraryID: l.ID, ExpectedVersion: l.Version, Name: l.Name, PolicyVersion: l.PolicyVersion, EmbeddingModelID: &first})
	code(t, err, core.ErrInvalid)
}

func TestExplicitEmptyIndexingStrategyIsRejected(t *testing.T) {
	e, _ := open(t)
	ctx := context.Background()
	none := core.IndexingStrategy{}
	_, err := e.CreateLibrary(ctx, core.CreateLibraryInput{RequestID: "no-index-create", Name: "无索引", Kind: "general", WikiLanguage: "zh-CN", IndexingStrategy: &none})
	code(t, err, core.ErrInvalid)
	l := library(t, e, "indexing-guard")
	_, err = e.UpdateLibrary(ctx, core.UpdateLibraryInput{RequestID: "no-index-update", LibraryID: l.ID, ExpectedVersion: l.Version, Name: l.Name, PolicyVersion: l.PolicyVersion, IndexingStrategy: &none})
	code(t, err, core.ErrInvalid)
}
