package remote

import (
	"context"
	"net/url"
	"strconv"

	"github.com/JieWaZi/neux-knowledge/core"
)

// customActivityList preserves the remote activity stream's actual success status and final cursor.
type customActivityList struct {
	Success    bool             `json:"success"`     // The server explicitly confirmed query success.
	Data       []*core.AuditLog `json:"data"`        // Persisted activities for the currently authorized library.
	NextCursor uint64           `json:"next_cursor"` // Last activity identity on this page, or zero for an empty page.
}

// ListLibraryActivity follows server configuration-edit authorization and does not infer activity access from ordinary read permission.
func (c *Custom) ListLibraryActivity(ctx context.Context, query core.ActivityQuery) ([]*core.AuditLog, uint64, error) {
	values := url.Values{"after_id": {strconv.FormatUint(query.AfterID, 10)}, "limit": {strconv.Itoa(query.Limit)}, "action": {string(query.Action)}, "outcome": {string(query.Outcome)}, "actor": {query.ActorUserID}}
	v, err := customGet[customActivityList](ctx, c, "/activity", values, "success", "data", "next_cursor")
	if err != nil {
		return nil, 0, err
	}
	if !v.Success || v.Data == nil || (len(v.Data) == 0 && v.NextCursor != 0) {
		return nil, 0, protocolError()
	}
	for _, entry := range v.Data {
		if entry == nil || entry.ID == 0 || entry.ScopeID != c.ref.KnowledgeBaseID {
			return nil, 0, protocolError()
		}
	}
	if len(v.Data) > 0 && v.NextCursor != v.Data[len(v.Data)-1].ID {
		return nil, 0, protocolError()
	}
	return v.Data, v.NextCursor, nil
}
