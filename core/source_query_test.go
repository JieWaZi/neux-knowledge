package core_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/JieWaZi/neux-knowledge/core"
)

func TestSourceQueryFiltersBeforePagination(t *testing.T) {
	ctx := context.Background()
	e, opt := open(t)
	l := library(t, e, "source-query")
	db, err := sql.Open("sqlite", opt.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var matching []*core.SourceVersion
	for i := 0; i < 24; i++ {
		name := fmt.Sprintf("document-%02d", i)
		if i >= 20 {
			name = fmt.Sprintf("Match_%%-%02d", i)
		}
		s := source(t, e, l, name)
		if _, err = db.Exec("UPDATE knowledge_source_versions SET created_at = ? WHERE id = ?", i/2, s.ID); err != nil {
			t.Fatal(err)
		}
		if i >= 20 {
			matching = append(matching, s)
		}
	}
	if _, err = e.ArchiveSource(ctx, core.ArchiveSourceInput{RequestID: "archive-query", LibraryID: l.ID, SourceVersionID: matching[3].ID, ExpectedVersion: 1, Archived: true}); err != nil {
		t.Fatal(err)
	}
	got, n, err := e.ListSourcesQuery(ctx, l.ID, core.SourceQuery{Offset: 1, Limit: 1, Query: "mATCH_%", Sort: "oldest"})
	if err != nil || n != 3 || len(got) != 1 {
		t.Fatalf("filtered page=%v total=%d err=%v", got, n, err)
	}
	oldest, _, _ := e.ListSourcesQuery(ctx, l.ID, core.SourceQuery{Query: "match_%", Sort: "oldest"})
	newest, _, _ := e.ListSourcesQuery(ctx, l.ID, core.SourceQuery{Query: "match_%", Sort: "newest"})
	if len(oldest) != 3 || len(newest) != 3 {
		t.Fatal("literal matching failed")
	}
	for i := range oldest {
		if oldest[i].ID != newest[len(newest)-1-i].ID {
			t.Fatal("sort is not stable reverse")
		}
	}
	_, n, err = e.ListSourcesQuery(ctx, l.ID, core.SourceQuery{Query: "absent"})
	if err != nil || n != 0 {
		t.Fatalf("empty=%d %v", n, err)
	}
	_, n, err = e.ListSources(ctx, l.ID, 0, 100)
	if err != nil || n != 24 {
		t.Fatal("legacy excludes archives", n, err)
	}
	_, _, err = e.ListSourcesQuery(ctx, l.ID, core.SourceQuery{State: "invented"})
	code(t, err, core.ErrInvalid)
}

func TestSourceQueryUsesCurrentActivityBeforeCount(t *testing.T) {
	ctx := context.Background()
	e, opt := open(t)
	l := library(t, e, "activity-query")
	pending := source(t, e, l, "pending")
	retry := source(t, e, l, "retry")
	processing := source(t, e, l, "processing")
	done := source(t, e, l, "done")
	terminal := source(t, e, l, "terminal")
	dead := source(t, e, l, "dead")
	j := acceptIsolatedSources(t, e, l, retry, processing, done, terminal, dead)
	if err := e.EnsureOrganizeSourceWork(ctx, j.ID); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", opt.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec("UPDATE knowledge_source_versions SET state = 'failed', diagnostic = 'old failure' WHERE id IN (?,?)", retry.ID, done.ID)
	exec("UPDATE knowledge_source_versions SET state = 'processed' WHERE id = ?", terminal.ID)
	exec("UPDATE knowledge_organize_source_work SET state = 'done' WHERE source_version_id = ?", done.ID)
	exec("UPDATE knowledge_organize_source_work SET state = 'dead', diagnostic = 'current failure' WHERE source_version_id = ?", dead.ID)
	exec("UPDATE knowledge_organize_source_work SET started_at = 10 WHERE source_version_id = ?", processing.ID)
	exec("INSERT INTO knowledge_organize_source_work (id,job_id,job_attempt,source_version_id,state,started_at) VALUES ('old-attempt',?,?,?,'pending',10)", j.ID, j.Attempt-1, retry.ID)
	for state, want := range map[string][]string{"pending": {pending.ID}, "queued": {retry.ID}, "processing": {processing.ID}, "failed": {done.ID, dead.ID}, "processed": {terminal.ID}} {
		got, n, err := e.ListSourcesQuery(ctx, l.ID, core.SourceQuery{State: state, Limit: 1})
		if err != nil || n != int64(len(want)) || len(got) != 1 || got[0].State != state {
			t.Fatalf("state %s got=%+v total=%d err=%v", state, got, n, err)
		}
	}
	raw, _ := e.GetSource(ctx, l.ID, retry.ID)
	projected, err := e.SourceActivity(ctx, l.ID, []*core.SourceVersion{raw})
	if err != nil || projected[0].State != "queued" || projected[0].Diagnostic != "" || raw.State != "failed" {
		t.Fatalf("projection mutated raw or failed %+v %+v %v", raw, projected, err)
	}
}

func TestSourceQueryUnicodeAndTerminalStates(t *testing.T) {
	ctx := context.Background()
	e, opt := open(t)
	l := library(t, e, "unicode-query")
	s := source(t, e, l, "ÉCOLE_百分%")
	got, n, err := e.ListSourcesQuery(ctx, l.ID, core.SourceQuery{Query: "école_百分%"})
	if err != nil || n != 1 || len(got) != 1 {
		t.Fatalf("unicode literal query total=%d items=%v err=%v", n, got, err)
	}
	db, err := sql.Open("sqlite", opt.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for raw, filter := range map[string]string{"no-change": "unchanged", "cancelled": "cancelled"} {
		if _, err = db.Exec("UPDATE knowledge_source_versions SET state = ? WHERE id = ?", raw, s.ID); err != nil {
			t.Fatal(err)
		}
		got, n, err = e.ListSourcesQuery(ctx, l.ID, core.SourceQuery{State: filter})
		if err != nil || n != 1 || got[0].State != raw {
			t.Fatalf("state=%s total=%d err=%v", filter, n, err)
		}
	}
}
