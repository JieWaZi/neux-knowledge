package store

import (
	"context"
	"errors"
	"os"

	types "github.com/JieWaZi/neux-knowledge/internal/domain"
	"gorm.io/gorm"
)

// DeleteLibrary revokes the library and all business content in one transaction, retaining the minimum manifest needed to resume disk deletion.
func (s *Store) DeleteLibrary(ctx context.Context, in types.DeleteLibraryInput) (*types.Receipt, error) {
	release, err := s.lockBlobs(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	err = s.blobBinding(ctx, false)
	if err != nil {
		return nil, err
	}
	receipt, err := s.mutate(ctx, in.RequestID, "library.delete", in.LibraryID, in, func(tx *gorm.DB) (*types.Receipt, error) {
		l, err := getLibrary(tx, in.LibraryID, false)
		if err != nil {
			return nil, err
		}
		if in.ExpectedVersion != l.Version {
			return nil, fail(types.ErrVersionConflict, "library.delete", "library changed")
		}
		// The write lock covers blocker checks and full library revocation; in-flight publication either finishes first or no longer finds the task and library.
		if err = tx.Model(&types.Library{}).Where("id = ?", l.ID).UpdateColumn("version", l.Version).Error; err != nil {
			return nil, err
		}
		var jobs []types.Job
		if err = tx.Where("library_id = ?", l.ID).Find(&jobs).Error; err != nil {
			return nil, err
		}
		jobIDs := []string{}
		for _, j := range jobs {
			if j.State == "queued" || j.State == "processing" || j.State == "cancel_requested" {
				return nil, fail(types.ErrVersionConflict, "library.delete", "active jobs must finish before library deletion")
			}
			jobIDs = append(jobIDs, j.ID)
		}
		if err = eachPurgeBatch(jobIDs, func(ids []string) error {
			var stages []types.Stage
			if e := tx.Where("job_id IN ?", ids).Find(&stages).Error; e != nil {
				return e
			}
			for _, st := range stages {
				if st.State == "running" {
					return fail(types.ErrVersionConflict, "library.delete", "running stage blocks library deletion")
				}
			}
			return nil
		}); err != nil {
			return nil, err
		}
		record := types.LibraryDeletionRecord{LibraryID: l.ID, Version: 1, PolicyVersion: l.PolicyVersion, State: "pending", Objects: []types.PurgeBlobCleanup{}}
		keys := []string{}
		var sources []types.SourceVersion
		if err = tx.Where("library_id = ?", l.ID).Find(&sources).Error; err != nil {
			return nil, err
		}
		for _, v := range sources {
			if !validBlobKey(v.RawKey) || v.RawKey != v.SHA256 {
				return nil, fail(types.ErrStorage, "library.delete", "invalid source object identity")
			}
			keys = append(keys, v.RawKey)
		}
		var imports []types.ImportBlobIntent
		if err = tx.Where("library_id = ?", l.ID).Find(&imports).Error; err != nil {
			return nil, err
		}
		for _, v := range imports {
			if v.PurgeID != "" {
				continue
			}
			if !validBlobKey(v.RawKey) || !validImportTemp(v.TempKey) {
				return nil, fail(types.ErrStorage, "library.delete", "invalid import object identity")
			}
			keys = append(keys, v.RawKey)
			record.Objects = append(record.Objects, types.PurgeBlobCleanup{Key: v.TempKey, Kind: "temporary", State: "pending"})
		}
		// Source purges removed their business rows, but their unfinished disk manifests must still be inherited.
		var purges []types.SourcePurgeRecord
		if err = tx.Where("library_id = ?", l.ID).Find(&purges).Error; err != nil {
			return nil, err
		}
		for _, p := range purges {
			for _, o := range p.Objects {
				if o.State != "deleted" {
					if (o.Kind == "temporary" && !validImportTemp(o.Key)) || (o.Kind != "temporary" && !validBlobKey(o.Key)) {
						return nil, fail(types.ErrStorage, "library.delete", "invalid inherited cleanup identity")
					}
					record.Objects = append(record.Objects, o)
				}
			}
		}
		for _, key := range sortedUnique(keys) {
			record.Objects = append(record.Objects, types.PurgeBlobCleanup{Key: key, Kind: "raw", State: "pending"})
		}
		seen := map[string]bool{}
		objects := record.Objects[:0]
		for _, object := range record.Objects {
			key := object.Kind + ":" + object.Key
			if !seen[key] {
				seen[key] = true
				object.State = "pending"
				objects = append(objects, object)
			}
		}
		record.Objects = objects
		if err = tx.Create(&record).Error; err != nil {
			return nil, err
		}
		if err = eachPurgeBatch(jobIDs, func(ids []string) error { return tx.Where("job_id IN ?", ids).Delete(&types.Stage{}).Error }); err != nil {
			return nil, err
		}
		for _, model := range []any{&types.WikiPage{}, &types.WikiPageRevision{}, &types.WikiPageIssue{}, &types.WikiFolder{}} {
			if err = tx.Unscoped().Where("knowledge_base_id = ?", l.ID).Delete(model).Error; err != nil {
				return nil, err
			}
		}
		if err = deleteDocumentChunks(tx, l.ID, ""); err != nil {
			return nil, err
		}
		for _, model := range []any{&types.WikiFinalizeOp{}, &types.OrganizeSourceWork{}, &types.Job{}, &types.SourceHead{}, &types.SourceVersion{}, &types.ImportBlobIntent{}, &types.SourcePurgeRecord{}, &types.SourceTombstone{}} {
			if err = tx.Unscoped().Where("library_id = ?", l.ID).Delete(model).Error; err != nil {
				return nil, err
			}
		}
		if err = tx.Where("id = ?", l.ID).Delete(&types.Library{}).Error; err != nil {
			return nil, err
		}
		return &types.Receipt{ResourceID: l.ID, Version: l.Version + 1, State: "accepted"}, nil
	})
	if err != nil {
		return nil, err
	}
	// Cleanup failure still returns the committed receipt; persistent state remains pending and can resume after restart.
	_ = s.cleanupLibraryDeletion(ctx, in.LibraryID, 0)
	return receipt, nil
}
func (s *Store) GetLibraryDeletion(ctx context.Context, id string) (result *types.LibraryDeletionStatus, err error) {
	err = s.transaction(ctx, func(tx *gorm.DB) error {
		var r types.LibraryDeletionRecord
		if e := tx.Where("library_id = ?", id).Take(&r).Error; e != nil {
			return e
		}
		result = &types.LibraryDeletionStatus{LibraryID: r.LibraryID, Version: r.Version, PolicyVersion: r.PolicyVersion, State: r.State}
		for _, o := range r.Objects {
			switch o.State {
			case "deleted":
				result.DeletedBlobCount++
			case "retained_shared":
				result.RetainedSharedBlobCount++
			default:
				result.PendingBlobCount++
			}
		}
		return nil
	})
	return
}
func (s *Store) cleanupLibraryDeletion(ctx context.Context, id string, expected int64) error {
	return s.transaction(ctx, func(tx *gorm.DB) error { return s.cleanupLibraryDeletionTx(ctx, tx, id, expected) })
}

func (s *Store) cleanupLibraryDeletionTx(ctx context.Context, tx *gorm.DB, id string, expected int64) error {
	var r types.LibraryDeletionRecord
	if err := tx.Where("library_id = ?", id).Take(&r).Error; err != nil {
		return err
	}
	if expected > 0 && r.Version != expected {
		return fail(types.ErrVersionConflict, "library.delete.recover", "deletion changed")
	}
	if err := tx.Model(&r).UpdateColumn("version", r.Version).Error; err != nil {
		return err
	}
	r.State = "completed"
	for i := range r.Objects {
		o := &r.Objects[i]
		if o.State == "deleted" {
			continue
		}
		if (o.Kind == "temporary" && !validImportTemp(o.Key)) || (o.Kind != "temporary" && !validBlobKey(o.Key)) {
			return fail(types.ErrStorage, "library.delete.recover", "invalid cleanup object")
		}
		var shared int64
		if o.Kind != "temporary" {
			if err := tx.Model(&types.SourceVersion{}).Where("raw_key = ?", o.Key).Count(&shared).Error; err != nil {
				return err
			}
			var pending int64
			if err := tx.Model(&types.ImportBlobIntent{}).Where("raw_key = ? AND purge_id = ?", o.Key, "").Count(&pending).Error; err != nil {
				return err
			}
			shared += pending
		}
		if shared > 0 {
			o.State = "retained_shared"
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.raw.Remove(o.Key); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fail(types.ErrStorage, "library.delete.recover", "raw cleanup remains pending")
		}
		o.State = "deleted"
	}
	r.Version++
	return tx.Model(&r).Select("state", "version", "objects").Updates(&r).Error
}

// RecoverLibraryDeletion resumes the original persistent manifest; the deleted library identity is used only for authorization and reconciliation.
func (s *Store) RecoverLibraryDeletion(ctx context.Context, in types.RecoverLibraryDeletionInput) (*types.Receipt, error) {
	if in.RequestID == "" || in.ExpectedVersion < 1 {
		return nil, fail(types.ErrInvalid, "library.delete.recover", "requestId and deletion version required")
	}
	release, err := s.lockBlobs(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	err = s.blobBinding(ctx, false)
	if err != nil {
		return nil, err
	}

	return s.mutate(ctx, in.RequestID, "library.delete.recover", in.LibraryID, in, func(tx *gorm.DB) (*types.Receipt, error) {
		if err := s.cleanupLibraryDeletionTx(ctx, tx, in.LibraryID, in.ExpectedVersion); err != nil {
			return nil, err
		}
		var r types.LibraryDeletionRecord
		if err := tx.Where("library_id = ?", in.LibraryID).Take(&r).Error; err != nil {
			return nil, err
		}
		return &types.Receipt{ResourceID: in.LibraryID, Version: r.Version, State: r.State}, nil
	})
}

// resumeLibraryDeletions advances at most one hundred pending manifests under the source-file lock already held at startup; blocked records can resume through explicit recovery.
func (s *Store) resumeLibraryDeletions(ctx context.Context) error {
	var pending []types.LibraryDeletionRecord
	if err := s.db.WithContext(ctx).Where("state = ?", "pending").Order("library_id ASC").Limit(100).Find(&pending).Error; err != nil {
		return translate("library.delete.resume", err)
	}
	for _, r := range pending {
		if err := ctx.Err(); err != nil {
			return err
		}
		// A single disk failure must not prevent the knowledge core from opening; after rollback, the original manifest remains available for explicit recovery.
		_ = s.cleanupLibraryDeletion(ctx, r.LibraryID, 0)
	}
	return ctx.Err()
}
