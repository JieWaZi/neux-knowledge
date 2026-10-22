package store

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	types "github.com/JieWaZi/neux-knowledge/internal/domain"
	"gorm.io/gorm"
)

func TestBlobDatabaseKeepsOneAuthoritativeRawRoot(t *testing.T) {
	ctx := context.Background()
	s, db, raw, l := auditStore(t)
	v := purgeStoreSource(t, s, l)
	otherRaw := t.TempDir()
	if alternate, err := Open(ctx, db, otherRaw); err == nil {
		alternate.Close()
		t.Fatal("same database accepted a different raw root while open")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if alternate, err := Open(ctx, db, otherRaw); err == nil {
		alternate.Close()
		t.Fatal("same database forgot raw root after close")
	}
	reopened, err := Open(ctx, db, raw)
	if err != nil {
		t.Fatal("original raw root cannot reopen", err)
	}
	defer reopened.Close()
	if original, err := reopened.ReadRaw(ctx, l.ID, v.ID); err != nil || string(original) == "" {
		t.Fatal("authoritative raw bytes unavailable", err)
	}
	owner, err := os.ReadFile(filepath.Join(raw, blobOwnerName))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(otherRaw, blobOwnerName), owner, 0600); err != nil {
		t.Fatal(err)
	}
	if alternate, err := Open(ctx, db, otherRaw); err == nil {
		alternate.Close()
		t.Fatal("copied owner record made another root authoritative")
	}
	if err = reopened.Close(); err != nil {
		t.Fatal(err)
	}
	saved := raw + "-saved"
	if err = os.Rename(raw, saved); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(raw, 0700); err != nil {
		t.Fatal(err)
	}
	if replacement, err := Open(ctx, db, raw); err == nil {
		replacement.Close()
		t.Fatal("empty replacement at original path lost random root identity")
	}
	if err = os.RemoveAll(raw); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(saved, raw); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(ctx, db, raw)
	if err != nil {
		t.Fatal("restored original root rejected", err)
	}
	_ = restored.Close()
}

func TestBlobOwnerMutationCannotBypassOpenStoreValidation(t *testing.T) {
	ctx := context.Background()
	s, _, raw, l := auditStore(t)
	v := purgeStoreSource(t, s, l)
	retained, err := s.ImportSource(ctx, types.ImportSourceInput{RequestID: "readable-other", LibraryID: l.ID, Filename: "other.md", Bytes: []byte("other raw")})
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.GetSource(ctx, l.ID, retained.ResourceID)
	if err != nil {
		t.Fatal(err)
	}
	purge := confirmStorePurge(t, s, l, v.SourceID)
	data, err := os.ReadFile(filepath.Join(raw, blobOwnerName))
	if err != nil {
		t.Fatal(err)
	}
	var owner map[string]any
	if err = json.Unmarshal(data, &owner); err != nil {
		t.Fatal(err)
	}
	owner["rootId"] = "ffffffffffffffffffffffffffffffff"
	corrupted, _ := json.Marshal(owner)
	if err = os.WriteFile(filepath.Join(raw, blobOwnerName), corrupted, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = s.PreviewSourcePurge(ctx, types.PurgePreviewInput{LibraryID: l.ID, SourceID: other.SourceID}); err == nil {
		t.Fatal("preview ignored changed raw ownership")
	}
	if _, err = s.ReadRaw(ctx, l.ID, other.ID); err == nil {
		t.Fatal("raw read ignored changed ownership")
	}
	if _, err = s.ImportSource(ctx, types.ImportSourceInput{RequestID: "changed-owner", LibraryID: l.ID, Filename: "new.md", Bytes: []byte("new bytes")}); err == nil {
		t.Fatal("import ignored changed raw ownership")
	}
	if _, err = s.RecoverSourcePurge(ctx, types.RecoverSourcePurgeInput{RequestID: "changed-owner-recover", LibraryID: l.ID, PurgeID: purge.ResourceID, ExpectedPolicyVersion: l.PolicyVersion}); err == nil {
		t.Fatal("cleanup ignored changed raw ownership")
	}
	if _, err = os.Stat(filepath.Join(raw, v.RawKey)); err != nil {
		t.Fatal("ownership mismatch removed original bytes")
	}
}

func TestDatabaseSerializesFirstClaimsFromDifferentRawRoots(t *testing.T) {
	ctx := context.Background()
	s, db, raw, _ := auditStore(t)
	if err := s.db.Where("id = ?", "raw").Delete(&blobRootBinding{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(raw, blobOwnerName)); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	gate := make(chan struct{})
	results := make(chan *Store, 2)
	for _, path := range []string{raw, t.TempDir()} {
		go func(rawPath string) {
			<-gate
			store, _ := Open(ctx, db, rawPath)
			results <- store
		}(path)
	}
	close(gate)
	claimed := 0
	for range 2 {
		if store := <-results; store != nil {
			claimed++
			_ = store.Close()
		}
	}
	if claimed != 1 {
		t.Fatal("different raw roots did not select one database authority", claimed)
	}
}

func TestImportRetryKeepsSourceIdentityAndRemovesTemporaryCopy(t *testing.T) {
	ctx := context.Background()
	s, _, raw, l := auditStore(t)
	in := types.ImportSourceInput{RequestID: "retry-new", LibraryID: l.ID, Filename: "new.md", Bytes: []byte("retry original")}
	if err := s.db.Callback().Create().Before("gorm:create").Register("blob:registration-fault", func(db *gorm.DB) {
		if db.Statement.Table == "knowledge_source_versions" {
			db.AddError(errors.New("source registration failed"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ImportSource(ctx, in); err == nil {
		t.Fatal("fault did not trigger")
	}
	var pending types.ImportBlobIntent
	if err := s.db.Where("request_id = ?", in.RequestID).Take(&pending).Error; err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(raw, pending.TempKey), []byte("leftover partial copy"), 0600); err != nil {
		t.Fatal(err)
	}
	_ = s.db.Callback().Create().Remove("blob:registration-fault")
	rec, err := s.ImportSource(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	v, err := s.GetSource(ctx, l.ID, rec.ResourceID)
	if err != nil || v.SourceID != pending.SourceID {
		t.Fatalf("retry changed frozen source identity: %+v %v", v, err)
	}
	if _, err = os.Stat(filepath.Join(raw, pending.TempKey)); !os.IsNotExist(err) {
		t.Fatal("successful import left untracked temporary original")
	}
	var count int64
	if err = s.db.Model(&types.ImportBlobIntent{}).Where("request_id = ?", in.RequestID).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("import and intent removal were not committed together")
	}
}

func TestBlobLifecycleAcrossProcessExit(t *testing.T) {
	if os.Getenv("NEUX_BLOB_LOCK_CHILD") == "1" {
		s, err := Open(context.Background(), os.Getenv("NEUX_BLOB_DB"), os.Getenv("NEUX_BLOB_RAW"))
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		release, err := s.lockBlobs(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		fmt.Println("blob lifecycle locked")
		_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
		return
	}
	ctx := context.Background()
	s, db, raw, l := auditStore(t)
	v := purgeStoreSource(t, s, l)
	purge := confirmStorePurge(t, s, l, v.SourceID)
	deadline, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(deadline, os.Args[0], "-test.run=^TestBlobLifecycleAcrossProcessExit$")
	cmd.Env = append(os.Environ(), "NEUX_BLOB_LOCK_CHILD=1", "NEUX_BLOB_DB="+db, "NEUX_BLOB_RAW="+raw)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "blob lifecycle locked\n" {
		t.Fatalf("child owner unavailable: %q %v", line, err)
	}
	short, stop := context.WithTimeout(ctx, 50*time.Millisecond)
	_, err = s.ImportSource(short, types.ImportSourceInput{RequestID: "blocked-import", LibraryID: l.ID, Filename: "blocked.md", Bytes: []byte("new bytes")})
	stop()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("import bypassed process owner: %v", err)
	}
	short, stop = context.WithTimeout(ctx, 50*time.Millisecond)
	input := types.RecoverSourcePurgeInput{RequestID: "recover-process", LibraryID: l.ID, PurgeID: purge.ResourceID, ExpectedPolicyVersion: l.PolicyVersion}
	_, err = s.RecoverSourcePurge(short, input)
	stop()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("purge bypassed process owner: %v", err)
	}
	if _, err = os.Stat(filepath.Join(raw, v.RawKey)); err != nil {
		t.Fatal("live process lock failed to protect raw")
	}
	if err = cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	if _, err = s.RecoverSourcePurge(ctx, input); err != nil {
		t.Fatalf("owner exit did not permit exact cleanup recovery: %v", err)
	}
	if _, err = os.Stat(filepath.Join(raw, v.RawKey)); !os.IsNotExist(err) {
		t.Fatal("recovered cleanup did not remove original")
	}
}
