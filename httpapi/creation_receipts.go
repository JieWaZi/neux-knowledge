package httpapi

import (
	"context"
	"net/http"

	"github.com/JieWaZi/neux-knowledge/core"
)

// creationReceipt allows only the actual creating principal to recover its own creation receipt; request identity is not authorization.
func (h *Handler) creationReceipt(ctx context.Context, requestID, subject string) (*core.Receipt, error) {
	if requestID == "" {
		return nil, invalid()
	}
	v, err := h.engine.GetReceipt(ctx, requestID)
	if err != nil {
		return nil, err
	}
	denied := &APIError{Code: "forbidden", Status: http.StatusForbidden}
	if subject == "" || v.Operation != "library.create" || v.LibraryID == "" || v.ResourceID != v.LibraryID || v.State != "saved" {
		return nil, denied
	}
	rows, _, err := h.engine.ListLibraryActivity(ctx, v.LibraryID, core.ActivityQuery{Action: core.AuditActionKBCreated, Outcome: core.AuditOutcomeSuccess, ActorUserID: subject, Limit: 1})
	if err != nil {
		return nil, err
	}
	if len(rows) != 1 || rows[0].RequestID != requestID || rows[0].Operation != "library.create" || rows[0].ScopeID != v.LibraryID || rows[0].TargetID != v.LibraryID || rows[0].ActorUserID != subject {
		return nil, denied
	}
	return v, nil
}
