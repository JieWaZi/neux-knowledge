package pipeline

import (
	"context"
	types "github.com/JieWaZi/neux-knowledge/internal/domain"
	"strings"
)

func (s *liteService) claimWikiIdentitySlug(
	ctx context.Context,
	kbID, pageType, title, proposedSlug string,
	authoritative bool,
	batchCtx *WikiBatchContext,
) string {
	identity := normalizeWikiIdentityTitle(title)
	expectedPrefix := pageType + "/"
	if identity == "" || proposedSlug == "" || !strings.HasPrefix(proposedSlug, expectedPrefix) {
		return proposedSlug
	}

	claim := proposedSlug
	if batchCtx != nil {
		localKey := pageType + "\x00" + identity
		if authoritative {
			// Redis is the cross-batch source of truth when it answered.
			// Exact existing-page hits also overwrite a stale local value.
			batchCtx.identityClaims.Store(localKey, claim)
		} else {
			actual, _ := batchCtx.identityClaims.LoadOrStore(localKey, claim)
			if existing, ok := actual.(string); ok && strings.HasPrefix(existing, expectedPrefix) {
				claim = existing
			}
		}
	}
	return claim
}

func (s *liteService) stabilizeExtractedIdentities(
	ctx context.Context,
	kbID, pageType string,
	items []extractedItem,
	mergeTargets, exactTargets map[string]string,
	batchCtx *WikiBatchContext,
) []extractedItem {
	out := make([]extractedItem, 0, len(items))
	bySlug := make(map[string]int, len(items))
	claimedByIdentity := make(map[string]string, len(items))
	for _, item := range items {
		originalSlug := item.Slug
		authoritative := false
		if target := exactTargets[originalSlug]; target != "" {
			item.Slug = target
			authoritative = true
		} else if target := mergeTargets[originalSlug]; target != "" {
			item.Slug = target
		}
		identity := normalizeWikiIdentityTitle(item.Name)
		if !authoritative {
			if slug, ok := claimedByIdentity[identity]; ok && slug != "" {
				item.Slug = slug
			} else {
				item.Slug = s.claimWikiIdentitySlug(
					ctx, kbID, pageType, item.Name, item.Slug, false, batchCtx,
				)
				if identity != "" {
					claimedByIdentity[identity] = item.Slug
				}
			}
		} else {
			item.Slug = s.claimWikiIdentitySlug(
				ctx, kbID, pageType, item.Name, item.Slug, true, batchCtx,
			)
			if identity != "" {
				claimedByIdentity[identity] = item.Slug
			}
		}
		if idx, ok := bySlug[item.Slug]; ok {
			out[idx] = mergeExtractedIdentity(out[idx], item)
			continue
		}
		bySlug[item.Slug] = len(out)
		out = append(out, item)
	}
	return out
}

func identityPageCacheKey(pageType, identity string) string {
	return pageType + "\x00" + identity
}

// otherWikiPageType returns the entity/concept counterpart of a page type.
// The cross-type identity fallback (issue #3526) only applies between these
// two; summary/index pages are excluded.
func otherWikiPageType(pageType string) string {
	if pageType == types.WikiPageTypeEntity {
		return types.WikiPageTypeConcept
	}
	return types.WikiPageTypeEntity
}

func loadCachedIdentityPages(batchCtx *WikiBatchContext, pageType, identity string) ([]*types.WikiPageLite, bool) {
	if batchCtx == nil {
		return nil, false
	}
	v, ok := batchCtx.identityPages.Load(identityPageCacheKey(pageType, identity))
	if !ok {
		return nil, false
	}
	pages, _ := v.([]*types.WikiPageLite)
	return pages, true
}

func storeCachedIdentityPages(batchCtx *WikiBatchContext, pageType, identity string, pages []*types.WikiPageLite) {
	if batchCtx == nil {
		return
	}
	if pages == nil {
		pages = []*types.WikiPageLite{}
	}
	batchCtx.identityPages.Store(identityPageCacheKey(pageType, identity), pages)
}

func bindExactIdentityPages(
	itemSlugs []string,
	pages []*types.WikiPageLite,
	pageType, identity string,
	candidatePages map[string]*types.WikiPageLite,
	itemCandidates map[string]map[string]bool,
) {
	if len(itemSlugs) == 0 || len(pages) == 0 {
		return
	}
	for _, p := range pages {
		if p == nil || p.Slug == "" || p.PageType != pageType {
			continue
		}
		if normalizeWikiIdentityTitle(p.Title) != identity {
			continue
		}
		if _, ok := candidatePages[p.Slug]; !ok {
			candidatePages[p.Slug] = p
		}
		for _, slug := range itemSlugs {
			own := itemCandidates[slug]
			if own == nil {
				own = make(map[string]bool)
				itemCandidates[slug] = own
			}
			own[p.Slug] = true
		}
	}
}

func (s *liteService) attachExactIdentityPages(
	ctx context.Context,
	kbID, pageType string,
	items []extractedItem,
	candidatePages map[string]*types.WikiPageLite,
	itemCandidates map[string]map[string]bool,
	batchCtx *WikiBatchContext,
) {
	if len(items) == 0 {
		return
	}

	slugsByIdentity := make(map[string][]string, len(items))
	for _, item := range items {
		identity := normalizeWikiIdentityTitle(item.Name)
		if identity == "" || item.Slug == "" {
			continue
		}
		slugsByIdentity[identity] = append(slugsByIdentity[identity], item.Slug)
	}
	if len(slugsByIdentity) == 0 {
		return
	}

	cached := make(map[string][]*types.WikiPageLite, len(slugsByIdentity))
	miss := make([]string, 0, len(slugsByIdentity))
	for identity := range slugsByIdentity {
		if pages, ok := loadCachedIdentityPages(batchCtx, pageType, identity); ok {
			cached[identity] = pages
			continue
		}
		miss = append(miss, identity)
	}

	if len(miss) > 0 {
		pages, err := s.store.FindWikiPagesByNormalizedTitles(ctx, kbID, pageType, miss)
		if err != nil {
			logWarnf(ctx, "wiki ingest: exact identity lookup failed for %s (%d titles): %v", pageType, len(miss), err)
		} else {
			byIdentity := make(map[string][]*types.WikiPageLite, len(miss))
			for _, p := range pages {
				if p == nil {
					continue
				}
				identity := normalizeWikiIdentityTitle(p.Title)
				if identity == "" {
					continue
				}
				byIdentity[identity] = append(byIdentity[identity], p)
			}
			for _, identity := range miss {
				hits := byIdentity[identity]
				if hits == nil {
					hits = []*types.WikiPageLite{}
				}
				cached[identity] = hits
				storeCachedIdentityPages(batchCtx, pageType, identity, hits)
			}
		}
	}

	for identity, slugs := range slugsByIdentity {
		bindExactIdentityPages(slugs, cached[identity], pageType, identity, candidatePages, itemCandidates)
	}

	// Cross-type safety net (issue #3526): the trigram probe that feeds
	// itemCandidates usually surfaces a same-title page of the other type,
	// but not always (formatting drift beyond whitespace/case). Fetch those
	// pages explicitly so exactIdentityTarget can reuse them instead of
	// materializing a concept/X + entity/X twin on re-ingest.
	other := otherWikiPageType(pageType)
	crossCached := make(map[string][]*types.WikiPageLite, len(slugsByIdentity))
	crossMiss := make([]string, 0, len(slugsByIdentity))
	for identity := range slugsByIdentity {
		if pages, ok := loadCachedIdentityPages(batchCtx, other, identity); ok {
			crossCached[identity] = pages
			continue
		}
		crossMiss = append(crossMiss, identity)
	}
	if len(crossMiss) > 0 {
		pages, err := s.store.FindWikiPagesByNormalizedTitles(ctx, kbID, other, crossMiss)
		if err != nil {
			logWarnf(ctx, "wiki ingest: cross-type identity lookup failed for %s (%d titles): %v",
				other, len(crossMiss), err)
		} else {
			byIdentity := make(map[string][]*types.WikiPageLite, len(crossMiss))
			for _, p := range pages {
				if p == nil {
					continue
				}
				identity := normalizeWikiIdentityTitle(p.Title)
				if identity == "" {
					continue
				}
				byIdentity[identity] = append(byIdentity[identity], p)
			}
			for _, identity := range crossMiss {
				hits := byIdentity[identity]
				if hits == nil {
					hits = []*types.WikiPageLite{}
				}
				crossCached[identity] = hits
				storeCachedIdentityPages(batchCtx, other, identity, hits)
			}
		}
	}
	for identity, slugs := range slugsByIdentity {
		for _, p := range crossCached[identity] {
			if p == nil || p.Slug == "" || p.PageType != other {
				continue
			}
			if _, ok := candidatePages[p.Slug]; !ok {
				candidatePages[p.Slug] = p
			}
			for _, slug := range slugs {
				own := itemCandidates[slug]
				if own == nil {
					own = make(map[string]bool)
					itemCandidates[slug] = own
				}
				own[p.Slug] = true
			}
		}
	}
}

func collectExactIdentityTargets(
	items []extractedItem,
	pageType string,
	itemCandidates map[string]map[string]bool,
	candidatePages map[string]*types.WikiPageLite,
	exactTargets map[string]string,
) {
	for _, item := range items {
		if target := exactIdentityTarget(item, pageType, itemCandidates[item.Slug], candidatePages); target != "" {
			exactTargets[item.Slug] = target
		}
	}
}

// reclaimExtractedIdentities re-runs exact title lookup plus identity claims
// after citation discovery. Citation new_slugs skip the extract-time dedup
// pass, so without this they can materialize a second same-title page.
func (s *liteService) reclaimExtractedIdentities(
	ctx context.Context,
	kbID string,
	entities, concepts []extractedItem,
	batchCtx *WikiBatchContext,
) ([]extractedItem, []extractedItem) {
	if len(entities) == 0 && len(concepts) == 0 {
		return entities, concepts
	}
	candidatePages := make(map[string]*types.WikiPageLite)
	itemCandidates := make(map[string]map[string]bool)
	s.attachExactIdentityPages(ctx, kbID, types.WikiPageTypeEntity, entities, candidatePages, itemCandidates, batchCtx)
	s.attachExactIdentityPages(ctx, kbID, types.WikiPageTypeConcept, concepts, candidatePages, itemCandidates, batchCtx)
	exactTargets := make(map[string]string)
	collectExactIdentityTargets(entities, types.WikiPageTypeEntity, itemCandidates, candidatePages, exactTargets)
	collectExactIdentityTargets(concepts, types.WikiPageTypeConcept, itemCandidates, candidatePages, exactTargets)
	return s.stabilizeExtractedIdentities(ctx, kbID, types.WikiPageTypeEntity, entities, nil, exactTargets, batchCtx),
		s.stabilizeExtractedIdentities(ctx, kbID, types.WikiPageTypeConcept, concepts, nil, exactTargets, batchCtx)
}

// remapSlugUpdatesByIdentity re-reads identity claims after every map worker
// has finished so in-flight slug choices converge before Reduce groups and
// locks by slug. Summary/retract updates keep their original slugs.
func (s *liteService) remapSlugUpdatesByIdentity(
	ctx context.Context,
	kbID string,
	slugUpdates map[string][]SlugUpdate,
	batchCtx *WikiBatchContext,
) map[string][]SlugUpdate {
	if len(slugUpdates) == 0 {
		return slugUpdates
	}
	out := make(map[string][]SlugUpdate, len(slugUpdates))
	claimedByIdentity := make(map[string]string, len(slugUpdates))
	for _, updates := range slugUpdates {
		for _, u := range updates {
			if u.Type == types.WikiPageTypeEntity || u.Type == types.WikiPageTypeConcept {
				title := u.Item.Name
				if title == "" {
					title = u.Slug
				}
				identity := normalizeWikiIdentityTitle(title)
				var claimed string
				if identity != "" {
					claimed = claimedByIdentity[u.Type+"\x00"+identity]
				}
				if claimed == "" {
					claimed = s.claimWikiIdentitySlug(ctx, kbID, u.Type, title, u.Slug, false, batchCtx)
					if identity != "" && claimed != "" {
						claimedByIdentity[u.Type+"\x00"+identity] = claimed
					}
				}
				if claimed != "" {
					u.Slug = claimed
					u.Item.Slug = claimed
				}
			}
			out[u.Slug] = append(out[u.Slug], u)
		}
	}
	return out
}
