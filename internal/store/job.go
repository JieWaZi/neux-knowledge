package store

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/JieWaZi/neux-knowledge/internal/domain"
	"gorm.io/gorm"
)

func hasOperation(p types.PermissionSnapshot, op string) bool {
	for _, v := range p.Operations {
		if v == op {
			return true
		}
	}
	return false
}
func getJob(tx *gorm.DB, id string) (*types.Job, error) {
	var j types.Job
	if err := tx.Where("id = ?", id).Take(&j).Error; err != nil {
		return nil, err
	}
	return &j, nil
}
func activeJob(tx *gorm.DB, id string) (*types.Job, *types.Library, error) {
	j, err := getJob(tx, id)
	if err != nil {
		return nil, nil, err
	}
	if j.CancelRequested || j.State == "cancelled" {
		return nil, nil, fail(types.ErrCancelled, "job", "job was cancelled")
	}
	if j.State != "queued" && j.State != "processing" {
		return nil, nil, fail(types.ErrVersionConflict, "job", "job is not active")
	}
	l, err := getLibrary(tx, j.LibraryID, true)
	if err != nil {
		return nil, nil, err
	}
	if l.PolicyVersion != j.Permission.PolicyVersion || !hasOperation(j.Permission, "organize") {
		return nil, nil, fail(types.ErrScopeViolation, "job", "accepted permission is no longer valid")
	}
	return j, l, nil
}
func validateFrozenSources(tx *gorm.DB, j *types.Job) error {
	if len(j.SourceVersionIDs) == 0 {
		return fail(types.ErrInvalid, "job.sources", "job has no frozen sources")
	}
	if len(j.SourceVersionIDs) > 1000 {
		return fail(types.ErrInvalid, "job.sources", "source batch exceeds supported bound")
	}
	var sources []types.SourceVersion
	if err := tx.Where("id IN ?", j.SourceVersionIDs).Find(&sources).Error; err != nil {
		return err
	}
	if len(sources) != len(j.SourceVersionIDs) {
		return fail(types.ErrScopeViolation, "job.sources", "frozen source is missing")
	}
	heads := make([]string, 0, len(sources))
	for _, v := range sources {
		if v.LibraryID != j.LibraryID || v.Archived {
			return fail(types.ErrScopeViolation, "job.sources", "source is archived or out of scope")
		}
		heads = append(heads, v.SourceID)
	}
	var current []types.SourceHead
	if err := tx.Where("id IN ?", heads).Find(&current).Error; err != nil {
		return err
	}
	latest := map[string]string{}
	for _, h := range current {
		if h.LibraryID != j.LibraryID {
			return fail(types.ErrScopeViolation, "job.sources", "source head belongs to another library")
		}
		latest[h.ID] = h.VersionID
	}
	for _, v := range sources {
		if latest[v.SourceID] != v.ID {
			return fail(types.ErrVersionConflict, "job.sources", "source version has changed")
		}
	}
	return nil
}

// AcceptJob persists only the frozen scope and sources; it does not execute models.
func (s *Store) AcceptJob(ctx context.Context, in types.AcceptJobInput) (*types.Receipt, error) {
	return s.mutate(ctx, in.RequestID, "job.accept", in.LibraryID, in, func(tx *gorm.DB) (*types.Receipt, error) {
		l, err := getLibrary(tx, in.LibraryID, true)
		if err != nil {
			return nil, err
		}
		if l.Version != in.ExpectedVersion || l.StructureVersion != in.StructureVersion {
			return nil, fail(types.ErrVersionConflict, "job.accept", "library or structure changed")
		}
		if in.Permission.PolicyVersion != l.PolicyVersion || !hasOperation(in.Permission, "organize") {
			return nil, fail(types.ErrScopeViolation, "job.accept", "organize permission is required")
		}
		if strings.TrimSpace(in.RulesVersion) == "" || strings.TrimSpace(in.ConfigDigest) == "" {
			return nil, fail(types.ErrInvalid, "job.accept", "frozen rules and configuration are required")
		}
		if err := types.ValidateOrganizeLibrary(l); err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		for _, id := range in.SourceVersionIDs {
			if id == "" || seen[id] {
				return nil, fail(types.ErrInvalid, "job.accept", "source IDs must be unique")
			}
			seen[id] = true
		}
		if len(in.SourceVersionIDs) == 0 {
			if in.Rebuild {
				return nil, fail(types.ErrInvalid, "job.accept", "rebuild requires explicit source versions")
			}
			return &types.Receipt{State: "no-op"}, nil
		}
		j := types.Job{ActivityActor: types.ActivityActorFromContext(ctx), ID: newID(), LibraryID: l.ID, RequestID: in.RequestID, State: "queued", Attempt: 1, Version: 1, StructureVersion: l.StructureVersion, SourceVersionIDs: in.SourceVersionIDs, Permission: in.Permission, RulesVersion: in.RulesVersion, ConfigDigest: in.ConfigDigest, LibrarySnapshot: l, EmbeddingFingerprint: in.EmbeddingFingerprint, AttemptStartedAt: time.Now().UnixMilli(), CreatedAt: time.Now().UnixMilli(), UpdatedAt: time.Now().UnixMilli()}
		if err = validateFrozenSources(tx, &j); err != nil {
			return nil, err
		}
		var sources []types.SourceVersion
		if err = tx.Where("id IN ?", j.SourceVersionIDs).Find(&sources).Error; err != nil {
			return nil, err
		}
		pending := make([]string, 0, len(sources))
		for _, v := range sources {
			if in.Rebuild || (v.State != "processed" && v.State != "no-change") {
				pending = append(pending, v.ID)
			}
		}
		if len(pending) == 0 {
			return &types.Receipt{State: "no-op"}, nil
		}
		j.SourceVersionIDs = pending
		var active int64
		if err = tx.Model(&types.Job{}).Where("library_id = ? AND state IN ?", l.ID, []string{"queued", "processing", "cancel_requested"}).Count(&active).Error; err != nil {
			return nil, err
		}
		if active > 0 {
			return nil, fail(types.ErrVersionConflict, "job.accept", "another library writer is active")
		}
		if in.Rebuild {
			// Reparse clears only derived chunks, vectors, and summaries; original files and maintained Wiki content remain.
			// Validation and cleanup occur in the acceptance transaction; mutate directly returns the original receipt when the same request is replayed.
			for _, source := range sources {
				if err = tx.Where("library_id = ? AND source_version_id = ?", l.ID, source.ID).Delete(&types.EmbeddingChunkIndex{}).Error; err != nil {
					return nil, err
				}
				changed := tx.Model(&types.SourceVersion{}).Where("id = ? AND library_id = ? AND row_version = ?", source.ID, l.ID, source.RowVersion).Updates(map[string]any{
					"state": "pending", "description": "", "profile": nil, "summary_status": "", "coverage": nil, "diagnostic": "", "row_version": source.RowVersion + 1,
				})
				if changed.Error != nil {
					return nil, changed.Error
				}
				if changed.RowsAffected != 1 {
					return nil, fail(types.ErrVersionConflict, "job.accept", "source state changed before rebuild")
				}
			}
		}
		if err = tx.Create(&j).Error; err != nil {
			return nil, err
		}
		return &types.Receipt{ResourceID: j.ID, JobID: j.ID, Version: j.Version, State: "accepted"}, nil
	})
}

// GetJob reads the persistent task and current attempt.
func (s *Store) GetJob(ctx context.Context, id string) (result *types.Job, err error) {
	err = s.transaction(ctx, func(tx *gorm.DB) error { var e error; result, e = getJob(tx, id); return e })
	return
}

// ListJobs returns a page of actual tasks.
func (s *Store) ListJobs(ctx context.Context, lib string, offset, limit int) (items []*types.Job, total int64, err error) {
	offset, limit = pagination(offset, limit)
	err = s.transaction(ctx, func(tx *gorm.DB) error {
		if _, e := getLibrary(tx, lib, false); e != nil {
			return e
		}
		q := tx.Model(&types.Job{}).Where("library_id = ?", lib)
		if e := q.Count(&total).Error; e != nil {
			return e
		}
		return q.Order("created_at DESC, id ASC").Offset(offset).Limit(limit).Find(&items).Error
	})
	return
}

// CancelJob sets a persistent barrier on the task row shared with publication transactions; published results remain.
func (s *Store) CancelJob(ctx context.Context, in types.JobActionInput) (*types.Receipt, error) {
	return s.mutate(ctx, in.RequestID, "job.cancel", "", in, func(tx *gorm.DB) (*types.Receipt, error) {
		j, err := getJob(tx, in.JobID)
		if err != nil {
			return nil, err
		}
		if j.Version != in.ExpectedVersion {
			return nil, fail(types.ErrVersionConflict, "job.cancel", "job changed")
		}
		if j.CancelRequested || j.State == "completed" || j.State == "partial_failed" || j.State == "failed" || j.State == "cancelled" {
			return &types.Receipt{LibraryID: j.LibraryID, ResourceID: j.ID, JobID: j.ID, Version: j.Version, State: j.State}, nil
		}
		var running int64
		if err := tx.Model(&types.Stage{}).Where("job_id = ? AND job_attempt = ? AND state = ?", j.ID, j.Attempt, "running").Count(&running).Error; err != nil {
			return nil, err
		}
		state := "cancelled"
		if running > 0 {
			state = "cancel_requested"
		}
		r := tx.Model(&types.Job{}).Where("id = ? AND version = ?", j.ID, j.Version).Updates(map[string]any{"cancel_requested": true, "state": state, "version": j.Version + 1, "updated_at": time.Now().UnixMilli()})
		if r.Error != nil {
			return nil, r.Error
		}
		if r.RowsAffected != 1 {
			return nil, fail(types.ErrVersionConflict, "job.cancel", "job changed")
		}
		return &types.Receipt{LibraryID: j.LibraryID, ResourceID: j.ID, JobID: j.ID, Version: j.Version + 1, State: state}, nil
	})
}

// settleCancelledJob settles internal cancellation and releases the write slot only after the currently running stage ends.
func settleCancelledJob(tx *gorm.DB, j *types.Job) error {
	if !j.CancelRequested || j.State != "cancel_requested" {
		return nil
	}
	var running int64
	if err := tx.Model(&types.Stage{}).Where("job_id = ? AND job_attempt = ? AND state = ?", j.ID, j.Attempt, "running").Count(&running).Error; err != nil {
		return err
	}
	if running > 0 {
		return nil
	}
	state := "cancelled"

	r := tx.Model(&types.Job{}).Where("id = ? AND version = ? AND cancel_requested = ?", j.ID, j.Version, true).Updates(map[string]any{"state": state, "version": j.Version + 1, "updated_at": time.Now().UnixMilli()})
	if r.Error != nil {
		return r.Error
	}
	if r.RowsAffected != 1 {
		return fail(types.ErrVersionConflict, "job.cancel", "job changed during cancellation settlement")
	}
	j.Version++
	j.State = state
	return nil
}

// RetryJob accepts a failed task again without expanding permissions or discarding earlier stage audits.
func (s *Store) RetryJob(ctx context.Context, in types.JobActionInput) (*types.Receipt, error) {
	return s.mutate(ctx, in.RequestID, "job.retry", "", in, func(tx *gorm.DB) (*types.Receipt, error) {
		j, err := getJob(tx, in.JobID)
		if err != nil {
			return nil, err
		}
		if j.Version != in.ExpectedVersion {
			return nil, fail(types.ErrVersionConflict, "job.retry", "job changed")
		}
		if j.State != "failed" && j.State != "partial_failed" && j.State != "cancelled" {
			return nil, fail(types.ErrInvalid, "job.retry", "only terminal unsuccessful jobs can retry")
		}
		l, err := getLibrary(tx, j.LibraryID, true)
		if err != nil {
			return nil, err
		}
		if j.Permission.PolicyVersion != l.PolicyVersion {
			return nil, fail(types.ErrScopeViolation, "job.retry", "permission changed")
		}
		if err = validateFrozenSources(tx, j); err != nil {
			return nil, err
		}
		r := tx.Model(&types.Job{}).Where("id = ? AND version = ?", j.ID, j.Version).Updates(map[string]any{"attempt": j.Attempt + 1, "state": "queued", "diagnostic": "", "attempt_started_at": time.Now().UnixMilli(), "cancel_requested": false, "version": j.Version + 1, "structure_version": l.StructureVersion, "updated_at": time.Now().UnixMilli()})
		if r.Error != nil {
			return nil, r.Error
		}
		if r.RowsAffected != 1 {
			return nil, fail(types.ErrVersionConflict, "job.retry", "job changed")
		}
		return &types.Receipt{LibraryID: l.ID, ResourceID: j.ID, JobID: j.ID, Version: j.Version + 1, State: "accepted"}, nil
	})
}
func getStage(tx *gorm.DB, id string) (*types.Stage, error) {
	var st types.Stage
	if err := tx.Where("id = ?", id).Take(&st).Error; err != nil {
		return nil, err
	}
	return &st, nil
}

// GetStage reads inputs, execution bindings, completion facts, and checkpoints.
func (s *Store) GetStage(ctx context.Context, id string) (result *types.Stage, err error) {
	err = s.transaction(ctx, func(tx *gorm.DB) error { var e error; result, e = getStage(tx, id); return e })
	return
}

// ListStages returns all attempts of the same task for recovery and auditing.
func (s *Store) ListStages(ctx context.Context, id string) (items []*types.Stage, err error) {
	err = s.transaction(ctx, func(tx *gorm.DB) error {
		if _, e := getJob(tx, id); e != nil {
			return e
		}
		return tx.Where("job_id = ?", id).Order("created_at ASC, id ASC").Find(&items).Error
	})
	return
}

func validatePublication(p *types.Publication, j *types.Job) error {
	for id, version := range p.PageVersions {
		if id == "" || version < 1 {
			return fail(types.ErrCandidateInvalid, "publication", "invalid page read version")
		}
	}
	sources := map[string]bool{}
	for _, id := range j.SourceVersionIDs {
		sources[id] = true
	}
	readSources := map[string]bool{}
	for _, id := range p.SourceVersionIDs {
		if !sources[id] || readSources[id] {
			return fail(types.ErrScopeViolation, "publication", "source read is out of scope or duplicated")
		}
		readSources[id] = true
	}
	seen := map[string]bool{}
	for _, c := range p.Sources {
		if !sources[c.SourceVersionID] || seen[c.SourceVersionID] {
			return fail(types.ErrScopeViolation, "publication", "source conclusion is out of scope or duplicated")
		}
		seen[c.SourceVersionID] = true
		switch c.State {
		case "processed", "no-change":
			if !c.Complete || len(c.Coverage) == 0 || !json.Valid(c.Coverage) {
				return fail(types.ErrCandidateInvalid, "publication", "successful source requires confirmed coverage")
			}
		case "failed", "cancelled":
			if c.Diagnostic == "" {
				return fail(types.ErrCandidateInvalid, "publication", "unsuccessful source requires diagnostic")
			}
		default:
			return fail(types.ErrCandidateInvalid, "publication", "invalid source conclusion")
		}
	}
	ids := map[string]bool{}
	slugs := map[string]bool{}
	for _, m := range p.Pages {
		if m.Page.ID != "" && ids[m.Page.ID] || slugs[m.Page.Slug] {
			return fail(types.ErrCandidateInvalid, "publication", "duplicate page mutation")
		}
		ids[m.Page.ID] = true
		slugs[m.Page.Slug] = true
	}
	return nil
}

// FailJob settles an actual failure using the task CAS shared with publication and cancellation; published content is not rolled back.
func (s *Store) FailJob(ctx context.Context, in types.FailJobInput) (*types.Receipt, error) {
	return s.mutate(ctx, in.RequestID, "job.fail", "", in, func(tx *gorm.DB) (*types.Receipt, error) {
		j, err := getJob(tx, in.JobID)
		if err != nil {
			return nil, err
		}
		if j.Version != in.ExpectedVersion {
			return nil, fail(types.ErrVersionConflict, "job.fail", "job changed")
		}
		if j.CancelRequested || j.State == "cancelled" {
			return nil, fail(types.ErrCancelled, "job.fail", "job was cancelled")
		}
		if j.State != "queued" && j.State != "processing" {
			return nil, fail(types.ErrVersionConflict, "job.fail", "job is already terminal")
		}
		if strings.TrimSpace(in.Diagnostic) == "" {
			return nil, fail(types.ErrInvalid, "job.fail", "failure diagnostic is required")
		}
		allowed := map[string]bool{}
		for _, id := range j.SourceVersionIDs {
			allowed[id] = true
		}
		seen := map[string]bool{}
		for _, c := range in.Sources {
			if !allowed[c.SourceVersionID] || seen[c.SourceVersionID] {
				return nil, fail(types.ErrScopeViolation, "job.fail", "source is not frozen for this job or is duplicated")
			}
			seen[c.SourceVersionID] = true
			if (c.State != "failed" && c.State != "cancelled") || c.Diagnostic == "" {
				return nil, fail(types.ErrInvalid, "job.fail", "only diagnosed failure conclusions are allowed")
			}
			v, err := getSource(tx, j.LibraryID, c.SourceVersionID)
			if err != nil {
				return nil, err
			}
			if v.State == "processed" || v.State == "no-change" {
				continue
			}
			r := tx.Model(&types.SourceVersion{}).Where("id = ? AND row_version = ?", v.ID, v.RowVersion).Updates(map[string]any{"state": c.State, "coverage": []byte(c.Coverage), "diagnostic": c.Diagnostic, "row_version": v.RowVersion + 1})
			if r.Error != nil {
				return nil, r.Error
			}
			if r.RowsAffected != 1 {
				return nil, fail(types.ErrVersionConflict, "job.fail", "source changed")
			}
		}
		var published int64
		if len(j.SourceVersionIDs) > 0 {
			if err = tx.Model(&types.SourceVersion{}).Where("id IN ? AND state IN ?", j.SourceVersionIDs, []string{"processed", "no-change"}).Count(&published).Error; err != nil {
				return nil, err
			}
		}
		state := "failed"
		if published > 0 {
			state = "partial_failed"
		}
		r := tx.Model(&types.Job{}).Where("id = ? AND version = ? AND cancel_requested = ?", j.ID, j.Version, false).Updates(map[string]any{"state": state, "diagnostic": in.Diagnostic, "version": j.Version + 1, "updated_at": time.Now().UnixMilli()})
		if r.Error != nil {
			return nil, r.Error
		}
		if r.RowsAffected != 1 {
			return nil, fail(types.ErrVersionConflict, "job.fail", "job changed")
		}
		return &types.Receipt{LibraryID: j.LibraryID, ResourceID: j.ID, JobID: j.ID, Version: j.Version + 1, State: state}, nil
	})
}

// GetActiveJob reads only the sole task occupying this library's slot; historical ordering does not determine current state.
func (s *Store) GetActiveJob(ctx context.Context, libraryID string) (result *types.Job, err error) {
	err = s.transaction(ctx, func(tx *gorm.DB) error {
		var rows []*types.Job
		if e := tx.Where("library_id = ? AND state IN ?", libraryID, []string{"queued", "processing", "cancel_requested"}).Limit(1).Find(&rows).Error; e != nil {
			return e
		}
		if len(rows) > 0 {
			result = rows[0]
		}
		return nil
	})
	return
}

// ListJobsForActor 只查询同一库的原作业表，JSON 身份来自受信 ActivityActor，不读取宿主配置副本。
func (s *Store) ListJobsForActor(ctx context.Context, lib, actor string, offset, limit int) (items []*types.Job, total int64, err error) {
	if lib == "" || actor == "" || offset < 0 || limit < 1 || limit > 100 {
		return nil, 0, fail(types.ErrInvalid, "job.list", "explicit actor and pagination required")
	}
	err = s.transaction(ctx, func(tx *gorm.DB) error {
		if _, e := getLibrary(tx, lib, false); e != nil {
			return e
		}
		query := tx.Model(&types.Job{}).Where("library_id = ? AND json_extract(activity_actor, '$.userId') = ?", lib, actor)
		if e := query.Count(&total).Error; e != nil {
			return e
		}
		return query.Order("created_at DESC, id ASC").Offset(offset).Limit(limit).Find(&items).Error
	})
	return
}
