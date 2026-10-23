package core_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/JieWaZi/neux-knowledge/core"
)

func TestJobQueueAcceptsDistinctSourcesAndProjectsEveryQueuedFile(t *testing.T) {
	ctx := context.Background()
	e, options := open(t)
	lib := library(t, e, "queue")
	first := source(t, e, lib, "first")
	second := source(t, e, lib, "second")
	third := source(t, e, lib, "third")
	job := accept(t, e, lib, first)
	input := core.AcceptJobInput{RequestID: "second-job", LibraryID: lib.ID, ExpectedVersion: lib.Version, StructureVersion: lib.StructureVersion, SourceVersionIDs: []string{second.ID}, Permission: job.Permission, RulesVersion: "rules", ConfigDigest: "config"}
	queued, err := e.AcceptJob(ctx, input)
	if err != nil {
		t.Fatal("distinct files must queue", err)
	}
	replay, err := e.AcceptJob(ctx, input)
	if err != nil || replay.JobID != queued.JobID {
		t.Fatal("request replay duplicated queue", replay, err)
	}
	rows, err := e.SourceActivity(ctx, lib.ID, []*core.SourceVersion{first, second, third})
	if err != nil || rows[0].State != "queued" || rows[1].State != "queued" || rows[2].State != "pending" {
		t.Fatal("queue state is incomplete", rows, err)
	}
	filtered, total, err := e.ListSourcesQuery(ctx, lib.ID, core.SourceQuery{State: "queued", Limit: 10})
	if err != nil || total != 2 || len(filtered) != 2 {
		t.Fatal("queued filter lost waiting sources", filtered, total, err)
	}
	input.RequestID = "overlap"
	input.SourceVersionIDs = []string{second.ID, third.ID}
	_, err = e.AcceptJob(ctx, input)
	code(t, err, core.ErrVersionConflict)
	_, total, err = e.ListJobs(ctx, lib.ID, 0, 100)
	if err != nil || total != 2 {
		t.Fatal("overlap partially accepted", total, err)
	}
	secondJob, _ := e.GetJob(ctx, queued.JobID)
	if secondJob.AttemptStartedAt <= job.AttemptStartedAt {
		t.Fatal("queue order is not monotonic")
	}
	head, err := e.GetActiveJob(ctx, lib.ID)
	if err != nil || head == nil || head.ID != job.ID {
		t.Fatal("queue head changed", head, err)
	}
	if err = e.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := core.Open(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	head, err = reopened.GetActiveJob(ctx, lib.ID)
	if err != nil || head == nil || head.ID != job.ID {
		t.Fatal("restart lost queue order", head, err)
	}
	_, err = reopened.FailOrganize(ctx, job.ID, job.Attempt, "first failed")
	if err != nil {
		t.Fatal(err)
	}
	head, err = reopened.GetActiveJob(ctx, lib.ID)
	if err != nil || head == nil || head.ID != queued.JobID {
		t.Fatal("failed head did not yield", head, err)
	}
}

func TestJobQueueRejectsLogicalSourceOverlapAndRetry(t *testing.T) {
	ctx := context.Background()
	e, _ := open(t)
	lib := library(t, e, "overlap")
	src := source(t, e, lib, "first")
	job := accept(t, e, lib, src)
	if _, err := e.FailOrganize(ctx, job.ID, job.Attempt, "failure"); err != nil {
		t.Fatal(err)
	}
	input := core.AcceptJobInput{RequestID: "replacement-job", LibraryID: lib.ID, ExpectedVersion: lib.Version, StructureVersion: lib.StructureVersion, SourceVersionIDs: []string{src.ID}, Permission: job.Permission, RulesVersion: "rules", ConfigDigest: "config"}
	if _, err := e.AcceptJob(ctx, input); err != nil {
		t.Fatal(err)
	}
	failed, _ := e.GetJob(ctx, job.ID)
	_, err := e.RetryJob(ctx, core.JobActionInput{RequestID: "retry-overlap", JobID: job.ID, ExpectedVersion: failed.Version})
	code(t, err, core.ErrVersionConflict)
	newVersion, err := e.ImportSource(ctx, core.ImportSourceInput{RequestID: "new-version", LibraryID: lib.ID, SourceID: src.SourceID, ExpectedVersion: src.Version, Filename: src.Filename, Bytes: []byte("updated")})
	if err != nil {
		t.Fatal(err)
	}
	input.RequestID = "overlap-other-version"
	input.SourceVersionIDs = []string{newVersion.ResourceID}
	_, err = e.AcceptJob(ctx, input)
	code(t, err, core.ErrVersionConflict)
}

func TestJobQueueConcurrentDuplicateAcceptsOnlyOnce(t *testing.T) {
	ctx := context.Background()
	e, _ := open(t)
	lib := library(t, e, "concurrent-queue")
	src := source(t, e, lib, "one")
	var wait sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			_, err := e.AcceptJob(ctx, core.AcceptJobInput{RequestID: fmt.Sprintf("concurrent-%d", index), LibraryID: lib.ID, ExpectedVersion: lib.Version, StructureVersion: lib.StructureVersion, SourceVersionIDs: []string{src.ID}, Permission: core.PermissionSnapshot{PolicyVersion: lib.PolicyVersion, Operations: []string{"organize"}}, RulesVersion: "rules", ConfigDigest: "config"})
			results <- err
		}(i)
	}
	wait.Wait()
	close(results)
	accepted := 0
	for err := range results {
		if err == nil {
			accepted++
		} else {
			code(t, err, core.ErrVersionConflict)
		}
	}
	if accepted != 1 {
		t.Fatalf("accepted=%d, want one", accepted)
	}
}

func TestJobQueueTerminalSourceDisplayStillReportsReservation(t *testing.T) {
	ctx := context.Background()
	e, opt := open(t)
	lib := library(t, e, "mixed-queue")
	first := source(t, e, lib, "failed-in-active-job")
	second := source(t, e, lib, "processed-in-active-job")
	third := source(t, e, lib, "still-processing")
	input := core.AcceptJobInput{RequestID: "mixed", LibraryID: lib.ID, ExpectedVersion: lib.Version, StructureVersion: lib.StructureVersion, SourceVersionIDs: []string{first.ID, second.ID, third.ID}, Permission: core.PermissionSnapshot{PolicyVersion: lib.PolicyVersion, Operations: []string{"organize"}}, RulesVersion: "rules", ConfigDigest: "config"}
	accepted, err := e.AcceptJob(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", opt.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec("INSERT INTO knowledge_organize_source_work (id,library_id,job_id,job_attempt,source_version_id,state,diagnostic) VALUES (?,?,?,?,?,'dead','failed in this attempt')", "dead-work", lib.ID, accepted.JobID, 1, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("UPDATE knowledge_source_versions SET state='failed' WHERE id=?", first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("UPDATE knowledge_source_versions SET state='processed' WHERE id=?", second.ID); err != nil {
		t.Fatal(err)
	}
	rows, _, err := e.ListSourcesQuery(ctx, lib.ID, core.SourceQuery{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		raw, _ := json.Marshal(row)
		var fields map[string]any
		if err = json.Unmarshal(raw, &fields); err != nil {
			t.Fatal(err)
		}
		if fields["organizeBlocked"] != true {
			t.Fatalf("active task reservation missing: %s", raw)
		}
		if row.ID == first.ID && row.State != "failed" {
			t.Fatal("failure display was lost", row)
		}
		if row.ID == second.ID && row.State != "processed" {
			t.Fatal("success display was lost", row)
		}
	}
}
