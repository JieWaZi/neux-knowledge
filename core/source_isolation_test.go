package core_test

import (
	"context"
	"testing"

	"github.com/JieWaZi/neux-knowledge/core"
)

func acceptIsolatedSources(t *testing.T, e *core.Engine, l *core.Library, sources ...*core.SourceVersion) *core.Job {
	t.Helper()
	ids := []string{}
	for _, source := range sources {
		ids = append(ids, source.ID)
	}
	r, err := e.AcceptJob(context.Background(), core.AcceptJobInput{RequestID: "isolated-job", LibraryID: l.ID, ExpectedVersion: l.Version, StructureVersion: l.StructureVersion, SourceVersionIDs: ids, Permission: core.PermissionSnapshot{PolicyVersion: l.PolicyVersion, Operations: []string{"organize"}}, RulesVersion: "rules", ConfigDigest: "config"})
	if err != nil {
		t.Fatal(err)
	}
	j, err := e.GetJob(context.Background(), r.JobID)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func makeSourceUnavailable(t *testing.T, e *core.Engine, source *core.SourceVersion, kind string) {
	t.Helper()
	var err error
	if kind == "archive" {
		_, err = e.ArchiveSource(context.Background(), core.ArchiveSourceInput{RequestID: "isolate-archive-" + source.ID, LibraryID: source.LibraryID, SourceVersionID: source.ID, ExpectedVersion: source.RowVersion, Archived: true})
	} else {
		_, err = e.ImportSource(context.Background(), core.ImportSourceInput{RequestID: "isolate-replace-" + source.ID, LibraryID: source.LibraryID, SourceID: source.SourceID, ExpectedVersion: source.Version, Filename: "replacement.md", Bytes: []byte("新版本原件")})
	}
	if err != nil {
		t.Fatal(err)
	}
}

func TestSourceWorkCanRecordItsInactiveSourceFailure(t *testing.T) {
	for _, kind := range []string{"archive", "replace"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			e, _ := open(t)
			l := library(t, e, "work-inactive")
			raw := source(t, e, l, "work-inactive")
			j := acceptIsolatedSources(t, e, l, raw)
			makeSourceUnavailable(t, e, raw, kind)
			if err := e.EnsureOrganizeSourceWork(ctx, j.ID); err != nil {
				t.Fatal("inactive source prevented work registration", err)
			}
			_, err := e.DeferOrganizeSourceWork(ctx, core.SourceWorkActionInput{RequestID: "defer-inactive", JobID: j.ID, JobAttempt: 1, SourceVersionID: raw.ID, ExpectedVersion: 1, Diagnostic: "来源已失活", NotBefore: 1800000005000})
			if err != nil {
				t.Fatal("inactive source prevented recording failure", err)
			}
		})
	}
}
