package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	types "github.com/JieWaZi/neux-knowledge/internal/domain"
)

func sourceWorkFixture(t *testing.T, extraSources ...string) (*Store, *types.Library, *types.SourceVersion, *types.Job) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(ctx, filepath.Join(dir, "work.db"), filepath.Join(dir, "raw"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	l, err := s.CreateLibrary(ctx, types.CreateLibraryInput{RequestID: "work-library", Name: "重排", Kind: "general", WikiLanguage: "zh-CN"})
	if err != nil {
		t.Fatal(err)
	}
	library, _ := s.GetLibrary(ctx, l.ResourceID)
	raw, err := s.ImportSource(ctx, types.ImportSourceInput{RequestID: "work-source", LibraryID: library.ID, Filename: "work.md", Bytes: []byte("实际来源")})
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{raw.ResourceID}
	for _, name := range extraSources {
		additional, err := s.ImportSource(ctx, types.ImportSourceInput{RequestID: "work-source-" + name, LibraryID: library.ID, Filename: name + ".md", Bytes: []byte("来源" + name)})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, additional.ResourceID)
	}
	job, err := s.AcceptJob(ctx, types.AcceptJobInput{RequestID: "work-job", LibraryID: library.ID, ExpectedVersion: library.Version, StructureVersion: library.StructureVersion, SourceVersionIDs: ids, Permission: types.PermissionSnapshot{PolicyVersion: library.PolicyVersion, Operations: []string{"organize"}}, RulesVersion: "rules", ConfigDigest: "config"})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.EnsureOrganizeSourceWork(ctx, job.JobID); err != nil {
		t.Fatal(err)
	}
	version, err := s.GetSource(ctx, library.ID, raw.ResourceID)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := s.GetJob(ctx, job.JobID)
	if err != nil {
		t.Fatal(err)
	}
	return s, library, version, accepted
}

func TestSourceWorkRetryBudgetUsesPersistentCASAndReceipts(t *testing.T) {
	ctx := context.Background()
	queue, _, raw, job := sourceWorkFixture(t)
	for count := 1; count <= 6; count++ {
		in := types.SourceWorkActionInput{JobAttempt: 1, RequestID: fmt.Sprintf("work-defer-%d", count), JobID: job.ID, SourceVersionID: raw.ID, ExpectedVersion: int64(count), Diagnostic: "reduce failed", NotBefore: 1800000000000 + int64(count)*5000}
		receipt, err := queue.DeferOrganizeSourceWork(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		replay, err := queue.DeferOrganizeSourceWork(ctx, in)
		if err != nil || replay.Version != receipt.Version {
			t.Fatalf("receipt replay=%+v %v", replay, err)
		}
		items, err := queue.ListOrganizeSourceWork(ctx, job.ID)
		wantState := "pending"
		if count == 6 {
			wantState = "dead"
		}
		if err != nil || len(items) != 1 || items[0].State != wantState || items[0].FailureCount != count || items[0].Version != int64(count+1) || items[0].NotBefore != in.NotBefore {
			t.Fatalf("retry queue=%+v %v", items, err)
		}
	}
}

func TestDeletingLibraryRemovesSourceWorkDiagnostics(t *testing.T) {
	ctx := context.Background()
	s, library, source, job := sourceWorkFixture(t)
	if _, err := s.DeferOrganizeSourceWork(ctx, types.SourceWorkActionInput{JobAttempt: 1, RequestID: "work-private-diagnostic", JobID: job.ID, SourceVersionID: source.ID, ExpectedVersion: 1, Diagnostic: "PRIVATE_DOCUMENT_COPY", NotBefore: 1800000005000}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FailJob(ctx, types.FailJobInput{JobActionInput: types.JobActionInput{RequestID: "fail-work-for-delete", JobID: job.ID, ExpectedVersion: job.Version}, Diagnostic: "failed", Sources: []types.SourceConclusion{{SourceVersionID: source.ID, State: "failed", Diagnostic: "failed"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteLibrary(ctx, types.DeleteLibraryInput{RequestID: "delete-work-library", LibraryID: library.ID, ExpectedVersion: library.Version}); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := s.db.Model(&types.OrganizeSourceWork{}).Where("library_id = ?", library.ID).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("deleted library retained source retry diagnostics: %d %v", count, err)
	}
}

func TestPurgingSourceRemovesSourceWorkInternalCopies(t *testing.T) {
	ctx := context.Background()
	s, library, source, job := sourceWorkFixture(t)
	if _, err := s.DeferOrganizeSourceWork(ctx, types.SourceWorkActionInput{JobAttempt: 1, RequestID: "work-private-diagnostic", JobID: job.ID, SourceVersionID: source.ID, ExpectedVersion: 1, Diagnostic: "PRIVATE_DOCUMENT_COPY", NotBefore: 1800000005000}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FailJob(ctx, types.FailJobInput{JobActionInput: types.JobActionInput{RequestID: "fail-work-for-purge", JobID: job.ID, ExpectedVersion: job.Version}, Diagnostic: "failed", Sources: []types.SourceConclusion{{SourceVersionID: source.ID, State: "failed", Diagnostic: "failed"}}}); err != nil {
		t.Fatal(err)
	}
	preview, err := s.PreviewSourcePurge(ctx, types.PurgePreviewInput{LibraryID: library.ID, SourceID: source.SourceID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ConfirmSourcePurge(ctx, types.ConfirmSourcePurgeInput{RequestID: "purge-source-work", LibraryID: library.ID, SourceID: source.SourceID, PreviewDigest: preview.Digest, Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err = s.db.Model(&types.OrganizeSourceWork{}).Where("library_id = ?", library.ID).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("purged source retained internal retry copies: %d %v", count, err)
	}
}

func TestSourceWorkRejectsLatePreviousJobAttempt(t *testing.T) {
	ctx := context.Background()
	s, _, source, job := sourceWorkFixture(t)
	if _, err := s.FailJob(ctx, types.FailJobInput{JobActionInput: types.JobActionInput{RequestID: "old-attempt-failure", JobID: job.ID, ExpectedVersion: job.Version}, Diagnostic: "failed", Sources: []types.SourceConclusion{{SourceVersionID: source.ID, State: "failed", Diagnostic: "failed"}}}); err != nil {
		t.Fatal(err)
	}
	job, err := s.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.RetryJob(ctx, types.JobActionInput{RequestID: "new-attempt", JobID: job.ID, ExpectedVersion: job.Version}); err != nil {
		t.Fatal(err)
	}
	if err = s.EnsureOrganizeSourceWork(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	for _, attempt := range []int{0, 1} {
		in := types.SourceWorkActionInput{JobAttempt: attempt, RequestID: fmt.Sprintf("late-old-attempt-%d", attempt), JobID: job.ID, SourceVersionID: source.ID, ExpectedVersion: 1, Diagnostic: "late failure", NotBefore: 1800000005000}
		_, err = s.DeferOrganizeSourceWork(ctx, in)
		var domainErr *types.Error
		if !errors.As(err, &domainErr) || domainErr.Code != types.ErrStaleAttempt {
			t.Fatalf("old attempt changed new document queue: %v", err)
		}
	}
	items, err := s.ListOrganizeSourceWork(ctx, job.ID)
	if err != nil || len(items) != 1 || items[0].JobAttempt != 2 || items[0].FailureCount != 0 || items[0].Version != 1 {
		t.Fatalf("late attempt changed queue=%+v %v", items, err)
	}
}
