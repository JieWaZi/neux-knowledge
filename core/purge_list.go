package core

import "context"

// SourcePurgeList is an authoritative cleanup manifest window without source text or internal file keys.
type SourcePurgeList struct {
	Items []SourcePurgeStatus `json:"items"` // Cleanup progress window.
	Total int64               `json:"total"` // Total record count in the library.
}

// ListSourcePurges supports querying and recovering the original cleanup after closing the dialog or restarting.
func (e *Engine) ListSourcePurges(ctx context.Context, libraryID string, offset, limit int) (*SourcePurgeList, error) {
	rows, total, err := e.store.ListSourcePurges(ctx, libraryID, offset, limit)
	if err != nil {
		return nil, err
	}
	return &SourcePurgeList{Items: rows, Total: total}, nil
}
