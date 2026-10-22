package core_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/JieWaZi/neux-knowledge/core"
)

func TestRebuildResetsOnlyExplicitDocumentAndReplayDoesNotClearNewWork(t *testing.T) {
	ctx := context.Background()
	e, options := open(t)
	lib := library(t, e, "rebuild")
	src := source(t, e, lib, "chosen")
	other := source(t, e, lib, "untouched")
	preserved := page(t, e, lib, "concept/manual")
	lib, _ = e.GetLibrary(ctx, lib.ID)
	job := accept(t, e, lib, src)
	raw, _ := e.ReadRaw(ctx, lib.ID, src.ID)
	artifact, err := (core.MarkdownReader{}).Read(ctx, core.ReadRequest{SourceID: src.SourceID, SourceVersionID: src.ID, SHA256: src.SHA256, Filename: src.Filename, MIME: src.MIME, Raw: raw, Scope: "all"})
	if err != nil {
		t.Fatal(err)
	}
	if err = e.IndexDocument(ctx, job.ID, src.ID, artifact, core.DocumentEmbedder{}); err != nil {
		t.Fatal(err)
	}
	seeded, err := e.ListDocumentChunks(ctx, lib.ID, src.ID)
	if err != nil || len(seeded) == 0 {
		t.Fatal("test did not seed old derived chunks", err)
	}
	db, err := sql.Open("sqlite", options.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec("UPDATE knowledge_jobs SET state='completed' WHERE id=?", job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("UPDATE knowledge_source_versions SET state='processed', description='old summary', summary_status='completed', diagnostic='old failure' WHERE id=?", src.ID); err != nil {
		t.Fatal(err)
	}
	before, _ := e.GetSource(ctx, lib.ID, src.ID)
	input := core.AcceptJobInput{RequestID: "ordinary", LibraryID: lib.ID, ExpectedVersion: lib.Version, StructureVersion: lib.StructureVersion, SourceVersionIDs: []string{src.ID}, Permission: core.PermissionSnapshot{PolicyVersion: lib.PolicyVersion, Operations: []string{"organize"}}, RulesVersion: "rules", ConfigDigest: "config"}
	ordinary, err := e.AcceptJob(ctx, input)
	if err != nil || ordinary.State != "no-op" {
		t.Fatal("ordinary organize changed processed source", ordinary, err)
	}
	input.RequestID = "rebuild"
	input.Rebuild = true
	receipt, err := e.AcceptJob(ctx, input)
	if err != nil || receipt.JobID == "" {
		t.Fatal("rebuild skipped processed source", receipt, err)
	}
	changed, _ := e.GetSource(ctx, lib.ID, src.ID)
	if changed.State != "pending" || changed.Description != "" || changed.Profile != nil || changed.SummaryStatus != "" || changed.Diagnostic != "" || changed.RowVersion != before.RowVersion+1 {
		t.Fatal("old parse state survived rebuild", changed)
	}
	chunks, err := e.ListDocumentChunks(ctx, lib.ID, src.ID)
	if err != nil || len(chunks) != 0 {
		t.Fatal("old index survived rebuild", chunks, err)
	}
	actual, _ := e.ReadRaw(ctx, lib.ID, src.ID)
	if string(actual) != string(raw) {
		t.Fatal("rebuild changed immutable bytes")
	}
	freshPage, _ := e.GetPage(ctx, lib.ID, preserved.ID)
	if freshPage.Content != preserved.Content || freshPage.Version != preserved.Version {
		t.Fatal("rebuild changed maintained wiki")
	}
	untouched, _ := e.GetSource(ctx, lib.ID, other.ID)
	if untouched.RowVersion != other.RowVersion {
		t.Fatal("rebuild reset another document")
	}
	if _, err = db.Exec("UPDATE knowledge_source_versions SET description='new progress' WHERE id=?", src.ID); err != nil {
		t.Fatal(err)
	}
	replay, err := e.AcceptJob(ctx, input)
	if err != nil || replay.JobID != receipt.JobID {
		t.Fatal("rebuild replay created another job", err)
	}
	replayed, _ := e.GetSource(ctx, lib.ID, src.ID)
	if replayed.Description != "new progress" || replayed.RowVersion != changed.RowVersion {
		t.Fatal("replay reset new work", replayed)
	}
	input.RequestID = "blocked-rebuild"
	_, err = e.AcceptJob(ctx, input)
	code(t, err, core.ErrVersionConflict)
	blocked, _ := e.GetSource(ctx, lib.ID, src.ID)
	if blocked.Description != "new progress" || blocked.RowVersion != changed.RowVersion {
		t.Fatal("rejected rebuild mutated source", blocked)
	}
}
