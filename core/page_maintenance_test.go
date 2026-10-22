// Copyright (C) 2026 Tencent. All rights reserved.
// See THIRD_PARTY_NOTICES.md for third-party attribution and license information.
package core_test

import (
	"context"
	"github.com/JieWaZi/neux-knowledge/core"
	"strings"
	"testing"
)

func TestAgentWriteAndExactReplaceProtection(t *testing.T) {
	e, _ := open(t)
	ctx := context.Background()
	l := library(t, e, "agent")
	r, err := e.WriteAgentPage(ctx, core.WritePageInput{RequestID: "agent-create", LibraryID: l.ID, StructureVersion: l.StructureVersion, Page: core.Page{Slug: "concept/a", Title: "A", PageType: "concept", Content: "target target"}})
	if err != nil {
		t.Fatal(err)
	}
	p, _ := e.GetPage(ctx, l.ID, r.ResourceID)
	if p.LastEditSource != "agent" || len(p.ProtectedContent) != 0 {
		t.Fatalf("agent marked manual: %+v", p)
	}
	p.Content = "manual target target"
	_, err = e.WritePage(ctx, core.WritePageInput{RequestID: "manual", LibraryID: l.ID, StructureVersion: l.StructureVersion, ExpectedVersion: p.Version, Page: *p})
	if err != nil {
		t.Fatal(err)
	}
	p, _ = e.GetPage(ctx, l.ID, p.ID)
	changed := *p
	changed.Content = "overwrite"
	_, err = e.WriteAgentPage(ctx, core.WritePageInput{RequestID: "blocked", LibraryID: l.ID, StructureVersion: l.StructureVersion, ExpectedVersion: p.Version, Page: changed})
	code(t, err, core.ErrManualProtected)
	in := core.ReplacePageTextInput{RequestID: "replace", LibraryID: l.ID, PageID: p.ID, ExpectedVersion: p.Version, StructureVersion: l.StructureVersion, OldText: "target", NewText: "replacement"}
	rec, err := e.ReplacePageText(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	again, err := e.ReplacePageText(ctx, in)
	if err != nil || again.Version != rec.Version {
		t.Fatalf("replay %+v %v", again, err)
	}
	p, _ = e.GetPage(ctx, l.ID, p.ID)
	if p.Content != "manual replacement replacement" || p.LastEditSource != "agent" || len(p.ProtectedContent) != 1 || p.ProtectedContent[0] != p.Content {
		t.Fatalf("replace protection %+v", p)
	}
	in.RequestID = "stale"
	_, err = e.ReplacePageText(ctx, in)
	code(t, err, core.ErrVersionConflict)
	in.RequestID = "missing"
	in.ExpectedVersion = p.Version
	in.OldText = "missing"
	_, err = e.ReplacePageText(ctx, in)
	code(t, err, core.ErrInvalid)
	rev, err := e.GetRevision(ctx, l.ID, p.ID, p.Version-1)
	if err != nil || rev.Content != "manual target target" {
		t.Fatalf("revision %+v %v", rev, err)
	}
}

func TestRenameDeleteStableIdentityHistoryAndLinks(t *testing.T) {
	e, _ := open(t)
	ctx := context.Background()
	l := library(t, e, "rename")
	a := page(t, e, l, "concept/old")
	r, err := e.WriteAgentPage(ctx, core.WritePageInput{RequestID: "linked", LibraryID: l.ID, StructureVersion: l.StructureVersion, Page: core.Page{Slug: "concept/link", Title: "Link", PageType: "concept", Content: "[[concept/old]] and [[concept/old|标签]]"}})
	if err != nil {
		t.Fatal(err)
	}
	rec, err := e.RenamePage(ctx, core.RenamePageInput{RequestID: "rename", LibraryID: l.ID, PageID: a.ID, ExpectedVersion: a.Version, StructureVersion: l.StructureVersion, NewSlug: "concept/new"})
	if err != nil {
		t.Fatal(err)
	}
	if rec.ResourceID != a.ID || len(rec.PageIDs) != 2 {
		t.Fatalf("stable identity %+v", rec)
	}
	a, _ = e.GetPage(ctx, l.ID, a.ID)
	b, _ := e.GetPage(ctx, l.ID, r.ResourceID)
	if a.Slug != "concept/new" || b.Content != "[[concept/new]] and [[concept/new|标签]]" {
		t.Fatalf("rename %+v %+v", a, b)
	}
	old, err := e.GetRevision(ctx, l.ID, a.ID, 1)
	if err != nil || old.Slug != "concept/old" {
		t.Fatalf("history %+v %v", old, err)
	}
	l, _ = e.GetLibrary(ctx, l.ID)
	_, err = e.DeletePage(ctx, core.DeletePageInput{RequestID: "delete", LibraryID: l.ID, PageID: a.ID, ExpectedVersion: a.Version, StructureVersion: l.StructureVersion})
	if err != nil {
		t.Fatal(err)
	}
	a, _ = e.GetPage(ctx, l.ID, a.ID)
	b, _ = e.GetPage(ctx, l.ID, b.ID)
	if a.Status != "archived" || strings.Contains(b.Content, "[[") || !strings.Contains(b.Content, "标签") {
		t.Fatalf("archive links %+v %+v", a, b)
	}
	if _, err = e.GetRevision(ctx, l.ID, a.ID, a.Version-1); err != nil {
		t.Fatal(err)
	}
}

// Exact text replacement cases.
func TestAgentReplaceTextUpstreamExactCases(t *testing.T) {
	cases := []struct {
		name    string // Upstream scenario name.
		content string // Current complete content.
		oldText string // Exact original text.
		newText string // Replacement text.
		want    string // Expected complete content.
	}{
		{"single match", "before target after", "target", "replacement", "before replacement after"},
		{"not recursive", "a a", "a", "aa", "aa aa"},
		{"delete matches", "keep-X-keep-X", "X", "", "keep--keep-"},
		{"unicode", "旧文本与旧文本", "旧文本", "新文本", "新文本与新文本"},
		{"markdown", "See [[concept/old]] and [[concept/old]].", "[[concept/old]]", "[[concept/new]]", "See [[concept/new]] and [[concept/new]]."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, _ := open(t)
			ctx := context.Background()
			l := library(t, e, "exact")
			r, err := e.WriteAgentPage(ctx, core.WritePageInput{RequestID: "create", LibraryID: l.ID, StructureVersion: l.StructureVersion, Page: core.Page{Slug: "exact", Title: "Exact", PageType: "concept", Content: tc.content}})
			if err != nil {
				t.Fatal(err)
			}
			_, err = e.ReplacePageText(ctx, core.ReplacePageTextInput{RequestID: "replace", LibraryID: l.ID, PageID: r.ResourceID, ExpectedVersion: 1, StructureVersion: l.StructureVersion, OldText: tc.oldText, NewText: tc.newText})
			if err != nil {
				t.Fatal(err)
			}
			p, _ := e.GetPage(ctx, l.ID, r.ResourceID)
			if p.Content != tc.want {
				t.Fatalf("got %q want %q", p.Content, tc.want)
			}
		})
	}
}

func TestAgentWholeWriteRejectsDroppedRowsButExactDeleteAllows(t *testing.T) {
	e, _ := open(t)
	ctx := context.Background()
	l := library(t, e, "rows")
	body := "| Subject | Value |\n| --- | --- |\n| alpha | 1 |\n| beta | 2 |"
	rec, err := e.WriteAgentPage(ctx, core.WritePageInput{RequestID: "rows", LibraryID: l.ID, StructureVersion: l.StructureVersion, Page: core.Page{Slug: "rows", Title: "Rows", PageType: "concept", Content: body}})
	if err != nil {
		t.Fatal(err)
	}
	p, _ := e.GetPage(ctx, l.ID, rec.ResourceID)
	p.Content = strings.ReplaceAll(body, "\n| beta | 2 |", "")
	_, err = e.WriteAgentPage(ctx, core.WritePageInput{RequestID: "truncate", LibraryID: l.ID, StructureVersion: l.StructureVersion, ExpectedVersion: p.Version, Page: *p})
	code(t, err, core.ErrCandidateInvalid)
	_, err = e.ReplacePageText(ctx, core.ReplacePageTextInput{RequestID: "explicit-delete", LibraryID: l.ID, PageID: p.ID, ExpectedVersion: p.Version, StructureVersion: l.StructureVersion, OldText: "\n| beta | 2 |", NewText: ""})
	if err != nil {
		t.Fatal(err)
	}
}

func TestAgentEvidenceRejectsFabricationAndCrossLibrary(t *testing.T) {
	e, _ := open(t)
	ctx := context.Background()
	l := library(t, e, "evidence")
	other := library(t, e, "foreign")
	own := source(t, e, l, "own")
	foreign := source(t, e, other, "foreign")
	in := core.WritePageInput{RequestID: "fabricated", LibraryID: l.ID, StructureVersion: l.StructureVersion, Page: core.Page{Slug: "evidence", Title: "Evidence", PageType: "concept", Content: "claim", Evidence: evidence(own)}}
	_, err := e.WriteAgentPage(ctx, in)
	code(t, err, core.ErrCandidateInvalid)
	in.RequestID = "foreign"
	in.Page.Evidence = evidence(foreign)
	_, err = e.WriteAgentPage(ctx, in)
	code(t, err, core.ErrScopeViolation)
	in.RequestID = "context"
	in.Page.Evidence = core.PageEvidence{Sources: []core.SourceRef{{SourceID: own.SourceID, SourceVersionID: own.ID, SHA256: own.SHA256, ClaimKind: "document_context"}}}
	if _, err = e.WriteAgentPage(ctx, in); err != nil {
		t.Fatal(err)
	}
}

// Slug normalization and summary-page namespace assertions.
func TestAgentWriteUpstreamSlugNormalization(t *testing.T) {
	for _, slug := range []string{"Entity/Acme Corp", "  concept/rag  ", "entity/上海中心大厦"} {
		e, _ := open(t)
		ctx := context.Background()
		l := library(t, e, "slug")
		r, err := e.WriteAgentPage(ctx, core.WritePageInput{RequestID: "slug", LibraryID: l.ID, StructureVersion: l.StructureVersion, Page: core.Page{Slug: slug, Title: "Slug", PageType: "concept", Content: "body"}})
		if err != nil {
			t.Fatal(err)
		}
		p, _ := e.GetPage(ctx, l.ID, r.ResourceID)
		want := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(slug)), " ", "-")
		if p.Slug != want {
			t.Fatalf("got %q want %q", p.Slug, want)
		}
	}
	for _, slug := range []string{"  ", "/entity/x", "entity/x/", "entity//x", "entity/x!y", "summary/fabricated"} {
		e, _ := open(t)
		l := library(t, e, "badslug")
		_, err := e.WriteAgentPage(context.Background(), core.WritePageInput{RequestID: "bad", LibraryID: l.ID, StructureVersion: l.StructureVersion, Page: core.Page{ID: "preallocated", Slug: slug, Title: "Slug", PageType: "concept", Content: "body"}})
		code(t, err, core.ErrInvalid)
	}
}

func TestAgentMaintenanceCannotSkipStructureCAS(t *testing.T) {
	e, _ := open(t)
	ctx := context.Background()
	l := library(t, e, "structure")
	p := page(t, e, l, "a")
	_, err := e.ReplacePageText(ctx, core.ReplacePageTextInput{RequestID: "negative-structure", LibraryID: l.ID, PageID: p.ID, ExpectedVersion: p.Version, StructureVersion: -1, OldText: "原", NewText: "新"})
	code(t, err, core.ErrVersionConflict)
}

func TestRenameSkipsPagesWithOnlyCodeExamples(t *testing.T) {
	e, _ := open(t)
	ctx := context.Background()
	l := library(t, e, "code-example")
	target := page(t, e, l, "concept/old")
	example := "```md\n[[concept/old]]\n```"
	rec, err := e.WriteAgentPage(ctx, core.WritePageInput{RequestID: "example", LibraryID: l.ID, StructureVersion: l.StructureVersion, Page: core.Page{Slug: "example", Title: "Example", PageType: "concept", Content: example}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.RenamePage(ctx, core.RenamePageInput{RequestID: "rename", LibraryID: l.ID, PageID: target.ID, ExpectedVersion: target.Version, StructureVersion: l.StructureVersion, NewSlug: "concept/new"})
	if err != nil {
		t.Fatal(err)
	}
	p, _ := e.GetPage(ctx, l.ID, rec.ResourceID)
	if p.Content != example || p.Version != 1 {
		t.Fatalf("rewrote a code example: %+v", p)
	}
}

func TestMaintenanceCascadeOnlyRewritesParsedWikiLinks(t *testing.T) {
	for _, operation := range []string{"rename", "delete"} {
		for _, scenario := range []struct {
			name string // Actual link representations combined with adjacent examples.
			link string // Content links that the relationship scanner must recognize.
			code string // Code examples that must be preserved verbatim.
		}{
			{"mixed-code", "[[concept/old|标签]]", "\n`[[concept/old]]`\n```md\n[[concept/old|代码示例]]\n```"},
			{"normalized-link", "[[ Concept/Old |标签]]", ""},
			{"normalized-mixed-code", "[[ Concept/Old |标签]]", "\n`[[concept/old]]`\n```md\n[[concept/old|代码示例]]\n```"},
		} {
			t.Run(operation+"/"+scenario.name, func(t *testing.T) {
				e, _ := open(t)
				ctx := context.Background()
				l := library(t, e, "mixed-links")
				target := page(t, e, l, "concept/old")
				body := scenario.link + scenario.code
				rec, err := e.WriteAgentPage(ctx, core.WritePageInput{RequestID: "mixed-page", LibraryID: l.ID, StructureVersion: l.StructureVersion, Page: core.Page{Slug: "mixed", Title: "Mixed", PageType: "concept", Content: body}})
				if err != nil {
					t.Fatal(err)
				}
				want := "[[concept/new|标签]]" + scenario.code
				if operation == "rename" {
					_, err = e.RenamePage(ctx, core.RenamePageInput{RequestID: "cascade", LibraryID: l.ID, PageID: target.ID, ExpectedVersion: target.Version, StructureVersion: l.StructureVersion, NewSlug: "concept/new"})
				} else {
					_, err = e.DeletePage(ctx, core.DeletePageInput{RequestID: "cascade", LibraryID: l.ID, PageID: target.ID, ExpectedVersion: target.Version, StructureVersion: l.StructureVersion})
					want = "标签" + scenario.code
				}
				if err != nil {
					t.Fatal(err)
				}
				p, err := e.GetPage(ctx, l.ID, rec.ResourceID)
				if err != nil {
					t.Fatal(err)
				}
				if p.Content != want || p.Version != 2 {
					t.Fatalf("cascade content=%q version=%d; want content=%q version=2", p.Content, p.Version, want)
				}
				if operation == "delete" && len(p.OutLinks) != 0 {
					t.Fatalf("archived page still has incoming relation: %v", p.OutLinks)
				}
			})
		}
	}
}
