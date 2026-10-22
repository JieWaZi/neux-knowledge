package store

import (
	"context"
	"errors"

	types "github.com/JieWaZi/neux-knowledge/internal/domain"
	"gorm.io/gorm"
)

// stageReadsUnavailableSource reads only the actual source dependencies persisted by the stage.
func stageReadsUnavailableSource(stage *types.Stage, sourceID string) bool {
	if sourceID == "" {
		return false
	}
	if stage.SourceVersionID == sourceID {
		return true
	}
	for _, id := range stage.SourceVersionIDs {
		if id == sourceID {
			return true
		}
	}
	return false
}

func sourceIsFrozen(j *types.Job, sourceID string) bool {
	for _, id := range j.SourceVersionIDs {
		if id == sourceID {
			return true
		}
	}
	return false
}

// frozenSourceIdentity checks only the accepted scope and actual ownership; failure records must not depend on the current head or archive state.
func frozenSourceIdentity(tx *gorm.DB, j *types.Job, id string) (*types.SourceVersion, error) {
	if !sourceIsFrozen(j, id) {
		return nil, fail(types.ErrScopeViolation, "job.source", "source is outside the frozen job")
	}
	var source types.SourceVersion
	if err := tx.Where("id = ?", id).Take(&source).Error; err != nil {
		return nil, err
	}
	if source.LibraryID != j.LibraryID {
		return nil, fail(types.ErrScopeViolation, "job.source", "source belongs to another library")
	}
	return &source, nil
}

// validateOrganizeSource checks liveness only for this actual contribution, without reading unrelated sources or historical page attribution.
func validateOrganizeSource(tx *gorm.DB, j *types.Job, id string) error {
	source, err := frozenSourceIdentity(tx, j, id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return fail(types.ErrSourceUnavailable, "job.source", "frozen source is missing")
	}
	if err != nil {
		return err
	}
	if source.Archived {
		return fail(types.ErrSourceUnavailable, "job.source", "frozen source is archived")
	}
	var head types.SourceHead
	if err = tx.Where("id = ?", source.SourceID).Take(&head).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fail(types.ErrSourceUnavailable, "job.source", "frozen source head is missing")
		}
		return err
	}
	if head.LibraryID != j.LibraryID {
		return fail(types.ErrScopeViolation, "job.source", "source head belongs to another library")
	}
	if head.VersionID != source.ID {
		return fail(types.ErrSourceUnavailable, "job.source", "frozen source was superseded")
	}
	return nil
}

// ValidateOrganizeSource provides a read-only single-source barrier for the trusted pipeline; unavailable sources do not masquerade as revoked permissions.
func (s *Store) ValidateOrganizeSource(ctx context.Context, jobID string, attempt int, sourceID string) error {
	return s.transaction(ctx, func(tx *gorm.DB) error {
		j, _, err := activeJob(tx, jobID)
		if err != nil {
			return err
		}
		if j.Attempt != attempt {
			return fail(types.ErrStaleAttempt, "job.source", "job attempt was superseded")
		}
		return validateOrganizeSource(tx, j, sourceID)
	})
}

func validatePublicationSources(tx *gorm.DB, j *types.Job, p *types.Publication) error {
	reads := map[string]bool{}
	for _, id := range p.SourceVersionIDs {
		reads[id] = true
	}
	for _, conclusion := range p.Sources {
		if conclusion.State == "processed" || conclusion.State == "no-change" {
			reads[conclusion.SourceVersionID] = true
		}
	}
	for _, mutation := range p.Pages {
		prior := map[string]bool{}
		if mutation.Page.ID != "" {
			old, err := getPage(tx, j.LibraryID, mutation.Page.ID)
			if err != nil && !(mutation.ExpectedVersion == 0 && errors.Is(err, gorm.ErrRecordNotFound)) {
				return err
			}
			if old != nil {
				for _, ref := range old.Evidence.Sources {
					prior[digest(ref)] = true
				}
			}
		}
		for _, ref := range mutation.Page.Evidence.Sources {
			// Identical historical evidence promises only its original source identity; it does not require old source files to remain the current head.
			if !prior[digest(ref)] {
				reads[ref.SourceVersionID] = true
			}
		}
	}
	for id := range reads {
		if err := validateOrganizeSource(tx, j, id); err != nil {
			return err
		}
	}
	return nil
}
