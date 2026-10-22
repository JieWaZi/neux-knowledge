package httpapi

import (
	"net/http"

	"github.com/JieWaZi/neux-knowledge/core"
)

// wikiMaintenanceRoute delegates maintenance actions to the sole core transaction using existing read/write grants.
func (h *Handler) wikiMaintenanceRoute(w http.ResponseWriter, r *http.Request, p []string, lib string, policy int64, subject string) bool {
	if len(p) < 3 {
		return false
	}
	agent := p[2] == "pages" && len(p) == 4 && p[3] == "agent-write"
	action := p[2] == "pages" && len(p) == 5 && (p[4] == "replace-text" || p[4] == "rename" || p[4] == "delete")
	if !agent && !action {
		return false
	}
	if r.Method == http.MethodGet {
		respond(w, nil, &APIError{Code: "not_found", Status: 404})
		return true
	}
	ctx := r.Context()
	var result *core.Receipt
	var err error
	// The authentication policy was verified under the entry lock; freeze the same generation here, which the core transaction checks again.
	bind := func(gotLib string, expected *int64) error {
		if err := same(gotLib, lib); err != nil {
			return err
		}
		if *expected != 0 && *expected != policy {
			return &APIError{Code: "policy_changed", Status: 403}
		}
		*expected = policy
		return nil
	}
	switch {
	case agent:
		var in core.WritePageInput
		if err = decode(w, r, &in); err == nil {
			err = bind(in.LibraryID, &in.ExpectedPolicyVersion)
		}
		if err == nil {
			in.Page.LastEditorID = subject
			result, err = h.engine.WriteAgentPage(ctx, in)
		}
	case action && p[4] == "replace-text":
		var in core.ReplacePageTextInput
		if err = decode(w, r, &in); err == nil {
			err = bind(in.LibraryID, &in.ExpectedPolicyVersion)
		}
		if err == nil {
			err = same(in.PageID, p[3])
		}
		if err == nil {
			result, err = h.engine.ReplacePageText(ctx, in)
		}
	case action && p[4] == "rename":
		var in core.RenamePageInput
		if err = decode(w, r, &in); err == nil {
			err = bind(in.LibraryID, &in.ExpectedPolicyVersion)
		}
		if err == nil {
			err = same(in.PageID, p[3])
		}
		if err == nil {
			result, err = h.engine.RenamePage(ctx, in)
		}
	case action && p[4] == "delete":
		var in core.DeletePageInput
		if err = decode(w, r, &in); err == nil {
			err = bind(in.LibraryID, &in.ExpectedPolicyVersion)
		}
		if err == nil {
			err = same(in.PageID, p[3])
		}
		if err == nil {
			result, err = h.engine.DeletePage(ctx, in)
		}
	default:
		err = &APIError{Code: "not_found", Status: 404}
	}
	respond(w, result, err)
	return true
}
