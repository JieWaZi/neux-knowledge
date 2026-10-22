package remote

import (
	"context"
	"net/url"

	"github.com/JieWaZi/neux-knowledge/core"
)

// PreviewSourcePurge reads only the actual cleanup impact on the fixed remote resource without creating a local copy.
func (c *Custom) PreviewSourcePurge(ctx context.Context, in core.PurgePreviewInput) (*core.SourcePurgePreview, error) {
	if err := c.target(in.LibraryID); err != nil {
		return nil, err
	}
	if !safeID(in.SourceID) {
		return nil, &Error{Code: "invalid_request"}
	}
	p, err := customGet[core.SourcePurgePreview](ctx, c, "/source-purges/preview", url.Values{"sourceId": {in.SourceID}}, "libraryId", "sourceId", "versions", "digest", "blockingJobIds", "rawOwnership", "boundaries")
	if err == nil && (p.LibraryID != in.LibraryID || p.SourceID != in.SourceID || len(p.Digest) != 64 || p.PolicyVersion < 1) {
		return nil, protocolError()
	}
	return p, err
}

// ConfirmSourcePurge forwards only explicit confirmation; unknown outcomes require querying the receipt with the same requestId.
func (c *Custom) ConfirmSourcePurge(ctx context.Context, in core.ConfirmSourcePurgeInput) (*core.Receipt, error) {
	if err := c.target(in.LibraryID); err != nil {
		return nil, err
	}
	if !safeID(in.SourceID) || !in.Confirmed || len(in.PreviewDigest) != 64 {
		return nil, &Error{Code: "invalid_request"}
	}
	return c.write(ctx, "/source-purges/confirm", in.RequestID, in)
}

// GetSourcePurge queries actual cleanup progress, including shared retention and blocking caused by unknown ownership.
func (c *Custom) GetSourcePurge(ctx context.Context, purgeID string) (*core.SourcePurgeStatus, error) {
	if !safeID(purgeID) {
		return nil, &Error{Code: "invalid_request"}
	}
	p, err := customGet[core.SourcePurgeStatus](ctx, c, "/source-purges/"+purgeID, nil, "id", "libraryId", "sourceId", "state", "version", "versions", "boundaries")
	if err == nil && (p.ID != purgeID || p.LibraryID != c.ref.KnowledgeBaseID || p.Version < 1 || (p.State != "pending" && p.State != "completed" && p.State != "blocked")) {
		return nil, protocolError()
	}
	return p, err
}

// RecoverSourcePurge continues the original manifest under current remote authorization without switching to local deletion.
func (c *Custom) RecoverSourcePurge(ctx context.Context, in core.RecoverSourcePurgeInput) (*core.Receipt, error) {
	if err := c.target(in.LibraryID); err != nil {
		return nil, err
	}
	if !safeID(in.PurgeID) || in.ExpectedPolicyVersion < 1 {
		return nil, &Error{Code: "invalid_request"}
	}
	return c.write(ctx, "/source-purges/"+in.PurgeID+"/recover", in.RequestID, in)
}

// ListSourcePurges reads the server's authoritative manifest independently of local dialog state or whether the source still exists.
func (c *Custom) ListSourcePurges(ctx context.Context, offset, limit int) (*CustomList[core.SourcePurgeStatus], error) {
	p, err := customList[core.SourcePurgeStatus](ctx, c, "/source-purges", nil, offset, limit)
	if err != nil {
		return nil, err
	}
	for _, v := range p.Items {
		if v.LibraryID != c.ref.KnowledgeBaseID || !safeID(v.ID) || v.Version < 1 || (v.State != "pending" && v.State != "completed" && v.State != "blocked") {
			return nil, protocolError()
		}
	}
	return p, nil
}
