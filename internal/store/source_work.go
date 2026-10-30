package store

import (
	"context"
	"strings"
	"time"

	types "github.com/JieWaZi/neux-knowledge/internal/domain"
	"gorm.io/gorm"
)

// initializeOrganizeSourceWork 与接纳或重试共用原事务，当前尝试的冻结来源关系在 queued 时已完整持久化。
func initializeOrganizeSourceWork(tx *gorm.DB, j *types.Job) error {
	if types.IsMaintenanceJob(j) {
		return nil
	}
	var sources []types.SourceVersion
	if err := tx.Select("id, library_id, state").Where("id IN ?", j.SourceVersionIDs).Find(&sources).Error; err != nil {
		return err
	}
	if len(sources) != len(j.SourceVersionIDs) {
		return fail(types.ErrNotFound, "source.work.initialize", "frozen source identity is missing")
	}
	now := time.Now().UnixMilli()
	work := make([]types.OrganizeSourceWork, 0, len(sources))
	for _, source := range sources {
		if source.LibraryID != j.LibraryID {
			return fail(types.ErrScopeViolation, "source.work.initialize", "source belongs to another library")
		}
		state := "pending"
		if source.State == "processed" || source.State == "no-change" {
			state = "done"
		}
		work = append(work, types.OrganizeSourceWork{ID: newID(), LibraryID: j.LibraryID, JobID: j.ID, JobAttempt: j.Attempt, SourceVersionID: source.ID, State: state, Version: 1, CreatedAt: now, UpdatedAt: now})
	}
	return tx.CreateInBatches(work, 100).Error
}

// EnsureOrganizeSourceWork 核对接纳事务已登记的完整来源关系，不延迟补写或重置已提交的失败预算。
func (s *Store) EnsureOrganizeSourceWork(ctx context.Context, jobID string) error {
	return s.transaction(ctx, func(tx *gorm.DB) error {
		j, _, err := activeJob(tx, jobID)
		if err != nil {
			return err
		}
		if types.IsMaintenanceJob(j) {
			return nil
		}
		var count int64
		if err := tx.Model(&types.OrganizeSourceWork{}).
			Where("library_id = ? AND job_id = ? AND job_attempt = ? AND source_version_id IN ?", j.LibraryID, j.ID, j.Attempt, j.SourceVersionIDs).
			Count(&count).Error; err != nil {
			return err
		}
		if count != int64(len(j.SourceVersionIDs)) {
			return fail(types.ErrNotFound, "source.work.ensure", "accepted source membership is incomplete")
		}
		return nil
	})
}

// ListOrganizeSourceWork reads all work for the current task attempt, retaining deferred and dead-letter facts for recovery decisions.
func (s *Store) ListOrganizeSourceWork(ctx context.Context, jobID string) (items []*types.OrganizeSourceWork, err error) {
	err = s.transaction(ctx, func(tx *gorm.DB) error {
		j, e := getJob(tx, jobID)
		if e != nil {
			return e
		}
		return tx.Where("job_id = ? AND job_attempt = ?", j.ID, j.Attempt).Order("created_at ASC, source_version_id ASC").Find(&items).Error
	})
	return
}

// DeferOrganizeSourceWork atomically increments one source's failure budget; the first five failures requeue it and the sixth moves it to dead letter.
func (s *Store) DeferOrganizeSourceWork(ctx context.Context, in types.SourceWorkActionInput) (*types.Receipt, error) {
	return s.mutate(ctx, in.RequestID, "source.work.defer", "", in, func(tx *gorm.DB) (*types.Receipt, error) {
		j, _, err := activeJob(tx, in.JobID)
		if err != nil {
			return nil, err
		}
		if in.JobAttempt != j.Attempt {
			return nil, fail(types.ErrStaleAttempt, "source.work.defer", "job attempt was superseded")
		}
		frozen := false
		for _, id := range j.SourceVersionIDs {
			if id == in.SourceVersionID {
				frozen = true
				break
			}
		}
		if !frozen {
			return nil, fail(types.ErrScopeViolation, "source.work.defer", "source was not frozen for this job")
		}
		if strings.TrimSpace(in.Diagnostic) == "" || in.NotBefore <= 0 {
			return nil, fail(types.ErrInvalid, "source.work.defer", "failure diagnostic and retry time are required")
		}
		source, err := frozenSourceIdentity(tx, j, in.SourceVersionID)
		if err != nil {
			return nil, err
		}
		if source.State == "processed" || source.State == "no-change" {
			return nil, fail(types.ErrVersionConflict, "source.work.defer", "source already has a published conclusion")
		}
		var work types.OrganizeSourceWork
		if err = tx.Where("job_id = ? AND job_attempt = ? AND source_version_id = ?", j.ID, j.Attempt, in.SourceVersionID).Take(&work).Error; err != nil {
			return nil, err
		}
		if work.LibraryID != j.LibraryID || work.State != "pending" || work.Version != in.ExpectedVersion {
			return nil, fail(types.ErrVersionConflict, "source.work.defer", "source work changed or is no longer pending")
		}
		state := "pending"
		if work.FailureCount+1 > 5 {
			state = "dead"
		}
		r := tx.Model(&types.OrganizeSourceWork{}).Where("id = ? AND version = ? AND state = ?", work.ID, work.Version, "pending").Updates(map[string]any{"failure_count": work.FailureCount + 1, "not_before": in.NotBefore, "state": state, "diagnostic": in.Diagnostic, "version": work.Version + 1, "updated_at": time.Now().UnixMilli()})
		if r.Error != nil {
			return nil, r.Error
		}
		if r.RowsAffected != 1 {
			return nil, fail(types.ErrVersionConflict, "source.work.defer", "source work changed")
		}
		return &types.Receipt{LibraryID: j.LibraryID, ResourceID: work.ID, JobID: j.ID, JobAttempt: j.Attempt, Version: work.Version + 1, State: state}, nil
	})
}

// settleOrganizeSourceWork shares the source conclusion's publication transaction and retains the dead-letter budget for auditing.
func settleOrganizeSourceWork(tx *gorm.DB, j *types.Job, conclusion types.SourceConclusion) error {
	return tx.Model(&types.OrganizeSourceWork{}).Where("job_id = ? AND job_attempt = ? AND source_version_id = ? AND state = ?", j.ID, j.Attempt, conclusion.SourceVersionID, "pending").Updates(map[string]any{"state": "done", "not_before": 0, "diagnostic": conclusion.Diagnostic, "version": gorm.Expr("version + 1"), "updated_at": time.Now().UnixMilli()}).Error
}

// StartOrganizeSourceWork records the current source's start before actually reading it, without creating a host execution.
func (s *Store) StartOrganizeSourceWork(ctx context.Context, jobID string, attempt int, sourceID string) (changed bool, err error) {
	err = s.transaction(ctx, func(tx *gorm.DB) error {
		j, _, err := activeJob(tx, jobID)
		if err != nil {
			return err
		}
		if j.Attempt != attempt {
			return fail(types.ErrStaleAttempt, "source.work.start", "job attempt changed")
		}
		if err = validateOrganizeSource(tx, j, sourceID); err != nil {
			return err
		}
		result := tx.Model(&types.OrganizeSourceWork{}).Where("job_id = ? AND job_attempt = ? AND source_version_id = ? AND started_at = ?", j.ID, j.Attempt, sourceID, 0).Update("started_at", time.Now().UnixMilli())
		changed = result.RowsAffected > 0
		return result.Error
	})
	return
}
