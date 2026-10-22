// The SQLite adapter places events and business receipts in one transaction; it excludes global, multitenant, and periodic cleanup systems.
package store

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	types "github.com/JieWaZi/neux-knowledge/internal/domain"
	"gorm.io/gorm"
)

const (
	auditScopeKnowledgeBase    = "knowledge_base"
	auditLogListLimitMax       = 100
	kbActivitySampleTitleLimit = 5
)

// ListLibraryActivity uses a single-table cursor query; the library identity is fixed by the entry point and cannot be overridden by filters.
func (s *Store) ListLibraryActivity(ctx context.Context, libraryID string, q *types.ActivityQuery) (entries []*types.AuditLog, err error) {
	entries = []*types.AuditLog{}
	err = s.transaction(ctx, func(tx *gorm.DB) error {
		if _, e := getLibrary(tx, libraryID, false); e != nil {
			return e
		}
		limit := 50
		if q != nil && q.Limit > 0 {
			limit = q.Limit
		}
		if limit > auditLogListLimitMax {
			limit = auditLogListLimitMax
		}
		tx = tx.Where("scope_type = ?", auditScopeKnowledgeBase).Where("scope_id = ?", libraryID)
		if q != nil {
			if q.AfterID > 0 {
				tx = tx.Where("id < ?", q.AfterID)
			}
			if q.Action != "" {
				tx = tx.Where("action = ?", q.Action)
			}
			if q.Outcome != "" {
				tx = tx.Where("outcome = ?", q.Outcome)
			}
			if q.ActorUserID != "" {
				tx = tx.Where("actor_user_id = ?", q.ActorUserID)
			}
		}
		return tx.Order("id DESC").Limit(limit).Find(&entries).Error
	})
	return
}

// kbActivityAppendSampleTitles preserves deduplication, trimming, and first-title mirroring rules.
func kbActivityAppendSampleTitles(details map[string]any, titles ...string) {
	if details == nil {
		return
	}
	samples := make([]string, 0, kbActivitySampleTitleLimit)
	seen := make(map[string]struct{}, kbActivitySampleTitleLimit)
	appendSample := func(title string) {
		title = strings.TrimSpace(title)
		if title == "" {
			return
		}
		if _, ok := seen[title]; ok {
			return
		}
		if len(samples) >= kbActivitySampleTitleLimit {
			return
		}
		seen[title] = struct{}{}
		samples = append(samples, title)
	}
	if existing, ok := details["title"].(string); ok {
		appendSample(existing)
	}
	for _, title := range titles {
		appendSample(title)
	}
	if len(samples) == 0 {
		return
	}
	details["title"] = samples[0]
	if len(samples) > 1 {
		details["titles"] = samples
	}
}

// sanitizeActivityDetails retains only bounded metadata needed for local public activity.
// This allowlist is a host adaptation, since upstream has no separate sanitizer; it must not accept arbitrary business configuration or diagnostic objects.
func sanitizeActivityDetails(details map[string]any) types.JSON {
	safe := map[string]any{}
	for key, value := range details {
		switch key {
		case "title", "task_id", "trigger", "processing_status", "via":
			if text, ok := value.(string); ok {
				runes := []rune(text)
				if len(runes) > 200 {
					runes = runes[:200]
				}
				safe[key] = string(runes)
			}
		case "count", "total", "failed", "skipped", "attempt":
			if n, ok := value.(int); ok && n >= 0 {
				safe[key] = n
			}
		case "changed_fields", "titles":
			if values, ok := value.([]string); ok {
				out := []string{}
				for _, v := range values {
					if len(out) == 20 {
						break
					}
					runes := []rune(v)
					if len(runes) > 200 {
						runes = runes[:200]
					}
					out = append(out, string(runes))
				}
				safe[key] = out
			}
		}
	}
	raw, _ := json.Marshal(safe)
	return types.JSON(raw)
}

// recordKBActivity records actual facts in the caller's business transaction; errors roll back the business writes in that transaction.
func recordKBActivity(tx *gorm.DB, requestID, operation, libraryID string, actor types.ActivityActor, action types.AuditAction, targetType, targetID string, outcome types.AuditOutcome, details map[string]any) error {
	if outcome == "" {
		outcome = types.AuditOutcomeSuccess
	}
	copied := map[string]any{}
	for key, value := range details {
		copied[key] = value
	}
	if actor.Trigger == "" {
		if actor.UserID != "" {
			actor.Trigger = "user"
		} else {
			actor.Trigger = "system"
		}
	}
	copied["trigger"] = actor.Trigger
	if _, ok := copied["task_id"]; ok {
		if _, exists := copied["processing_status"]; !exists {
			switch outcome {
			case types.AuditOutcomeAccepted:
				copied["processing_status"] = "pending"
			case types.AuditOutcomeSuccess:
				copied["processing_status"] = "completed"
			case types.AuditOutcomePartial:
				copied["processing_status"] = "partial"
			case types.AuditOutcomeFailed, types.AuditOutcomeDenied:
				copied["processing_status"] = "failed"
			case types.AuditOutcomeCanceled:
				copied["processing_status"] = "canceled"
			}
		}
	}
	if actor.UserID == "" {
		actor.Role = ""
	}
	return tx.Create(&types.AuditLog{RequestID: requestID, Operation: operation, ActorUserID: actor.UserID, ActorRole: actor.Role, Action: action, ScopeType: auditScopeKnowledgeBase, ScopeID: libraryID, TargetType: targetType, TargetID: targetID, Outcome: outcome, Details: sanitizeActivityDetails(copied), CreatedAt: time.Now()}).Error
}

// appendMutationActivity records only domain changes actually committed by the current transaction.
// It runs in mutate's new-request branch, so receipt replay cannot execute it again; it does not read bodies or raw configuration.
func appendMutationActivity(ctx context.Context, tx *gorm.DB, r *types.Receipt, input any) error {
	actor := types.ActivityActorFromContext(ctx)
	details := map[string]any{}
	action := types.AuditAction("")
	targetType, targetID := "knowledge_base", r.ResourceID
	outcome := types.AuditOutcomeSuccess
	switch r.Operation {
	case "library.create":
		action = types.AuditActionKBCreated
		if in, ok := input.(types.CreateLibraryInput); ok {
			kbActivityAppendSampleTitles(details, in.Name)
		}
	case "library.update":
		// UpdateLibrary holds old values in the same transaction and records actual field differences; do not record them again here.
		return nil
	case "page.agent-write", "page.replace-text", "page.rename", "page.delete":
		// Maintenance actions share a transaction with bodies, cascading page changes, and receipts; replaying the original request does not duplicate activity.
		action = types.AuditActionWikiContentChanged
		targetType, targetID = "wiki", r.LibraryID
		details["count"] = len(r.PageIDs)
	case "source.import":
		action = types.AuditActionKnowledgeCreated
		targetType = "knowledge"
		if in, ok := input.(types.ImportSourceInput); ok {
			kbActivityAppendSampleTitles(details, in.Filename)
			if in.SourceID != "" {
				action = types.AuditActionKnowledgeUpdated
			}
		}
	case "job.accept", "job.retry", "job.fail", "job.cancel", "stage.outcome", "publication.publish":
		if r.JobID == "" || r.State == "no-op" {
			return nil
		}
		j, err := getJob(tx, r.JobID)
		if err != nil {
			return err
		}
		targetType, targetID = "knowledge_task", j.ID
		details["task_id"] = j.ID
		details["attempt"] = j.Attempt
		details["total"] = len(j.SourceVersionIDs)
		actor = j.ActivityActor
		action = types.AuditActionKnowledgeReparseStarted
		switch r.Operation {
		case "job.accept", "job.retry":
			if r.State != "accepted" {
				return nil
			}
			outcome = types.AuditOutcomeAccepted
			if r.Operation == "job.retry" {
				actor = types.ActivityActorFromContext(ctx)
				if err := tx.Model(&types.Job{}).Where("id = ?", j.ID).Select("activity_actor").Updates(&types.Job{ActivityActor: actor}).Error; err != nil {
					return err
				}
			}
		case "job.cancel":
			if in, ok := input.(types.JobActionInput); ok && r.Version == in.ExpectedVersion {
				return nil
			}
			actor = types.ActivityActorFromContext(ctx)
			action = types.AuditActionKnowledgeParseCanceled
			outcome = types.AuditOutcomeAccepted
			if j.State == "cancelled" {
				outcome = types.AuditOutcomeCanceled
			}
		case "stage.outcome":
			if j.State != "cancelled" {
				return nil
			}
			action = types.AuditActionKnowledgeParseCanceled
			outcome = types.AuditOutcomeCanceled
		case "publication.publish":
			if len(r.PageIDs) > 0 {
				wikiDetails := map[string]any{"count": len(r.PageIDs), "task_id": j.ID, "attempt": j.Attempt}
				if err := recordKBActivity(tx, r.RequestID, r.Operation, r.LibraryID, actor, types.AuditActionWikiContentChanged, "wiki", r.LibraryID, types.AuditOutcomeSuccess, wikiDetails); err != nil {
					return err
				}
			}
			if j.State == "processing" {
				return nil
			}
			fallthrough
		case "job.fail":
			switch j.State {
			case "completed":
				outcome = types.AuditOutcomeSuccess
			case "partial_failed":
				outcome = types.AuditOutcomePartial
			case "failed":
				outcome = types.AuditOutcomeFailed
			default:
				return nil
			}
		}
	default:
		return nil
	}
	if r.LibraryID == "" || action == "" {
		return nil
	}
	return recordKBActivity(tx, r.RequestID, r.Operation, r.LibraryID, actor, action, targetType, targetID, outcome, details)
}

// RecordLibraryExecutionDefaultsActivity adds the unique activity record for execution defaults already committed by the host.
// Only fixed operations are accepted; this neither changes the core library version nor permits arbitrary activity bodies or configuration writes.
func (s *Store) RecordLibraryExecutionDefaultsActivity(ctx context.Context, libraryID, requestID string) error {
	if strings.TrimSpace(requestID) == "" {
		return fail(types.ErrInvalid, "library.execution_defaults", "requestId is required")
	}
	return s.transaction(ctx, func(tx *gorm.DB) error {
		if _, err := getLibrary(tx, libraryID, false); err != nil {
			return err
		}
		var prior []types.AuditLog
		if err := tx.Where("request_id = ? AND operation = ? AND action = ? AND outcome = ?", requestID, "library.execution_defaults", types.AuditActionKBUpdated, types.AuditOutcomeSuccess).Limit(1).Find(&prior).Error; err != nil {
			return err
		}
		if len(prior) > 0 {
			if prior[0].ScopeID != libraryID {
				return fail(types.ErrIdempotencyConflict, "library.execution_defaults", "requestId has different library")
			}
			return nil
		}
		return recordKBActivity(tx, requestID, "library.execution_defaults", libraryID, types.ActivityActorFromContext(ctx), types.AuditActionKBUpdated, "knowledge_base", libraryID, types.AuditOutcomeSuccess, map[string]any{"changed_fields": []string{"execution_defaults"}})
	})
}
