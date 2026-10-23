package core_test

import (
	"context"
	"github.com/JieWaZi/neux-knowledge/core"
	"testing"
)

func TestWritePageChecksExpectedPolicyInsideTransaction(t *testing.T) {
	ctx := context.Background()
	engine, _ := open(t)
	defer engine.Close()
	lib := library(t, engine, "write-policy")
	created, err := engine.WritePage(ctx, core.WritePageInput{RequestID: "create", LibraryID: lib.ID, StructureVersion: lib.StructureVersion, ExpectedPolicyVersion: lib.PolicyVersion, Page: core.Page{Slug: "concept/policy", Title: "Policy", PageType: "concept", Content: "old"}})
	if err != nil {
		t.Fatal(err)
	}
	page, _ := engine.GetPage(ctx, lib.ID, created.ResourceID)
	current, _ := engine.GetLibrary(ctx, lib.ID)
	if _, err := engine.UpdateLibrary(ctx, core.UpdateLibraryInput{RequestID: "revoke", LibraryID: lib.ID, ExpectedVersion: current.Version, Name: current.Name, PolicyVersion: current.PolicyVersion + 1}); err != nil {
		t.Fatal(err)
	}
	input := core.WritePageInput{RequestID: "stale-authority", LibraryID: lib.ID, ExpectedVersion: page.Version, StructureVersion: current.StructureVersion, ExpectedPolicyVersion: lib.PolicyVersion, Page: *page}
	input.Page.Content = "must not commit"
	_, err = engine.WritePage(ctx, input)
	code(t, err, core.ErrScopeViolation)
	after, _ := engine.GetPage(ctx, lib.ID, page.ID)
	if after.Content != "old" || after.Version != page.Version {
		t.Fatal("stale policy changed page")
	}
	if _, err := engine.GetReceipt(ctx, input.RequestID); err == nil {
		t.Fatal("rejected write left success receipt")
	}
	input.RequestID = "current-authority"
	input.ExpectedPolicyVersion = current.PolicyVersion + 1
	if _, err := engine.WritePage(ctx, input); err != nil {
		t.Fatal(err)
	}
}

func TestWriteThemeChecksExpectedPolicyInsideTransaction(t *testing.T) {
	ctx := context.Background()
	engine, _ := open(t)
	defer engine.Close()
	lib := library(t, engine, "theme-policy")
	_, err := engine.UpdateLibrary(ctx, core.UpdateLibraryInput{RequestID: "revoke", LibraryID: lib.ID, ExpectedVersion: lib.Version, Name: lib.Name, PolicyVersion: lib.PolicyVersion + 1})
	if err != nil {
		t.Fatal(err)
	}
	_, err = engine.WriteTheme(ctx, core.WriteThemeInput{RequestID: "stale-theme", LibraryID: lib.ID, StructureVersion: lib.StructureVersion, ExpectedPolicyVersion: lib.PolicyVersion, Theme: core.Theme{Name: "不可提交", Manual: true}})
	code(t, err, core.ErrScopeViolation)
	themes, err := engine.ListThemes(ctx, lib.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, theme := range themes {
		if theme.Name == "不可提交" {
			t.Fatal("revoked theme committed")
		}
	}
	if _, err := engine.GetReceipt(ctx, "stale-theme"); err == nil {
		t.Fatal("rejected theme left receipt")
	}
}

func TestImportSourceChecksExpectedPolicyInsideTransaction(t *testing.T) {
	ctx := context.Background()
	engine, _ := open(t)
	defer engine.Close()
	lib := library(t, engine, "source-policy")
	if _, err := engine.UpdateLibrary(ctx, core.UpdateLibraryInput{RequestID: "changed", LibraryID: lib.ID, ExpectedVersion: lib.Version, Name: lib.Name, PolicyVersion: lib.PolicyVersion + 1}); err != nil {
		t.Fatal(err)
	}
	_, err := engine.ImportSource(ctx, core.ImportSourceInput{RequestID: "stale-source", LibraryID: lib.ID, ExpectedPolicyVersion: lib.PolicyVersion, Filename: "source.md", Bytes: []byte("不应提交")})
	code(t, err, core.ErrScopeViolation)
	_, total, err := engine.ListSources(ctx, lib.ID, 0, 10)
	if err != nil || total != 0 {
		t.Fatal("stale source committed", total, err)
	}
	if _, err := engine.GetReceipt(ctx, "stale-source"); err == nil {
		t.Fatal("stale source received success receipt")
	}
}
