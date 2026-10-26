package pipeline

import (
	"context"
	"fmt"
	types "github.com/JieWaZi/neux-knowledge/internal/domain"
	"github.com/JieWaZi/neux-knowledge/internal/wiki"
	"strings"
	"sync"
	"unicode/utf8"
)

type wikiIngestPageRef struct {
	Slug  string // Knowledge page slugs actually touched by this document.
	Title string // Page display name.
}
type upstreamDocResult struct {
	KnowledgeID string              // Stable document identity.
	DocTitle    string              // Actual document title.
	Summary     string              // One-sentence document summary.
	Pages       []wikiIngestPageRef // Pages touched by this document.
	MapStats    map[string]any      // Actual extraction statistics.
}
type WikiBatchContext struct {
	identityClaims  *sync.Map                                         // Knowledge identity reservations shared by concurrent Map operations in this batch.
	identityPages   *sync.Map                                         // Batch cache for exact name lookups.
	SlugTitleMany   func(context.Context, []string) map[string]string // Actual titles of existing links.
	PlannedFolderID map[string]string                                 // Actual row identities for batch folder planning.

	ExtractionGranularity       types.WikiExtractionGranularity      // Extraction granularity for this batch.
	ContentInstructions         string                               // Library content rules.
	ExtractionInstructions      string                               // Library extraction rules.
	SummaryContentByKnowledgeID func(context.Context, string) string // Old summary for each stable source identity.
}

func (s *liteService) mapOneDocument(ctx context.Context, source *types.SourceVersion, artifact ReadArtifact, pages []*types.WikiPage) (*upstreamDocResult, []SlugUpdate, error) {
	batchCtx := s.newBatchContext(pages)
	knowledgeID := source.SourceID
	lang := language(s.library.WikiLanguage)

	// Open a postprocess.wiki subspan under the parent attempt's
	// postprocess stage so the actual per-doc work (LLM extraction +
	// summary + classification) shows up in the trace tree. Returns
	// nil when the parent attempt is gone (no panic on missing
	// lookups — span tracker is best-effort).

	// Guard against the ingest/delete race: if the user deleted the doc while
	// this task was queued (wikiIngestDelay = 30s) or while an earlier stage
	// was in flight, we must NOT proceed to LLM extraction — doing so would
	// create wiki pages whose source_refs point at a ghost knowledge ID,
	// permanently unreachable via wiki_read_source_doc.
	if err := s.store.ValidateOrganizeSource(ctx, s.job.ID, s.job.Attempt, source.ID); err != nil {
		return nil, nil, err
	}
	chunks := artifact.Blocks
	if len(chunks) == 0 {
		return nil, nil, nil
	}
	var err error
	fullContent := reconstructContent(chunks)
	content := fullContent
	if len([]rune(content)) > maxContentForWiki {
		content = string([]rune(content)[:maxContentForWiki])
	}

	// Refuse to run LLM-based extraction when the document carries no real
	// text — e.g. a scanned PDF whose pages were converted to images but where
	// VLM OCR produced nothing usable. Without this guard the LLM would have
	// only image markup left and would happily fabricate entities/concepts.
	if !hasSufficientTextContent(content) {
		return nil, nil, nil
	}

	docTitle := source.Filename
	// Citation source reference. We deliberately use only the knowledge ID
	// (not docTitle, which is typically the upload filename) so the filename
	// does not leak into citation strings that downstream LLM prompts may
	// surface during wiki page editing.
	sourceRef := knowledgeID
	oldPageSlugs := existingSourcePageSlugs(pages, source.SourceID)

	// Pass 0: lightweight candidate slug extraction (skeleton only).
	// On failure we fall back to the legacy single-shot extractor so the doc
	// still gets ingested, just without chunk-level citations.
	var (
		extractedEntities []extractedItem
		extractedConcepts []extractedItem
		slugItems         map[string]extractedItem
		pass0Failed       bool
	)
	extractedEntities, extractedConcepts, slugItems, err = s.extractUpstream(ctx, source, content, lang, oldPageSlugs, pages, batchCtx, false)
	if err != nil {
		pass0Failed = true
		extractedEntities, extractedConcepts, slugItems, err = s.extractUpstream(ctx, source, content, lang, oldPageSlugs, pages, batchCtx, true)
		if err != nil {
			return nil, nil, err
		}
	}

	// Build slug listing for Summary's wiki-link input.
	var summaryExtractedPages []string
	for slug := range slugItems {
		summaryExtractedPages = append(summaryExtractedPages, slug)
	}
	// Wiki summary slug is derived from the knowledge ID rather than the
	// docTitle (which is typically the upload filename). Filename-based slugs
	// like "summary/mx5280-pdf" expose the filename in cross-link contexts
	// that downstream LLM prompts read; a UUID-based slug is uglier but
	// hallucination-safe.
	summarySlug := fmt.Sprintf("summary/%s", slugify(knowledgeID))
	var slugListing string
	for _, slug := range summaryExtractedPages {
		if item, ok := slugItems[slug]; ok {
			aliases := ""
			if len(item.Aliases) > 0 {
				aliases = fmt.Sprintf(" (Aliases: %s)", strings.Join(item.Aliases, ", "))
			}
			slugListing += fmt.Sprintf("- [[%s]] = %s%s\n", slug, item.Name, aliases)
		} else {
			slugListing += fmt.Sprintf("- [[%s]]\n", slug)
		}
	}

	// Summary and chunk classification are independent given Pass 0 output —
	// run them in parallel. Summary handles wiki-link injection; classification
	// attaches concrete chunk IDs to each candidate slug.
	var (
		summaryContent string
		summaryErr     error
		citations      map[string][]string
		newSlugs       []newSlugFromCitation
		batchCount     int
	)

	// Both calls run in parallel goroutines under the same wikiSpan
	// parent — their subspans will visually overlap in the trace view,
	// which correctly reflects their wall-clock concurrency.
	if !pass0Failed {
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		summaryContent, summaryErr = s.generate(ctx, "summary", source.ID, []string{source.ID}, wiki.WikiSummaryPrompt, map[string]string{
			"Content":            wikiSummarySourceInput(fullContent),
			"Language":           lang,
			"ExtractedSlugs":     slugListing,
			"CustomInstructions": batchCtx.ContentInstructions,
			"InstructionScope":   "wiki_content",
		})
		if summaryErr != nil {
		} else {
		}
	}()
	go func() {
		defer wg.Done()
		// Skip citation pass when Pass 0 has fallen back to the legacy path —
		// the legacy output already contains paraphrased Details, so chunk
		// citations would be redundant and we'd spend LLM calls for nothing.
		if pass0Failed {
			citations = map[string][]string{}
			return
		}
		candidatesXML := renderCandidateSlugsXML(extractedEntities, extractedConcepts)
		citations, newSlugs, batchCount = s.classifyUpstream(ctx, source.ID, candidatesXML, chunks, lang)
	}()
	wg.Wait()

	// Merge citations back into the item structs (non-failing; items without
	// citations simply keep their Description+Details fallback).
	var uncited int
	extractedEntities, extractedConcepts, uncited = mergeCitationsIntoItems(extractedEntities, extractedConcepts, citations, newSlugs)
	extractedEntities, extractedConcepts = s.reclaimExtractedIdentities(ctx, s.job.LibraryID, extractedEntities, extractedConcepts, batchCtx)

	// Rebuild slugItems so stale entries (for slugs that did not survive the
	// merge) and brand-new slugs discovered by the citation pass are both
	// reflected in summaryExtractedPages tracking.
	slugItems = make(map[string]extractedItem, len(extractedEntities)+len(extractedConcepts))
	for _, item := range extractedEntities {
		if item.Slug != "" && item.Name != "" {
			slugItems[item.Slug] = item
		}
	}
	for _, item := range extractedConcepts {
		if item.Slug != "" && item.Name != "" {
			slugItems[item.Slug] = item
		}
	}

	// extractedPages records every wiki page this document materialized
	// (entities, concepts, plus the summary page appended below). The
	// slug is used for link/retract bookkeeping; the title is retained for
	// trace output and finalize processing.
	extractedPages := make([]wikiIngestPageRef, 0, len(slugItems)+1)
	for slug, item := range slugItems {
		title := item.Name
		if title == "" {
			title = slug
		}
		extractedPages = append(extractedPages, wikiIngestPageRef{Slug: slug, Title: title})
	}

	// Count total distinct chunks cited across all slugs for logging.
	citedChunkSet := make(map[string]bool)
	for _, ids := range citations {
		for _, id := range ids {
			citedChunkSet[id] = true
		}
	}

	var updates []SlugUpdate
	// docSummaryLine is the one-sentence headline used for terse log/audit
	// previews and for <document_added> blocks in retract prompts.
	// docSummary is the full summary body attached to each entity/concept
	// update so the editor model gets rich framing in <source_context>.
	var docSummaryLine string
	var docSummary string

	if summaryErr != nil {
		// Summary is the headline artifact of an ingested document — a
		// document with no summary page is half-ingested and leaves the
		// entity/concept updates hanging without a root to link back to
		// from the index. Historically we just logged and moved on,
		// which meant a single transient 504 permanently dropped the
		// summary page for that document.
		//
		// Returning an error here sends the op to failedOps (see the
		// map-phase loop in ProcessWikiIngest), which requeueFailedOps
		// appends back onto the pending list so the next batch retries.
		// The internal retries in generateWithTemplate already exhaust
		// the LLM's own transient-error budget before we give up here.
		return nil, nil, fmt.Errorf("generate summary: %w", summaryErr)
	}
	sumLine, sumBody := splitSummaryLine(summaryContent)
	if sumBody == "" {
		sumBody = summaryContent
	}
	if sumLine == "" {
		sumLine = docTitle
	}
	docSummaryLine = sumLine
	docSummary = sumBody
	if strings.TrimSpace(docSummary) == "" {
		docSummary = sumLine
	}
	updates = append(updates, SlugUpdate{
		Slug:        summarySlug,
		Type:        types.WikiPageTypeSummary,
		DocTitle:    docTitle,
		KnowledgeID: knowledgeID,
		SourceRef:   sourceRef,
		Language:    lang,
		SummaryLine: sumLine,
		SummaryBody: sumBody,
	})
	extractedPages = append(extractedPages, wikiIngestPageRef{Slug: summarySlug, Title: docTitle})

	// Entities
	for _, item := range extractedEntities {
		if item.Slug != "" {
			updates = append(updates, SlugUpdate{
				Slug:         item.Slug,
				Type:         types.WikiPageTypeEntity,
				Item:         item,
				DocTitle:     docTitle,
				KnowledgeID:  knowledgeID,
				SourceRef:    sourceRef,
				Language:     lang,
				SourceChunks: item.SourceChunks,
				DocSummary:   docSummary,
			})
		}
	}

	// Concepts
	for _, item := range extractedConcepts {
		if item.Slug != "" {
			updates = append(updates, SlugUpdate{
				Slug:         item.Slug,
				Type:         types.WikiPageTypeConcept,
				Item:         item,
				DocTitle:     docTitle,
				KnowledgeID:  knowledgeID,
				SourceRef:    sourceRef,
				Language:     lang,
				SourceChunks: item.SourceChunks,
				DocSummary:   docSummary,
			})
		}
	}

	// Reconcile old page set against new extraction.
	//
	// Three cases:
	//
	//  (a) oldSlug ∉ new  → "retractStale": the doc no longer mentions this
	//      page's subject, so strip its ref (and possibly delete the page
	//      if this was the only source). Passes the NEW content as the
	//      retract context — if the LLM finds matching facts it trims
	//      them, otherwise the retract is a near no-op, which is fine.
	//
	//  (b) oldSlug ∈ new AND slug is an entity/concept page  → reparse
	//      swap: emit BOTH a "retract" (carrying the doc's PRIOR summary
	//      body as the old-version signal) AND the normal addition. The
	//      reduce stage sees HasAdditions=1 + HasRetractions=1 and the
	//      WikiPageModifyUserPrompt correctly tells the editor model to
	//      remove the old K section and add the new K section in one
	//      pass — giving us replace-not-append semantics that "append
	//      new K on top of old K" would otherwise violate.
	//
	//  (c) oldSlug ∈ new AND slug is a summary page (summary/...) →
	//      nothing to do here. reduceSlugUpdates' summary branch
	//      unconditionally overwrites the whole page from the new
	//      SummaryBody, so emitting an extra retract would just be
	//      dead weight that the summary branch discards anyway.
	//
	// priorContribution is the doc's LAST summary body, fetched lazily
	// at this point (rather than pre-loaded into the batch context).
	// Empty on first-ever ingest — in that case oldPageSlugs is also
	// empty, so we never consult it.
	priorContribution := batchCtx.SummaryContentByKnowledgeID(ctx, knowledgeID)

	newSlugSet := make(map[string]bool, len(extractedPages))
	for _, ns := range extractedPages {
		newSlugSet[ns.Slug] = true
	}

	var reparseOverlap, staleCount int
	for oldSlug := range oldPageSlugs {
		if newSlugSet[oldSlug] {
			// Skip summary slugs — they're overwritten wholesale by the
			// summary update, retract would be ignored downstream.
			if strings.HasPrefix(oldSlug, "summary/") {
				continue
			}
			reparseOverlap++
			updates = append(updates, SlugUpdate{
				Slug:              oldSlug,
				Type:              "retract",
				RetractDocContent: priorContribution,
				DocTitle:          docTitle,
				KnowledgeID:       knowledgeID,
				Language:          lang,
			})
			continue
		}
		staleCount++
		updates = append(updates, SlugUpdate{
			Slug:              oldSlug,
			Type:              "retractStale",
			RetractDocContent: content,
			DocTitle:          docTitle,
			KnowledgeID:       knowledgeID,
			Language:          lang,
		})
	}

	// Map-phase metrics get attached to the postprocess.wiki span's
	// output, but we do NOT EndSpan here — the batch driver keeps the
	// span open through reduce + index rebuild + cross-link injection
	// + page publish, then closes it once this doc's pages have all
	// been written. That way the span's duration reflects the full
	// "wiki processing for this knowledge" time the user sees in the
	// trace viewer, not just the LLM extraction slice.
	mapStats := map[string]any{
		"doc_title":        previewText(docTitle, 120),
		"chunks":           len(chunks),
		"candidate_slugs":  len(slugItems),
		"cited_chunks":     len(citedChunkSet),
		"uncited_slugs":    uncited,
		"new_slugs":        len(newSlugs),
		"updates":          len(updates),
		"reparse_slugs":    reparseOverlap,
		"stale_slugs":      staleCount,
		"extracted_pages":  len(extractedPages),
		"summary_chars":    utf8.RuneCountInString(docSummary),
		"pass0_fallback":   pass0Failed,
		"classify_batches": batchCount,
		"summary_preview":  previewText(docSummaryLine, 160),
	}

	return &upstreamDocResult{
		KnowledgeID: knowledgeID,
		DocTitle:    docTitle,
		Summary:     docSummaryLine,
		Pages:       extractedPages,
		MapStats:    mapStats,
	}, updates, nil
}

func (s *liteService) newBatchContext(pages []*types.WikiPage) *WikiBatchContext {
	return &WikiBatchContext{identityClaims: &s.identityClaims, identityPages: &s.identityPages, SlugTitleMany: func(_ context.Context, slugs []string) map[string]string {
		result := map[string]string{}
		for _, slug := range slugs {
			for _, page := range pages {
				if page.Slug == slug && page.Status != "archived" && page.PageType != "index" {
					result[slug] = page.Title
				}
			}
		}
		return result
	}, ExtractionGranularity: s.granularity(), ContentInstructions: s.contentInstructions(), ExtractionInstructions: s.extractionInstructions(), SummaryContentByKnowledgeID: func(_ context.Context, id string) string {
		for _, page := range pages {
			if page.PageType != "summary" || page.Status == "archived" {
				continue
			}
			for _, ref := range page.Evidence.Sources {
				if ref.SourceID == id {
					return page.Content
				}
			}
		}
		return ""
	}}
}
func existingSourcePageSlugs(pages []*types.WikiPage, sourceID string) map[string]bool {
	result := map[string]bool{}
	for _, page := range pages {
		if page.Status == "archived" {
			continue
		}
		for _, ref := range page.Evidence.Sources {
			if ref.SourceID == sourceID {
				result[page.Slug] = true
				break
			}
		}
	}
	return result
}
func (s *liteService) extractUpstream(ctx context.Context, source *types.SourceVersion, content, lang string, oldPageSlugs map[string]bool, pages []*types.WikiPage, batchCtx *WikiBatchContext, legacy bool) ([]extractedItem, []extractedItem, map[string]extractedItem, error) {
	var prevSlugsText string
	if len(oldPageSlugs) > 0 {
		var sb strings.Builder
		for _, slug := range sortedKeys(oldPageSlugs) {
			if !strings.HasPrefix(slug, "entity/") && !strings.HasPrefix(slug, "concept/") {
				continue
			}
			fmt.Fprintf(&sb, "- %s\n", slug)
		}
		prevSlugsText = sb.String()
	}
	if prevSlugsText == "" {
		prevSlugsText = "(none — this is a new document)"
	}
	granularity := batchCtx.ExtractionGranularity.Normalize()
	prompt := wiki.WikiCandidateSlugPrompt
	if legacy {
		prompt = wiki.WikiKnowledgeExtractPrompt
	}
	var result combinedExtraction
	err := s.generateJSON(ctx, "map", source.ID, []string{source.ID}, prompt, map[string]string{"Content": content, "Language": lang, "PreviousSlugs": prevSlugsText, "Granularity": string(granularity), "GranularityGuidance": wiki.WikiGranularityGuidance(string(granularity)), "CustomInstructions": batchCtx.ExtractionInstructions, "InstructionScope": "wiki_extraction"}, &result)
	if err != nil {
		return nil, nil, nil, err
	}
	result.Entities, result.Concepts = s.deduplicateExtractedBatch(ctx, source.ID, result.Entities, result.Concepts, batchCtx)
	slugItems := map[string]extractedItem{}
	for _, item := range result.Entities {
		if item.Slug != "" && item.Name != "" {
			slugItems[item.Slug] = item
		}
	}
	for _, item := range result.Concepts {
		if item.Slug != "" && item.Name != "" {
			slugItems[item.Slug] = item
		}
	}
	return result.Entities, result.Concepts, slugItems, nil
}
