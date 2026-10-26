package core_test

import (
	"context"
	"strings"
	"testing"

	"github.com/JieWaZi/neux-knowledge/core"
)

func TestAutoFixLibraryRepairsLinksArchivesGeneratedAndReplays(t *testing.T) {
	ctx := context.Background()
	e, _ := open(t)
	l := library(t, e, "auto-fix")
	generated := func(slug, content string) *core.Page {
		t.Helper()
		current, _ := e.GetLibrary(ctx, l.ID)
		rec, err := e.WriteAgentPage(ctx, core.WritePageInput{RequestID: "generated-" + slug, LibraryID: l.ID, StructureVersion: current.StructureVersion, Page: core.Page{Slug: slug, Title: slug, PageType: "concept", Content: content}})
		if err != nil {
			t.Fatal(err)
		}
		p, err := e.GetPage(ctx, l.ID, rec.ResourceID)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	short := generated("concept/short", "短文")
	linked := generated("concept/linked", strings.Repeat("完整正文", 30)+" [[concept/missing|保留标签]] [[concept/short]] `[[concept/code]]`")
	manual := writeAuditPage(t, e, l, core.Page{Slug: "concept/manual", Title: "人工", PageType: "concept", Content: "人工短文 [[concept/nope]]"})
	current, _ := e.GetLibrary(ctx, l.ID)
	input := core.AutoFixInput{RequestID: "fix", LibraryID: l.ID, StructureVersion: current.StructureVersion, ExpectedPolicyVersion: current.PolicyVersion}
	result, err := e.AutoFixLibrary(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Fixed != 2 || result.Receipt.Operation != "wiki.auto-fix" {
		t.Fatalf("result=%+v", result)
	}
	got, _ := e.GetPage(ctx, l.ID, linked.ID)
	if strings.Contains(got.Content, "[[concept/missing") || strings.Contains(got.Content, "[[concept/short]]") || !strings.Contains(got.Content, "保留标签") || !strings.Contains(got.Content, "`[[concept/code]]`") {
		t.Fatalf("links=%s", got.Content)
	}
	shortGot, _ := e.GetPage(ctx, l.ID, short.ID)
	if shortGot.Status != "archived" {
		t.Fatalf("not archived: %+v", shortGot)
	}
	manualGot, _ := e.GetPage(ctx, l.ID, manual.ID)
	if manualGot.Content != manual.Content || manualGot.Status == "archived" {
		t.Fatal("manual page changed")
	}
	replay, err := e.AutoFixLibrary(ctx, input)
	if err != nil || replay.Fixed != result.Fixed || replay.Receipt.CreatedAt != result.Receipt.CreatedAt {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	after, _ := e.GetPage(ctx, l.ID, linked.ID)
	if after.Version != got.Version {
		t.Fatal("replay created another revision")
	}
}

func TestAutoFixLibraryChecksVersionsAndRetainsLiveEvidence(t *testing.T) {
	ctx := context.Background()
	e, _ := open(t)
	l := library(t, e, "auto-fix-stale")
	old := source(t, e, l, "old")
	live := source(t, e, l, "live")
	ev := evidence(old)
	ev.Sources = append(ev.Sources, evidence(live).Sources...)
	for i := range ev.Sources {
		ev.Sources[i].ClaimKind = "document_context"
		ev.Sources[i].Excerpt = ""
		ev.Sources[i].ExcerptHash = ""
		ev.Sources[i].Locator = core.SourceLocator{}
	}
	r, err := e.WriteAgentPage(ctx, core.WritePageInput{RequestID: "stale-page", LibraryID: l.ID, StructureVersion: l.StructureVersion, Page: core.Page{Slug: "concept/stale", Title: "旧依据", PageType: "concept", Content: strings.Repeat("有效知识", 30), Evidence: ev}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.ArchiveSource(ctx, core.ArchiveSourceInput{RequestID: "archive-old", LibraryID: l.ID, SourceVersionID: old.ID, ExpectedVersion: old.RowVersion, Archived: true})
	if err != nil {
		t.Fatal(err)
	}
	l, _ = e.GetLibrary(ctx, l.ID)
	_, err = e.AutoFixLibrary(ctx, core.AutoFixInput{RequestID: "bad-structure", LibraryID: l.ID, StructureVersion: l.StructureVersion + 1, ExpectedPolicyVersion: l.PolicyVersion})
	code(t, err, core.ErrVersionConflict)
	_, err = e.AutoFixLibrary(ctx, core.AutoFixInput{RequestID: "bad-policy", LibraryID: l.ID, StructureVersion: l.StructureVersion, ExpectedPolicyVersion: l.PolicyVersion + 1})
	code(t, err, core.ErrScopeViolation)
	result, err := e.AutoFixLibrary(ctx, core.AutoFixInput{RequestID: "fix-stale", LibraryID: l.ID, StructureVersion: l.StructureVersion, ExpectedPolicyVersion: l.PolicyVersion})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := e.GetPage(ctx, l.ID, r.ResourceID)
	if result.Fixed != 1 || got.Status != "published" || len(got.Evidence.Sources) != 1 || got.Evidence.Sources[0].SourceVersionID != live.ID {
		t.Fatalf("result=%+v page=%+v", result, got)
	}
}

func TestAutoFixLibraryPreservesManualIncomingLinks(t *testing.T) {
	ctx := context.Background()
	e, _ := open(t)
	lib := library(t, e, "auto-fix-manual-incoming")
	short, err := e.WriteAgentPage(ctx, core.WritePageInput{RequestID: "short-page", LibraryID: lib.ID, StructureVersion: lib.StructureVersion, Page: core.Page{Slug: "concept/short", Title: "短页", PageType: "concept", Content: "短文"}})
	if err != nil {
		t.Fatal(err)
	}
	lib, _ = e.GetLibrary(ctx, lib.ID)
	manual := writeAuditPage(t, e, lib, core.Page{Slug: "concept/manual", Title: "人工页面", PageType: "concept", Content: strings.Repeat("人工确认的正文", 20) + " [[concept/short|保留人工链接]]"})
	lib, _ = e.GetLibrary(ctx, lib.ID)
	result, err := e.AutoFixLibrary(ctx, core.AutoFixInput{RequestID: "protect-manual-links", LibraryID: lib.ID, StructureVersion: lib.StructureVersion, ExpectedPolicyVersion: lib.PolicyVersion})
	if err != nil {
		t.Fatal(err)
	}
	archived, err := e.GetPage(ctx, lib.ID, short.ResourceID)
	if err != nil || archived.Status != "archived" {
		t.Fatalf("generated short page was not archived: %+v %v", archived, err)
	}
	current, err := e.GetPage(ctx, lib.ID, manual.ID)
	if err != nil || current.Content != manual.Content || current.Version != manual.Version {
		t.Fatalf("deterministic repair changed protected incoming content: before=%+v after=%+v err=%v", manual, current, err)
	}
	if result.Fixed != 1 {
		t.Fatalf("protected incoming page was counted as repaired: %+v", result)
	}
	found := false
	for _, issue := range result.Report.Issues {
		if issue.Type == "broken_link" && issue.PageID == manual.ID && issue.TargetID == "concept/short" {
			found = true
		}
	}
	if !found {
		t.Fatal("remaining protected link was hidden from the repair report")
	}
}
