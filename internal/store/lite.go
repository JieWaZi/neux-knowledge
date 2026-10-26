package store

import (
	"context"
	"encoding/json"
	"errors"
	types "github.com/JieWaZi/neux-knowledge/internal/domain"
	"github.com/JieWaZi/neux-knowledge/internal/wiki"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"strings"
	"time"
)

const (
	// liteJobDiagnosticSamples 限制任务终态展示的失败来源预览，完整诊断仍归原件所有。
	liteJobDiagnosticSamples = 3
	// liteJobDiagnosticRunes 限制每条冻结摘要的字符数，不读取或保存来源正文。
	liteJobDiagnosticRunes = 500
)

// PublishLite atomically commits results from the trusted Lite algorithm; model text cannot access this entry point.
func (s *Store) PublishLite(ctx context.Context, requestID, jobID string, attempt int, p *types.Publication, contributionKeys ...string) (*types.Receipt, error) {
	input := struct {
		JobID         string             // 知识任务身份。
		Attempt       int                // 当前任务尝试。
		Publication   *types.Publication // 页面与来源的发布内容。
		Contributions []string           `json:",omitempty"` // 与页面同事务确认的贡献身份。
	}{jobID, attempt, p, contributionKeys}
	return s.mutate(ctx, requestID, "lite.publish", "", input, func(tx *gorm.DB) (*types.Receipt, error) {
		j, l, err := activeJob(tx, jobID)
		if err != nil {
			return nil, err
		}
		if j.Attempt != attempt {
			return nil, fail(types.ErrStaleAttempt, "lite.publish", "job attempt changed")
		}
		if j.StructureVersion != l.StructureVersion {
			return nil, fail(types.ErrVersionConflict, "lite.publish", "library structure changed")
		}
		if j.LibrarySnapshot != nil && !j.LibrarySnapshot.IndexingStrategy.WikiEnabled && (len(p.Pages) > 0 || len(p.Themes) > 0) {
			return nil, fail(types.ErrCandidateInvalid, "publish", "RAG-only job cannot publish Wiki pages or themes")
		}
		if err = validatePublication(p, j); err != nil {
			return nil, err
		}
		if err = validatePublicationSources(tx, j, p); err != nil {
			return nil, err
		}
		allowed := map[string]bool{}
		for _, id := range j.SourceVersionIDs {
			allowed[id] = true
		}
		for _, conclusion := range p.Sources {
			if conclusion.State == "failed" || conclusion.State == "cancelled" {
				delete(allowed, conclusion.SourceVersionID)
			}
		}

		// A no-change conclusion also depends on read content; all read versions are validated in the same transaction as the write.
		for id, version := range p.PageVersions {
			page, e := getPage(tx, l.ID, id)
			if e != nil {
				return nil, e
			}
			if page.Version != version {
				return nil, fail(types.ErrVersionConflict, "publish", "read page changed")
			}
		}
		structureChanged := false
		for _, m := range p.Themes {
			if _, _, err = applyTheme(tx, l, m, true, pageEdit{job: j}); err != nil {
				return nil, err
			}
			structureChanged = true
		}
		pageIDs := []string{}
		pageVersion := 0
		for _, m := range p.Pages {
			page := m.Page
			changed, e := writePageWithEdit(tx, l, &page, m.ExpectedVersion, true, allowed, pageEdit{job: j})
			if e != nil {
				return nil, e
			}
			structureChanged = structureChanged || changed
			pageIDs = append(pageIDs, page.ID)
			pageVersion = page.Version
		}
		// 成功回执必须与页面写入原子提交，崩溃恢复不能再次应用同一贡献。
		if len(contributionKeys) > 0 && len(p.Pages) != 1 {
			return nil, fail(types.ErrInvalid, "lite.publish", "contribution checkpoint requires one page")
		}
		for _, key := range contributionKeys {
			row := types.Receipt{RequestID: key, Digest: digest(key), LibraryID: l.ID, JobID: j.ID, JobAttempt: j.Attempt, Version: int64(pageVersion), State: "applied", PageIDs: pageIDs}
			if err = tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
				return nil, err
			}
		}
		for _, conclusion := range p.Sources {
			v, e := getSource(tx, l.ID, conclusion.SourceVersionID)
			if e != nil {
				return nil, e
			}
			if v.State == "processed" || v.State == "no-change" {
				return nil, fail(types.ErrVersionConflict, "publish", "source already has a published conclusion")
			}
			updates := map[string]any{"state": conclusion.State, "coverage": []byte(conclusion.Coverage), "diagnostic": conclusion.Diagnostic, "row_version": v.RowVersion + 1}
			if conclusion.Description != "" {
				updates["description"] = conclusion.Description
				updates["profile"] = conclusion.Profile
				updates["summary_status"] = "completed"
			}
			r := tx.Model(&types.SourceVersion{}).Where("id = ? AND row_version = ?", v.ID, v.RowVersion).Updates(updates)
			if r.Error != nil {
				return nil, r.Error
			}
			if r.RowsAffected != 1 {
				return nil, fail(types.ErrVersionConflict, "publish", "source changed")
			}
			if err = settleOrganizeSourceWork(tx, j, conclusion); err != nil {
				return nil, err
			}
		}

		if !p.Maintenance {
			for _, mutation := range p.Pages {
				page := mutation.Page
				if page.PageType == "index" {
					continue
				}
				row := types.WikiFinalizeOp{ID: newID(), LibraryID: j.LibraryID, JobID: j.ID, JobAttempt: j.Attempt, Key: "slug/" + page.Slug, Kind: "slug", Slug: page.Slug, Title: page.Title, CreatedAt: time.Now().UnixMilli()}
				if err = tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "job_id"}, {Name: "job_attempt"}, {Name: "key"}}, DoUpdates: clause.AssignmentColumns([]string{"title"})}).Create(&row).Error; err != nil {
					return nil, err
				}
			}
			for _, mutation := range p.Pages {
				page := mutation.Page
				if page.PageType != "summary" || len(page.Evidence.Sources) == 0 {
					continue
				}
				source, e := getSource(tx, l.ID, page.Evidence.Sources[0].SourceVersionID)
				if e != nil {
					return nil, e
				}
				row := types.WikiFinalizeOp{ID: newID(), LibraryID: j.LibraryID, JobID: j.ID, JobAttempt: j.Attempt, Key: "change/" + source.ID, Kind: "added", DocTitle: source.Filename, DocSummary: page.Summary, CreatedAt: time.Now().UnixMilli()}
				if err = tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "job_id"}, {Name: "job_attempt"}, {Name: "key"}}, DoNothing: true}).Create(&row).Error; err != nil {
					return nil, err
				}
			}
			for _, conclusion := range p.Sources {
				if conclusion.State != "processed" && conclusion.State != "no-change" {
					continue
				}
				source, e := getSource(tx, l.ID, conclusion.SourceVersionID)
				if e != nil {
					return nil, e
				}
				row := types.WikiFinalizeOp{ID: newID(), LibraryID: j.LibraryID, JobID: j.ID, JobAttempt: j.Attempt, Key: "change/" + source.ID, Kind: "added", DocTitle: source.Filename, DocSummary: conclusion.Description, CreatedAt: time.Now().UnixMilli()}
				if err = tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "job_id"}, {Name: "job_attempt"}, {Name: "key"}}, DoNothing: true}).Create(&row).Error; err != nil {
					return nil, err
				}
			}
		}
		if structureChanged {
			if err = bumpStructure(tx, l); err != nil {
				return nil, err
			}
		}
		if len(p.Pages) > 0 {
			if err := refreshBacklinks(tx, l.ID); err != nil {
				return nil, err
			}
		}
		state := "processing"
		diagnostics := []string{}
		if p.Final {
			var all []types.SourceVersion
			if err = tx.Where("id IN ?", j.SourceVersionIDs).Find(&all).Error; err != nil {
				return nil, err
			}
			if len(all) != len(j.SourceVersionIDs) {
				return nil, fail(types.ErrNotFound, "publish", "final source identity is missing")
			}
			failed := 0
			for _, v := range all {
				if v.LibraryID != j.LibraryID {
					return nil, fail(types.ErrScopeViolation, "publish", "final source belongs to another library")
				}
				switch v.State {
				case "processed", "no-change":
				case "failed", "cancelled":
					failed++
					if len(diagnostics) < liteJobDiagnosticSamples {
						text := []rune(strings.TrimSpace(v.Filename) + ": " + strings.TrimSpace(v.Diagnostic))
						if len(text) > liteJobDiagnosticRunes {
							text = text[:liteJobDiagnosticRunes]
						}
						diagnostics = append(diagnostics, string(text))
					}
				default:
					return nil, fail(types.ErrCandidateInvalid, "publish", "final publication has incomplete sources")
				}
			}
			state = "completed"
			if failed > 0 {
				state = "partial_failed"
			}
			if failed == len(all) {
				state = "failed"
			}
		}
		updates := map[string]any{"version": j.Version + 1, "structure_version": l.StructureVersion, "state": state, "updated_at": time.Now().UnixMilli()}
		if p.Final {
			// 与真实终态一起冻结当前尝试的有界失败摘要，复用上面的来源读取，不在活动列表逐条补诊断。
			updates["diagnostic"] = strings.Join(diagnostics, "\n")
		}
		r := tx.Model(&types.Job{}).Where("id = ? AND version = ? AND attempt = ? AND cancel_requested = ?", j.ID, j.Version, j.Attempt, false).Updates(updates)
		if r.Error != nil {
			return nil, r.Error
		}
		if r.RowsAffected != 1 {
			return nil, fail(types.ErrVersionConflict, "publish", "job changed")
		}
		return &types.Receipt{LibraryID: l.ID, ResourceID: j.ID, JobID: j.ID, Version: j.Version + 1, PageIDs: pageIDs, State: "published"}, nil
	})
}

// BeginModelStage saves actual input for one independent call without creating a host execution binding.
func (s *Store) BeginModelStage(ctx context.Context, jobID string, attempt int, name, kind string, sourceIDs []string, input json.RawMessage) (result *types.Stage, err error) {
	err = s.transaction(ctx, func(tx *gorm.DB) error {
		j, _, e := activeJob(tx, jobID)
		if e != nil {
			return e
		}
		if j.Attempt != attempt {
			return fail(types.ErrStaleAttempt, "lite.stage", "job attempt changed")
		}
		for _, id := range sourceIDs {
			if e = validateOrganizeSource(tx, j, id); e != nil {
				return e
			}
		}
		var prior types.Stage
		e = tx.Where("job_id = ? AND job_attempt = ? AND name = ? AND kind = ? AND state = ? AND outcome = ? AND completeness = ?", j.ID, attempt, name, kind, "finished", "succeeded", "complete").Order("created_at DESC").Take(&prior).Error
		if e == nil && kind != "reading" {
			result = &prior
			return nil
		}
		if e != nil && !errors.Is(e, gorm.ErrRecordNotFound) {
			return e
		}
		st := types.Stage{ID: newID(), JobID: j.ID, Name: name, Kind: kind, JobAttempt: j.Attempt, Attempt: 1, Version: 1, State: "running", Outcome: "unknown", Completeness: "unknown", Input: input, SourceVersionIDs: sourceIDs, CreatedAt: time.Now().UnixMilli()}
		if len(sourceIDs) == 1 {
			st.SourceVersionID = sourceIDs[0]
		}
		if e = tx.Create(&st).Error; e != nil {
			return e
		}
		result = &st
		return tx.Model(&types.Job{}).Where("id = ? AND version = ?", j.ID, j.Version).Updates(map[string]any{"state": "processing", "version": j.Version + 1, "updated_at": time.Now().UnixMilli()}).Error
	})
	return
}

// FinishModelStage saves raw output, actual errors, and usage for one call; cancellation can release the slot after the last call ends.
func (s *Store) FinishModelStage(ctx context.Context, id, text, reason, diagnostic string, usage *types.ModelUsage, diagnostics *types.ModelDiagnostics) error {
	return s.transaction(ctx, func(tx *gorm.DB) error {
		st, e := getStage(tx, id)
		if e != nil {
			return e
		}
		j, e := getJob(tx, st.JobID)
		if e != nil {
			return e
		}
		if j.Attempt != st.JobAttempt {
			return fail(types.ErrStaleAttempt, "lite.stage", "job attempt changed")
		}
		outcome := "succeeded"
		complete := "complete"
		if diagnostic != "" {
			outcome = "failed"
		}
		if j.CancelRequested {
			outcome = "cancelled"
		}
		if reason == "length" || reason == "max_tokens" || reason == "max_output_tokens" {
			complete = "truncated"
		}
		if e = tx.Model(&types.Stage{}).Where("id = ? AND state = ?", st.ID, "running").Updates(map[string]any{"state": "finished", "outcome": outcome, "completeness": complete, "result_text": text, "stop_reason": reason, "diagnostic": diagnostic, "usage": nullableJSON(usage), "diagnostics": nullableJSON(diagnostics), "finished_at": time.Now().UnixMilli(), "version": st.Version + 1}).Error; e != nil {
			return e
		}
		return settleCancelledJob(tx, j)
	})
}

// RecoverLiteStages marks calls left behind by abnormal process exit as failed; subsequent sources retry under the failure budget.
func (s *Store) RecoverLiteStages(ctx context.Context, jobID string, attempt int) error {
	return s.transaction(ctx, func(tx *gorm.DB) error {
		j, e := getJob(tx, jobID)
		if e != nil {
			return e
		}
		if j.Attempt != attempt {
			return fail(types.ErrStaleAttempt, "lite.recover", "job attempt changed")
		}
		if e = tx.Model(&types.Stage{}).Where("job_id = ? AND job_attempt = ? AND state = ?", jobID, attempt, "running").Updates(map[string]any{"state": "finished", "outcome": "failed", "diagnostic": "model call interrupted by process exit", "finished_at": time.Now().UnixMilli()}).Error; e != nil {
			return e
		}
		return settleCancelledJob(tx, j)
	})
}

// FindSimilarWikiPages uses the ported repository's bounded candidate query within the same library.
func (s *Store) FindSimilarWikiPages(ctx context.Context, libraryID, query string) ([]*types.WikiPageLite, error) {
	return NewWikiPageRepository(s.db).FindSimilarPages(ctx, libraryID, query, []string{types.WikiPageTypeEntity, types.WikiPageTypeConcept}, 5)
}

// RecordModelParseFailure marks a natively successful but structurally invalid response as a parse failure, preserving its raw text.
func (s *Store) RecordModelParseFailure(ctx context.Context, jobID, kind, sourceID, raw, diagnostic string) error {
	return s.transaction(ctx, func(tx *gorm.DB) error {
		return tx.Model(&types.Stage{}).Where("job_id = ? AND kind = ? AND source_version_id = ? AND result_text = ? AND outcome = ?", jobID, kind, sourceID, raw, "succeeded").Updates(map[string]any{"outcome": "failed", "diagnostic": diagnostic}).Error
	})
}

func nullableJSON(value any) any {
	if value == nil {
		return nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	if string(raw) == "null" {
		return nil
	}
	return string(raw)
}

// ListWikiFinalizeOps 读取同任务全部已发布收尾意图；显式重试不丢弃旧尝试尚未完成的页面与文档变化。
func (s *Store) ListWikiFinalizeOps(ctx context.Context, jobID string, attempt int) (items []*types.WikiFinalizeOp, err error) {
	err = s.transaction(ctx, func(tx *gorm.DB) error {
		return tx.Where("job_id = ? AND job_attempt <= ?", jobID, attempt).Order("job_attempt ASC, created_at ASC, key ASC").Find(&items).Error
	})
	return
}

// ListSourceContributionPages 按稳定来源身份读取完整贡献集合，包含归档页而不扫描无关正文到管线内存。
func (s *Store) ListSourceContributionPages(ctx context.Context, libraryID string, sourceIDs []string, afterSlug string, limit int) (items []*types.WikiPage, err error) {
	if len(sourceIDs) == 0 {
		return nil, nil
	}
	if len(sourceIDs) > 1000 || limit < 1 || limit > 100 {
		return nil, fail(types.ErrInvalid, "lite.source-pages", "source scope or page batch exceeds supported bound")
	}
	err = s.transaction(ctx, func(tx *gorm.DB) error {
		return tx.Where("knowledge_base_id = ? AND slug > ?", libraryID, afterSlug).
			Where("EXISTS (SELECT 1 FROM json_each(evidence, '$.sources') WHERE json_extract(value, '$.sourceId') IN ?)", sourceIDs).
			Order("slug ASC").Limit(limit).Find(&items).Error
	})
	return
}

// UpdateWikiMaintenance performs automatic link maintenance writes without creating content history versions.
func (s *Store) UpdateWikiMaintenance(ctx context.Context, jobID string, attempt int, page *types.WikiPage, expected int) error {
	readTime := page.UpdatedAt
	var writtenAt time.Time
	err := s.transaction(ctx, func(tx *gorm.DB) error {
		j, l, err := activeJob(tx, jobID)
		if err != nil {
			return err
		}
		if j.Attempt != attempt {
			return fail(types.ErrStaleAttempt, "lite.links", "job attempt changed")
		}
		if j.StructureVersion != l.StructureVersion {
			return fail(types.ErrVersionConflict, "lite.links", "library structure changed")
		}
		existing, err := getPage(tx, l.ID, page.ID)
		if err != nil {
			return err
		}
		if existing.Version != expected || existing.UpdatedAt.UnixMilli() != readTime.UnixMilli() {
			return fail(types.ErrVersionConflict, "lite.links", "page changed")
		}
		for _, text := range existing.ProtectedContent {
			if !strings.Contains(page.Content, text) {
				return fail(types.ErrManualProtected, "lite.links", "protected manual content changed")
			}
		}
		page.OutLinks = wiki.ParseOutLinks(page.Content)
		linksJSON, err := json.Marshal(page.OutLinks)
		if err != nil {
			return err
		}
		writtenAt = nextPageWriteTime(existing.UpdatedAt)
		r := tx.Model(&types.WikiPage{}).Where("id = ? AND version = ? AND updated_at = ?", page.ID, expected, existing.UpdatedAt).Updates(map[string]any{"content": page.Content, "out_links": linksJSON, "updated_at": writtenAt})
		if r.Error != nil {
			return r.Error
		}
		if r.RowsAffected != 1 {
			return fail(types.ErrVersionConflict, "lite.links", "page changed")
		}
		if err := pruneWrittenRevisions(tx, page.ID, expected); err != nil {
			return err
		}
		return refreshBacklinks(tx, l.ID)
	})
	if err == nil {
		page.UpdatedAt = writtenAt
	}
	return err
}

// FindWikiPagesByNormalizedTitles uses a bounded exact identity query compatible with SQLite.
func (s *Store) FindWikiPagesByNormalizedTitles(ctx context.Context, libraryID, pageType string, identities []string) ([]*types.WikiPageLite, error) {
	return NewWikiPageRepository(s.db).FindPagesByNormalizedTitles(ctx, libraryID, pageType, identities)
}

// LoadLitePlan 读取同一任务已完成的文档计划，重试尝试复用不可变来源，不跨任务复用。
func (s *Store) LoadLitePlan(ctx context.Context, jobID string, attempt int, sourceID string) (string, error) {
	var row types.Stage
	err := s.db.WithContext(ctx).Where("job_id = ? AND job_attempt <= ? AND name = ? AND kind = ? AND state = ? AND outcome = ?", jobID, attempt, "plan/"+sourceID, "document_plan", "finished", "succeeded").Order("created_at DESC").Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil
	}
	return row.ResultText, translate("lite.plan.read", err)
}

// SaveLitePlan 保存已验证的提取与引用结果，重试仍须校验来源、任务尝试和最终发布权限。
func (s *Store) SaveLitePlan(ctx context.Context, jobID string, attempt int, sourceID, payload string) error {
	return s.transaction(ctx, func(tx *gorm.DB) error {
		j, _, err := activeJob(tx, jobID)
		if err != nil {
			return err
		}
		if j.Attempt != attempt {
			return fail(types.ErrStaleAttempt, "lite.plan", "job attempt changed")
		}
		if err = validateOrganizeSource(tx, j, sourceID); err != nil {
			return err
		}
		row := types.Stage{ID: newID(), JobID: jobID, JobAttempt: attempt, Name: "plan/" + sourceID, Kind: "document_plan", SourceVersionID: sourceID, SourceVersionIDs: []string{sourceID}, Attempt: 1, Version: 1, State: "finished", Outcome: "succeeded", Completeness: "complete", ResultText: payload, Input: json.RawMessage(`{}`), CreatedAt: time.Now().UnixMilli(), FinishedAt: time.Now().UnixMilli()}
		return tx.Create(&row).Error
	})
}

// HasCurrentLitePageCheckpoint 确认单页阶段已提交且仍归本任务版本所有，恢复时保留权限与读取时间校验。
func (s *Store) HasCurrentLitePageCheckpoint(ctx context.Context, jobID string, attempt int, key string, page *types.WikiPage) (applied bool, err error) {
	err = s.transaction(ctx, func(tx *gorm.DB) error {
		applied = false
		j, l, e := activeJob(tx, jobID)
		if e != nil {
			return e
		}
		if j.Attempt != attempt {
			return fail(types.ErrStaleAttempt, "lite.page-checkpoint", "job attempt changed")
		}
		if j.StructureVersion != l.StructureVersion {
			return fail(types.ErrVersionConflict, "lite.page-checkpoint", "library structure changed")
		}
		var marker types.Receipt
		e = tx.Where("request_id = ? AND job_id = ? AND job_attempt <= ? AND state = ?", key, j.ID, attempt, "applied").Take(&marker).Error
		if errors.Is(e, gorm.ErrRecordNotFound) {
			return nil
		}
		if e != nil {
			return e
		}
		if page == nil || len(marker.PageIDs) != 1 || marker.PageIDs[0] != page.ID {
			return fail(types.ErrVersionConflict, "lite.page-checkpoint", "checkpoint page changed")
		}
		current, e := getPage(tx, l.ID, page.ID)
		if e != nil {
			return e
		}
		if marker.Version != int64(current.Version) || current.Version != page.Version || page.UpdatedAt.IsZero() || current.UpdatedAt.UnixMilli() != page.UpdatedAt.UnixMilli() {
			return fail(types.ErrVersionConflict, "lite.page-checkpoint", "committed page changed")
		}
		// 原文未变的首次发布可能没有任务内容版本回执；阶段回执仍固定其实际提交版本。
		var version types.Receipt
		e = tx.Where("request_id = ?", jobPageVersionKey(j, current.ID)).Take(&version).Error
		if e == nil && (version.Version != int64(current.Version) || current.LastEditSource != types.WikiEditSourcePipeline) {
			return fail(types.ErrVersionConflict, "lite.page-checkpoint", "another edit superseded this job version")
		}
		if e != nil && !errors.Is(e, gorm.ErrRecordNotFound) {
			return e
		}
		applied = true
		return nil
	})
	return
}

// HasLiteContribution 查询同一任务已提交的来源贡献，兼容早期按尝试保存的回执。
func (s *Store) HasLiteContribution(ctx context.Context, jobID, contributionHash string) (bool, error) {
	var row types.Receipt
	key := "lite/" + jobID + "/applied/" + contributionHash
	legacy := "lite/" + jobID + "/%/applied/" + contributionHash
	err := s.db.WithContext(ctx).Where("job_id = ? AND state = ? AND (request_id = ? OR request_id LIKE ?)", jobID, "applied", key, legacy).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	return err == nil, translate("lite.contribution.read", err)
}

// AdoptLiteContribution 仅将已原子提交的旧内容回执映射到稳定贡献身份，不凭模型的新正文确认成功。
func (s *Store) AdoptLiteContribution(ctx context.Context, jobID string, attempt int, identity string, legacyHashes []string) (applied bool, err error) {
	err = s.transaction(ctx, func(tx *gorm.DB) error {
		j, _, e := activeJob(tx, jobID)
		if e != nil {
			return e
		}
		if j.Attempt != attempt {
			return fail(types.ErrStaleAttempt, "lite.contribution-adopt", "job attempt changed")
		}
		for _, h := range legacyHashes {
			var marker types.Receipt
			key := "lite/" + jobID + "/applied/" + h
			legacy := "lite/" + jobID + "/%/applied/" + h
			e = tx.Where("job_id = ? AND job_attempt <= ? AND state = ? AND (request_id = ? OR request_id LIKE ?)", jobID, attempt, "applied", key, legacy).Take(&marker).Error
			if errors.Is(e, gorm.ErrRecordNotFound) {
				continue
			}
			if e != nil {
				return e
			}
			marker.RequestID = "lite/" + jobID + "/applied/" + identity
			marker.Digest = digest(marker.RequestID)
			if e = tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&marker).Error; e != nil {
				return e
			}
			applied = true
			return nil
		}
		return nil
	})
	return
}
