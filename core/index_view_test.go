package core_test

import (
	"context"
	"github.com/JieWaZi/neux-knowledge/core"
	"testing"
)

func TestWikiIndexIncludesWholeLibraryWithAndWithoutPersistentIntro(t *testing.T) {
	for _, hasIndex := range []bool{false, true} {
		t.Run(map[bool]string{false: "existing-wiki-no-index", true: "existing-persistent-index"}[hasIndex], func(t *testing.T) {
			ctx := context.Background()
			e, _ := open(t)
			lib := library(t, e, "index")
			for _, p := range []core.Page{{Slug: "concept/old", Title: "旧知识", PageType: "concept", Content: "旧正文", Summary: "旧知识说明"}, {Slug: "topic/manual", Title: "人工主题", PageType: "topic", Content: "人工正文"}} {
				lib, _ = e.GetLibrary(ctx, lib.ID)
				if _, err := e.WritePage(ctx, core.WritePageInput{RequestID: p.Slug, LibraryID: lib.ID, StructureVersion: lib.StructureVersion, Page: p}); err != nil {
					t.Fatal(err)
				}
			}
			intro := ""
			if hasIndex {
				intro = "# 全库知识\n已有范围和新文档都可通过下方目录访问。"
				lib, _ = e.GetLibrary(ctx, lib.ID)
				if _, err := e.WritePage(ctx, core.WritePageInput{RequestID: "index", LibraryID: lib.ID, StructureVersion: lib.StructureVersion, Page: core.Page{Slug: "index", Title: "Index", PageType: "index", Content: intro, Summary: intro}}); err != nil {
					t.Fatal(err)
				}
			}
			before, total, _ := e.ListPages(ctx, core.PageQuery{LibraryID: lib.ID})
			_ = before
			view, err := e.GetIndexView(ctx, lib.ID, nil, 1, "")
			if err != nil {
				t.Fatal(err)
			}
			if hasIndex && view.Intro != "# Wiki Index\n已有范围和新文档都可通过下方目录访问。" || !hasIndex && view.Intro != "" {
				t.Fatal("intro must be rendered independently from directory")
			}
			found := map[string]bool{}
			for _, g := range view.Groups {
				for _, entry := range g.Items {
					found[entry.Slug] = true
					if entry.Slug == "index" {
						t.Fatal("index included in own directory")
					}
				}
			}
			if !found["concept/old"] || !found["topic/manual"] {
				t.Fatal("whole-library directory missing existing/manual pages", found)
			}
			if hasIndex {
				saved, err := e.GetPageBySlug(ctx, lib.ID, "index")
				if err != nil || saved.Content != intro || saved.Title != "Index" || saved.Version != 1 {
					t.Fatalf("read rewrote legacy index: %+v %v", saved, err)
				}
			}
			if _, after, _ := e.ListPages(ctx, core.PageQuery{LibraryID: lib.ID}); after != total {
				t.Fatal("read API created a page")
			}
			if _, err = e.GetIndexView(ctx, lib.ID, nil, 50, "invalid"); err == nil {
				t.Fatal("bad cursor accepted")
			}
		})
	}
}

func TestWikiIndexGroupsPaginateIndependentlyAndKeepLibraryBoundary(t *testing.T) {
	ctx := context.Background()
	e, _ := open(t)
	lib := library(t, e, "paged")
	other := library(t, e, "other")
	for _, entry := range []struct{ libID, slug, title, kind, status string }{{lib.ID, "concept/a", "A", "concept", "published"}, {lib.ID, "concept/b", "B", "concept", "published"}, {lib.ID, "concept/archived", "X", "concept", "archived"}, {lib.ID, "entity/a", "Entity", "entity", "published"}, {other.ID, "concept/private", "Private", "concept", "published"}} {
		l, _ := e.GetLibrary(ctx, entry.libID)
		if _, err := e.WritePage(ctx, core.WritePageInput{RequestID: entry.libID + entry.slug, LibraryID: entry.libID, StructureVersion: l.StructureVersion, Page: core.Page{Slug: entry.slug, Title: entry.title, PageType: entry.kind, Status: entry.status, Content: "大正文不应出现在目录", Summary: "简介"}}); err != nil {
			t.Fatal(err)
		}
	}
	view, err := e.GetIndexView(ctx, lib.ID, []string{"concept", "entity"}, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Groups) != 2 || view.Groups[0].Total != 2 || view.Groups[0].NextCursor != "1" || view.Groups[1].Total != 1 || view.Groups[1].NextCursor != "" {
		t.Fatalf("first groups=%+v", view.Groups)
	}
	next, err := e.GetIndexView(ctx, lib.ID, []string{"concept"}, 1, view.Groups[0].NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Groups) != 1 || len(next.Groups[0].Items) != 1 || next.Groups[0].Items[0].Slug == view.Groups[0].Items[0].Slug || next.Groups[0].NextCursor != "" {
		t.Fatalf("next=%+v", next)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = e.GetIndexView(cancelled, lib.ID, nil, 50, ""); err == nil {
		t.Fatal("cancelled read succeeded")
	}
}

func TestWikiIndexDoesNotReviveArchivedIntro(t *testing.T) {
	ctx := context.Background()
	e, _ := open(t)
	lib := library(t, e, "archived-index")
	for _, p := range []core.Page{{Slug: "concept/kept", Title: "正常知识", PageType: "concept", Content: "正常正文"}, {Slug: "index", Title: "Index", PageType: "index", Content: "已移除的导言", Summary: "已移除的导言", Status: "archived"}} {
		lib, _ = e.GetLibrary(ctx, lib.ID)
		if _, err := e.WritePage(ctx, core.WritePageInput{RequestID: p.Slug, LibraryID: lib.ID, StructureVersion: lib.StructureVersion, Page: p}); err != nil {
			t.Fatal(err)
		}
	}
	view, err := e.GetIndexView(ctx, lib.ID, nil, 50, "")
	if err != nil {
		t.Fatal(err)
	}
	if view.Intro != "" || view.Version != 0 {
		t.Fatalf("archived index leaked into active directory: %+v", view)
	}
	var count int64
	for _, g := range view.Groups {
		count += g.Total
	}
	if count != 1 {
		t.Fatalf("active directory lost: %d", count)
	}
}
