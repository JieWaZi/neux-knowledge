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
	var parsed taxonomyAssignments
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil
	}
	return parsed.paths()
}

type taxonomyAssignments struct {
	Assignments []struct {
		Slug string   `json:"slug"` // 本批已提取的目标知识地址。
		Path []string `json:"path"` // 为该目标规划的目录层级。
	} `json:"assignments"` // 本批目标的目录归属建议。
}

func (parsed taxonomyAssignments) paths() map[string][]string {
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
