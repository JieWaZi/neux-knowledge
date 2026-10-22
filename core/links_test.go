package core_test

import (
	"context"
	"github.com/JieWaZi/neux-knowledge/core"
	"testing"
)

func TestPageLinksFollowActualContentAndRestore(t *testing.T) {
	ctx := context.Background()
	e, _ := open(t)
	lib := library(t, e, "links")
	create := func(req, slug, body string) *core.Page {
		l, _ := e.GetLibrary(ctx, lib.ID)
		r, err := e.WritePage(ctx, core.WritePageInput{RequestID: req, LibraryID: lib.ID, StructureVersion: l.StructureVersion, Page: core.Page{Slug: slug, Title: slug, PageType: "concept", Content: body, OutLinks: []string{"forged"}}})
		if err != nil {
			t.Fatal(err)
		}
		p, err := e.GetPage(ctx, lib.ID, r.ResourceID)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	b := create("b", "concept/b", "目标知识")
	a := create("a", "concept/a", "参见 [[concept/b|知识B]] 和 [[concept/b]]。 `[[code-only]]`")
	if len(a.OutLinks) != 1 || a.OutLinks[0] != b.Slug {
		t.Fatalf("body links not projected: %v", a.OutLinks)
	}
	b, _ = e.GetPage(ctx, lib.ID, b.ID)
	if len(b.InLinks) != 1 || b.InLinks[0] != a.Slug || b.Version != 1 {
		t.Fatalf("backlinks or content version wrong %+v", b)
	}
	l, _ := e.GetLibrary(ctx, lib.ID)
	a.Content = "移除关联"
	_, err := e.WritePage(ctx, core.WritePageInput{RequestID: "unlink", LibraryID: lib.ID, StructureVersion: l.StructureVersion, ExpectedVersion: a.Version, Page: *a})
	if err != nil {
		t.Fatal(err)
	}
	b, _ = e.GetPage(ctx, lib.ID, b.ID)
	if len(b.InLinks) != 0 || b.Version != 1 {
		t.Fatal("stale backlink", b.InLinks)
	}
	_, err = e.RestorePage(ctx, core.RestorePageInput{RequestID: "restore-links", LibraryID: lib.ID, PageID: a.ID, ExpectedVersion: 2, StructureVersion: l.StructureVersion, RevisionVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	b, _ = e.GetPage(ctx, lib.ID, b.ID)
	if len(b.InLinks) != 1 {
		t.Fatal("restore omitted links")
	}
}
