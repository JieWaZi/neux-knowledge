package core

import (
	"context"
	types "github.com/JieWaZi/neux-knowledge/internal/domain"
)

// WikiIndexResponse preserves the upstream intro and paginated groups structure, avoiding duplicate storage of the complete index Markdown.
type WikiIndexResponse = types.WikiIndexResponse

// WikiIndexGroup exposes the actual index paginated independently by page type.
type WikiIndexGroup = types.WikiIndexGroup

// WikiIndexEntry is a lightweight index entry that does not read page content.
type WikiIndexEntry = types.WikiIndexEntry

// GetIndexView reads the structured library index and still provides the existing Wiki directory when no index is persisted.
func (e *Engine) GetIndexView(ctx context.Context, libraryID string, pageTypes []string, limit int, cursor string) (*WikiIndexResponse, error) {
	return e.store.GetIndexView(ctx, libraryID, pageTypes, limit, cursor)
}
