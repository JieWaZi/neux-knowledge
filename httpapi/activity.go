package httpapi

import (
	"net/http"
	"strconv"

	"github.com/JieWaZi/neux-knowledge/core"
)

// activityListResponse preserves the upstream success flag, activity array, and final cursor.
type activityListResponse struct {
	// Success indicates whether the query succeeded.
	Success bool `json:"success"`
	// Data contains the actual persisted activities on this library page.
	Data []*core.AuditLog `json:"data"`
	// NextCursor is the last event ID on this page, or zero for an empty page.
	NextCursor uint64 `json:"next_cursor"`
}

// parseAuditCursor preserves upstream tolerance for invalid cursors and page sizes.
func parseAuditCursor(r *http.Request) (uint64, int) {
	var afterID uint64
	if raw := r.URL.Query().Get("after_id"); raw != "" {
		if v, err := strconv.ParseUint(raw, 10, 64); err == nil {
			afterID = v
		}
	}
	limit := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			limit = v
		}
	}
	return afterID, limit
}

func (h *Handler) activityRoute(w http.ResponseWriter, r *http.Request, parts []string, lib string) bool {
	if len(parts) != 3 || parts[2] != "activity" || r.Method != http.MethodGet {
		return false
	}
	afterID, limit := parseAuditCursor(r)
	query := core.ActivityQuery{AfterID: afterID, Limit: limit, Action: core.AuditAction(r.URL.Query().Get("action")), Outcome: core.AuditOutcome(r.URL.Query().Get("outcome")), ActorUserID: r.URL.Query().Get("actor")}
	rows, cursor, err := h.engine.ListLibraryActivity(r.Context(), lib, query)
	respond(w, activityListResponse{Success: err == nil, Data: rows, NextCursor: cursor}, err)
	return true
}
