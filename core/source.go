package core

import "context"

// ImportSource performs the corresponding knowledge domain operation under the SDK contract.
func (e *Engine) ImportSource(ctx context.Context, in ImportSourceInput) (*Receipt, error) {
	return e.store.ImportSource(ctx, in)
}

// ArchiveSource performs the corresponding knowledge domain operation under the SDK contract.
func (e *Engine) ArchiveSource(ctx context.Context, in ArchiveSourceInput) (*Receipt, error) {
	return e.store.ArchiveSource(ctx, in)
}

// GetSource performs the corresponding knowledge domain operation under the SDK contract.
func (e *Engine) GetSource(ctx context.Context, libraryID, versionID string) (*SourceVersion, error) {
	return e.store.GetSource(ctx, libraryID, versionID)
}

// ListSources performs the corresponding knowledge domain operation under the SDK contract.
func (e *Engine) ListSources(ctx context.Context, libraryID string, offset, limit int) ([]*SourceVersion, int64, error) {
	return e.store.ListSources(ctx, libraryID, offset, limit)
}

// ReadRaw performs the corresponding knowledge domain operation under the SDK contract.
func (e *Engine) ReadRaw(ctx context.Context, libraryID, versionID string) ([]byte, error) {
	return e.store.ReadRaw(ctx, libraryID, versionID)
}
