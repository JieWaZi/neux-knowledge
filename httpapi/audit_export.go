package httpapi

import (
	"net/http"

	"github.com/JieWaZi/neux-knowledge/core"
)

// auditExportRoute exposes read-only audit and Markdown snapshot operations.
func (h *Handler) auditExportRoute(w http.ResponseWriter, r *http.Request, parts []string, libraryID string) bool {
	if len(parts) != 3 {
		return false
	}
	switch parts[2] {
	case "audit":
		if r.Method != http.MethodGet {
			respond(w, nil, &APIError{Code: "method_not_allowed", Status: http.StatusMethodNotAllowed})
			return true
		}
		v, err := h.engine.LintLibrary(r.Context(), libraryID)
		respond(w, v, err)
		return true
	case "markdown-export":
		if r.Method != http.MethodGet {
			respond(w, nil, &APIError{Code: "method_not_allowed", Status: http.StatusMethodNotAllowed})
			return true
		}
		ids := r.URL.Query()["pageId"]
		if len(ids) > 1000 {
			respond(w, nil, invalid())
			return true
		}
		v, err := h.engine.ExportMarkdown(r.Context(), core.ExportInput{LibraryID: libraryID, PageIDs: ids})
		respond(w, v, err)
		return true
	default:
		return false
	}
}
