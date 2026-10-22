package wiki

import (
	"regexp"
	"strings"
)

var wikiLinkRegex = regexp.MustCompile(`\[\[([^\]]+)\]\]`)

func normalizeSlug(slug string) string {
	slug = strings.ToLower(strings.TrimSpace(slug))
	slug = strings.ReplaceAll(slug, " ", "-")
	return slug
}

// slugNamespace returns the prefix of a slug up to (but excluding) the first
// '/', e.g. "summary/abc" -> "summary". Slugs without a '/' map to "".
func slugNamespace(slug string) string {
	if i := strings.IndexByte(slug, '/'); i >= 0 {
		return slug[:i]
	}
	return ""
}

// rewriteDeadWikiLinks walks every [[slug]] / [[slug|display]] occurrence in
// content and lets `resolve` decide, per link, whether to rewrite the slug.
// `resolve` receives the normalized slug and its display text and returns the
// replacement slug plus true to rewrite, or ("", false) to leave the link
// untouched. Display text is preserved verbatim. This is a pure helper — the
// resolution policy lives entirely in the callback.
func rewriteDeadWikiLinks(content string, resolve func(normSlug, display string) (string, bool)) (string, bool) {
	changed := false
	out := wikiLinkRegex.ReplaceAllStringFunc(content, func(match string) string {
		inner := match[2 : len(match)-2]
		rawSlug := inner
		display := ""
		if parts := strings.SplitN(inner, "|", 2); len(parts) == 2 {
			rawSlug = parts[0]
			display = strings.TrimSpace(parts[1])
		}
		norm := normalizeSlug(rawSlug)
		if norm == "" {
			return match
		}
		newSlug, ok := resolve(norm, display)
		if !ok || newSlug == "" || newSlug == norm {
			return match
		}
		changed = true
		if display != "" {
			return "[[" + newSlug + "|" + display + "]]"
		}
		return "[[" + newSlug + "]]"
	})
	return out, changed
}
