package core

import (
	"context"

	types "github.com/JieWaZi/neux-knowledge/internal/domain"
)

// OrganizeSourceWork exposes source rescheduling within the same job.
type OrganizeSourceWork = types.OrganizeSourceWork

// SourceWorkActionInput exposes idempotent deferral input for source work items.
type SourceWorkActionInput = types.SourceWorkActionInput

// ErrSourceUnavailable means a frozen source is inactive and can be settled per document; it does not indicate authorization revocation.
const ErrSourceUnavailable = types.ErrSourceUnavailable

// EnsureOrganizeSourceWork 核对当前尝试已在接纳事务中完整登记的来源关系。
func (e *Engine) EnsureOrganizeSourceWork(ctx context.Context, jobID string) error {
	return e.store.EnsureOrganizeSourceWork(ctx, jobID)
}

// ListOrganizeSourceWork returns pending, completed, and dead-letter sources for the current attempt.
func (e *Engine) ListOrganizeSourceWork(ctx context.Context, jobID string) ([]*OrganizeSourceWork, error) {
	return e.store.ListOrganizeSourceWork(ctx, jobID)
}

// DeferOrganizeSourceWork idempotently records a failure and its source-specific retry time.
func (e *Engine) DeferOrganizeSourceWork(ctx context.Context, in SourceWorkActionInput) (*Receipt, error) {
	return e.store.DeferOrganizeSourceWork(ctx, in)
}
