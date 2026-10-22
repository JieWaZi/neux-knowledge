package core_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/JieWaZi/neux-knowledge/core"
)

func open(t *testing.T) (*core.Engine, core.Options) {
	t.Helper()
	dir := t.TempDir()
	opt := core.Options{DBPath: filepath.Join(dir, "knowledge.db"), RawDir: filepath.Join(dir, "raw")}
	e, err := core.Open(context.Background(), opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	return e, opt
}
func library(t *testing.T, e *core.Engine, name string) *core.Library {
	t.Helper()
	r, err := e.CreateLibrary(context.Background(), core.CreateLibraryInput{RequestID: "lib-" + name, Name: name, Kind: "general", WikiLanguage: "zh-CN"})
	if err != nil {
		t.Fatal(err)
	}
	l, err := e.GetLibrary(context.Background(), r.ResourceID)
	if err != nil {
		t.Fatal(err)
	}
	return l
}
func source(t *testing.T, e *core.Engine, l *core.Library, name string) *core.SourceVersion {
	t.Helper()
	r, err := e.ImportSource(context.Background(), core.ImportSourceInput{RequestID: "src-" + name, LibraryID: l.ID, Filename: name + ".md", MIME: "text/markdown", Bytes: []byte("# 原件\n" + name)})
	if err != nil {
		t.Fatal(err)
	}
	s, err := e.GetSource(context.Background(), l.ID, r.ResourceID)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func page(t *testing.T, e *core.Engine, l *core.Library, name string) *core.Page {
	t.Helper()
	r, err := e.WritePage(context.Background(), core.WritePageInput{RequestID: "page-" + name, LibraryID: l.ID, StructureVersion: l.StructureVersion, Page: core.Page{KnowledgeBaseID: l.ID, Slug: name, Title: name, PageType: "concept", Content: "原正文", Aliases: []string{"别名" + name}}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := e.GetPage(context.Background(), l.ID, r.ResourceID)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func evidence(s *core.SourceVersion) core.PageEvidence {
	text := "摘录"
	h := sha256.Sum256([]byte(text))
	return core.PageEvidence{Sources: []core.SourceRef{{SourceID: s.SourceID, SourceVersionID: s.ID, SHA256: s.SHA256, Locator: core.SourceLocator{Kind: "unknown"}, Excerpt: text, ExcerptHash: hex.EncodeToString(h[:]), ClaimKind: "source_statement"}}}
}
func code(t *testing.T, err error, want core.ErrorCode) {
	t.Helper()
	var got *core.Error
	if !errors.As(err, &got) || got.Code != want {
		t.Fatalf("error=%v want code=%s", err, want)
	}
}
func TestRawOnlyReplayTraversalAndReopen(t *testing.T) {
	ctx := context.Background()
	e, opt := open(t)
	l := library(t, e, "raw")
	in := core.ImportSourceInput{RequestID: "raw-request", LibraryID: l.ID, Filename: "中文.md", Bytes: []byte{0xef, 0xbb, 0xbf, 0x61, 0, 0xff}}
	r, err := e.ImportSource(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := e.ImportSource(ctx, in)
	if err != nil || r.ResourceID != r2.ResourceID {
		t.Fatalf("replay=%v %v", r2, err)
	}
	in.Bytes = []byte("changed")
	_, err = e.ImportSource(ctx, in)
	code(t, err, core.ErrIdempotencyConflict)
	for i, name := range []string{"../escape.md", "/tmp/escape.md", "x/y.md", "x\\y.md"} {
		_, err = e.ImportSource(ctx, core.ImportSourceInput{RequestID: string(rune('a' + i)), LibraryID: l.ID, Filename: name, Bytes: []byte("x")})
		code(t, err, core.ErrInvalid)
	}
	pages, total, err := e.ListPages(ctx, core.PageQuery{LibraryID: l.ID})
	if err != nil || total != 0 || len(pages) != 0 {
		t.Fatalf("raw generated pages: %v %d %v", pages, total, err)
	}
	jobs, jtotal, err := e.ListJobs(ctx, l.ID, 0, 50)
	if err != nil || jtotal != 0 || len(jobs) != 0 {
		t.Fatal("raw created job", jobs, err)
	}
	if err = e.Close(); err != nil {
		t.Fatal(err)
	}
	e, err = core.Open(ctx, opt)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	got, err := e.ReadRaw(ctx, l.ID, r.ResourceID)
	if err != nil || !bytes.Equal(got, []byte{0xef, 0xbb, 0xbf, 0x61, 0, 0xff}) {
		t.Fatalf("raw=%v %v", got, err)
	}
}
func TestContentCASHistoryArchiveAndRestoreManualTheme(t *testing.T) {
	ctx := context.Background()
	e, _ := open(t)
	l := library(t, e, "cas")
	p := page(t, e, l, "concept")
	p.Content = "second"
	_, err := e.WritePage(ctx, core.WritePageInput{RequestID: "edit", LibraryID: l.ID, ExpectedVersion: 1, StructureVersion: l.StructureVersion, Page: *p})
	if err != nil {
		t.Fatal(err)
	}
	p.Content = "stale"
	_, err = e.WritePage(ctx, core.WritePageInput{RequestID: "stale", LibraryID: l.ID, ExpectedVersion: 1, StructureVersion: l.StructureVersion, Page: *p})
	code(t, err, core.ErrVersionConflict)
	_, n, err := e.ListRevisions(ctx, l.ID, p.ID, 0, 50)
	if err != nil || n != 1 {
		t.Fatalf("history=%d %v", n, err)
	}
	tr, err := e.WriteTheme(ctx, core.WriteThemeInput{RequestID: "theme", LibraryID: l.ID, StructureVersion: l.StructureVersion, Theme: core.Theme{KnowledgeBaseID: l.ID, Name: "人工", Manual: true}})
	if err != nil {
		t.Fatal(err)
	}
	l, _ = e.GetLibrary(ctx, l.ID)
	p, _ = e.GetPage(ctx, l.ID, p.ID)
	p.FolderID = tr.ResourceID
	p.SortOrder = 7
	p.ManualPlacement = true
	_, err = e.WritePage(ctx, core.WritePageInput{RequestID: "move", LibraryID: l.ID, ExpectedVersion: p.Version, StructureVersion: l.StructureVersion, Page: *p})
	if err != nil {
		t.Fatal(err)
	}
	l, _ = e.GetLibrary(ctx, l.ID)
	p, _ = e.GetPage(ctx, l.ID, p.ID)
	_, err = e.RestorePage(ctx, core.RestorePageInput{RequestID: "restore", LibraryID: l.ID, PageID: p.ID, ExpectedVersion: p.Version, RevisionVersion: 1, StructureVersion: l.StructureVersion})
	if err != nil {
		t.Fatal(err)
	}
	p, _ = e.GetPage(ctx, l.ID, p.ID)
	if p.LastEditSource != "revert" {
		t.Fatalf("restore provenance = %q", p.LastEditSource)
	}
	if p.Content != "原正文" || p.FolderID != tr.ResourceID || p.SortOrder != 7 || !p.ManualPlacement {
		t.Fatalf("bad restore %+v", p)
	}
	p.Status = "archived"
	_, err = e.WritePage(ctx, core.WritePageInput{RequestID: "archive-page", LibraryID: l.ID, ExpectedVersion: p.Version, StructureVersion: l.StructureVersion, Page: *p})
	if err != nil {
		t.Fatal(err)
	}
	_, n, err = e.ListRevisions(ctx, l.ID, p.ID, 0, 50)
	if err != nil || n != 4 {
		t.Fatalf("archive lost history %d %v", n, err)
	}
	list, n, err := e.ListPages(ctx, core.PageQuery{LibraryID: l.ID, Query: "别名concept", Status: "archived"})
	if err != nil || n != 1 || len(list) != 1 {
		t.Fatalf("alias search %d %v", n, err)
	}
}
func TestCrossLibraryRelationsRejected(t *testing.T) {
	ctx := context.Background()
	e, _ := open(t)
	a := library(t, e, "a")
	b := library(t, e, "b")
	s := source(t, e, b, "b")
	p := page(t, e, a, "a")
	tr, err := e.WriteTheme(ctx, core.WriteThemeInput{RequestID: "theme-b", LibraryID: b.ID, StructureVersion: b.StructureVersion, Theme: core.Theme{KnowledgeBaseID: b.ID, Name: "B"}})
	if err != nil {
		t.Fatal(err)
	}
	p.FolderID = tr.ResourceID
	_, err = e.WritePage(ctx, core.WritePageInput{RequestID: "bad-theme", LibraryID: a.ID, ExpectedVersion: p.Version, StructureVersion: a.StructureVersion, Page: *p})
	code(t, err, core.ErrScopeViolation)
	p.FolderID = ""
	p.SourceRefs = []string{s.ID + "|B"}
	_, err = e.WritePage(ctx, core.WritePageInput{RequestID: "bad-source", LibraryID: a.ID, ExpectedVersion: p.Version, StructureVersion: a.StructureVersion, Page: *p})
	code(t, err, core.ErrScopeViolation)
	_, err = e.GetPage(ctx, b.ID, p.ID)
	code(t, err, core.ErrNotFound)
}
func accept(t *testing.T, e *core.Engine, l *core.Library, s *core.SourceVersion) *core.Job {
	t.Helper()
	r, err := e.AcceptJob(context.Background(), core.AcceptJobInput{RequestID: "job-" + s.ID, LibraryID: l.ID, ExpectedVersion: l.Version, StructureVersion: l.StructureVersion, SourceVersionIDs: []string{s.ID}, Permission: core.PermissionSnapshot{PolicyVersion: l.PolicyVersion, Operations: []string{"organize"}, Scope: []string{"/approved/project"}}, RulesVersion: "rules-v1", ConfigDigest: "config-v1"})
	if err != nil {
		t.Fatal(err)
	}
	j, err := e.GetJob(context.Background(), r.ResourceID)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func TestTwoEnginesContentCAS(t *testing.T) {
	ctx := context.Background()
	a, opt := open(t)
	b, err := core.Open(ctx, opt)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	l := library(t, a, "clients")
	p := page(t, a, l, "same")
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i, e := range []*core.Engine{a, b} {
		wg.Add(1)
		go func(i int, e *core.Engine) {
			defer wg.Done()
			next := *p
			next.Content = string(rune('a' + i))
			_, err := e.WritePage(ctx, core.WritePageInput{RequestID: next.Content, LibraryID: l.ID, ExpectedVersion: p.Version, StructureVersion: l.StructureVersion, Page: next})
			errs <- err
		}(i, e)
	}
	wg.Wait()
	close(errs)
	success, conflict := 0, 0
	for err := range errs {
		if err == nil {
			success++
		} else {
			var x *core.Error
			if errors.As(err, &x) && x.Code == core.ErrVersionConflict {
				conflict++
			} else {
				t.Fatal(err)
			}
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("success=%d conflict=%d", success, conflict)
	}
	_, n, _ := a.ListRevisions(ctx, l.ID, p.ID, 0, 50)
	if n != 1 {
		t.Fatal("history advanced without commit", n)
	}
}

func TestFailureSettlementIsDurableAndCannotRewritePublishedSources(t *testing.T) {
	ctx := context.Background()
	e, opt := open(t)
	l := library(t, e, "failure")
	raw := source(t, e, l, "failure")
	j := accept(t, e, l, raw)
	_, err := e.FailJob(ctx, core.FailJobInput{JobActionInput: core.JobActionInput{RequestID: "fail", JobID: j.ID, ExpectedVersion: j.Version}, Diagnostic: "reader unsupported", Sources: []core.SourceConclusion{{SourceVersionID: raw.ID, State: "failed", Diagnostic: "reader unsupported"}}})
	if err != nil {
		t.Fatal(err)
	}
	e.Close()
	e, err = core.Open(ctx, opt)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	j, _ = e.GetJob(ctx, j.ID)
	raw, _ = e.GetSource(ctx, l.ID, raw.ID)
	if j.State != "failed" || j.Diagnostic != "reader unsupported" || raw.State != "failed" {
		t.Fatalf("failure lost %+v %+v", j, raw)
	}
	r, err := e.RetryJob(ctx, core.JobActionInput{RequestID: "retry-job", JobID: j.ID, ExpectedVersion: j.Version})
	if err != nil {
		t.Fatal(err)
	}
	j, _ = e.GetJob(ctx, r.ResourceID)
	if j.Diagnostic != "" {
		t.Fatal("previous attempt diagnosis leaked into retry", j.Diagnostic)
	}
	if j.Attempt != 2 {
		t.Fatal("attempt did not advance")
	}
	other := library(t, e, "other-failure")
	outside := source(t, e, other, "outside")
	_, err = e.FailJob(ctx, core.FailJobInput{JobActionInput: core.JobActionInput{RequestID: "bad-fail", JobID: j.ID, ExpectedVersion: j.Version}, Diagnostic: "x", Sources: []core.SourceConclusion{{SourceVersionID: outside.ID, State: "failed", Diagnostic: "x"}}})
	code(t, err, core.ErrScopeViolation)
}

func TestJobFreezesPreferencesAndRejectsConcurrentLibraryWriter(t *testing.T) {
	ctx := context.Background()
	e, _ := open(t)
	l := library(t, e, "frozen")
	a := source(t, e, l, "first")
	b := source(t, e, l, "second")
	j := accept(t, e, l, a)
	if j.LibrarySnapshot == nil || j.LibrarySnapshot.WikiConfig != nil {
		t.Fatal("accepted library configuration was not frozen")
	}
	if _, err := e.AcceptJob(ctx, core.AcceptJobInput{RequestID: "concurrent-writer", LibraryID: l.ID, ExpectedVersion: l.Version, StructureVersion: l.StructureVersion, SourceVersionIDs: []string{b.ID}, Permission: core.PermissionSnapshot{PolicyVersion: l.PolicyVersion, Operations: []string{"organize"}}, RulesVersion: "test", ConfigDigest: "test"}); err == nil {
		t.Fatal("two library writers were accepted")
	}
}

func TestCreateLibraryRejectsRetiredSoftwareKind(t *testing.T) {
	e, _ := open(t)
	_, err := e.CreateLibrary(context.Background(), core.CreateLibraryInput{RequestID: "retired-software", Name: "旧软件类型", Kind: "software", WikiLanguage: "zh-CN"})
	code(t, err, core.ErrInvalid)
}
