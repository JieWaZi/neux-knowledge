package core

import "context"

// CreateLibrary performs the corresponding knowledge domain operation under the SDK contract.
func (e *Engine) CreateLibrary(ctx context.Context, in CreateLibraryInput) (*Receipt, error) {
	return e.store.CreateLibrary(ctx, in)
}

// UpdateLibrary performs the corresponding knowledge domain operation under the SDK contract.
func (e *Engine) UpdateLibrary(ctx context.Context, in UpdateLibraryInput) (*Receipt, error) {
	return e.store.UpdateLibrary(ctx, in)
}

// GetLibrary performs the corresponding knowledge domain operation under the SDK contract.
func (e *Engine) GetLibrary(ctx context.Context, id string) (*Library, error) {
	return e.store.GetLibrary(ctx, id)
}

// ListLibraries performs the corresponding knowledge domain operation under the SDK contract.
func (e *Engine) ListLibraries(ctx context.Context, offset, limit int) ([]*Library, int64, error) {
	return e.store.ListLibraries(ctx, offset, limit)
}
