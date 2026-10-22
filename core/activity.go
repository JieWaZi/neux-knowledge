package core

import (
	"context"
	types "github.com/JieWaZi/neux-knowledge/internal/domain"
)

// AuditLog reuses the immutable activity domain model.
type AuditLog = types.AuditLog

// AuditAction preserves the upstream action values.
type AuditAction = types.AuditAction

// AuditOutcome distinguishes acceptance, success, failure, partial success, cancellation, and rejection.
type AuditOutcome = types.AuditOutcome

// ActivityQuery preserves upstream cursors and exact filtering semantics.
type ActivityQuery = types.ActivityQuery

// ActivityActor is the actual initiating identity supplied by a trusted host.
type ActivityActor = types.ActivityActor

const (
	AuditActionKBCreated               = types.AuditActionKBCreated
	AuditActionKBUpdated               = types.AuditActionKBUpdated
	AuditActionKBDeleted               = types.AuditActionKBDeleted
	AuditActionKnowledgeCreated        = types.AuditActionKnowledgeCreated
	AuditActionKnowledgeUpdated        = types.AuditActionKnowledgeUpdated
	AuditActionKnowledgeDeleted        = types.AuditActionKnowledgeDeleted
	AuditActionKnowledgeReparseStarted = types.AuditActionKnowledgeReparseStarted
	AuditActionKnowledgeParseCanceled  = types.AuditActionKnowledgeParseCanceled
	AuditActionWikiContentChanged      = types.AuditActionWikiContentChanged
	AuditOutcomeSuccess                = types.AuditOutcomeSuccess
	AuditOutcomeAccepted               = types.AuditOutcomeAccepted
	AuditOutcomeDenied                 = types.AuditOutcomeDenied
	AuditOutcomeFailed                 = types.AuditOutcomeFailed
	AuditOutcomePartial                = types.AuditOutcomePartial
	AuditOutcomeCanceled               = types.AuditOutcomeCanceled
)

// WithActivityActor attaches the host-verified identity to this call's context.
func WithActivityActor(ctx context.Context, actor ActivityActor) context.Context {
	return types.WithActivityActor(ctx, actor)
}

// ListLibraryActivity queries persisted activities directly rather than inferring history from jobs or receipts.
// The cursor is the last ID on this page, or zero for an empty page, following upstream semantics.
func (e *Engine) ListLibraryActivity(ctx context.Context, libraryID string, q ActivityQuery) (items []*AuditLog, nextCursor uint64, err error) {
	items, err = e.store.ListLibraryActivity(ctx, libraryID, &q)
	if err == nil && len(items) > 0 {
		nextCursor = items[len(items)-1].ID
	}
	return
}

// RecordLibraryExecutionDefaultsActivity records a fixed activity only for execution defaults already committed by the host.
// The same request can be retried safely without changing the library version; arbitrary activity payloads are not accepted.
func (e *Engine) RecordLibraryExecutionDefaultsActivity(ctx context.Context, libraryID, requestID string) error {
	return e.store.RecordLibraryExecutionDefaultsActivity(ctx, libraryID, requestID)
}
