package core_test

import (
	"context"
	"github.com/JieWaZi/neux-knowledge/core"
	"testing"
)

func TestJobsForActorFiltersBeforeCountAndPagination(t *testing.T) {
	e, _ := open(t)
	defer e.Close()
	ctx := context.Background()
	lib := library(t, e, "job-actor")
	source, err := e.ImportSource(ctx, core.ImportSourceInput{RequestID: "source", LibraryID: lib.ID, Filename: "source.md", Bytes: []byte("原件")})
	if err != nil {
		t.Fatal(err)
	}
	for _, actor := range []string{"key-a", "key-b"} {
		receipt, err := e.AcceptJob(core.WithActivityActor(ctx, core.ActivityActor{UserID: actor, Trigger: "api"}), core.AcceptJobInput{RequestID: actor, LibraryID: lib.ID, ExpectedVersion: lib.Version, StructureVersion: lib.StructureVersion, SourceVersionIDs: []string{source.ResourceID}, Permission: core.PermissionSnapshot{PolicyVersion: lib.PolicyVersion, Operations: []string{"organize"}}, RulesVersion: "rules", ConfigDigest: "config"})
		if err != nil {
			t.Fatal(err)
		}
		job, _ := e.GetJob(ctx, receipt.JobID)
		if _, err = e.CancelOrganize(ctx, core.JobActionInput{RequestID: "cancel-" + actor, JobID: job.ID, ExpectedVersion: job.Version}); err != nil {
			t.Fatal(err)
		}
	}
	rows, total, err := e.ListJobsForActor(ctx, lib.ID, "key-a", 0, 1)
	if err != nil || total != 1 || len(rows) != 1 || rows[0].ActivityActor.UserID != "key-a" {
		t.Fatal(rows, total, err)
	}
	rows, total, err = e.ListJobsForActor(ctx, lib.ID, "other", 0, 1)
	if err != nil || total != 0 || len(rows) != 0 {
		t.Fatal("other actor count leaked", rows, total, err)
	}
	if _, _, err = e.ListJobsForActor(ctx, lib.ID, "", 0, 10); err == nil {
		t.Fatal("empty actor enumerated whole library")
	}
}
