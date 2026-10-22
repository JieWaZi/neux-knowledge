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
