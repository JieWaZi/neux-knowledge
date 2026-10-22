package store

import (
	"context"
	"errors"
	types "github.com/JieWaZi/neux-knowledge/internal/domain"
	"os"
	"path/filepath"
	"testing"
)

func TestDeleteLibraryRemovesContentAndKeepsSharedRaw(t *testing.T) {
	ctx := context.Background()
	s, _, raw, l := auditStore(t)
	v := purgeStoreSource(t, s, l)
	other, err := s.CreateLibrary(ctx, types.CreateLibraryInput{RequestID: "other", Name: "other", Kind: "general", WikiLanguage: "zh"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.ReadRaw(ctx, l.ID, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.ImportSource(ctx, types.ImportSourceInput{RequestID: "shared", LibraryID: other.LibraryID, Filename: "shared.txt", Bytes: b})
	if err != nil {
		t.Fatal(err)
	}
	in := types.DeleteLibraryInput{RequestID: "delete", LibraryID: l.ID, ExpectedVersion: l.Version}
	receipt, err := s.DeleteLibrary(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Operation != "library.delete" {
		t.Fatal(receipt)
	}
	if _, err = s.GetLibrary(ctx, l.ID); err == nil {
		t.Fatal("library survives")
	}
	if _, err = s.GetSource(ctx, l.ID, v.ID); err == nil {
		t.Fatal("source survives")
	}
	if _, err = s.ReadRaw(ctx, other.LibraryID, r.ResourceID); err != nil {
		t.Fatal("shared original lost", err)
	}
	if _, err = os.Stat(filepath.Join(raw, v.RawKey)); err != nil {
		t.Fatal(err)
	}
	status, err := s.GetLibraryDeletion(ctx, l.ID)
	if err != nil || status.State != "completed" || status.RetainedSharedBlobCount != 1 {
		t.Fatalf("%+v %v", status, err)
	}
	retry, err := s.DeleteLibrary(ctx, in)
	if err != nil || retry.RequestID != receipt.RequestID {
		t.Fatal(retry, err)
	}
	in.ExpectedVersion++
	if _, err = s.DeleteLibrary(ctx, in); err == nil {
		t.Fatal("changed retry accepted")
	}
}
func TestDeleteLibraryRejectsActiveWorkAndStaleVersion(t *testing.T) {
	ctx := context.Background()
	s, _, _, l := auditStore(t)
	in := types.DeleteLibraryInput{RequestID: "delete", LibraryID: l.ID, ExpectedVersion: l.Version + 1}
	_, err := s.DeleteLibrary(ctx, in)
	var typed *types.Error
	if !errors.As(err, &typed) || typed.Code != types.ErrVersionConflict {
		t.Fatal(err)
	}
	if err = s.db.Create(&types.Job{ID: "active", LibraryID: l.ID, State: "processing"}).Error; err != nil {
		t.Fatal(err)
	}
	in.ExpectedVersion = l.Version
	if _, err = s.DeleteLibrary(ctx, in); err == nil {
		t.Fatal("active work deleted")
	}
	if _, err = s.GetLibrary(ctx, l.ID); err != nil {
		t.Fatal(err)
	}
}
func TestDeleteLibraryDeletesExclusiveRaw(t *testing.T) {
	ctx := context.Background()
	s, _, raw, l := auditStore(t)
	v := purgeStoreSource(t, s, l)
	if _, err := s.DeleteLibrary(ctx, types.DeleteLibraryInput{RequestID: "delete", LibraryID: l.ID, ExpectedVersion: l.Version}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(raw, v.RawKey)); !os.IsNotExist(err) {
		t.Fatal("exclusive raw remains", err)
	}
}

func TestDeleteLibraryKeepsDurableCleanupAfterFailureAndReopen(t *testing.T) {
	ctx := context.Background()
	s, dbPath, raw, l := auditStore(t)
	v := purgeStoreSource(t, s, l)
	// Use a directory to simulate object deletion failure; recovery must continue the original manifest without rescanning the root.
	objectPath := filepath.Join(raw, v.RawKey)
	if err := os.Remove(objectPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(objectPath, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(objectPath, "child"), []byte("block removal"), 0600); err != nil {
		t.Fatal(err)
	}
	receipt, err := s.DeleteLibrary(ctx, types.DeleteLibraryInput{RequestID: "delete", LibraryID: l.ID, ExpectedVersion: l.Version})
	if err != nil || receipt.State != "accepted" {
		t.Fatal(receipt, err)
	}
	status, err := s.GetLibraryDeletion(ctx, l.ID)
	if err != nil || status.State != "pending" || status.PendingBlobCount != 1 {
		t.Fatal(status, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.RemoveAll(objectPath); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, dbPath, raw)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	status, err = s.GetLibraryDeletion(ctx, l.ID)
	if err != nil {
		t.Fatal(err)
	}
	in := types.RecoverLibraryDeletionInput{RequestID: "recover", LibraryID: l.ID, ExpectedVersion: status.Version}
	for i := 0; i < 2; i++ {
		recovered, e := s.RecoverLibraryDeletion(ctx, in)
		if e != nil || recovered.State != "completed" {
			t.Fatal(recovered, e)
		}
	}
	status, err = s.GetLibraryDeletion(ctx, l.ID)
	if err != nil || status.DeletedBlobCount != 1 || status.State != "completed" {
		t.Fatal(status, err)
	}
}

func TestDeleteLibraryRejectsForeignOrMissingRootIdentity(t *testing.T) {
	ctx := context.Background()
	s, _, raw, l := auditStore(t)
	v := purgeStoreSource(t, s, l)
	binding := blobRootBinding{}
	if err := s.db.Take(&binding).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&blobRootBinding{}).Where("id = ?", "raw").Update("root_identity", "foreign").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteLibrary(ctx, types.DeleteLibraryInput{RequestID: "delete", LibraryID: l.ID, ExpectedVersion: l.Version}); err == nil {
		t.Fatal("foreign root permitted logical delete")
	}
	if _, err := s.GetLibrary(ctx, l.ID); err != nil {
		t.Fatal("foreign root removed library", err)
	}
	if err := s.db.Model(&blobRootBinding{}).Where("id = ?", "raw").Updates(map[string]any{"root_identity": binding.RootIdentity, "root_id": ""}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteLibrary(ctx, types.DeleteLibraryInput{RequestID: "delete", LibraryID: l.ID, ExpectedVersion: l.Version}); err == nil {
		t.Fatal("missing root identity permitted logical delete")
	}
	if _, err := os.Stat(filepath.Join(raw, v.RawKey)); err != nil {
		t.Fatal("invalid ownership removed original", err)
	}
}

func TestOpenRecoversPendingLibraryDeletion(t *testing.T) {
	ctx := context.Background()
	s, dbPath, raw, l := auditStore(t)
	v := purgeStoreSource(t, s, l)
	objectPath := filepath.Join(raw, v.RawKey)
	if err := os.Remove(objectPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(objectPath, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(objectPath, "child"), []byte("block"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteLibrary(ctx, types.DeleteLibraryInput{RequestID: "delete", LibraryID: l.ID, ExpectedVersion: l.Version}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(objectPath); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, dbPath, raw)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	status, err := reopened.GetLibraryDeletion(ctx, l.ID)
	if err != nil || status.State != "completed" {
		t.Fatalf("pending deletion lost startup recovery: %+v %v", status, err)
	}
}
