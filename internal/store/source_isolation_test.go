package store

import (
	"context"
	"errors"
	"testing"

	types "github.com/JieWaZi/neux-knowledge/internal/domain"
)

func TestDocumentChunksValidateOnlyTargetSource(t *testing.T) {
	for _, operation := range []string{"save", "complete"} {
		for _, kind := range []string{"archive", "replace"} {
			for _, relevant := range []bool{false, true} {
				name := operation + "/" + kind + "/unrelated"
				if relevant {
					name = operation + "/" + kind + "/target"
				}
				t.Run(name, func(t *testing.T) {
					ctx := context.Background()
					s, library, target, job := sourceWorkFixture(t, "sibling")
					siblingID := ""
					for _, id := range job.SourceVersionIDs {
						if id != target.ID {
							siblingID = id
						}
					}
					sibling, err := s.GetSource(ctx, library.ID, siblingID)
					if err != nil {
						t.Fatal(err)
					}
					rows := []types.EmbeddingChunkIndex{{ID: "isolated-chunk", ChunkID: "child-1", ChunkType: "child", State: "pending", EmbeddingModelID: "model", Fingerprint: "fp", Dimensions: 2, Vector: []float32{1, 0}}}
					if operation == "complete" {
						if err = s.SaveDocumentChunks(ctx, job.ID, target.ID, rows); err != nil {
							t.Fatal(err)
						}
					}
					changed := sibling
					if relevant {
						changed = target
					}
					if kind == "archive" {
						_, err = s.ArchiveSource(ctx, types.ArchiveSourceInput{RequestID: "inactive-for-index", LibraryID: library.ID, SourceVersionID: changed.ID, ExpectedVersion: changed.RowVersion, Archived: true})
					} else {
						_, err = s.ImportSource(ctx, types.ImportSourceInput{RequestID: "inactive-for-index", LibraryID: library.ID, SourceID: changed.SourceID, ExpectedVersion: changed.Version, Filename: "new.md", Bytes: []byte("新原件")})
					}
					if err != nil {
						t.Fatal(err)
					}
					if operation == "save" {
						err = s.SaveDocumentChunks(ctx, job.ID, target.ID, rows)
					} else {
						err = s.CompleteDocumentChunks(ctx, job.ID, target.ID, rows)
					}
					if relevant {
						var typed *types.Error
						if !errors.As(err, &typed) || typed.Code != types.ErrSourceUnavailable {
							t.Fatalf("inactive index target accepted: %v", err)
						}
					} else if err != nil {
						t.Fatal("unrelated source blocked target index", err)
					}
				})
			}
		}
	}
}

func TestDocumentChunksRejectUnfrozenSourceEvenWithNoRows(t *testing.T) {
	ctx := context.Background()
	s, library, _, job := sourceWorkFixture(t)
	outside, err := s.ImportSource(ctx, types.ImportSourceInput{RequestID: "outside-index", LibraryID: library.ID, Filename: "outside.md", Bytes: []byte("未冻结")})
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range []func(context.Context, string, string, []types.EmbeddingChunkIndex) error{s.SaveDocumentChunks, s.CompleteDocumentChunks} {
		err = operation(ctx, job.ID, outside.ResourceID, nil)
		var typed *types.Error
		if !errors.As(err, &typed) || typed.Code != types.ErrScopeViolation {
			t.Fatalf("unfrozen index source accepted: %v", err)
		}
	}
}

func TestValidateOrganizeSourceSeparatesAvailabilityFromScopeAndAttempt(t *testing.T) {
	ctx := context.Background()
	s, library, target, job := sourceWorkFixture(t, "sibling")
	if err := s.ValidateOrganizeSource(ctx, job.ID, job.Attempt, target.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ArchiveSource(ctx, types.ArchiveSourceInput{RequestID: "archive-preflight", LibraryID: library.ID, SourceVersionID: target.ID, ExpectedVersion: target.RowVersion, Archived: true}); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []struct {
		attempt int             // Task attempt declared by this preflight.
		source  string          // Actual source version for this preflight.
		code    types.ErrorCode // Expected distinct failure category.
	}{{job.Attempt, target.ID, types.ErrSourceUnavailable}, {0, target.ID, types.ErrStaleAttempt}, {job.Attempt, "outside", types.ErrScopeViolation}} {
		err := s.ValidateOrganizeSource(ctx, job.ID, scenario.attempt, scenario.source)
		var typed *types.Error
		if !errors.As(err, &typed) || typed.Code != scenario.code {
			t.Fatalf("preflight error=%v want=%s", err, scenario.code)
		}
	}
	current, err := s.GetJob(ctx, job.ID)
	if err != nil || current.Version != job.Version {
		t.Fatalf("read-only preflight changed job=%+v %v", current, err)
	}
	work, err := s.ListOrganizeSourceWork(ctx, job.ID)
	if err != nil || len(work) != 2 {
		t.Fatalf("queue=%+v %v", work, err)
	}
	for _, item := range work {
		if item.Version != 1 || item.FailureCount != 0 || item.State != "pending" {
			t.Fatalf("read-only preflight mutated retry queue=%+v", item)
		}
	}
}
