package types

import (
	"context"
	"time"
)

// AuditAction retains upstream dot-separated knowledge library activity categories.
type AuditAction string

const (
	AuditActionKBCreated               AuditAction = "kb.created"
	AuditActionKBUpdated               AuditAction = "kb.updated"
	AuditActionKBDeleted               AuditAction = "kb.deleted"
	AuditActionKnowledgeCreated        AuditAction = "knowledge.created"
	AuditActionKnowledgeUpdated        AuditAction = "knowledge.updated"
	AuditActionKnowledgeDeleted        AuditAction = "knowledge.deleted"
	AuditActionKnowledgeReparseStarted AuditAction = "knowledge.reparse_started"
	AuditActionKnowledgeParseCanceled  AuditAction = "knowledge.parse_canceled"
	AuditActionWikiContentChanged      AuditAction = "wiki.content_changed"
)

// AuditOutcome separates asynchronous acceptance from terminal business
// results and middleware-level rejections. The UI can therefore avoid
// presenting a queued operation as already completed.
type AuditOutcome string

const (
	AuditOutcomeSuccess AuditOutcome = "success"
	// Accepted means the durable request was created and asynchronous work was
	// submitted, but the background operation has not reached a terminal state.
	AuditOutcomeAccepted AuditOutcome = "accepted"
	AuditOutcomeDenied   AuditOutcome = "denied"
	AuditOutcomeFailed   AuditOutcome = "failed"
	AuditOutcomePartial  AuditOutcome = "partial"
	AuditOutcomeCanceled AuditOutcome = "canceled"
)

// AuditLog retains upstream immutable events, scope, and increasing cursors for local single-library authority.
// RequestID is unique together with action and outcome, making activities and business receipts idempotent in the same transaction.
type AuditLog struct {
	// ID is an increasing event cursor paginated in descending order.
	ID uint64 `json:"id" gorm:"primaryKey;autoIncrement;index:idx_activity_scope_desc,priority:3,sort:desc"`
	// RequestID binds an actually committed idempotent write.
	RequestID string `json:"request_id" gorm:"not null;uniqueIndex:idx_activity_request_event,priority:1"`
	// ActorUserID identifies the actual initiator; empty for system triggers without a user.
	ActorUserID string `json:"actor_user_id" gorm:"index:idx_activity_actor"`
	// ActorRole retains the host-validated role without inferring administrator status.
	ActorRole string `json:"actor_role"`
	// Operation binds the actual write use case and forms the idempotency identity with request, action, and outcome.
	Operation string `json:"operation" gorm:"not null;uniqueIndex:idx_activity_request_event,priority:2"`
	// Action is the activity category retained from the upstream contract.
	Action AuditAction `json:"action" gorm:"not null;uniqueIndex:idx_activity_request_event,priority:3"`
	// ScopeType fixes the knowledge library scope.
	ScopeType string `json:"scope_type" gorm:"index:idx_activity_scope_desc,priority:1"`
	// ScopeID identifies the knowledge library owning this activity.
	ScopeID string `json:"scope_id" gorm:"index:idx_activity_scope_desc,priority:2"`
	// TargetType identifies the library, original, or organization job actually changed.
	TargetType string `json:"target_type"`
	// TargetID identifies the actual target resource.
	TargetID string `json:"target_id"`
	// Outcome distinguishes persistent acceptance from an actual terminal state.
	Outcome AuditOutcome `json:"outcome" gorm:"not null;uniqueIndex:idx_activity_request_event,priority:4"`
	// Details stores only bounded allowlisted metadata without content, credentials, or raw diagnostics.
	Details JSON `json:"details" gorm:"type:json"`
	// CreatedAt is the actual event commit time; pagination still uses ID.
	CreatedAt time.Time `json:"created_at"`
}

// TableName fixes the local activity table without relying on GORM pluralization.
func (AuditLog) TableName() string { return "knowledge_activity_logs" }

// ActivityQuery retains upstream cursors, page sizes, and exact filters; the entry point fixes library scope.
type ActivityQuery struct {
	// AfterID is the last event identifier on the previous page; zero starts at the latest event.
	AfterID uint64
	// Limit defaults to 50, with a maximum of 100.
	Limit int
	// Action matches an exact action; empty applies no restriction.
	Action AuditAction
	// Outcome matches an exact outcome; empty applies no restriction.
	Outcome AuditOutcome
	// ActorUserID matches an exact actual user identifier; empty applies no restriction.
	ActorUserID string
}

// ActivityActor is injected by the trusted host and frozen at job acceptance; it cannot come from request content.
type ActivityActor struct {
	// UserID is the host-authenticated principal; empty when no actual user exists.
	UserID string `json:"userId,omitempty"`
	// Role is the host-validated role; empty when no local role is configured.
	Role string `json:"role,omitempty"`
	// Trigger identifies a user or system trigger; omission derives it from the actual identity.
	Trigger string `json:"trigger,omitempty"`
}

type activityActorContextKey struct{}

// WithActivityActor attaches the actual initiator identity to this trusted call context.
func WithActivityActor(ctx context.Context, actor ActivityActor) context.Context {
	return context.WithValue(ctx, activityActorContextKey{}, actor)
}

// ActivityActorFromContext returns the actual initiator identity; a default context represents a system trigger.
func ActivityActorFromContext(ctx context.Context) ActivityActor {
	actor, _ := ctx.Value(activityActorContextKey{}).(ActivityActor)
	if actor.Trigger == "" {
		if actor.UserID != "" {
			actor.Trigger = "user"
		} else {
			actor.Trigger = "system"
		}
	}
	if actor.UserID == "" {
		actor.Role = ""
	}
	return actor
}
