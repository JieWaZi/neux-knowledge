// Copyright (C) 2026 Tencent. All rights reserved.
// See THIRD_PARTY_NOTICES.md for license information.

package store

import (
	"context"
	"sort"
	"strings"

	types "github.com/JieWaZi/neux-knowledge/internal/domain"
	"github.com/JieWaZi/neux-knowledge/internal/wiki"
	"gorm.io/gorm"
)

// AutoFixLibrary 迁移 WeKnora WikiLintService.AutoFix 的三类修复；库快照、证据与人工保护采用本地事务约束。
func (s *Store) AutoFixLibrary(ctx context.Context, in types.AutoFixInput) (*types.AutoFixResult, error) {
	report, err := s.LintLibrary(ctx, in.LibraryID)
	if err != nil {
		return nil, err
	}
	receipt, err := s.mutate(ctx, in.RequestID, "wiki.auto-fix", in.LibraryID, in, func(tx *gorm.DB) (*types.Receipt, error) {
		l, err := maintenanceLibrary(tx, in.LibraryID, in.StructureVersion, in.ExpectedPolicyVersion)
		if err != nil {
			return nil, err
		}
		if report.StructureVersion != l.StructureVersion || report.PolicyVersion != l.PolicyVersion {
			return nil, fail(types.ErrVersionConflict, "wiki.auto-fix", "library changed after lint")
		}
		issues := map[string][]types.LintIssue{}
		for _, issue := range report.Issues {
			if issue.Type == "broken_link" || issue.Type == "empty_content" || issue.Type == "stale_ref" {
				issues[issue.PageID] = append(issues[issue.PageID], issue)
			}
		}
		ids := make([]string, 0, len(issues))
		for id := range issues {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		changed := map[string]bool{}
		archived := []types.WikiPage{}
		cache := map[string]auditSource{}
		for _, id := range ids {
			p, err := maintenancePage(tx, l, id, issues[id][0].PageVersion)
			if err != nil {
				return nil, err
			}
			if p.PageType == types.WikiPageTypeIndex || p.Status == types.WikiPageStatusArchived || p.LastEditSource == types.WikiEditSourceUser || len(p.ProtectedContent) > 0 {
				continue
			}
			before := digest(p)
			for _, issue := range issues[id] {
				switch issue.Type {
				case "broken_link":
					if issue.TargetID != "" {
						p.Content = wiki.RewritePageLinks(p.Content, issue.TargetID, "")
					}
				case "empty_content":
					// 与上游保持字节长度语义；只读核查的字符计数仍可提示更宽的人工复查范围。
					if len(strings.TrimSpace(p.Content)) < 50 {
						p.Status = types.WikiPageStatusArchived
					}
				case "stale_ref":
					if issue.TargetID == "" {
						continue
					}
					source, err := getAuditSource(tx, l.ID, issue.TargetID, cache)
					if err != nil {
						return nil, err
					}
					// 上游只清理已删除来源；新版出现不等于旧版不存在，交给修复 Agent 核对正文。
					if source.version != nil && !source.version.Archived {
						continue
					}
					remaining := make([]types.SourceRef, 0, len(p.Evidence.Sources))
					for _, ref := range p.Evidence.Sources {
						if ref.SourceVersionID != issue.TargetID {
							remaining = append(remaining, ref)
						}
					}
					p.Evidence.Sources = remaining
					p.SourceRefs = nil
					if len(remaining) == 0 && len(p.Evidence.Code) == 0 {
						p.Status = types.WikiPageStatusArchived
					}
				}
			}
			if before == digest(p) {
				continue
			}
			if _, err = writePageWithEdit(tx, l, p, p.Version, true, nil, pageEdit{agent: true}); err != nil {
				return nil, err
			}
			changed[p.ID] = true
			if p.Status == types.WikiPageStatusArchived {
				archived = append(archived, *p)
			}
		}
		for _, p := range archived {
			rewrite := func(content string) string { return wiki.RewritePageLinks(content, p.Slug, "") }
			ids, err := rewriteIncomingPages(tx, l, p.ID, p.Slug, "", rewrite, true)
			if err != nil {
				return nil, err
			}
			for _, id := range ids {
				changed[id] = true
			}
		}
		if len(archived) > 0 {
			if err = bumpStructure(tx, l); err != nil {
				return nil, err
			}
		}
		if len(changed) > 0 {
			if err = refreshBacklinks(tx, l.ID); err != nil {
				return nil, err
			}
		}
		pageIDs := make([]string, 0, len(changed))
		for id := range changed {
			pageIDs = append(pageIDs, id)
		}
		sort.Strings(pageIDs)
		return &types.Receipt{ResourceID: l.ID, Version: l.StructureVersion, State: "saved", PageIDs: pageIDs}, nil
	})
	if err != nil {
		return nil, err
	}
	current, err := s.LintLibrary(ctx, in.LibraryID)
	if err != nil {
		return nil, err
	}
	return &types.AutoFixResult{Receipt: receipt, Fixed: len(receipt.PageIDs), Report: current}, nil
}
