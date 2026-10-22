// Copyright (C) 2026 Tencent. All rights reserved.
// See THIRD_PARTY_NOTICES.md for license information.

package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	types "github.com/JieWaZi/neux-knowledge/internal/domain"
	"github.com/JieWaZi/neux-knowledge/internal/wiki"
	"gorm.io/gorm"
)

const auditCursorBatch = 200
const lintMinimumContentRunes = 50
const exportPageLimit = 1000
const markdownExportFormatVersion = 1

// walkAuditPages reads bounded bodies using a primary-key cursor; all batches share the caller's read transaction.
func walkAuditPages(tx *gorm.DB, libraryID string, publishedOnly bool, pageIDs []string, visit func(*types.WikiPage) error) error {
	cursor := ""
	for {
		query := tx.Where("knowledge_base_id = ? AND id > ?", libraryID, cursor)
		if publishedOnly {
			query = query.Where("status = ?", "published")
		} else {
			query = query.Where("status <> ?", "archived")
		}
		if len(pageIDs) > 0 {
			query = query.Where("id IN ?", pageIDs)
		}
		var pages []types.WikiPage
		if err := query.Order("id ASC").Limit(auditCursorBatch).Find(&pages).Error; err != nil {
			return err
		}
		if len(pages) == 0 {
			return nil
		}
		for i := range pages {
			if err := visit(&pages[i]); err != nil {
				return err
			}
		}
		cursor = pages[len(pages)-1].ID
	}
}

type auditSource struct {
	version *types.SourceVersion // Immutable source version in this library, or nil if absent.
	head    *types.SourceHead    // Current head of the stable source in this library, or nil if absent.
}

func getAuditSource(tx *gorm.DB, libraryID, versionID string, cache map[string]auditSource) (auditSource, error) {
	if prior, ok := cache[versionID]; ok {
		return prior, nil
	}
	result := auditSource{}
	var source types.SourceVersion
	err := tx.Select("id", "library_id", "source_id", "version", "filename", "sha256", "archived").Where("library_id = ? AND id = ?", libraryID, versionID).Take(&source).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		cache[versionID] = result
		return result, nil
	}
	if err != nil {
		return result, err
	}
	result.version = &source
	var head types.SourceHead
	err = tx.Where("library_id = ? AND id = ?", libraryID, source.SourceID).Take(&head).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return result, err
	}
	if err == nil {
		result.head = &head
	}
	cache[versionID] = result
	return result, nil
}

// LintLibrary checks orphan pages, broken links, short bodies, and unavailable sources without destructive AutoFix operations.
// Relations are recalculated from the body and sources verified against the sole Evidence record; read errors return directly rather than masquerading as unavailable sources.
func (s *Store) LintLibrary(ctx context.Context, libraryID string) (result *types.LintReport, err error) {
	err = s.transaction(ctx, func(tx *gorm.DB) error {
		library, err := getLibrary(tx, libraryID, false)
		if err != nil {
			return err
		}
		report := &types.LintReport{LibraryID: library.ID, LibraryVersion: library.Version, StructureVersion: library.StructureVersion, PolicyVersion: library.PolicyVersion, Issues: []types.LintIssue{}}
		var slugs []string
		if err = tx.Model(&types.WikiPage{}).Where("knowledge_base_id = ? AND status <> ?", libraryID, "archived").Pluck("slug", &slugs).Error; err != nil {
			return err
		}
		live := map[string]bool{}
		for _, slug := range slugs {
			live[slug] = true
		}
		report.PageCount = len(slugs)
		incoming := map[string]bool{}
		if err = walkAuditPages(tx, libraryID, false, nil, func(page *types.WikiPage) error {
			for _, target := range wiki.ParseOutLinks(page.Content) {
				if target != page.Slug && live[target] {
					incoming[target] = true
				}
			}
			return nil
		}); err != nil {
			return err
		}
		cache := map[string]auditSource{}
		err = walkAuditPages(tx, libraryID, false, nil, func(page *types.WikiPage) error {
			add := func(kind, severity, target, description string) {
				report.Issues = append(report.Issues, types.LintIssue{Type: kind, Severity: severity, PageID: page.ID, PageSlug: page.Slug, PageVersion: page.Version, TargetID: target, Description: description})
			}
			if page.PageType != types.WikiPageTypeIndex && !incoming[page.Slug] {
				add("orphan_page", "warning", "", "页面没有来自其他活动页面的入链")
			}
			for _, target := range wiki.ParseOutLinks(page.Content) {
				if !live[target] {
					add("broken_link", "error", target, "内部链接的目标在当前库中不存在或已归档")
				}
			}
			if count := utf8.RuneCountInString(strings.TrimSpace(page.Content)); count < lintMinimumContentRunes {
				add("empty_content", "warning", "", fmt.Sprintf("正文少于 %d 个字符，当前 %d 个；仅供人工核查", lintMinimumContentRunes, count))
			}
			seen := map[string]bool{}
			stale := map[string]bool{}
			if marker := types.ParseSourcePurgeReview(page.PageMetadata); marker != nil {
				for _, id := range marker.UnavailableVersionIDs {
					stale[id] = true
					add("stale_ref", "error", id, "来源版本已永久清理；保留的正文需要人工核查")
				}
			}
			for _, ref := range page.Evidence.Sources {
				seen[ref.SourceVersionID] = true
				source, e := getAuditSource(tx, libraryID, ref.SourceVersionID, cache)
				if e != nil {
					return e
				}
				diagnostic := ""
				switch {
				case source.version == nil:
					diagnostic = "引用的来源版本在当前库中不存在"
				case ref.SourceID != source.version.SourceID || ref.SHA256 != source.version.SHA256:
					diagnostic = "来源身份或摘要与引用不一致"
				case source.version.Archived:
					diagnostic = "引用的来源版本已归档"
				case source.head == nil || source.head.VersionID != ref.SourceVersionID || source.head.Version != source.version.Version:
					diagnostic = "引用的来源已存在新版本或缺少当前版本记录"
				}
				if diagnostic != "" && !stale[ref.SourceVersionID] {
					stale[ref.SourceVersionID] = true
					add("stale_ref", "error", ref.SourceVersionID, diagnostic)
				}
			}
			for _, sourceRef := range page.SourceRefs {
				id := types.WikiSourceKnowledgeID(sourceRef)
				if id != "" && !seen[id] && !stale[id] {
					seen[id] = true
					add("stale_ref", "error", id, "来源引用缺少对应的权威证据")
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		sort.Slice(report.Issues, func(i, j int) bool {
			a, b := report.Issues[i], report.Issues[j]
			if a.PageID != b.PageID {
				return a.PageID < b.PageID
			}
			if a.Type != b.Type {
				return a.Type < b.Type
			}
			return a.TargetID < b.TargetID
		})
		result = report
		return nil
	})
	return
}

func exportThemePath(themes map[string]types.WikiFolder, id string) ([]string, error) {
	path := []string{}
	seen := map[string]bool{}
	for id != "" {
		folder, ok := themes[id]
		if !ok || seen[id] {
			return nil, fail(types.ErrInvalid, "export", "theme parent chain is missing or cyclic")
		}
		seen[id] = true
		path = append(path, folder.Name)
		id = folder.ParentID
	}
	for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
		path[i], path[j] = path[j], path[i]
	}
	return path, nil
}

// ExportMarkdown builds an export copy in one consistent read transaction without reading raw files, creating receipts, or changing published records.
func (s *Store) ExportMarkdown(ctx context.Context, in types.ExportInput) (result *types.MarkdownExport, err error) {
	if len(in.PageIDs) > exportPageLimit {
		return nil, fail(types.ErrInvalid, "export", "too many selected pages")
	}
	selected := map[string]bool{}
	for _, id := range in.PageIDs {
		if strings.TrimSpace(id) == "" || selected[id] {
			return nil, fail(types.ErrInvalid, "export", "selected page IDs must be nonempty and unique")
		}
		selected[id] = true
	}
	err = s.transaction(ctx, func(tx *gorm.DB) error {
		library, err := getLibrary(tx, in.LibraryID, false)
		if err != nil {
			return err
		}
		bundle := &types.MarkdownExport{FormatVersion: markdownExportFormatVersion, LibraryID: library.ID, LibraryVersion: library.Version, StructureVersion: library.StructureVersion, PolicyVersion: library.PolicyVersion, Files: []types.MarkdownFile{}}
		var folders []types.WikiFolder
		if err = tx.Where("knowledge_base_id = ?", library.ID).Find(&folders).Error; err != nil {
			return err
		}
		themes := map[string]types.WikiFolder{}
		for _, folder := range folders {
			themes[folder.ID] = folder
		}
		cache := map[string]auditSource{}
		paths := map[string]bool{}
		err = walkAuditPages(tx, library.ID, true, in.PageIDs, func(page *types.WikiPage) error {
			themePath, err := exportThemePath(themes, page.FolderID)
			if err != nil {
				return err
			}
			metadata := types.ExportPageMetadata{LibraryID: library.ID, LibraryVersion: library.Version, StructureVersion: library.StructureVersion, PolicyVersion: library.PolicyVersion, PageID: page.ID, PageVersion: page.Version, Slug: page.Slug, Title: page.Title, PageType: page.PageType, WikiLanguage: page.WikiLanguage, ThemeID: page.FolderID, ThemePath: themePath, SortOrder: page.SortOrder, Sources: []types.ExportSourceRef{}, Code: page.Evidence.Code, SourcePurgeReview: types.ParseSourcePurgeReview(page.PageMetadata)}
			for _, ref := range page.Evidence.Sources {
				source, err := getAuditSource(tx, library.ID, ref.SourceVersionID, cache)
				if err != nil {
					return err
				}
				exported := types.ExportSourceRef{SourceID: ref.SourceID, SourceVersionID: ref.SourceVersionID, SHA256: ref.SHA256, Locator: ref.Locator, ClaimKind: ref.ClaimKind}
				if source.version != nil {
					exported.Version = source.version.Version
					exported.Filename = source.version.Filename
				}
				metadata.Sources = append(metadata.Sources, exported)
			}
			frontmatter, err := json.Marshal(metadata)
			if err != nil {
				return err
			}
			markdown := "---\n" + string(frontmatter) + "\n---\n" + page.Content
			name := "page-" + hash([]byte(page.ID)) + ".md"
			if paths[name] {
				return fail(types.ErrInvalid, "export", "export filename collision")
			}
			paths[name] = true
			bundle.Files = append(bundle.Files, types.MarkdownFile{Path: name, SHA256: hash([]byte(markdown)), Metadata: metadata, Markdown: markdown})
			return nil
		})
		if err != nil {
			return err
		}
		if len(in.PageIDs) > 0 && len(bundle.Files) != len(in.PageIDs) {
			return fail(types.ErrNotFound, "export", "selected published page was not found in this library")
		}
		result = bundle
		return nil
	})
	return
}
