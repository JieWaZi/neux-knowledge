package remote

import (
	"context"

	"github.com/JieWaZi/neux-knowledge/core"
)

// WriteAgentPage uses the actual remote agent write endpoint; the original request identity supports recovery from unknown outcomes.
func (c *Custom) WriteAgentPage(ctx context.Context, in core.WritePageInput) (*core.Receipt, error) {
	if err := c.target(in.LibraryID); err != nil {
		return nil, err
	}
	return c.writeMaintenance(ctx, "/pages/agent-write", in.RequestID, "page.agent-write", in.Page.ID, in)
}

// ReplacePageText performs exact replacement against the currently frozen remote resource.
func (c *Custom) ReplacePageText(ctx context.Context, in core.ReplacePageTextInput) (*core.Receipt, error) {
	if err := c.target(in.LibraryID); err != nil {
		return nil, err
	}
	if !safeID(in.PageID) {
		return nil, &Error{Code: "invalid_resource"}
	}
	return c.writeMaintenance(ctx, "/pages/"+in.PageID+"/replace-text", in.RequestID, "page.replace-text", in.PageID, in)
}

// RenamePage preserves the remote page identity and atomically updates links.
func (c *Custom) RenamePage(ctx context.Context, in core.RenamePageInput) (*core.Receipt, error) {
	if err := c.target(in.LibraryID); err != nil {
		return nil, err
	}
	if !safeID(in.PageID) || !safeSlug(in.NewSlug) {
		return nil, &Error{Code: "invalid_resource"}
	}
	return c.writeMaintenance(ctx, "/pages/"+in.PageID+"/rename", in.RequestID, "page.rename", in.PageID, in)
}

// DeletePage archives the page remotely and removes inbound links, retaining the original request identity for unknown outcomes.
func (c *Custom) DeletePage(ctx context.Context, in core.DeletePageInput) (*core.Receipt, error) {
	if err := c.target(in.LibraryID); err != nil {
		return nil, err
	}
	if !safeID(in.PageID) {
		return nil, &Error{Code: "invalid_resource"}
	}
	return c.writeMaintenance(ctx, "/pages/"+in.PageID+"/delete", in.RequestID, "page.delete", in.PageID, in)
}

// writeMaintenance validates the page maintenance action, known target, saved state, and positive version.
func (c *Custom) writeMaintenance(ctx context.Context, suffix, requestID, operation, resourceID string, in any) (*core.Receipt, error) {
	receipt, err := c.write(ctx, suffix, requestID, in)
	if err != nil {
		return nil, err
	}
	if receipt.Operation != operation || resourceID != "" && receipt.ResourceID != resourceID || receipt.State != "saved" || receipt.Version < 1 {
		return nil, &OutcomeUnknownError{RequestID: requestID}
	}
	return receipt, nil
}
