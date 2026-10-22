package core_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/JieWaZi/neux-knowledge/core"
)

func TestSourceWorkQueuePersistsOnlyFrozenSources(t *testing.T) {
	ctx := context.Background()
	e, opt := open(t)
	l := library(t, e, "source-work")
	raw := source(t, e, l, "frozen")
	_ = source(t, e, l, "not-frozen")
	j := accept(t, e, l, raw)
	for range 2 {
		if err := e.EnsureOrganizeSourceWork(ctx, j.ID); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", opt.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count, attempt, failures, version int
	var sourceID, state string
	if err = db.QueryRow("SELECT COUNT(*), source_version_id, job_attempt, failure_count, state, version FROM knowledge_organize_source_work WHERE job_id = ?", j.ID).Scan(&count, &sourceID, &attempt, &failures, &state, &version); err != nil {
		t.Fatal(err)
	}
	if count != 1 || sourceID != raw.ID || attempt != 1 || failures != 0 || state != "pending" || version != 1 {
		t.Fatalf("frozen retry work not durable or duplicated: %d %s %d %d %s %d", count, sourceID, attempt, failures, state, version)
	}
}

func TestSourceWorkFailureBudgetAndReceiptSurviveReopen(t *testing.T) {
	ctx := context.Background()
	e, opt := open(t)
	l := library(t, e, "source-work-budget")
	raw := source(t, e, l, "source-work-budget")
	j := accept(t, e, l, raw)
	if err := e.EnsureOrganizeSourceWork(ctx, j.ID); err != nil {
		t.Fatal(err)
	}
	q := e
	for count := 1; count <= 6; count++ {
		items, err := q.ListOrganizeSourceWork(ctx, j.ID)
		if err != nil || len(items) != 1 {
			t.Fatalf("queue=%+v %v", items, err)
		}
		in := core.SourceWorkActionInput{JobAttempt: 1, RequestID: fmt.Sprintf("source-defer-%d", count), JobID: j.ID, SourceVersionID: raw.ID, ExpectedVersion: items[0].Version, Diagnostic: "reduce failed", NotBefore: 1800000000000 + int64(count)*5000}
		r, err := q.DeferOrganizeSourceWork(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		replayed, err := q.DeferOrganizeSourceWork(ctx, in)
		if err != nil || replayed.ResourceID != r.ResourceID || replayed.Version != r.Version {
			t.Fatalf("defer receipt replay=%+v %v", replayed, err)
		}
		items, err = q.ListOrganizeSourceWork(ctx, j.ID)
		wantState := "pending"
		if count == 6 {
			wantState = "dead"
		}
		if err != nil || len(items) != 1 || items[0].FailureCount != count || items[0].Version != int64(count+1) || items[0].State != wantState || items[0].NotBefore != in.NotBefore || items[0].Diagnostic != "reduce failed" {
			t.Fatalf("failure budget not preserved: %+v %v", items, err)
		}
		if count == 3 {
			if err = e.Close(); err != nil {
				t.Fatal(err)
			}
			e, err = core.Open(ctx, opt)
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			q = e
			if err = e.EnsureOrganizeSourceWork(ctx, j.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	items, _ := q.ListOrganizeSourceWork(ctx, j.ID)
	_, err := q.DeferOrganizeSourceWork(ctx, core.SourceWorkActionInput{JobAttempt: 1, RequestID: "dead-cannot-requeue", JobID: j.ID, SourceVersionID: raw.ID, ExpectedVersion: items[0].Version, Diagnostic: "another failure", NotBefore: 1800000060000})
	code(t, err, core.ErrVersionConflict)
	after, _ := e.GetSource(ctx, l.ID, raw.ID)
	if after.State != "pending" || after.RowVersion != raw.RowVersion {
		t.Fatalf("retry queue rewrote source facts: %+v", after)
	}
}

func TestSourceWorkRejectsForeignStaleAndCancelledUpdates(t *testing.T) {
	ctx := context.Background()
	e, _ := open(t)
	l := library(t, e, "source-work-fences")
	raw := source(t, e, l, "source-work-fences")
	outside := source(t, e, l, "outside-work")
	j := accept(t, e, l, raw)
	if err := e.EnsureOrganizeSourceWork(ctx, j.ID); err != nil {
		t.Fatal(err)
	}
	q := e
	in := core.SourceWorkActionInput{JobAttempt: 1, RequestID: "outside-work", JobID: j.ID, SourceVersionID: outside.ID, ExpectedVersion: 1, Diagnostic: "failed", NotBefore: 1800000005000}
	_, err := q.DeferOrganizeSourceWork(ctx, in)
	code(t, err, core.ErrScopeViolation)
	in.RequestID, in.SourceVersionID, in.ExpectedVersion = "stale-work", raw.ID, 2
	_, err = q.DeferOrganizeSourceWork(ctx, in)
	code(t, err, core.ErrVersionConflict)
	if _, err = e.CancelOrganize(ctx, core.JobActionInput{RequestID: "cancel-source-work", JobID: j.ID, ExpectedVersion: j.Version}); err != nil {
		t.Fatal(err)
	}
	in.RequestID, in.ExpectedVersion = "cancelled-work", 1
	_, err = q.DeferOrganizeSourceWork(ctx, in)
	code(t, err, core.ErrCancelled)
	items, err := q.ListOrganizeSourceWork(ctx, j.ID)
	if err != nil || len(items) != 1 || items[0].Version != 1 || items[0].FailureCount != 0 {
		t.Fatalf("invalid update mutated queue=%+v %v", items, err)
	}
	for _, id := range []string{"outside-work", "stale-work", "cancelled-work"} {
		if _, err := e.GetReceipt(ctx, id); err == nil {
			t.Fatal("rejected defer produced receipt", id)
		}
	}
}
