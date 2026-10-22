package core

import "context"

// WritePage performs the corresponding knowledge domain operation under the SDK contract.
func (e *Engine) WritePage(ctx context.Context, in WritePageInput) (*Receipt, error) {
	return e.store.WritePage(ctx, in)
}

// RestorePage performs the corresponding knowledge domain operation under the SDK contract.
func (e *Engine) RestorePage(ctx context.Context, in RestorePageInput) (*Receipt, error) {
	return e.store.RestorePage(ctx, in)
}

// GetPage performs the corresponding knowledge domain operation under the SDK contract.
func (e *Engine) GetPage(ctx context.Context, libraryID, pageID string) (*Page, error) {
	return e.store.GetPage(ctx, libraryID, pageID)
}

// GetPageBySlug performs the corresponding knowledge domain operation under the SDK contract.
func (e *Engine) GetPageBySlug(ctx context.Context, libraryID, slug string) (*Page, error) {
	return e.store.GetPageBySlug(ctx, libraryID, slug)
}

// ListPages performs the corresponding knowledge domain operation under the SDK contract.
func (e *Engine) ListPages(ctx context.Context, q PageQuery) ([]*Page, int64, error) {
	return e.store.ListPages(ctx, q)
}

// ListRevisions performs the corresponding knowledge domain operation under the SDK contract.
func (e *Engine) ListRevisions(ctx context.Context, libraryID, pageID string, offset, limit int) ([]*Revision, int64, error) {
	return e.store.ListRevisions(ctx, libraryID, pageID, offset, limit)
}

// GetRevision performs the corresponding knowledge domain operation under the SDK contract.
func (e *Engine) GetRevision(ctx context.Context, libraryID, pageID string, version int) (*Revision, error) {
	return e.store.GetRevision(ctx, libraryID, pageID, version)
}

// WriteTheme performs the corresponding knowledge domain operation under the SDK contract.
func (e *Engine) WriteTheme(ctx context.Context, in WriteThemeInput) (*Receipt, error) {
	return e.store.WriteTheme(ctx, in)
}

// ListThemes performs the corresponding knowledge domain operation under the SDK contract.
func (e *Engine) ListThemes(ctx context.Context, libraryID string) ([]*Theme, error) {
	return e.store.ListThemes(ctx, libraryID)
}

// WriteAgentPage saves agent-authored page content while retaining existing human protections.
func (e *Engine) WriteAgentPage(ctx context.Context, in WritePageInput) (*Receipt, error) {
	return e.store.WriteAgentPage(ctx, in)
}

// ReplacePageText replaces every exact match and atomically saves evidence and history.
func (e *Engine) ReplacePageText(ctx context.Context, in ReplacePageTextInput) (*Receipt, error) {
	return e.store.ReplacePageText(ctx, in)
}

// RenamePage preserves stable page identity and atomically updates readable links.
func (e *Engine) RenamePage(ctx context.Context, in RenamePageInput) (*Receipt, error) {
	return e.store.RenamePage(ctx, in)
}

// DeletePage archives the current page and atomically removes inbound links while retaining revision history.
func (e *Engine) DeletePage(ctx context.Context, in DeletePageInput) (*Receipt, error) {
	return e.store.DeletePage(ctx, in)
}
