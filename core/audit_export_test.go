package core_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/JieWaZi/neux-knowledge/core"

	"gopkg.in/yaml.v3"
)

func writeAuditPage(t *testing.T, e *core.Engine, l *core.Library, p core.Page) *core.Page {
	t.Helper()
	current, err := e.GetLibrary(context.Background(), l.ID)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := e.WritePage(context.Background(), core.WritePageInput{RequestID: "audit-page-" + p.Slug, LibraryID: l.ID, StructureVersion: current.StructureVersion, Page: p})
	if err != nil {
		t.Fatal(err)
	}
	saved, err := e.GetPage(context.Background(), l.ID, rec.ResourceID)
	if err != nil {
		t.Fatal(err)
	}
	return saved
}

func TestLintFourChecksAreScopedAndReadOnly(t *testing.T) {
	ctx := context.Background()
	e, _ := open(t)
	l := library(t, e, "lint")
	s := source(t, e, l, "lint-source")
	a := writeAuditPage(t, e, l, core.Page{Slug: "concept/a", Title: "甲", PageType: "concept", Content: strings.Repeat("知", 60) + " [[concept/b]] [[concept/missing]] `[[concept/code]]`", Evidence: evidence(s)})
	writeAuditPage(t, e, l, core.Page{Slug: "concept/b", Title: "乙", PageType: "concept", Content: strings.Repeat("知", 49)})
	writeAuditPage(t, e, l, core.Page{Slug: "concept/c", Title: "丙", PageType: "concept", Content: strings.Repeat("知", 50) + " [[concept/c]]"})
	writeAuditPage(t, e, l, core.Page{Slug: "archived", Title: "归档", PageType: "concept", Status: "archived", Content: "[[concept/a]]"})
	foreign := library(t, e, "foreign-lint")
	writeAuditPage(t, e, foreign, core.Page{Slug: "concept/missing", Title: "其他库", PageType: "concept", Content: "存在于其他库"})
	_, err := e.ArchiveSource(ctx, core.ArchiveSourceInput{RequestID: "lint-archive", LibraryID: l.ID, SourceVersionID: s.ID, ExpectedVersion: s.RowVersion, Archived: true})
	if err != nil {
		t.Fatal(err)
	}
	before, err := e.GetPage(ctx, l.ID, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, revisions, err := e.ListRevisions(ctx, l.ID, a.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	report, err := e.LintLibrary(ctx, l.ID)
	if err != nil {
		t.Fatal(err)
	}
	if report.PageCount != 3 {
		t.Fatalf("wrong live page scope: %+v", report)
	}
	want := map[string]int{"orphan_page": 2, "broken_link": 1, "empty_content": 1, "stale_ref": 1}
	got := map[string]int{}
	for _, issue := range report.Issues {
		got[issue.Type]++
		if issue.PageID == "" || issue.PageVersion < 1 {
			t.Fatal("finding lacks frozen page identity")
		}
		if issue.Type == "broken_link" && issue.TargetID != "concept/missing" {
			t.Fatalf("code or foreign links counted: %+v", issue)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("findings=%v want=%v", got, want)
	}
	after, err := e.GetPage(ctx, l.ID, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("lint modified page")
	}
	_, afterRevisions, err := e.ListRevisions(ctx, l.ID, a.ID, 0, 100)
	if err != nil || afterRevisions != revisions {
		t.Fatalf("lint created history: %d %v", afterRevisions, err)
	}
}

func TestLintReportsSupersededSourceVersion(t *testing.T) {
	ctx := context.Background()
	e, _ := open(t)
	l := library(t, e, "stale-head")
	s := source(t, e, l, "versioned")
	writeAuditPage(t, e, l, core.Page{Slug: "concept/version", Title: "旧版依据", PageType: "concept", Content: strings.Repeat("知", 60), Evidence: evidence(s)})
	_, err := e.ImportSource(ctx, core.ImportSourceInput{RequestID: "source-new-head", LibraryID: l.ID, SourceID: s.SourceID, ExpectedVersion: s.Version, Filename: "versioned.md", Bytes: []byte("新版")})
	if err != nil {
		t.Fatal(err)
	}
	report, err := e.LintLibrary(ctx, l.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, issue := range report.Issues {
		if issue.Type == "stale_ref" && issue.TargetID == s.ID {
			return
		}
	}
	t.Fatal("superseded reference not reported")
}

func TestMarkdownExportFrozenMetadataSafeNamesAndNoRaw(t *testing.T) {
	ctx := context.Background()
	e, _ := open(t)
	l := library(t, e, "export")
	s := source(t, e, l, "private-source\n---\ninjectedFilename: true")
	theme, err := e.WriteTheme(ctx, core.WriteThemeInput{RequestID: "export-theme", LibraryID: l.ID, StructureVersion: l.StructureVersion, Theme: core.Theme{Name: "中文主题"}})
	if err != nil {
		t.Fatal(err)
	}
	ref := evidence(s)
	ref.Sources[0].Excerpt = "RAW-SENSITIVE-EXCERPT"
	ref.Sources[0].ExcerptHash = fmt.Sprintf("%x", sha256.Sum256([]byte(ref.Sources[0].Excerpt)))
	title := "标题\n---\nadmin: true\n#"
	p := writeAuditPage(t, e, l, core.Page{Slug: "../../outside", Title: title, PageType: "concept", FolderID: theme.ResourceID, Content: "# 导出的知识\n\n正文保持原字节。", Evidence: ref})
	writeAuditPage(t, e, l, core.Page{Slug: "draft", Title: "草稿", PageType: "concept", Status: "draft", Content: "不导出草稿"})
	writeAuditPage(t, e, l, core.Page{Slug: "archived", Title: "归档", PageType: "concept", Status: "archived", Content: "不导出归档"})
	bundle, err := e.ExportMarkdown(ctx, core.ExportInput{LibraryID: l.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Files) != 1 {
		t.Fatalf("exported nonpublished pages: %+v", bundle)
	}
	file := bundle.Files[0]
	if filepath.Base(file.Path) != file.Path || !strings.HasSuffix(file.Path, ".md") || strings.Contains(file.Path, "..") {
		t.Fatalf("unsafe export path %q", file.Path)
	}
	if file.Metadata.PageID != p.ID || file.Metadata.PageVersion != p.Version || file.SHA256 != fmt.Sprintf("%x", sha256.Sum256([]byte(file.Markdown))) {
		t.Fatal("missing immutable exported identity")
	}
	if !reflect.DeepEqual(file.Metadata.ThemePath, []string{"中文主题"}) || file.Metadata.ThemeID != theme.ResourceID {
		t.Fatalf("theme metadata drifted: %+v", file.Metadata)
	}
	parts := strings.SplitN(file.Markdown, "\n---\n", 2)
	if len(parts) != 2 || parts[1] != p.Content {
		t.Fatal("export changed body")
	}
	var metadata map[string]any
	if err = yaml.Unmarshal([]byte(strings.TrimPrefix(parts[0], "---\n")), &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata["title"] != title || metadata["admin"] != nil || metadata["injectedFilename"] != nil {
		t.Fatalf("frontmatter injection: %v", metadata)
	}
	if strings.Contains(file.Markdown, "RAW-SENSITIVE-EXCERPT") || strings.Contains(file.Markdown, "# 原件") {
		t.Fatal("export leaked raw source or evidence excerpt")
	}
	if len(file.Metadata.Sources) != 1 || file.Metadata.Sources[0].SourceVersionID != s.ID || file.Metadata.Sources[0].Version != s.Version || file.Metadata.Sources[0].Filename != s.Filename {
		t.Fatal("source version omitted")
	}
	before := file.Markdown
	file.Markdown = "external changes"
	current, err := e.GetPage(ctx, l.ID, p.ID)
	if err != nil || current.Content != p.Content {
		t.Fatal("export became writable authority")
	}
	again, err := e.ExportMarkdown(ctx, core.ExportInput{LibraryID: l.ID, PageIDs: []string{p.ID}})
	if err != nil || again.Files[0].Markdown != before {
		t.Fatalf("unchanged snapshot export drift: %v", err)
	}
}

func TestAuditExportBoundaryErrors(t *testing.T) {
	ctx := context.Background()
	e, _ := open(t)
	l := library(t, e, "export-boundary")
	other := library(t, e, "other-export")
	p := writeAuditPage(t, e, other, core.Page{Slug: "foreign", Title: "其他库", PageType: "concept", Content: "正文"})
	draft := writeAuditPage(t, e, other, core.Page{Slug: "draft", Title: "草稿", PageType: "concept", Status: "draft", Content: "草稿"})
	_, err := e.LintLibrary(ctx, "missing")
	code(t, err, core.ErrNotFound)
	_, err = e.ExportMarkdown(ctx, core.ExportInput{LibraryID: l.ID, PageIDs: []string{p.ID}})
	code(t, err, core.ErrNotFound)
	_, err = e.ExportMarkdown(ctx, core.ExportInput{LibraryID: l.ID, PageIDs: []string{"missing"}})
	code(t, err, core.ErrNotFound)
	_, err = e.ExportMarkdown(ctx, core.ExportInput{LibraryID: other.ID, PageIDs: []string{draft.ID}})
	code(t, err, core.ErrNotFound)
	_, err = e.ExportMarkdown(ctx, core.ExportInput{LibraryID: other.ID, PageIDs: []string{p.ID, p.ID}})
	code(t, err, core.ErrInvalid)
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = e.ExportMarkdown(cancelled, core.ExportInput{LibraryID: l.ID})
	if err != context.Canceled {
		t.Fatalf("cancelled export=%v", err)
	}
}
