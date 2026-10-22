package httpapi

import (
	"net/http"

	"github.com/JieWaZi/neux-knowledge/core"
)

// sourcePurgeRoute reuses outer per-library authentication, separate purge permission, write IP policy, and the policy read lock.
func (h *Handler) sourcePurgeRoute(w http.ResponseWriter, r *http.Request, parts []string, libraryID string) bool {
	if parts[2] != "source-purges" {
		return false
	}
	ctx := r.Context()
	switch {
	case len(parts) == 3 && r.Method == http.MethodGet:
		offset, limit, err := pagination(r)
		if err != nil {
			respond(w, nil, err)
			return true
		}
		v, err := h.engine.ListSourcePurges(ctx, libraryID, offset, limit)
		if err != nil {
			respond(w, nil, err)
			return true
		}
		respond(w, List[core.SourcePurgeStatus]{Items: v.Items, Total: v.Total, Offset: offset, Limit: limit}, nil)
	case len(parts) == 4 && parts[3] == "preview" && r.Method == http.MethodGet:
		v, err := h.engine.PreviewSourcePurge(ctx, core.PurgePreviewInput{LibraryID: libraryID, SourceID: r.URL.Query().Get("sourceId")})
		respond(w, v, err)
	case len(parts) == 4 && parts[3] == "confirm" && r.Method == http.MethodPost:
		var in core.ConfirmSourcePurgeInput
		err := decode(w, r, &in)
		if err == nil {
			err = same(in.LibraryID, libraryID)
		}
		if err != nil {
			respond(w, nil, err)
			return true
		}
		v, err := h.engine.ConfirmSourcePurge(ctx, in)
		respond(w, v, err)
	case len(parts) == 5 && parts[4] == "recover" && r.Method == http.MethodPost:
		var in core.RecoverSourcePurgeInput
		err := decode(w, r, &in)
		if err == nil {
			err = same(in.LibraryID, libraryID)
		}
		if err == nil {
			err = same(in.PurgeID, parts[3])
		}
		if err != nil {
			respond(w, nil, err)
			return true
		}
		v, err := h.engine.RecoverSourcePurge(ctx, in)
		respond(w, v, err)
	case len(parts) == 4 && r.Method == http.MethodGet:
		v, err := h.engine.GetSourcePurge(ctx, libraryID, parts[3])
		respond(w, v, err)
	default:
		respond(w, nil, &APIError{Code: "method_not_allowed", Status: 405})
	}
	return true
}
