package pipeline

import (
	"context"
	"fmt"
	types "github.com/JieWaZi/neux-knowledge/internal/domain"
	"github.com/JieWaZi/neux-knowledge/internal/wiki"
	"log/slog"
	"sort"
	"strings"
)

func writeDedupCandidateGroup(
	buf *strings.Builder, item extractedItem, itemType string, candidates []*types.WikiPageLite,
) {
	fmt.Fprintf(buf, "  <item slug=%q type=%q>\n", item.Slug, itemType)
	fmt.Fprintf(buf, "    <name>%s</name>\n", xmlEscape(item.Name))
	for _, alias := range item.Aliases {
		if alias == "" {
			continue
		}
		fmt.Fprintf(buf, "    <alias>%s</alias>\n", xmlEscape(alias))
	}
	buf.WriteString("    <candidates>\n")
	for _, p := range candidates {
		if p == nil {
			continue
		}
		fmt.Fprintf(buf, "      <page slug=%q type=%q>\n", p.Slug, p.PageType)
		fmt.Fprintf(buf, "        <name>%s</name>\n", xmlEscape(p.Title))
		for _, alias := range []string(p.Aliases) {
			if alias == "" {
				continue
			}
			fmt.Fprintf(buf, "        <alias>%s</alias>\n", xmlEscape(alias))
		}
		buf.WriteString("      </page>\n")
	}
	buf.WriteString("    </candidates>\n")
	buf.WriteString("  </item>\n")
}

// xmlEscape escapes the minimal set of characters that can break XML text
// content. Slugs are ASCII-only so they don't need escaping when used as
// attribute values.
func xmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

func (s *liteService) deduplicateExtractedBatch(
	ctx context.Context, sourceID string,
	entities, concepts []extractedItem,
	batchCtx *WikiBatchContext,
) ([]extractedItem, []extractedItem) {
	if len(entities) == 0 && len(concepts) == 0 {
		return entities, concepts
	}
	kbID := s.job.LibraryID

	// Build the candidate set: for each new item, ask the repo for
	// the top-K trigram-similar pages and union the results. Dedup by
	// slug as we go so the prompt only carries each candidate once.
	//
	// itemCandidates additionally records, per new item, the slugs that
	// surfaced for THAT item specifically. The prompt only ever sees the
	// flattened union, so validMerge below uses this per-item scoping to
	// reject a merge whose target was pulled in for a *different* item —
	// the class of hallucination the union otherwise enables (e.g. weak
	// models emitting entity/tencent-open → entity/hiring-agent, which
	// share no trigram signal and were never candidates for each other).
	candidatePages := make(map[string]*types.WikiPageLite)
	itemCandidates := make(map[string]map[string]bool)
	probe := func(item extractedItem) {
		queries := make([]string, 0, 1+len(item.Aliases))
		if item.Name != "" {
			queries = append(queries, item.Name)
		}
		for _, alias := range item.Aliases {
			if alias != "" {
				queries = append(queries, alias)
			}
		}
		own := itemCandidates[item.Slug]
		if own == nil {
			own = make(map[string]bool)
			itemCandidates[item.Slug] = own
		}
		for _, q := range queries {
			pages, err := s.findSimilarPages(ctx, kbID, q,
				[]string{types.WikiPageTypeEntity, types.WikiPageTypeConcept},
				dedupCandidateTopK)
			if err != nil {
				logWarnf(ctx, "wiki ingest: dedup FindSimilarPages(%q) failed: %v", q, err)
				continue
			}
			for _, p := range pages {
				if p == nil || p.Slug == "" {
					continue
				}
				if _, ok := candidatePages[p.Slug]; !ok {
					candidatePages[p.Slug] = p
				}
				own[p.Slug] = true
			}
		}
	}
	for _, e := range entities {
		probe(e)
	}
	for _, c := range concepts {
		probe(c)
	}
	s.attachExactIdentityPages(ctx, kbID, types.WikiPageTypeEntity, entities, candidatePages, itemCandidates, batchCtx)
	s.attachExactIdentityPages(ctx, kbID, types.WikiPageTypeConcept, concepts, candidatePages, itemCandidates, batchCtx)

	// Resolve exact same-type, same-title candidates deterministically before
	// asking the model about semantic/alias variants. Besides avoiding an LLM
	// call for the obvious case, this makes an already-materialized page
	// authoritative for the identity reservation below.
	exactTargets := make(map[string]string)
	mergeTargets := make(map[string]string)
	collectExactIdentityTargets(entities, types.WikiPageTypeEntity, itemCandidates, candidatePages, exactTargets)
	collectExactIdentityTargets(concepts, types.WikiPageTypeConcept, itemCandidates, candidatePages, exactTargets)

	stabilize := func() ([]extractedItem, []extractedItem) {
		return s.stabilizeExtractedIdentities(
				ctx, kbID, types.WikiPageTypeEntity, entities, mergeTargets, exactTargets, batchCtx,
			), s.stabilizeExtractedIdentities(
				ctx, kbID, types.WikiPageTypeConcept, concepts, mergeTargets, exactTargets, batchCtx,
			)
	}

	if len(candidatePages) == 0 {
		// No similar existing pages — identity reservations still make
		// concurrent batches converge before they materialize new pages.
		logDebugf(ctx, "wiki ingest: no similar existing pages found for %d new items", len(entities)+len(concepts))
		return stabilize()
	}
	logDebugf(ctx, "wiki ingest: %d similar existing pages selected for %d new items",
		len(candidatePages), len(entities)+len(concepts))

	// Group each new item with ONLY the existing pages that surfaced for
	// its own similarity probe. Presenting the model two flat lists (all
	// new items × all candidates) invites cross-item mispairings — it has
	// no way to tell which candidate is relevant to which item, so a weak
	// model pairs unrelated slugs that merely coexist in the prompt. A
	// per-item shortlist turns dedup into a local yes/no decision against
	// a handful of genuinely-similar pages and makes cross-item pairings
	// structurally unnatural to express. Items with no candidate are
	// omitted entirely (they cannot merge and only add hallucination
	// surface + tokens).
	var candBuf strings.Builder
	groups := 0
	renderGroup := func(item extractedItem, itemType string) {
		if exactTargets[item.Slug] != "" {
			return
		}
		cset := itemCandidates[item.Slug]
		if len(cset) == 0 {
			return
		}
		slugs := make([]string, 0, len(cset))
		for slug := range cset {
			// Skip the item's own slug: an identically-slugged existing
			// page is a re-ingest/update, not a merge target.
			if slug == item.Slug {
				continue
			}
			if _, ok := candidatePages[slug]; ok {
				slugs = append(slugs, slug)
			}
		}
		if len(slugs) == 0 {
			return
		}
		sort.Strings(slugs)
		pages := make([]*types.WikiPageLite, 0, len(slugs))
		for _, slug := range slugs {
			pages = append(pages, candidatePages[slug])
		}
		writeDedupCandidateGroup(&candBuf, item, itemType, pages)
		groups++
	}
	for _, item := range entities {
		renderGroup(item, "entity")
	}
	for _, item := range concepts {
		renderGroup(item, "concept")
	}
	if groups == 0 {
		// Every item was resolved exactly or has no safe semantic candidate.
		return stabilize()
	}

	var dedupeResult struct {
		Merges map[string]string `json:"merges"` // 候选地址到已确认同一知识地址的合并映射。
	}
	if err := s.generateJSON(ctx, "dedup", sourceID, []string{sourceID}, wiki.WikiDeduplicationPrompt, map[string]string{"Candidates": candBuf.String()}, &dedupeResult); err != nil {
		logWarnf(ctx, "wiki ingest: deduplication failed: %v", err)
		return stabilize()
	}

	validMerge := func(srcSlug, dstSlug string) bool {
		if reason := dedupMergeRejectReason(srcSlug, dstSlug, itemCandidates[srcSlug]); reason != "" {
			logWarnf(ctx, "wiki ingest: dedup rejected %s → %s (%s)", srcSlug, dstSlug, reason)
			return false
		}
		return true
	}

	for _, item := range entities {
		if exactTargets[item.Slug] != "" {
			continue
		}
		if existingSlug, ok := dedupeResult.Merges[item.Slug]; ok && validMerge(item.Slug, existingSlug) {
			logDebugf(ctx, "wiki ingest: dedup merge %s → %s", item.Slug, existingSlug)
			mergeTargets[item.Slug] = existingSlug
		}
	}
	for _, item := range concepts {
		if exactTargets[item.Slug] != "" {
			continue
		}
		if existingSlug, ok := dedupeResult.Merges[item.Slug]; ok && validMerge(item.Slug, existingSlug) {
			logDebugf(ctx, "wiki ingest: dedup merge %s → %s", item.Slug, existingSlug)
			mergeTargets[item.Slug] = existingSlug
		}
	}

	return stabilize()
}

func (s *liteService) findSimilarPages(ctx context.Context, kbID, query string, _ []string, _ int) ([]*types.WikiPageLite, error) {
	return s.store.FindSimilarWikiPages(ctx, kbID, query)
}
func logDebugf(ctx context.Context, format string, args ...any) {
	slog.DebugContext(ctx, fmt.Sprintf(format, args...))
}
