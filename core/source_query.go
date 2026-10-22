package core

import (
	"context"
	types "github.com/JieWaZi/neux-knowledge/internal/domain"
)

// SourceQuery specifies display-state filtering and global pagination for unarchived sources.
type SourceQuery = types.SourceQuery

// ListSourcesQuery filters, counts, and paginates by display state within one read transaction.
func (e *Engine) ListSourcesQuery(ctx context.Context, libraryID string, query SourceQuery) ([]*SourceVersion, int64, error) {
	return e.store.ListSourcesQuery(ctx, libraryID, query)
}

// SourceActivity copies the source and projects the current job attempt without mutating the persisted original.
func (e *Engine) SourceActivity(ctx context.Context, libraryID string, sources []*SourceVersion) ([]*SourceVersion, error) {
	return e.store.SourceActivity(ctx, libraryID, sources)
}
