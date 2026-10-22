package remote

import (
	"context"
	"github.com/JieWaZi/neux-knowledge/core"
)

// DeleteLibrary submits permanent deletion to the fixed remote library without cleaning a local database.
func (c *Custom) DeleteLibrary(ctx context.Context, in core.DeleteLibraryInput) (*core.Receipt, error) {
	if err := c.target(in.LibraryID); err != nil {
		return nil, err
	}
	if in.ExpectedVersion < 1 {
		return nil, &Error{Code: "invalid_request"}
	}
	return c.write(ctx, "/delete", in.RequestID, in)
}

// GetLibraryDeletion queries actual source cleanup status for a deleted library.
func (c *Custom) GetLibraryDeletion(ctx context.Context) (*core.LibraryDeletionStatus, error) {
	v, err := customGet[core.LibraryDeletionStatus](ctx, c, "/deletion", nil, "libraryId", "version", "policyVersion", "state", "deletedBlobCount", "retainedSharedBlobCount", "blockedBlobCount", "pendingBlobCount")
	if err == nil && (v.LibraryID != c.ref.KnowledgeBaseID || v.Version < 1 || v.PolicyVersion < 1 || (v.State != "pending" && v.State != "blocked" && v.State != "completed") || v.DeletedBlobCount < 0 || v.RetainedSharedBlobCount < 0 || v.BlockedBlobCount < 0 || v.PendingBlobCount < 0) {
		return nil, protocolError()
	}
	return v, err
}

// RecoverLibraryDeletion resumes source cleanup using the server manifest version.
func (c *Custom) RecoverLibraryDeletion(ctx context.Context, in core.RecoverLibraryDeletionInput) (*core.Receipt, error) {
	if err := c.target(in.LibraryID); err != nil {
		return nil, err
	}
	if in.ExpectedVersion < 1 {
		return nil, &Error{Code: "invalid_request"}
	}
	return c.write(ctx, "/deletion/recover", in.RequestID, in)
}
