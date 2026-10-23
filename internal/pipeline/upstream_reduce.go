package pipeline

import (
	"context"
	"errors"
	"fmt"
	types "github.com/JieWaZi/neux-knowledge/internal/domain"
	"github.com/JieWaZi/neux-knowledge/internal/wiki"
	"github.com/google/uuid"
	"log/slog"
	"regexp"
	"sort"
	"strings"
)

func (s *upstreamReducer) reduceSlugUpdates(ctx context.Context, kbID, slug string, updates []SlugUpdate, batchCtx *WikiBatchContext) (changed bool, affectedType string, additionFailed bool, updateDeferred bool, err error) {
	// Final safety net for the ingest/delete race: between Map (which already
	// checks isKnowledgeGone) and Reduce there is a long LLM call where the
	// source document may be deleted. Drop any addition/summary updates whose
	// knowledge no longer exists so we don't resurrect a ghost source_ref.
	// Retract updates are kept — they actively remove refs, which is what we
	// want when the doc is gone.
	updates, err = s.filterLiveUpdates(ctx, kbID, updates)
	if err != nil {
		return false, "", false, false, err
	}
	if len(updates) == 0 {
		return false, "", false, false, nil
	}

	var page *types.WikiPage
	page, err = s.GetPageBySlug(ctx, kbID, slug)
	exists := (err == nil && page != nil)

	if !exists {
		hasAdditions := false
		for _, u := range updates {
			if u.Type == types.WikiPageTypeEntity || u.Type == types.WikiPageTypeConcept || u.Type == "summary" {
				hasAdditions = true
				break
			}
		}
		if !hasAdditions {
			return false, "", false, false, nil
		}

		page = &types.WikiPage{
			ID:              uuid.New().String(),
			TenantID:        0,
			KnowledgeBaseID: kbID,
			Slug:            slug,
			Status:          types.WikiPageStatusDraft,
			SourceRefs:      types.StringArray{},
			Aliases:         types.StringArray{},
		}
		// Reset err: GetPageBySlug returned "not found" which we just
		// handled by synthesizing the page. Don't leak that error to
		// the named return — subsequent assignments would mask it
		// anyway, but be explicit.
		err = nil
	}

	affectedType = "ingest"

	var summaryUpdate *SlugUpdate
	var retracts []SlugUpdate
	var additions []SlugUpdate

	for i, u := range updates {
		if u.Type == "summary" {
			summaryUpdate = &updates[i]
		} else if u.Type == "retract" || u.Type == "retractStale" {
			retracts = append(retracts, u)
			affectedType = "retract"
		} else if u.Type == types.WikiPageTypeEntity || u.Type == types.WikiPageTypeConcept {
			additions = append(additions, u)
			affectedType = "ingest" // Additions override retracts type
		}
	}

	if summaryUpdate != nil {
		page.Title = summaryUpdate.DocTitle + " - Summary"
		page.Content = summaryUpdate.SummaryBody
		page.Summary = summaryUpdate.SummaryLine
		page.PageType = types.WikiPageTypeSummary
		page.SourceRefs = appendUniqueString(page.SourceRefs, summaryUpdate.SourceRef)
		// Summary pages don't carry chunk-level citations (they are document-
		// level synopses generated from the whole content). Clear any stale
		// chunk refs that may remain if this slug was once an entity page
		// and got converted to a summary page.
		page.ChunkRefs = types.StringArray{}
		changed = true

		if exists {
			// A summary rewrite can drop table rows too, and the refusal has
			// the same meaning here: the page kept its previous body, so this
			// slug's contribution never landed and its documents must survive
			// the trim phase. Classify the explicit signal; do not read it off
			// the page version.
			_, writeErr := s.UpdatePage(ctx, page)
			updateDeferred, err = classifyWikiPageWrite(writeErr)
			if updateDeferred {
				err = writeErr
			}
		} else {
			_, err = s.CreatePage(ctx, page)
		}
		return changed, affectedType, false, updateDeferred, err
	}

	var remainingSourcesContent strings.Builder
	var deletedContent strings.Builder
	var relatedSlugs strings.Builder
	var newContentBuilder strings.Builder
	var sharedSourceContexts strings.Builder
	var docTitles []string

	language := resolveSlugUpdateLanguage(ctx, updates)

	if len(retracts) > 0 {
		for _, r := range retracts {
			fmt.Fprintf(&deletedContent, "<document>\n<title>%s</title>\n<content>\n%s\n</content>\n</document>\n\n", r.DocTitle, r.RetractDocContent)
		}

		retractKIDs := make(map[string]bool)
		for _, r := range retracts {
			retractKIDs[r.KnowledgeID] = true
		}

		for _, ref := range page.SourceRefs {
			refKnowledgeID, refTitle := types.ParseWikiSourceRef(ref)
			if refKnowledgeID == "" {
				continue
			}
			if refTitle == "" {
				// Legacy bare refs carry no title; the ID is the only label
				// available for the retract prompt.
				refTitle = refKnowledgeID
			}

			if retractKIDs[refKnowledgeID] {
				continue
			}

			if content := batchCtx.SummaryContentByKnowledgeID(ctx, refKnowledgeID); content != "" {
				fmt.Fprintf(&remainingSourcesContent, "<document>\n<title>%s</title>\n<content>\n%s\n</content>\n</document>\n\n", refTitle, content)
			} else {
				fmt.Fprintf(&remainingSourcesContent, "<document>\n<title>%s</title>\n<content>\n(summary not available)\n</content>\n</document>\n\n", refTitle)
			}
		}
		if remainingSourcesContent.Len() == 0 {
			remainingSourcesContent.WriteString("(no remaining sources)")
		}

		newRefs := types.StringArray{}
		for _, ref := range page.SourceRefs {
			if !retractKIDs[types.WikiSourceKnowledgeID(ref)] {
				newRefs = append(newRefs, ref)
			}
		}
		page.SourceRefs = newRefs
	}

	if len(additions) > 0 {
		// Resolve SourceChunks → chunk contents in a single batched query per
		// knowledge ID, so the <new_information> block can quote the chunks
		// verbatim instead of relying on the short Details paraphrase.
		chunkContentByID := s.resolveCitedChunks(additions)
		// A document summary is shared by every page derived from that document.
		// Render a deterministic, de-duplicated block before any page-specific
		// metadata so provider prefix caches can reuse it across parallel reduce
		// calls.
		sourceContextByRef := make(map[string]string)

		for _, add := range additions {
			cited := collectCitedChunkContent(add.SourceChunks, chunkContentByID)
			// Frame the chunks with the document-level summary body so the
			// editor model knows BOTH what the document is about AND what
			// kind of document it is (resume vs announcement vs product
			// page vs schedule). The one-sentence headline alone was too
			// terse to keep the editor grounded on longer or multi-topic
			// source documents, and calibrating tone (self-reported vs
			// third-party authoritative) benefits from the richer context.
			sourceCtx := strings.TrimSpace(add.DocSummary)
			if sourceCtx != "" {
				contextKey := add.SourceRef
				if contextKey == "" {
					contextKey = add.KnowledgeID + "\x00" + add.DocTitle
				}
				sourceContextByRef[contextKey] = fmt.Sprintf(
					"<document>\n<title>%s</title>\n<context>\n%s\n</context>\n</document>\n",
					add.DocTitle, sourceCtx,
				)
			}
			if cited != "" {
				fmt.Fprintf(&newContentBuilder,
					"<document>\n<title>%s</title>\n<content>\n**%s**: %s\n\n%s\n</content>\n</document>\n\n",
					add.DocTitle, add.Item.Name, add.Item.Description, cited)
			} else {
				// Fallback: no citations available (legacy path, citation pass
				// failed, or bad chunk IDs were filtered out) — stick with
				// the short Details summary so the page still gets real text.
				fmt.Fprintf(&newContentBuilder,
					"<document>\n<title>%s</title>\n<content>\n**%s**: %s\n\n%s\n</content>\n</document>\n\n",
					add.DocTitle, add.Item.Name, add.Item.Description, add.Item.Details)
			}
			docTitles = appendUniqueString(docTitles, add.DocTitle)

			for _, alias := range add.Item.Aliases {
				page.Aliases = appendUniqueString(page.Aliases, alias)
			}
			page.SourceRefs = appendUniqueString(page.SourceRefs, add.SourceRef)

			if page.Title == "" {
				page.Title = add.Item.Name
			}
			if page.PageType == "" {
				page.PageType = add.Type
			}
		}

		contextKeys := make([]string, 0, len(sourceContextByRef))
		for key := range sourceContextByRef {
			contextKeys = append(contextKeys, key)
		}
		sort.Strings(contextKeys)
		for _, key := range contextKeys {
			sharedSourceContexts.WriteString(sourceContextByRef[key])
		}
	}

	if len(additions) > 0 || len(retracts) > 0 {
		titles := batchCtx.SlugTitleMany(ctx, []string(page.OutLinks))

		// slugHandles escape high-entropy slugs behind short reference handles
		// (ref-1, ref-2, …) for the editor LLM, then translates them back to
		// real slugs after generation (see decodeContent below).
		slugHandles := wiki.NewSlugHandles()

		// Escape every out-link slug behind a request-local handle so the editor
		// never has to retype a real slug — this is where UUID-based summary
		// slugs (summary/<uuid>) got mangled into 404-ing links. Both the
		// <valid_wiki_links> listing AND the [[...]] refs already present in
		// <existing_page_content> use the SAME handle table, so the
		// model sees one consistent, copy-safe identifier space; we translate
		// the handles back to real slugs after generation.
		known := make(map[string]struct{}, len(page.OutLinks))
		for _, outSlug := range page.OutLinks {
			known[outSlug] = struct{}{}
			slugHandles.Handle(outSlug) // pre-assign for stable ordering
		}
		for _, outSlug := range page.OutLinks {
			if title := titles[outSlug]; title != "" {
				fmt.Fprintf(&relatedSlugs, "- %s (%s)\n", slugHandles.Handle(outSlug), title)
			}
		}

		// Older generated pages may still contain short chunk aliases such as
		// [c003]. They are internal ingest metadata; keep the editor context
		// clean so a subsequent update cannot copy them into rewritten prose.
		existingContent := stripWikiInlineChunkCitations(page.Content)
		existingContent = slugHandles.EncodeContent(existingContent, known)
		if !exists || existingContent == "" {
			existingContent = "(New page)"
		}

		hasAdditionsStr := ""
		if len(additions) > 0 {
			hasAdditionsStr = "1"
		}
		hasRetractionsStr := ""
		if len(retracts) > 0 {
			hasRetractionsStr = "1"
		}

		// Fall back gracefully if title/type are still unset (shouldn't happen
		// for well-formed updates — both get populated from `additions` above,
		// and retract-only paths require an existing page — but stay defensive
		// so we never feed the LLM an empty identity block).
		pageTitle := page.Title
		if pageTitle == "" {
			pageTitle = slug
		}
		pageType := string(page.PageType)
		if pageType == "" {
			pageType = "wiki page"
		}
		pageAliases := strings.Join(page.Aliases, ", ")

		var updatedContent string
		updatedContent, err = s.generate(ctx, "reduce", "", sourceIDsFor(updates), wiki.WikiPageModifyUserPrompt, map[string]string{
			"HasAdditions":            hasAdditionsStr,
			"HasRetractions":          hasRetractionsStr,
			"PageSlug":                slug,
			"PageTitle":               pageTitle,
			"PageType":                pageType,
			"PageAliases":             pageAliases,
			"ExistingContent":         existingContent,
			"SharedSourceContexts":    sharedSourceContexts.String(),
			"NewContent":              newContentBuilder.String(),
			"DeletedContent":          deletedContent.String(),
			"RemainingSourcesContent": remainingSourcesContent.String(),
			"AvailableSlugs":          relatedSlugs.String(),
			"Language":                language,
			"CustomInstructions":      batchCtx.ContentInstructions,
			"InstructionScope":        "wiki_content",
		})

		if err == nil && updatedContent != "" {
			// Translate request-local handles (ref-N) the model copied from the
			// encoded context back to their real slugs BEFORE the content is
			// parsed/stored, so out_links reflect real pages again.
			updatedContent = slugHandles.DecodeContent(updatedContent)
			updatedSummary, updatedBody := splitSummaryLine(updatedContent)
			if updatedBody != "" {
				page.Content = updatedBody
			} else {
				page.Content = updatedContent
			}
			if updatedSummary != "" {
				page.Summary = updatedSummary
			}
			changed = true
		} else if err != nil {
			logWarnf(ctx, "wiki ingest: update/retract failed for slug %s: %v", slug, err)
			// Flag addition failures so the batch can sanitize stale
			// [[slug]] references in the doc's summary page and keep the
			// missing page out of finalize processing.
			// Retract-only failures don't poison anything (they leave
			// the existing page unchanged), so don't flag those.
			if len(additions) > 0 {
				additionFailed = true
			}
			return false, affectedType, additionFailed, false, err
		}
	}

	// Apply the batch taxonomy plan, but only to pages that aren't already
	// filed — so brand-new pages get a coherent folder while previously-filed
	// or user-moved pages keep their placement (manual edits are authoritative).
	// The page's category_path cache is derived from folder_id downstream by
	// CreatePage/UpdatePage, so assigning the folder id is sufficient here.
	if page.FolderID == "" && batchCtx != nil {
		if fid := batchCtx.PlannedFolderID[slug]; fid != "" {
			page.FolderID = fid
		}
	}

	if changed {
		// Refresh chunk refs in-place on the page so they persist alongside
		// the rest of the row. Retract-only updates (no additions) preserve
		// the existing refs; addition rounds append the newly-cited chunks
		// on top of what was already there, deduplicated.
		page.ChunkRefs = mergeChunkRefs(page.ChunkRefs, additions)
		if exists {
			// A retraction removes a deleted document's contribution on
			// purpose, so this write is allowed to drop the table rows that
			// contribution owned. Saying so explicitly is what lets the choke
			// point in UpdatePage refuse an accidental truncation everywhere
			// else; without it a retract round would be refused and the stale
			// content would stay on the page for good.
			writeCtx := ctx
			if len(retracts) > 0 {
				writeCtx = withWikiShrinkAllowed(ctx)
			}
			// UpdatePage refuses a machine rewrite that drops too many table
			// rows still on the page and says so with
			// ErrWikiWriteDroppedTableRows. That refusal is not a failure — the
			// page is intact and this caller did nothing wrong — but it does
			// mean this slug's contribution never landed, and its documents
			// must be kept for a later batch. The version cannot stand in for
			// that signal: a successful write that changes nothing
			// user-visible leaves `version` alone too (see the version-bump
			// policy in wiki_page.go), so only the explicit refusal is
			// classified as deferred.
			_, updateErr := s.UpdatePage(writeCtx, page)
			updateDeferred, err = classifyWikiPageWrite(updateErr)
			if updateDeferred {
				err = updateErr
			}
		} else {
			_, err = s.CreatePage(ctx, page)
		}
		return true, affectedType, additionFailed, updateDeferred, err
	}

	return false, "", additionFailed, false, nil
}

// upstreamReducer assembles the repository boundaries required for per-page reduction.
type upstreamReducer struct {
	*liteService                                   // Library task and independent model port.
	pages          []*types.WikiPage               // Base pages generated by this batch.
	artifacts      map[string]ReadArtifact         // Actual chunks for the current source.
	sources        map[string]*types.SourceVersion // Current immutable source.
	updates        []SlugUpdate                    // Actual contributions and retractions for this page.
	additionFailed bool                            // Retain the old page and requeue the source on model failure.
	deferred       bool                            // Defer contributions when the page guard rejects the write.
}
type wikiShrinkKey struct{}

func withWikiShrinkAllowed(ctx context.Context) context.Context {
	return context.WithValue(ctx, wikiShrinkKey{}, true)
}
func logWarnf(ctx context.Context, format string, args ...any) {
	slog.WarnContext(ctx, fmt.Sprintf(format, args...))
}

var inlineChunkCitations = regexp.MustCompile(`[ \t]*\[c\d{3,}(?:\s*[,;]\s*c\d{3,})*\]`)

func stripWikiInlineChunkCitations(content string) string {
	return inlineChunkCitations.ReplaceAllString(content, "")
}
func resolveSlugUpdateLanguage(_ context.Context, updates []SlugUpdate) string {
	for _, u := range updates {
		if u.Language != "" {
			return u.Language
		}
	}
	return "Chinese"
}
func (s *upstreamReducer) filterLiveUpdates(ctx context.Context, _ string, updates []SlugUpdate) ([]SlugUpdate, error) {
	result := []SlugUpdate{}
	for _, u := range updates {
		if u.Type == "retract" || u.Type == "retractStale" {
			result = append(result, u)
			continue
		}
		if err := s.store.ValidateOrganizeSource(ctx, s.job.ID, s.job.Attempt, u.SourceVersionID); err != nil {
			var domain *types.Error
			if errors.As(err, &domain) && domain.Code == types.ErrSourceUnavailable {
				continue
			}
			return nil, err
		}
		result = append(result, u)
	}
	s.updates = result
	return result, nil
}
func (s *upstreamReducer) GetPageBySlug(_ context.Context, _ string, slug string) (*types.WikiPage, error) {
	for _, page := range s.pages {
		if page.Slug == slug {
			copy := clonePage(*page)
			// Upstream source_refs use stable knowledgeID values; local authoritative Evidence retains source versions and maps them to stable SourceID values at the boundary.
			copy.SourceRefs = nil
			for _, ref := range copy.Evidence.Sources {
				copy.SourceRefs = appendUniqueString(copy.SourceRefs, ref.SourceID)
			}
			return &copy, nil
		}
	}
	return nil, &types.Error{Code: types.ErrNotFound, Operation: "lite.page", Detail: "wiki page not found"}
}
func (s *upstreamReducer) resolveCitedChunks(additions []SlugUpdate) map[string]string {
	result := map[string]string{}
	for _, u := range additions {
		for _, block := range s.artifacts[u.SourceVersionID].Blocks {
			result[block.ID] = block.Content
		}
	}
	return result
}
func (s *upstreamReducer) CreatePage(ctx context.Context, page *types.WikiPage) (*types.WikiPage, error) {
	return s.savePage(ctx, page, 0)
}
func (s *upstreamReducer) UpdatePage(ctx context.Context, page *types.WikiPage) (*types.WikiPage, error) {
	expected := page.Version
	if allowed, _ := ctx.Value(wikiShrinkKey{}).(bool); !allowed {
		for _, old := range s.pages {
			if old.Slug != page.Slug {
				continue
			}
			missing := wiki.MissingRowIdentities(old.Content, page.Content)
			if len(missing) > 0 && wiki.RowIdentityLossRatio(len(missing), wiki.UniqueRowIdentityCount(old.Content)) > .20 {
				s.deferred = true
				return nil, &types.Error{Code: types.ErrManualProtected, Operation: "lite.page.rows", Detail: fmt.Sprintf("%s: wiki rewrite dropped %d of %d table rows", page.Slug, len(missing), wiki.UniqueRowIdentityCount(old.Content))}
			}
		}
	}
	return s.savePage(ctx, page, expected)
}
func (s *upstreamReducer) savePage(ctx context.Context, page *types.WikiPage, expected int) (*types.WikiPage, error) {
	page.WikiLanguage = s.library.WikiLanguage
	page.Status = "published"
	// Retractions delete old-version evidence by stable SourceID; a reparsed new version adds only the actual chunk references from this run.
	retractIDs := map[string]bool{}
	for _, u := range s.updates {
		if u.Type == "retract" || u.Type == "retractStale" {
			retractIDs[u.KnowledgeID] = true
		}
	}
	kept := []types.SourceRef{}
	for _, ref := range page.Evidence.Sources {
		if !retractIDs[ref.SourceID] {
			kept = append(kept, ref)
		}
	}
	page.Evidence.Sources = kept
	for _, u := range s.updates {
		if u.Type == "retract" || u.Type == "retractStale" {
			continue
		}
		source := s.sources[u.SourceVersionID]
		if source == nil {
			return nil, invalid("source disappeared from current batch")
		}
		if u.Type == "summary" || len(u.SourceChunks) == 0 {
			page.Evidence.Sources = appendEvidence(page.Evidence.Sources, documentRef(source))
		}
		for _, id := range u.SourceChunks {
			for _, block := range s.artifacts[u.SourceVersionID].Blocks {
				if block.ID == id {
					page.Evidence.Sources = appendEvidence(page.Evidence.Sources, types.SourceRef{SourceID: source.SourceID, SourceVersionID: source.ID, SHA256: source.SHA256, Locator: block.Locator, Excerpt: block.Content, ExcerptHash: hash([]byte(block.Content)), ClaimKind: "source_statement"})
					break
				}
			}
		}
	}
	page.SourceRefs = nil
	mutation := types.PageMutation{Page: *page, ExpectedVersion: expected}
	keys := make([]string, 0, len(s.updates))
	for _, u := range s.updates {
		keys = append(keys, s.contributionKey(u))
	}
	_, err := s.publish(ctx, "page/"+hash(marshal(mutation)), &types.Publication{Pages: []types.PageMutation{mutation}, SourceVersionIDs: sourceIDsFor(s.updates)}, keys...)
	return page, err
}
func classifyWikiPageWrite(err error) (bool, error) {
	if err == nil {
		return false, nil
	}
	var domain *types.Error
	if errors.As(err, &domain) && domain.Code == types.ErrManualProtected {
		return true, nil
	}
	return false, err
}
func mergeChunkRefs(current types.StringArray, additions []SlugUpdate) types.StringArray {
	seen := make(map[string]bool, len(current))
	out := make(types.StringArray, 0, len(current))
	for _, id := range current {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	for _, add := range additions {
		for _, chunkID := range add.SourceChunks {
			if chunkID == "" || seen[chunkID] {
				continue
			}
			seen[chunkID] = true
			out = append(out, chunkID)
		}
	}
	return out
}
