package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	types "github.com/JieWaZi/neux-knowledge/internal/domain"
	"gorm.io/gorm"
)

func TestPurgePreviewRejectsUnserializableSnapshot(t *testing.T) {
	ctx := context.Background()
	s, _, _, l := auditStore(t)
	v := purgeStoreSource(t, s, l)
	job := types.Job{ID: "damaged-job", LibraryID: l.ID, State: "cancelled"}
	if err := s.db.Create(&job).Error; err != nil {
		t.Fatal(err)
	}
	stage := types.Stage{ID: "damaged-stage", JobID: job.ID, Input: json.RawMessage(`{"private":"INVALID_SNAPSHOT_SECRET"`)}
	if err := s.db.Create(&stage).Error; err != nil {
		t.Fatal(err)
	}
	preview, err := s.PreviewSourcePurge(ctx, types.PurgePreviewInput{LibraryID: l.ID, SourceID: v.SourceID})
	if err == nil || preview != nil {
		t.Fatal("invalid persisted JSON produced a purge commitment")
	}
	if strings.Contains(err.Error(), "INVALID_SNAPSHOT_SECRET") {
		t.Fatal("invalid snapshot error exposed stored source material")
	}
}

func TestPurgeTracksInterruptedImportAndRevokesItsReplay(t *testing.T) {
	ctx := context.Background()
	s, _, raw, l := auditStore(t)
	r, err := s.ImportSource(ctx, types.ImportSourceInput{RequestID: "initial", LibraryID: l.ID, Filename: "one.md", Bytes: []byte("registered")})
	if err != nil {
		t.Fatal(err)
	}
	version, err := s.GetSource(ctx, l.ID, r.ResourceID)
	if err != nil {
		t.Fatal(err)
	}
	in := types.ImportSourceInput{RequestID: "interrupted", LibraryID: l.ID, SourceID: version.SourceID, ExpectedVersion: 1, Filename: "two.md", Bytes: []byte("INTERRUPTED_SECRET")}
	fault := errors.New("simulated source registration interruption")
	if err = s.db.Callback().Create().Before("gorm:create").Register("purge:import-fault", func(db *gorm.DB) {
		if db.Statement.Table == "knowledge_source_versions" {
			db.AddError(fault)
		}
	}); err != nil {
		t.Fatal(err)
	}
	_, err = s.ImportSource(ctx, in)
	if err == nil {
		t.Fatal("fault did not trigger")
	}
	_ = s.db.Callback().Create().Remove("purge:import-fault")
	var intent types.ImportBlobIntent
	if err = s.db.Where("request_id = ?", in.RequestID).Take(&intent).Error; err != nil {
		t.Fatalf("interrupted raw lost source attribution: %v", err)
	}
	if intent.SourceID != version.SourceID || intent.RawKey != hash(in.Bytes) || intent.TempKey == "" {
		t.Fatalf("intent does not freeze actual import: %+v", intent)
	}
	if err = os.WriteFile(filepath.Join(raw, intent.TempKey), []byte("PARTIAL_SECRET"), 0600); err != nil {
		t.Fatal(err)
	}
	preview, err := s.PreviewSourcePurge(ctx, types.PurgePreviewInput{LibraryID: l.ID, SourceID: version.SourceID})
	if err != nil {
		t.Fatal(err)
	}
	if preview.PendingImportCount != 1 {
		t.Fatal("preview omitted interrupted import")
	}
	purge, err := s.ConfirmSourcePurge(ctx, types.ConfirmSourcePurgeInput{RequestID: "confirm", LibraryID: l.ID, SourceID: version.SourceID, PreviewDigest: preview.Digest, Confirmed: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.RecoverSourcePurge(ctx, types.RecoverSourcePurgeInput{RequestID: "recover", LibraryID: l.ID, PurgeID: purge.ResourceID, ExpectedPolicyVersion: l.PolicyVersion})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{version.RawKey, intent.RawKey, intent.TempKey} {
		if _, err = os.Stat(filepath.Join(raw, name)); !os.IsNotExist(err) {
			t.Fatalf("purge retained attributed import copy %s", name)
		}
	}
	_, err = s.ImportSource(ctx, in)
	if err == nil {
		t.Fatal("revoked pending import replayed")
	}
	if strings.Contains(err.Error(), "SECRET") {
		t.Fatal("purge failure exposed source bytes")
	}
}

func TestBlobRootCannotBeClaimedByAnotherDatabase(t *testing.T) {
	ctx := context.Background()
	s, db, raw, _ := auditStore(t)
	other, err := Open(ctx, filepath.Join(filepath.Dir(db), "other.db"), raw)
	if err == nil {
		other.Close()
		t.Fatal("two databases claimed one raw root")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
}

func purgeStoreSource(t *testing.T, s *Store, l *types.Library) *types.SourceVersion {
	t.Helper()
	rec, err := s.ImportSource(context.Background(), types.ImportSourceInput{RequestID: "purge-source", LibraryID: l.ID, Filename: "source.md", Bytes: []byte("PURGE_INTERNAL_SECRET")})
	if err != nil {
		t.Fatal(err)
	}
	v, err := s.GetSource(context.Background(), l.ID, rec.ResourceID)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func confirmStorePurge(t *testing.T, s *Store, l *types.Library, sourceID string) *types.Receipt {
	t.Helper()
	ctx := context.Background()
	preview, err := s.PreviewSourcePurge(ctx, types.PurgePreviewInput{LibraryID: l.ID, SourceID: sourceID})
	if err != nil {
		t.Fatal(err)
	}
	rec, err := s.ConfirmSourcePurge(ctx, types.ConfirmSourcePurgeInput{RequestID: "purge-confirm", LibraryID: l.ID, SourceID: sourceID, PreviewDigest: preview.Digest, Confirmed: true})
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func TestPurgeRecoverAfterFilesystemAndDatabaseFailures(t *testing.T) {
	for _, faultKind := range []string{"filesystem", "database"} {
		t.Run(faultKind, func(t *testing.T) {
			ctx := context.Background()
			s, db, raw, l := auditStore(t)
			v := purgeStoreSource(t, s, l)
			rec := confirmStorePurge(t, s, l, v.SourceID)
			recoverInput := types.RecoverSourcePurgeInput{RequestID: "recover", LibraryID: l.ID, PurgeID: rec.ResourceID, ExpectedPolicyVersion: l.PolicyVersion}
			if faultKind == "filesystem" {
				if err := os.Remove(filepath.Join(raw, v.RawKey)); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(filepath.Join(raw, v.RawKey), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(raw, v.RawKey, "child"), []byte("secret"), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				updates := 0
				if err := s.db.Callback().Update().Before("gorm:update").Register("purge:progress-fault", func(db *gorm.DB) {
					if db.Statement.Table == "knowledge_source_purges" {
						updates++
						if updates == 2 {
							db.AddError(errors.New("simulated cleanup progress failure"))
						}
					}
				}); err != nil {
					t.Fatal(err)
				}
			}
			_, err := s.RecoverSourcePurge(ctx, recoverInput)
			if err == nil {
				t.Fatal("fault did not trigger")
			}
			if strings.Contains(err.Error(), "PURGE_INTERNAL_SECRET") {
				t.Fatal("cleanup error exposed raw text")
			}
			status, err := s.GetSourcePurge(ctx, l.ID, rec.ResourceID)
			if err != nil || status.State != "pending" {
				t.Fatalf("failed cleanup claimed completion: %+v %v", status, err)
			}
			if faultKind == "filesystem" {
				if err = os.RemoveAll(filepath.Join(raw, v.RawKey)); err != nil {
					t.Fatal(err)
				}
			} else {
				_ = s.db.Callback().Update().Remove("purge:progress-fault")
				if _, err = os.Stat(filepath.Join(raw, v.RawKey)); !os.IsNotExist(err) {
					t.Fatal("database fault did not occur after actual file deletion")
				}
			}
			if err = s.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := Open(ctx, db, raw)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			_, err = reopened.RecoverSourcePurge(ctx, recoverInput)
			if err != nil {
				t.Fatalf("same request did not recover persisted cleanup: %v", err)
			}
			status, err = reopened.GetSourcePurge(ctx, l.ID, rec.ResourceID)
			if err != nil || status.State != "completed" {
				t.Fatalf("reopened cleanup did not complete: %+v %v", status, err)
			}
		})
	}
}

func TestDeletionRemovesCurrentLiteInternalMaterial(t *testing.T) {
	for _, wholeLibrary := range []bool{false, true} {
		t.Run(fmt.Sprint(wholeLibrary), func(t *testing.T) {
			ctx := context.Background()
			s, _, _, l := auditStore(t)
			source := purgeStoreSource(t, s, l)
			job := types.Job{ID: "finished-job", LibraryID: l.ID, State: "completed"}
			if err := s.db.Create(&job).Error; err != nil {
				t.Fatal(err)
			}
			stage := types.Stage{ID: "finished-stage", JobID: job.ID, State: "finished", Input: json.RawMessage(`{"text":"PRIVATE"}`), ResultText: "PRIVATE", Usage: &types.ModelUsage{TotalTokens: 1}, Diagnostics: &types.ModelDiagnostics{Limitations: []string{"PRIVATE"}}}
			if err := s.db.Create(&stage).Error; err != nil {
				t.Fatal(err)
			}
			op := types.WikiFinalizeOp{ID: "finalize-op", LibraryID: l.ID, JobID: job.ID, Key: "added", Kind: "added", DocSummary: "PRIVATE"}
			if err := s.db.Create(&op).Error; err != nil {
				t.Fatal(err)
			}
			if wholeLibrary {
				if _, err := s.DeleteLibrary(ctx, types.DeleteLibraryInput{RequestID: "delete", LibraryID: l.ID, ExpectedVersion: l.Version}); err != nil {
					t.Fatal(err)
				}
			} else {
				confirmStorePurge(t, s, l, source.SourceID)
			}
			var count int64
			if err := s.db.Model(&types.WikiFinalizeOp{}).Where("library_id = ?", l.ID).Count(&count).Error; err != nil || count != 0 {
				t.Fatalf("finalize material remained: %d %v", count, err)
			}
			if !wholeLibrary {
				var current types.Stage
				if err := s.db.Where("id = ?", stage.ID).Take(&current).Error; err != nil {
					t.Fatal(err)
				}
				if current.ResultText != "" || current.Usage != nil || current.Diagnostics != nil || string(current.Input) != "{}" {
					t.Fatalf("stage material remained: %+v", current)
				}
			}
		})
	}
}
