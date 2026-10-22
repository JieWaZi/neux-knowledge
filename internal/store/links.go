package store

import (
	"encoding/json"

	types "github.com/JieWaZi/neux-knowledge/internal/domain"
	"gorm.io/gorm"
	"slices"
	"sort"
)

// refreshBacklinks projects backlinks from outgoing links in the same transaction; relations are body-derived indexes and do not independently advance body versions.
func refreshBacklinks(tx *gorm.DB, libraryID string) error {
	var pages []types.WikiPage
	if err := tx.Select("id", "slug", "status", "in_links", "out_links").Where("knowledge_base_id = ?", libraryID).Find(&pages).Error; err != nil {
		return err
	}

	active := map[string]bool{}
	incoming := map[string][]string{}
	for _, page := range pages {
		if page.Status != "archived" {
			active[page.Slug] = true
		}
	}
	for _, page := range pages {
		if !active[page.Slug] {
			continue
		}
		for _, target := range page.OutLinks {
			if target != page.Slug && active[target] {
				incoming[target] = append(incoming[target], page.Slug)
			}
		}
	}
	for _, page := range pages {
		expected := incoming[page.Slug]
		sort.Strings(expected)
		expected = slices.Compact(expected)
		if slices.Equal(page.InLinks, expected) {
			continue
		}

		raw, err := json.Marshal(expected)
		if err != nil {
			return err
		}
		if err := tx.Model(&types.WikiPage{}).Where("knowledge_base_id = ? AND id = ?", libraryID, page.ID).UpdateColumn("in_links", string(raw)).Error; err != nil {
			return err
		}
	}
	return nil
}
