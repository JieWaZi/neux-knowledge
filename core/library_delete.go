package core

import (
	"context"
	types "github.com/JieWaZi/neux-knowledge/internal/domain"
)

type DeleteLibraryInput = types.DeleteLibraryInput
type RecoverLibraryDeletionInput = types.RecoverLibraryDeletionInput
type LibraryDeletionStatus = types.LibraryDeletionStatus

// DeleteLibrary permanently removes knowledge content and cleans managed sources according to the persisted manifest.
func (e *Engine) DeleteLibrary(ctx context.Context, in DeleteLibraryInput) (*Receipt, error) {
	return e.store.DeleteLibrary(ctx, in)
}

// GetLibraryDeletion reads the minimal cleanup state that survives library deletion.
func (e *Engine) GetLibraryDeletion(ctx context.Context, id string) (*LibraryDeletionStatus, error) {
	return e.store.GetLibraryDeletion(ctx, id)
}

// RecoverLibraryDeletion continues source cleanup for an already confirmed library deletion.
func (e *Engine) RecoverLibraryDeletion(ctx context.Context, in RecoverLibraryDeletionInput) (*Receipt, error) {
	return e.store.RecoverLibraryDeletion(ctx, in)
}
