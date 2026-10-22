package pipeline

import (
	"encoding/json"
	types "github.com/JieWaZi/neux-knowledge/internal/domain"
	"sort"
	"strings"
)

func collectTaxonomyItems(slugUpdates map[string][]SlugUpdate) []wikiTaxonomyItem {
	slugs := make([]string, 0, len(slugUpdates))
	for slug := range slugUpdates {
		slugs = append(slugs, slug)
	}
	sort.Strings(slugs)

	items := make([]wikiTaxonomyItem, 0, len(slugs))
	for _, slug := range slugs {
		for _, u := range slugUpdates[slug] {
			if u.Type != types.WikiPageTypeEntity && u.Type != types.WikiPageTypeConcept {
				continue
			}
			title := strings.TrimSpace(u.Item.Name)
			if title == "" {
				title = slug
			}
			items = append(items, wikiTaxonomyItem{
				slug:     slug,
				title:    title,
				pageType: u.Type,
				about:    strings.TrimSpace(u.Item.Description),
			})
			break // one entry per slug is enough for classification
		}
	}
	return items
}

func parseTaxonomyAssignments(raw string) map[string][]string {
	raw = cleanLLMJSON(raw)
	if raw == "" {
		return nil
	}
	var parsed struct {
		Assignments []struct {
			Slug string   `json:"slug"` // Planned target knowledge.
			Path []string `json:"path"` // Planned topic path.
		} `json:"assignments"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil
	}
	out := make(map[string][]string, len(parsed.Assignments))
	for _, a := range parsed.Assignments {
		slug := strings.TrimSpace(a.Slug)
		if slug == "" {
			continue
		}
		out[slug] = a.Path
	}
	return out
}
