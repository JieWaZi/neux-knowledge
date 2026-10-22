package pipeline

import (
	types "github.com/JieWaZi/neux-knowledge/internal/domain"
	"sort"
	"strings"
	"unicode"
)

func dedupMergeRejectReason(srcSlug, dstSlug string, srcCandidates map[string]bool) string {
	if !srcCandidates[dstSlug] {
		// Covers both an outright hallucinated target (in no candidate
		// set at all) and a real page that was only similar to another
		// item. Either way the pair lacks a similarity signal for THIS
		// item, so it is not a safe merge.
		return "target is not a similarity candidate for this item"
	}
	srcSlash := strings.Index(srcSlug, "/")
	dstSlash := strings.Index(dstSlug, "/")
	if srcSlash <= 0 || dstSlash <= 0 {
		// A type-prefixed slug must look like "entity/foo" or
		// "concept/bar". An LLM that emits an un-prefixed slug here is
		// hallucinating; reject rather than fall through the prefix-
		// equality check (which would treat both empty prefixes as a
		// match).
		return "missing type prefix"
	}
	if srcSlug[:srcSlash+1] != dstSlug[:dstSlash+1] {
		return "type mismatch: " + srcSlug[:srcSlash+1] + " vs " + dstSlug[:dstSlash+1]
	}
	return ""
}

func normalizeWikiIdentityTitle(title string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return unicode.ToLower(r)
	}, strings.TrimSpace(title))
}

func exactIdentityTarget(
	item extractedItem,
	pageType string,
	candidates map[string]bool,
	pages map[string]*types.WikiPageLite,
) string {
	identity := normalizeWikiIdentityTitle(item.Name)
	if identity == "" {
		return ""
	}
	matches := make([]string, 0, 2)
	crossMatches := make([]string, 0, 2)
	for slug := range candidates {
		page := pages[slug]
		if page == nil || normalizeWikiIdentityTitle(page.Title) != identity {
			continue
		}
		if page.PageType == pageType {
			matches = append(matches, page.Slug)
		} else {
			crossMatches = append(crossMatches, page.Slug)
		}
	}
	if len(matches) > 0 {
		for _, slug := range matches {
			if slug == item.Slug {
				return slug
			}
		}
		sort.Strings(matches)
		return matches[0]
	}
	// Cross-type fallback: reuse the existing page even though its type
	// prefix differs from this batch's judgment. Downstream writes are keyed
	// by slug and adopt the existing page's type, so the twin never forms.
	if len(crossMatches) > 0 {
		sort.Strings(crossMatches)
		return crossMatches[0]
	}
	return ""
}

func preferWikiIdentityDisplayName(dst, src string) (name, extraAlias string) {
	if dst == "" {
		return src, ""
	}
	if src == "" || src == dst {
		return dst, ""
	}
	if normalizeWikiIdentityTitle(dst) == normalizeWikiIdentityTitle(src) {
		if len([]rune(src)) < len([]rune(dst)) {
			return src, dst
		}
		return dst, src
	}
	return dst, src
}

func mergeExtractedIdentity(dst, src extractedItem) extractedItem {
	name, extraAlias := preferWikiIdentityDisplayName(dst.Name, src.Name)
	dst.Name = name
	dst.Aliases = appendUniqueString(dst.Aliases, extraAlias)
	for _, alias := range src.Aliases {
		dst.Aliases = appendUniqueString(dst.Aliases, alias)
	}
	if len([]rune(src.Description)) > len([]rune(dst.Description)) {
		dst.Description = src.Description
	}
	if len([]rune(src.Details)) > len([]rune(dst.Details)) {
		dst.Details = src.Details
	}
	for _, chunkID := range src.SourceChunks {
		dst.SourceChunks = appendUniqueString(dst.SourceChunks, chunkID)
	}
	return dst
}

func appendUniqueString(values []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}
