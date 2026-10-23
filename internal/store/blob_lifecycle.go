package store

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sync"
	"time"

	types "github.com/JieWaZi/neux-knowledge/internal/domain"
	"gorm.io/gorm"
)

const blobLockName = ".neux-knowledge.lock"
const blobOwnerName = ".neux-knowledge-owner.json"

type blobOwner struct {
	Version          int    `json:"version"`                // Current bidirectional binding declaration format, fixed at 2.
	DatabaseIdentity string `json:"databaseIdentity"`       // SHA-256 of the actual canonical database path; personal absolute paths are not stored.
	RootID           string `json:"rootId,omitempty"`       // Random source-root identity assigned by the core.
	RootIdentity     string `json:"rootIdentity,omitempty"` // Digest of the canonical source-root path, preventing copied declarations from impersonating another root.
}

type blobRootBinding struct {
	ID           string `gorm:"primaryKey"` // Fixed at raw; the database permits only one authoritative source root.
	RootID       string // Nonempty random identity matching the source-root declaration.
	RootIdentity string // Digest of the canonical source-root path; missing source files cannot be rebound to an empty directory to pretend deletion succeeded.
}

func (blobRootBinding) TableName() string { return "knowledge_blob_root_bindings" }

// lockBlobs serializes uploads, reads, and deletions for the same raw root using an OS lock shared across processes.
func (s *Store) lockBlobs(ctx context.Context) (func(), error) {
	s.processorMu.Lock()
	if s.processorClosing {
		s.processorMu.Unlock()
		return nil, fail(types.ErrStorage, "blob.lock", "store is closing")
	}
	s.processors.Add(1)
	s.processorMu.Unlock()
	f, err := s.raw.OpenFile(blobLockName, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		s.processors.Done()
		return nil, wrap(types.ErrStorage, "blob.lock", err)
	}
	for {
		if err = ctx.Err(); err != nil {
			_ = f.Close()
			s.processors.Done()
			return nil, err
		}
		var acquired bool
		acquired, err = tryProcessorLock(f)
		if err != nil {
			_ = f.Close()
			s.processors.Done()
			return nil, wrap(types.ErrStorage, "blob.lock", err)
		}
		if acquired {
			var once sync.Once
			return func() { once.Do(func() { _ = f.Close(); s.processors.Done() }) }, nil
		}
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			_ = f.Close()
			s.processors.Done()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func (s *Store) readBlobOwner() (*blobOwner, error) {
	f, err := s.raw.Open(blobOwnerName)
	if err == nil {
		data, readErr := io.ReadAll(io.LimitReader(f, 4097))
		_ = f.Close()
		if readErr != nil {
			return nil, wrap(types.ErrStorage, "blob.owner", readErr)
		}
		var owner blobOwner
		if len(data) > 4096 || json.Unmarshal(data, &owner) != nil || owner.Version != 2 || owner.DatabaseIdentity == "" {
			return nil, fail(types.ErrStorage, "blob.owner", "invalid raw ownership record")
		}
		if owner.DatabaseIdentity != s.databaseIdentity {
			return nil, fail(types.ErrScopeViolation, "blob.owner", "raw root belongs to another database")
		}
		{
			rootID, decodeErr := hex.DecodeString(owner.RootID)
			if decodeErr != nil || len(rootID) != 16 || owner.RootIdentity != s.rawIdentity {
				return nil, fail(types.ErrScopeViolation, "blob.owner", "raw root identity does not match its ownership record")
			}
		}
		return &owner, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, wrap(types.ErrStorage, "blob.owner", err)
	}
	return nil, nil
}

func (s *Store) emptyBlobRoot() (bool, error) {
	dir, err := s.raw.Open(".")
	if err != nil {
		return false, wrap(types.ErrStorage, "blob.owner", err)
	}
	names, err := dir.Readdirnames(-1)
	_ = dir.Close()
	if err != nil {
		return false, wrap(types.ErrStorage, "blob.owner", err)
	}
	for _, name := range names {
		if name != blobLockName {
			return false, nil
		}
	}
	return true, nil
}

func (s *Store) writeBlobOwner(owner *blobOwner) error {
	data, _ := json.Marshal(owner)
	f, err := s.raw.OpenFile(blobOwnerName, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return wrap(types.ErrStorage, "blob.owner", err)
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return wrap(types.ErrStorage, "blob.owner", err)
	}
	return nil
}

// blobBinding checks the current bidirectional identities of the database and directory while holding the source-file lock.
func (s *Store) blobBinding(ctx context.Context, claimEmpty bool) error {
	owner, err := s.readBlobOwner()
	if err != nil {
		return err
	}
	return s.transaction(ctx, func(tx *gorm.DB) error {
		var binding blobRootBinding
		e := tx.Where("id = ?", "raw").Take(&binding).Error
		if e == nil {
			if binding.RootIdentity != s.rawIdentity {
				return fail(types.ErrScopeViolation, "blob.owner", "database belongs to another raw root")
			}
			if binding.RootID == "" {
				return fail(types.ErrScopeViolation, "blob.owner", "database raw root identity is missing")
			}
			if owner == nil || owner.Version != 2 || owner.RootID != binding.RootID {
				return fail(types.ErrScopeViolation, "blob.owner", "database and raw root ownership do not match")
			}
			return nil
		}
		if !errors.Is(e, gorm.ErrRecordNotFound) {
			return e
		}
		if !claimEmpty {
			return fail(types.ErrStorage, "blob.owner", "database raw root binding is missing")
		}
		empty, e := s.emptyBlobRoot()
		if e != nil {
			return e
		}
		var managed int64
		for _, model := range []any{&types.SourceHead{}, &types.SourceVersion{}, &types.ImportBlobIntent{}, &types.SourcePurgeRecord{}, &types.LibraryDeletionRecord{}} {
			var count int64
			if e = tx.Model(model).Count(&count).Error; e != nil {
				return e
			}
			managed += count
		}
		if managed > 0 {
			return fail(types.ErrScopeViolation, "blob.owner", "existing managed storage is missing its raw root binding")
		}
		binding = blobRootBinding{ID: "raw", RootIdentity: s.rawIdentity}
		if empty {
			owner = &blobOwner{Version: 2, DatabaseIdentity: s.databaseIdentity, RootID: newID(), RootIdentity: s.rawIdentity}
			binding.RootID = owner.RootID
		} else if owner != nil && owner.Version == 2 && managed == 0 {
			// Initial binding stopped after the declaration was written: recover the same random identity only if no source-file records exist.
			binding.RootID = owner.RootID
		} else {
			return fail(types.ErrScopeViolation, "blob.owner", "nonempty raw root has no current ownership binding")
		}
		// Acquire the SQLite write lock before writing the declaration so separate empty roots cannot claim ownership concurrently under different OS locks.
		if e = tx.Create(&binding).Error; e != nil {
			return e
		}
		if empty {
			if e = s.writeBlobOwner(owner); e != nil {
				return e
			}
		}
		return nil
	})
}

// prepareImport persists the exact intent before writing files; existing receipts and revoked requests must never rewrite bytes.
func (s *Store) prepareImport(ctx context.Context, in types.ImportSourceInput) (result *types.Receipt, intent *types.ImportBlobIntent, err error) {
	expected := digest(struct {
		Operation string // Matches mutate's operation digest contract.
		Input     any    // Complete original import input.
	}{"source.import", in})
	err = s.transaction(ctx, func(tx *gorm.DB) error {
		result = nil
		intent = nil
		var prior types.Receipt
		e := tx.Where("request_id = ?", in.RequestID).Take(&prior).Error
		if e == nil {
			if prior.Digest != expected {
				return fail(types.ErrIdempotencyConflict, "source.import", "requestId has different input")
			}
			result = &prior
			return nil
		}
		if !errors.Is(e, gorm.ErrRecordNotFound) {
			return e
		}
		var pending types.ImportBlobIntent
		e = tx.Where("request_id = ?", in.RequestID).Take(&pending).Error
		if e != nil && !errors.Is(e, gorm.ErrRecordNotFound) {
			return e
		}
		if e == nil {
			if pending.Digest != expected {
				return fail(types.ErrIdempotencyConflict, "source.import", "requestId has different input")
			}
			if pending.PurgeID != "" {
				return fail(types.ErrScopeViolation, "source.import", "import request was permanently revoked")
			}
			intent = &pending
		}
		library, e := getLibrary(tx, in.LibraryID, true)
		if e != nil {
			return e
		}
		if in.ExpectedPolicyVersion > 0 && library.PolicyVersion != in.ExpectedPolicyVersion {
			return fail(types.ErrScopeViolation, "source.import", "policy changed")
		}

		if in.SourceID != "" {
			var tombstones int64
			if e = tx.Model(&types.SourcePurgeRecord{}).Where("source_id = ?", in.SourceID).Count(&tombstones).Error; e != nil {
				return e
			}
			if tombstones > 0 {
				return fail(types.ErrScopeViolation, "source.import", "source identity was permanently revoked")
			}
			var head types.SourceHead
			if e = tx.Where("id = ?", in.SourceID).Take(&head).Error; e != nil {
				return e
			}
			if head.LibraryID != in.LibraryID {
				return fail(types.ErrScopeViolation, "source.import", "source belongs to another library")
			}
			if head.Version != in.ExpectedVersion {
				return fail(types.ErrVersionConflict, "source.import", "source changed")
			}
		} else if in.ExpectedVersion != 0 {
			return fail(types.ErrVersionConflict, "source.import", "new source requires version zero")
		}
		if intent != nil {
			return nil
		}
		sourceID := in.SourceID
		if sourceID == "" {
			sourceID = newID()
		}
		intent = &types.ImportBlobIntent{RequestID: in.RequestID, Digest: expected, LibraryID: in.LibraryID, SourceID: sourceID, RawKey: hash(in.Bytes), TempKey: ".import-" + hash([]byte(in.RequestID)) + ".tmp", CreatedAt: time.Now().UnixMilli()}
		return tx.Create(intent).Error
	})
	return
}
