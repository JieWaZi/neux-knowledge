package pipeline

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

func previewText(s string, maxRunes int) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\t", " ")
	for strings.Contains(s, "  ") {
		s = strings.ReplaceAll(s, "  ", " ")
	}
	r := []rune(s)
	if maxRunes <= 0 || len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes]) + "...(truncated)"
}

func previewStringSlice(items []string, limit int) string {
	if len(items) == 0 {
		return "[]"
	}
	if limit <= 0 {
		limit = 1
	}
	n := len(items)
	if n > limit {
		items = items[:limit]
	}
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, previewText(it, 48))
	}
	if n > limit {
		return fmt.Sprintf("[%s ...(+%d)]", strings.Join(out, ", "), n-limit)
	}
	return fmt.Sprintf("[%s]", strings.Join(out, ", "))
}

// previewExtractedItems returns a JSON-friendly preview of the first
// `limit` extracted entities or concepts so the trace viewer's
// postprocess.wiki.extract span shows actual names/slugs/descriptions
// instead of bare counts. Each item is trimmed to a small fixed
// budget — these end up serialised into the spans table's JSONB
// output column, so the cumulative size matters more than per-item
// fidelity.
func previewExtractedItems(items []extractedItem, limit int) []map[string]string {
	if limit <= 0 {
		limit = 1
	}
	n := len(items)
	if n > limit {
		items = items[:limit]
	}
	out := make([]map[string]string, 0, len(items))
	for _, it := range items {
		out = append(out, map[string]string{
			"name":        previewText(it.Name, 60),
			"slug":        it.Slug,
			"description": previewText(it.Description, 120),
		})
	}
	return out
}

// topCitedSlugs returns the top `limit` slugs by chunk-citation count.
// Used by postprocess.wiki.classify so the trace surfaces which
// candidate slugs the citation pass attached the most chunks to —
// useful when triaging "this LLM run extracted weird things" without
// having to open and diff full chunk lists.
func topCitedSlugs(citations map[string][]string, limit int) []map[string]any {
	if len(citations) == 0 {
		return nil
	}
	type entry struct {
		slug  string
		count int
	}
	entries := make([]entry, 0, len(citations))
	for slug, ids := range citations {
		entries = append(entries, entry{slug: slug, count: len(ids)})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].count != entries[j].count {
			return entries[i].count > entries[j].count
		}
		return entries[i].slug < entries[j].slug
	})
	if limit > 0 && len(entries) > limit {
		entries = entries[:limit]
	}
	out := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		out = append(out, map[string]any{
			"slug":   e.slug,
			"chunks": e.count,
		})
	}
	return out
}

// previewNewSlugs returns a JSON-friendly preview of the first
// `limit` slugs that the citation pass discovered (i.e. did not appear
// in pass-0's candidate list). Surfacing these makes "the citation
// LLM kept inventing entries" trivially diagnosable from the trace
// viewer.
func previewNewSlugs(items []newSlugFromCitation, limit int) []map[string]string {
	if limit <= 0 {
		limit = 1
	}
	n := len(items)
	if n > limit {
		items = items[:limit]
	}
	out := make([]map[string]string, 0, len(items))
	for _, it := range items {
		out = append(out, map[string]string{
			"name":   previewText(it.Name, 60),
			"slug":   it.Slug,
			"type":   it.Type,
			"chunks": fmt.Sprintf("%d", len(it.SourceChunks)),
		})
	}
	return out
}

// wikiLinkRE matches `[[slug]]` and `[[slug|display text]]` references
// inside wiki page content. The slug capture group rejects whitespace and
// the closing-bracket / pipe characters so we don't accidentally swallow
// adjacent text. Display text (group 2) is optional.
var wikiLinkRE = regexp.MustCompile(`\[\[([^\[\]\|\s]+)(?:\|([^\]]+))?\]\]`)
