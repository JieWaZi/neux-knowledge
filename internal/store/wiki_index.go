package store

import (
	"context"
	"errors"
	types "github.com/JieWaZi/neux-knowledge/internal/domain"
	"github.com/JieWaZi/neux-knowledge/internal/wiki"
	"gorm.io/gorm"
	"strconv"
	"strings"
)

// wikiIndexContentPageTypes preserves the fixed folder grouping order without mixing index rows into the directory.
var wikiIndexContentPageTypes = []string{types.WikiPageTypeSummary, types.WikiPageTypeEntity, types.WikiPageTypeConcept, types.WikiPageTypeSynthesis, types.WikiPageTypeComparison}

// GetIndexView provides a lightweight paginated directory without mistaking the introduction for the complete index.
// The local read-only entry point does not implicitly create an index; it returns the actual full-library directory even without an introduction.
func (s *Store) GetIndexView(ctx context.Context, kbID string, pageTypes []string, limit int, cursor string) (result *types.WikiIndexResponse, err error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	offset := 0
	if cursor != "" {
		v, e := strconv.Atoi(cursor)
		if e != nil || v < 0 {
			return nil, fail(types.ErrInvalid, "wiki.index", "invalid cursor")
		}
		offset = v
	}
	err = s.transaction(ctx, func(tx *gorm.DB) error {
		if _, e := getLibrary(tx, kbID, false); e != nil {
			return e
		}
		var indexPage types.WikiPage
		e := tx.Select("content", "summary", "version").Where("knowledge_base_id = ? AND slug = ? AND status <> ?", kbID, "index", types.WikiPageStatusArchived).Take(&indexPage).Error
		if e != nil && !errors.Is(e, gorm.ErrRecordNotFound) {
			return e
		}
		selected := pageTypes
		if len(selected) == 0 {
			selected = append([]string{}, wikiIndexContentPageTypes...)
			// General libraries may contain extended types such as topic/guide; append only types actually present so existing knowledge is retained.
			var actual []string
			if e := tx.Model(&types.WikiPage{}).Distinct("page_type").Where("knowledge_base_id = ? AND status <> ? AND page_type <> ?", kbID, types.WikiPageStatusArchived, types.WikiPageTypeIndex).Order("page_type ASC").Pluck("page_type", &actual).Error; e != nil {
				return e
			}
			for _, pt := range actual {
				found := false
				for _, known := range selected {
					found = found || pt == known
				}
				if !found {
					selected = append(selected, pt)
				}
			}
		}
		groups := make([]types.WikiIndexGroup, 0, len(selected))
		repo := NewWikiPageRepository(tx)
		for _, pt := range selected {
			entries, total, listErr := repo.ListByTypeLight(ctx, kbID, pt, limit, offset)
			if listErr != nil {
				return listErr
			}
			if entries == nil {
				entries = []types.WikiIndexEntry{}
			}
			for i := range entries {
				normalizeWikiIndexEntryHierarchy(&entries[i], pt)
			}
			next := ""
			if len(entries) == limit && int64(offset+len(entries)) < total {
				next = strconv.Itoa(offset + limit)
			}
			groups = append(groups, types.WikiIndexGroup{Type: pt, Total: total, Items: entries, NextCursor: next})
		}
		intro := indexPage.Content
		if strings.TrimSpace(intro) == "" {
			intro = indexPage.Summary
		}
		if strings.TrimSpace(intro) != "" {
			intro = wiki.NormalizeIndexIntro(intro)
		}
		result = &types.WikiIndexResponse{Intro: intro, Version: indexPage.Version, Groups: groups}
		return nil
	})
	return
}

func normalizeWikiIndexEntryHierarchy(entry *types.WikiIndexEntry, pageType string) {
	if entry == nil {
		return
	}
	cleanPath := types.StringArray(types.CleanWikiCategoryPath(entry.CategoryPath))
	entry.CategoryPath = cleanPath
	entry.Depth = len(cleanPath)
	display := strings.TrimSpace(entry.Title)
	if display == "" {
		display = strings.TrimSpace(entry.Slug)
	}
	entry.WikiPath = buildWikiPath(pageType, cleanPath, display)
}
func buildWikiPath(pageType string, categoryPath []string, display string) string {
	parts := make([]string, 0, len(categoryPath)+2)
	if pt := strings.TrimSpace(pageType); pt != "" {
		parts = append(parts, pt)
	}
	parts = append(parts, categoryPath...)
	if display != "" {
		parts = append(parts, display)
	}
	return strings.Join(parts, "/")
}
