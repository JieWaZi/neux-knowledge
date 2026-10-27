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

// DeletePage 归档页面并保留历史；提供规范页及双端守卫时原子改向入链，普通归档移除入链。
func (e *Engine) DeletePage(ctx context.Context, in DeletePageInput) (*Receipt, error) {
	return e.store.DeletePage(ctx, in)
}

// PurgePage 永久删除已归档重复页及其历史，同事务校验规范页并将剩余引用指向规范页。
func (e *Engine) PurgePage(ctx context.Context, in PurgePageInput) (*Receipt, error) {
	return e.store.PurgePage(ctx, in)
}

// ListPageDirectory 只查询目录展示字段，不读取正文、证据和引用关系。
func (e *Engine) ListPageDirectory(ctx context.Context, libraryID string, offset, limit int) ([]*PageDirectoryEntry, int64, error) {
	return e.store.ListPageDirectory(ctx, libraryID, offset, limit)
}

// CountPagesByTheme 一次聚合各目录的页面数，不读取页面内容。
func (e *Engine) CountPagesByTheme(ctx context.Context, libraryID string) (map[string]int64, error) {
	return e.store.CountPagesByTheme(ctx, libraryID)
}
