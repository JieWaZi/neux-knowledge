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
		query := tx.Where("scope_type = ?", auditScopeKnowledgeBase).Where("scope_id = ?", libraryID)
		if q != nil {
			if q.AfterID > 0 {
				query = query.Where("id < ?", q.AfterID)
			}
			if q.Action != "" {
				// 历史维护事件的旧分类只在读取时纠正，筛选仍在分页前按真实任务身份执行。
				maintenanceIDs := tx.Model(&types.Job{}).Select("id").Where("library_id = ?", libraryID).
					Where("COALESCE(json_array_length(source_version_ids), 0) = 0 AND json_array_length(json_extract(permission, '$.operations')) = 1 AND json_extract(permission, '$.operations[0]') = ?", "maintain")
				switch q.Action {
				case types.AuditActionWikiMaintenance:
					query = query.Where("action = ? OR (target_type = ? AND target_id IN (?))", q.Action, "knowledge_task", maintenanceIDs)
				case types.AuditActionWikiContentChanged, types.AuditActionKnowledgeReparseStarted, types.AuditActionKnowledgeParseCanceled:
					query = query.Where("action = ? AND NOT (target_type = ? AND target_id IN (?))", q.Action, "knowledge_task", maintenanceIDs)
				default:
					query = query.Where("action = ?", q.Action)
				}
			}
			if q.Outcome != "" {
				query = query.Where("outcome = ?", q.Outcome)
			}
			if q.ActorUserID != "" {
				query = query.Where("actor_user_id = ?", q.ActorUserID)
			}
		}
		if err := query.Order("id DESC").Limit(limit).Find(&entries).Error; err != nil {
			return err
		}
		return projectMaintenanceActivity(tx, libraryID, entries)
	})
	return
}

// projectMaintenanceActivity 只批量补齐当前有界活动页的任务身份，不改写历史或读取页面正文。
func projectMaintenanceActivity(tx *gorm.DB, libraryID string, entries []*types.AuditLog) error {
	ids := []string{}
	for _, entry := range entries {
		if entry.TargetType == "knowledge_task" {
			ids = append(ids, entry.TargetID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	var jobs []types.Job
	if err := tx.Select("id", "source_version_ids", "permission").Where("library_id = ? AND id IN ?", libraryID, ids).Find(&jobs).Error; err != nil {
		return err
	}
	maintenance := map[string]bool{}
	for index := range jobs {
		if types.IsMaintenanceJob(&jobs[index]) {
			maintenance[jobs[index].ID] = true
		}
	}
	for _, entry := range entries {
		if entry.TargetType != "knowledge_task" || !maintenance[entry.TargetID] {
			continue
		}
		entry.Action, entry.TargetType = types.AuditActionWikiMaintenance, "wiki_maintenance_task"
		metadata := map[string]any{}
		if err := json.Unmarshal(entry.Details, &metadata); err != nil {
			metadata = map[string]any{}
		}
		delete(metadata, "total")
		delete(metadata, "count")
		metadata["scope"] = "existing_wiki"
		// 旧事件没有可靠的页面变更计数，保持未知，不能从空来源或当前页数推断。
		entry.Details, _ = json.Marshal(metadata)
	}
	return nil
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
		case "title", "task_id", "trigger", "processing_status", "via", "scope", "canonicalPageId":
			if text, ok := value.(string); ok {
				runes := []rune(text)
				if len(runes) > 200 {
					runes = runes[:200]
				}
				safe[key] = string(runes)
			}
		case "count", "total", "failed", "skipped", "attempt", "changed_pages":
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

// appendActivityPageTitles 在原事务中只读取回执前五个页面的名称，包含本次软删除对象，不读取正文。
func appendActivityPageTitles(tx *gorm.DB, libraryID string, pageIDs []string, details map[string]any) error {
	if len(pageIDs) == 0 {
		return nil
	}
	preview := pageIDs
	if len(preview) > kbActivitySampleTitleLimit {
		preview = preview[:kbActivitySampleTitleLimit]
	}
	var pages []types.WikiPage
	if err := tx.Unscoped().Select("id", "title").Where("knowledge_base_id = ? AND id IN ?", libraryID, preview).Find(&pages).Error; err != nil {
		return err
	}
	titles := make(map[string]string, len(pages))
	for _, page := range pages {
		titles[page.ID] = page.Title
	}
	samples := make([]string, 0, len(preview))
	for _, id := range preview {
		samples = append(samples, titles[id])
	}
	kbActivityAppendSampleTitles(details, samples...)
	return nil
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
	case "page.agent-write", "page.replace-text", "page.rename", "page.delete", "wiki.auto-fix":
		// Maintenance actions share a transaction with bodies, cascading page changes, and receipts; replaying the original request does not duplicate activity.
		action = types.AuditActionWikiContentChanged
		targetType, targetID = "wiki", r.LibraryID
		details["count"] = len(r.PageIDs)
		if err := appendActivityPageTitles(tx, r.LibraryID, r.PageIDs, details); err != nil {
			return err
		}
	case "page.purge":
		action = types.AuditActionWikiContentChanged
		targetType, targetID = "wiki", r.LibraryID
		details["count"] = len(r.PageIDs)
		if in, ok := input.(*pagePurgeMutation); ok {
			kbActivityAppendSampleTitles(details, in.title)
			details["canonicalPageId"] = in.CanonicalPageID
		}
	case "source.import":
		action = types.AuditActionKnowledgeCreated
		targetType = "knowledge"
		in, ok := input.(types.ImportSourceInput)
		if mutation, isImport := input.(sourceImportMutation); isImport {
			if mutation.reused != nil && *mutation.reused {
				return nil
			}
			in, ok = mutation.ImportSourceInput, true
		}
		if ok {
			kbActivityAppendSampleTitles(details, in.Filename)
			if in.SourceID != "" {
				action = types.AuditActionKnowledgeUpdated
			}
		}
	case "job.accept", "job.retry", "job.fail", "job.cancel", "stage.outcome", "publication.publish", "lite.publish", "wiki.maintenance.complete":
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
		case "publication.publish", "lite.publish", "wiki.maintenance.complete":
			if len(r.PageIDs) > 0 {
				wikiDetails := map[string]any{"count": len(r.PageIDs), "task_id": j.ID, "attempt": j.Attempt}
				if err := appendActivityPageTitles(tx, r.LibraryID, r.PageIDs, wikiDetails); err != nil {
					return err
				}
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
		if types.IsMaintenanceJob(j) {
			action, targetType = types.AuditActionWikiMaintenance, "wiki_maintenance_task"
			delete(details, "total")
			details["scope"] = "existing_wiki"
			if outcome != types.AuditOutcomeAccepted {
				// 只读取本次尝试的真实写回执，重复修改同一页面只计一页。
				var receipts []types.Receipt
				if err := tx.Select("page_ids").Where("library_id = ? AND job_id = ? AND job_attempt = ?", j.LibraryID, j.ID, j.Attempt).Find(&receipts).Error; err != nil {
					return err
				}
				changed := map[string]bool{}
				for _, receipt := range receipts {
					for _, id := range receipt.PageIDs {
						changed[id] = true
					}
				}
				details["changed_pages"] = len(changed)
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
