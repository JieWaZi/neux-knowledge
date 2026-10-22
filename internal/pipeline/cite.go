package pipeline

import (
	"fmt"
	"github.com/JieWaZi/neux-knowledge/internal/modelcontext"
	"sort"
	"strings"
)

func splitChunksIntoCitationBatches(chunks []*Chunk) []chunkBatch {
	// Only cite text chunks — image/ocr chunks are already merged into the
	// text content via reconstructEnrichedContent and the LLM doesn't see
	// them as standalone units.
	filtered := make([]*Chunk, 0, len(chunks))
	for _, c := range chunks {
		if c == nil || c.Content == "" {
			continue
		}
		if c.ChunkType != "text" && c.ChunkType != "" {
			continue
		}
		filtered = append(filtered, c)
	}
	if len(filtered) == 0 {
		return nil
	}

	// Preserve document order for human-readable citation
	sort.Slice(filtered, func(i, j int) bool {
		if filtered[i].ChunkIndex == filtered[j].ChunkIndex {
			return filtered[i].StartAt < filtered[j].StartAt
		}
		return filtered[i].ChunkIndex < filtered[j].ChunkIndex
	})

	var batches []chunkBatch
	newBatch := func() chunkBatch {
		return chunkBatch{handles: modelcontext.NewHandleTable("c", 3, 0)}
	}
	current := newBatch()

	flush := func() {
		if len(current.chunks) == 0 {
			return
		}
		batches = append(batches, current)
		current = newBatch()
	}

	for _, c := range filtered {
		runeLen := len([]rune(c.Content))
		// If adding this chunk would exceed the budget and the current batch
		// isn't empty, flush first so the oversized chunk is not merged with
		// already-queued ones. Oversized chunks still go through — they just
		// get their own batch.
		if len(current.chunks) > 0 && current.totalRuneLen+runeLen > maxRunesPerCitationBatch {
			flush()
		}

		current.handles.Register(c.ID)
		current.chunks = append(current.chunks, c)
		current.totalRuneLen += runeLen
	}
	flush()

	return batches
}

func renderCandidateSlugsXML(entities, concepts []extractedItem) string {
	var sb strings.Builder
	write := func(item extractedItem, kind string) {
		aliases := ""
		if len(item.Aliases) > 0 {
			aliases = fmt.Sprintf(" aliases=%q", strings.Join(item.Aliases, ", "))
		}
		fmt.Fprintf(&sb, "- slug: %s, type: %s, name: %q%s, description: %s\n",
			item.Slug, kind, item.Name, aliases, item.Description)
	}
	for _, item := range entities {
		if item.Slug == "" || item.Name == "" {
			continue
		}
		write(item, "entity")
	}
	for _, item := range concepts {
		if item.Slug == "" || item.Name == "" {
			continue
		}
		write(item, "concept")
	}
	return sb.String()
}

func renderChunksXML(batch chunkBatch) string {
	var sb strings.Builder
	for _, c := range batch.chunks {
		handle, _ := batch.handles.Handle(c.ID)
		fmt.Fprintf(&sb, "<c id=%q index=\"%d\">\n%s\n</c>\n", handle, c.ChunkIndex, c.Content)
	}
	return sb.String()
}

func collectCitedChunkContent(chunkIDs []string, contentByID map[string]string) string {
	if len(chunkIDs) == 0 || len(contentByID) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, id := range chunkIDs {
		content, ok := contentByID[id]
		if !ok || strings.TrimSpace(content) == "" {
			continue
		}
		if sb.Len() > 0 {
			sb.WriteString("\n\n")
		}
		sb.WriteString(content)
	}
	return sb.String()
}

func mergeCitationsIntoItems(
	entities, concepts []extractedItem,
	citations map[string][]string,
	newSlugs []newSlugFromCitation,
) ([]extractedItem, []extractedItem, int) {
	uncited := 0

	for i := range entities {
		ids := citations[entities[i].Slug]
		entities[i].SourceChunks = ids
		if len(ids) == 0 {
			uncited++
		}
	}
	for i := range concepts {
		ids := citations[concepts[i].Slug]
		concepts[i].SourceChunks = ids
		if len(ids) == 0 {
			uncited++
		}
	}

	// Append new_slugs discovered by the citation pass, avoiding duplicates.
	existingSlugs := make(map[string]bool, len(entities)+len(concepts))
	for _, e := range entities {
		existingSlugs[e.Slug] = true
	}
	for _, c := range concepts {
		existingSlugs[c.Slug] = true
	}

	// Aggregate per-slug across batches so the same "newly discovered" slug
	// surfacing in multiple batches merges into a single item.
	type mergedNew struct {
		item extractedItem // Candidate merged by the same slug.
		typ  string        // Type of the merged candidate.
	}
	merged := make(map[string]*mergedNew)
	slugOrder := []string{}

	for _, ns := range newSlugs {
		if ns.Slug == "" || ns.Name == "" {
			continue
		}
		if existingSlugs[ns.Slug] {
			// Treat as citation for existing candidate
			continue
		}
		kind := strings.TrimSpace(strings.ToLower(ns.Type))
		if kind == "" {
			if strings.HasPrefix(ns.Slug, "concept/") {
				kind = "concept"
			} else {
				kind = "entity"
			}
		}
		existing, ok := merged[ns.Slug]
		if !ok {
			existing = &mergedNew{
				item: extractedItem{
					Name:         ns.Name,
					Slug:         ns.Slug,
					Aliases:      append([]string(nil), ns.Aliases...),
					Description:  ns.Description,
					Details:      ns.Details,
					SourceChunks: append([]string(nil), ns.SourceChunks...),
				},
				typ: kind,
			}
			merged[ns.Slug] = existing
			slugOrder = append(slugOrder, ns.Slug)
			continue
		}
		// Union source chunks
		seen := make(map[string]bool, len(existing.item.SourceChunks))
		for _, id := range existing.item.SourceChunks {
			seen[id] = true
		}
		for _, id := range ns.SourceChunks {
			if !seen[id] {
				existing.item.SourceChunks = append(existing.item.SourceChunks, id)
				seen[id] = true
			}
		}
	}

	for _, slug := range slugOrder {
		m := merged[slug]
		if m.typ == "concept" {
			concepts = append(concepts, m.item)
		} else {
			entities = append(entities, m.item)
		}
	}

	return entities, concepts, uncited
}
