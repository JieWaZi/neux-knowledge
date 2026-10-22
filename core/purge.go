package core

import (
	"context"
	types "github.com/JieWaZi/neux-knowledge/internal/domain"
)

// Public source cleanup models do not require hosts to import internal packages.
type (
	PurgePreviewInput       = types.PurgePreviewInput
	PurgeVersion            = types.PurgeVersion
	PurgePageImpact         = types.PurgePageImpact
	SourcePurgePreview      = types.SourcePurgePreview
	ConfirmSourcePurgeInput = types.ConfirmSourcePurgeInput
	RecoverSourcePurgeInput = types.RecoverSourcePurgeInput
	SourcePurgeStatus       = types.SourcePurgeStatus
	SourcePurgeReview       = types.SourcePurgeReview
)

// ParseSourcePurgeReview reads only allowed existing cleanup identities from pages for safe host display and export.
func ParseSourcePurgeReview(metadata JSON) *SourcePurgeReview {
	return types.ParseSourcePurgeReview(metadata)
}

// PreviewSourcePurge previews the actual impact on all versions and internal copies of a source without deleting them.
func (e *Engine) PreviewSourcePurge(ctx context.Context, in PurgePreviewInput) (*SourcePurgePreview, error) {
	return e.store.PreviewSourcePurge(ctx, in)
}

// ConfirmSourcePurge verifies the preview, atomically revokes the source, and creates a cleanup manifest; the receipt indicates acceptance only.
func (e *Engine) ConfirmSourcePurge(ctx context.Context, in ConfirmSourcePurgeInput) (*Receipt, error) {
	return e.store.ConfirmSourcePurge(ctx, in)
}

// GetSourcePurge reads actual source deletion, shared retention, and blocking caused by unknown ownership.
func (e *Engine) GetSourcePurge(ctx context.Context, libraryID, purgeID string) (*SourcePurgeStatus, error) {
	return e.store.GetSourcePurge(ctx, libraryID, purgeID)
}

// RecoverSourcePurge continues the same manifest under current authorization and can run independently after reopening the core.
func (e *Engine) RecoverSourcePurge(ctx context.Context, in RecoverSourcePurgeInput) (*Receipt, error) {
	return e.store.RecoverSourcePurge(ctx, in)
}
