package store

import (
	"context"
	"encoding/json"
	types "github.com/JieWaZi/neux-knowledge/internal/domain"
	"github.com/JieWaZi/neux-knowledge/internal/wiki"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"strings"
	"time"
)

// PublishLite atomically commits results from the trusted Lite algorithm; model text cannot access this entry point.
func (s *Store) PublishLite(ctx context.Context, requestID, jobID string, attempt int, p *types.Publication) (*types.Receipt, error) {
	input := struct {
		JobID       string             // Knowledge task.
		Attempt     int                // Current task attempt.
		Publication *types.Publication // Page and source changes parsed by the core.
	}{jobID, attempt, p}
	return s.mutate(ctx, requestID, "lite.publish", "", input, func(tx *gorm.DB) (*types.Receipt, error) {
		j, l, err := activeJob(tx, jobID)
		if err != nil {
			return nil, err
		}
		if j.Attempt != attempt {
			return nil, fail(types.ErrStaleAttempt, "lite.publish", "job attempt changed")
		}
		if j.StructureVersion != l.StructureVersion {
			return nil, fail(types.ErrVersionConflict, "lite.publish", "library structure changed")
		}
		if j.LibrarySnapshot != nil && !j.LibrarySnapshot.IndexingStrategy.WikiEnabled && (len(p.Pages) > 0 || len(p.Themes) > 0) {
			return nil, fail(types.ErrCandidateInvalid, "publish", "RAG-only job cannot publish Wiki pages or themes")
		}
		if err = validatePublication(p, j); err != nil {
			return nil, err
		}
		if err = validatePublicationSources(tx, j, p); err != nil {
			return nil, err
		}
		allowed := map[string]bool{}
		for _, id := range j.SourceVersionIDs {
			allowed[id] = true
		}
		for _, conclusion := range p.Sources {
			if conclusion.State == "failed" || conclusion.State == "cancelled" {
				delete(allowed, conclusion.SourceVersionID)
			}
		}

		// A no-change conclusion also depends on read content; all read versions are validated in the same transaction as the write.
		for id, version := range p.PageVersions {
			page, e := getPage(tx, l.ID, id)
			if e != nil {
				return nil, e
			}
			if page.Version != version {
				return nil, fail(types.ErrVersionConflict, "publish", "read page changed")
			}
		}
		structureChanged := false
		for _, m := range p.Themes {
			if _, err = applyTheme(tx, l, m, true); err != nil {
				return nil, err
			}
			structureChanged = true
		}
		pageIDs := []string{}
		for _, m := range p.Pages {
			page := m.Page
			changed, e := writePage(tx, l, &page, m.ExpectedVersion, true, allowed)
			if e != nil {
				return nil, e
			}
			structureChanged = structureChanged || changed
			pageIDs = append(pageIDs, page.ID)
		}
		for _, conclusion := range p.Sources {
			v, e := getSource(tx, l.ID, conclusion.SourceVersionID)
			if e != nil {
				return nil, e
			}
			if v.State == "processed" || v.State == "no-change" {
				return nil, fail(types.ErrVersionConflict, "publish", "source already has a published conclusion")
			}
			updates := map[string]any{"state": conclusion.State, "coverage": []byte(conclusion.Coverage), "diagnostic": conclusion.Diagnostic, "row_version": v.RowVersion + 1}
			if conclusion.Description != "" {
				updates["description"] = conclusion.Description
				updates["profile"] = conclusion.Profile
				updates["summary_status"] = "completed"
			}
			r := tx.Model(&types.SourceVersion{}).Where("id = ? AND row_version = ?", v.ID, v.RowVersion).Updates(updates)
			if r.Error != nil {
				return nil, r.Error
			}
			if r.RowsAffected != 1 {
				return nil, fail(types.ErrVersionConflict, "publish", "source changed")
			}
			if err = settleOrganizeSourceWork(tx, j, conclusion); err != nil {
				return nil, err
			}
		}

		if !p.Maintenance {
			for _, mutation := range p.Pages {
				page := mutation.Page
				if page.PageType == "index" {
					continue
				}
				row := types.WikiFinalizeOp{ID: newID(), LibraryID: j.LibraryID, JobID: j.ID, JobAttempt: j.Attempt, Key: "slug/" + page.Slug, Kind: "slug", Slug: page.Slug, Title: page.Title, CreatedAt: time.Now().UnixMilli()}
				if err = tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "job_id"}, {Name: "job_attempt"}, {Name: "key"}}, DoUpdates: clause.AssignmentColumns([]string{"title"})}).Create(&row).Error; err != nil {
					return nil, err
				}
			}
			for _, mutation := range p.Pages {
				page := mutation.Page
				if page.PageType != "summary" || len(page.Evidence.Sources) == 0 {
					continue
				}
				source, e := getSource(tx, l.ID, page.Evidence.Sources[0].SourceVersionID)
				if e != nil {
					return nil, e
				}
				row := types.WikiFinalizeOp{ID: newID(), LibraryID: j.LibraryID, JobID: j.ID, JobAttempt: j.Attempt, Key: "change/" + source.ID, Kind: "added", DocTitle: source.Filename, DocSummary: page.Summary, CreatedAt: time.Now().UnixMilli()}
				if err = tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "job_id"}, {Name: "job_attempt"}, {Name: "key"}}, DoNothing: true}).Create(&row).Error; err != nil {
					return nil, err
				}
			}
			for _, conclusion := range p.Sources {
				if conclusion.State != "processed" && conclusion.State != "no-change" {
					continue
				}
				source, e := getSource(tx, l.ID, conclusion.SourceVersionID)
				if e != nil {
					return nil, e
				}
				row := types.WikiFinalizeOp{ID: newID(), LibraryID: j.LibraryID, JobID: j.ID, JobAttempt: j.Attempt, Key: "change/" + source.ID, Kind: "added", DocTitle: source.Filename, DocSummary: conclusion.Description, CreatedAt: time.Now().UnixMilli()}
				if err = tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "job_id"}, {Name: "job_attempt"}, {Name: "key"}}, DoNothing: true}).Create(&row).Error; err != nil {
					return nil, err
				}
			}
		}
		if structureChanged {
			if err = bumpStructure(tx, l); err != nil {
				return nil, err
			}
		}
		if len(p.Pages) > 0 {
			if err := refreshBacklinks(tx, l.ID); err != nil {
				return nil, err
			}
		}
		state := "processing"
		if p.Final {
			var all []types.SourceVersion
			if err = tx.Where("id IN ?", j.SourceVersionIDs).Find(&all).Error; err != nil {
				return nil, err
			}
			if len(all) != len(j.SourceVersionIDs) {
				return nil, fail(types.ErrNotFound, "publish", "final source identity is missing")
			}
			failed := 0
			for _, v := range all {
				if v.LibraryID != j.LibraryID {
					return nil, fail(types.ErrScopeViolation, "publish", "final source belongs to another library")
				}
				switch v.State {
				case "processed", "no-change":
				case "failed", "cancelled":
					failed++
				default:
					return nil, fail(types.ErrCandidateInvalid, "publish", "final publication has incomplete sources")
				}
			}
			state = "completed"
			if failed > 0 {
				state = "partial_failed"
			}
			if failed == len(all) {
				state = "failed"
			}
		}
		r := tx.Model(&types.Job{}).Where("id = ? AND version = ? AND attempt = ? AND cancel_requested = ?", j.ID, j.Version, j.Attempt, false).Updates(map[string]any{"version": j.Version + 1, "structure_version": l.StructureVersion, "state": state, "updated_at": time.Now().UnixMilli()})
		if r.Error != nil {
			return nil, r.Error
		}
		if r.RowsAffected != 1 {
			return nil, fail(types.ErrVersionConflict, "publish", "job changed")
		}
		return &types.Receipt{LibraryID: l.ID, ResourceID: j.ID, JobID: j.ID, Version: j.Version + 1, PageIDs: pageIDs, State: "published"}, nil
	})
}

// BeginModelStage saves actual input for one independent call without creating a host execution binding.
func (s *Store) BeginModelStage(ctx context.Context, jobID string, attempt int, name, kind string, sourceIDs []string, input json.RawMessage) (result *types.Stage, err error) {
	err = s.transaction(ctx, func(tx *gorm.DB) error {
		j, _, e := activeJob(tx, jobID)
		if e != nil {
			return e
		}
		if j.Attempt != attempt {
			return fail(types.ErrStaleAttempt, "lite.stage", "job attempt changed")
		}
		for _, id := range sourceIDs {
			if e = validateOrganizeSource(tx, j, id); e != nil {
				return e
			}
		}
		st := types.Stage{ID: newID(), JobID: j.ID, Name: name, Kind: kind, JobAttempt: j.Attempt, Attempt: 1, Version: 1, State: "running", Outcome: "unknown", Completeness: "unknown", Input: input, SourceVersionIDs: sourceIDs, CreatedAt: time.Now().UnixMilli()}
		if len(sourceIDs) == 1 {
			st.SourceVersionID = sourceIDs[0]
		}
		if e = tx.Create(&st).Error; e != nil {
			return e
		}
		result = &st
		return tx.Model(&types.Job{}).Where("id = ? AND version = ?", j.ID, j.Version).Updates(map[string]any{"state": "processing", "version": j.Version + 1, "updated_at": time.Now().UnixMilli()}).Error
	})
	return
}

// FinishModelStage saves raw output, actual errors, and usage for one call; cancellation can release the slot after the last call ends.
func (s *Store) FinishModelStage(ctx context.Context, id, text, reason, diagnostic string, usage *types.ModelUsage, diagnostics *types.ModelDiagnostics) error {
	return s.transaction(ctx, func(tx *gorm.DB) error {
		st, e := getStage(tx, id)
		if e != nil {
			return e
		}
		j, e := getJob(tx, st.JobID)
		if e != nil {
			return e
		}
		if j.Attempt != st.JobAttempt {
			return fail(types.ErrStaleAttempt, "lite.stage", "job attempt changed")
		}
		outcome := "succeeded"
		complete := "complete"
		if diagnostic != "" {
			outcome = "failed"
		}
		if j.CancelRequested {
			outcome = "cancelled"
		}
		if reason == "length" || reason == "max_tokens" || reason == "max_output_tokens" {
			complete = "truncated"
		}
		if e = tx.Model(&types.Stage{}).Where("id = ? AND state = ?", st.ID, "running").Updates(map[string]any{"state": "finished", "outcome": outcome, "completeness": complete, "result_text": text, "stop_reason": reason, "diagnostic": diagnostic, "usage": nullableJSON(usage), "diagnostics": nullableJSON(diagnostics), "finished_at": time.Now().UnixMilli(), "version": st.Version + 1}).Error; e != nil {
			return e
		}
		return settleCancelledJob(tx, j)
	})
}

// RecoverLiteStages marks calls left behind by abnormal process exit as failed; subsequent sources retry under the failure budget.
func (s *Store) RecoverLiteStages(ctx context.Context, jobID string, attempt int) error {
	return s.transaction(ctx, func(tx *gorm.DB) error {
		j, e := getJob(tx, jobID)
		if e != nil {
			return e
		}
		if j.Attempt != attempt {
			return fail(types.ErrStaleAttempt, "lite.recover", "job attempt changed")
		}
		if e = tx.Model(&types.Stage{}).Where("job_id = ? AND job_attempt = ? AND state = ?", jobID, attempt, "running").Updates(map[string]any{"state": "finished", "outcome": "failed", "diagnostic": "model call interrupted by process exit", "finished_at": time.Now().UnixMilli()}).Error; e != nil {
			return e
		}
		return settleCancelledJob(tx, j)
	})
}

// FindSimilarWikiPages uses the ported repository's bounded candidate query within the same library.
func (s *Store) FindSimilarWikiPages(ctx context.Context, libraryID, query string) ([]*types.WikiPageLite, error) {
	return NewWikiPageRepository(s.db).FindSimilarPages(ctx, libraryID, query, []string{types.WikiPageTypeEntity, types.WikiPageTypeConcept}, 5)
}

// RecordModelParseFailure marks a natively successful but structurally invalid response as a parse failure, preserving its raw text.
func (s *Store) RecordModelParseFailure(ctx context.Context, jobID, kind, sourceID, raw, diagnostic string) error {
	return s.transaction(ctx, func(tx *gorm.DB) error {
		return tx.Model(&types.Stage{}).Where("job_id = ? AND kind = ? AND source_version_id = ? AND result_text = ? AND outcome = ?", jobID, kind, sourceID, raw, "succeeded").Updates(map[string]any{"outcome": "failed", "diagnostic": diagnostic}).Error
	})
}

func nullableJSON(value any) any {
	if value == nil {
		return nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	if string(raw) == "null" {
		return nil
	}
	return string(raw)
}

// ListWikiFinalizeOps reads the bounded finalization updates actually published by this task.
func (s *Store) ListWikiFinalizeOps(ctx context.Context, jobID string, attempt int) (items []*types.WikiFinalizeOp, err error) {
	err = s.transaction(ctx, func(tx *gorm.DB) error {
		return tx.Where("job_id = ? AND job_attempt = ?", jobID, attempt).Order("created_at ASC, key ASC").Find(&items).Error
	})
	return
}

// UpdateWikiMaintenance performs automatic link maintenance writes without creating content history versions.
func (s *Store) UpdateWikiMaintenance(ctx context.Context, jobID string, attempt int, page *types.WikiPage, expected int) error {
	return s.transaction(ctx, func(tx *gorm.DB) error {
		j, l, err := activeJob(tx, jobID)
		if err != nil {
			return err
		}
		if j.Attempt != attempt {
			return fail(types.ErrStaleAttempt, "lite.links", "job attempt changed")
		}
		if j.StructureVersion != l.StructureVersion {
			return fail(types.ErrVersionConflict, "lite.links", "library structure changed")
		}
		existing, err := getPage(tx, l.ID, page.ID)
		if err != nil {
			return err
		}
		if existing.Version != expected {
			return fail(types.ErrVersionConflict, "lite.links", "page changed")
		}
		for _, text := range existing.ProtectedContent {
			if !strings.Contains(page.Content, text) {
				return fail(types.ErrManualProtected, "lite.links", "protected manual content changed")
			}
		}
		page.OutLinks = wiki.ParseOutLinks(page.Content)
		linksJSON, err := json.Marshal(page.OutLinks)
		if err != nil {
			return err
		}
		if err = tx.Model(&types.WikiPage{}).Where("id = ? AND version = ?", page.ID, expected).Updates(map[string]any{"content": page.Content, "out_links": linksJSON}).Error; err != nil {
			return err
		}
		return refreshBacklinks(tx, l.ID)
	})
}

// FindWikiPagesByNormalizedTitles uses a bounded exact identity query compatible with SQLite.
func (s *Store) FindWikiPagesByNormalizedTitles(ctx context.Context, libraryID, pageType string, identities []string) ([]*types.WikiPageLite, error) {
	return NewWikiPageRepository(s.db).FindPagesByNormalizedTitles(ctx, libraryID, pageType, identities)
}
