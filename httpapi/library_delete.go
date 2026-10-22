package httpapi

import (
	"github.com/JieWaZi/neux-knowledge/core"
	"net/http"
)

func (h *Handler) libraryDeletionRoute(w http.ResponseWriter, r *http.Request, p []string, lib string) bool {
	if len(p) < 3 || (p[2] != "delete" && p[2] != "deletion") {
		return false
	}
	if p[2] == "deletion" && len(p) == 3 && r.Method == "GET" {
		v, e := h.engine.GetLibraryDeletion(r.Context(), lib)
		respond(w, v, e)
		return true
	}
	if p[2] == "delete" && len(p) == 3 && r.Method == "POST" {
		var in core.DeleteLibraryInput
		err := decode(w, r, &in)
		if err == nil {
			err = same(in.LibraryID, lib)
		}
		if err != nil {
			respond(w, nil, err)
		} else {
			v, e := h.engine.DeleteLibrary(r.Context(), in)
			respond(w, v, e)
		}
		return true
	}
	if p[2] == "deletion" && len(p) == 4 && p[3] == "recover" && r.Method == "POST" {
		var in core.RecoverLibraryDeletionInput
		err := decode(w, r, &in)
		if err == nil {
			err = same(in.LibraryID, lib)
		}
		if err != nil {
			respond(w, nil, err)
		} else {
			v, e := h.engine.RecoverLibraryDeletion(r.Context(), in)
			respond(w, v, e)
		}
		return true
	}
	respond(w, nil, &APIError{Code: "not_found", Status: 404})
	return true
}
