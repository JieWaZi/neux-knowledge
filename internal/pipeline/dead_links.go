package pipeline

import (
	"strings"
)

func stripDeadWikiLinks(
	content string,
	deadSlugs map[string]struct{},
	liveSlugs map[string]struct{},
	titleToSlug map[string]string,
) (string, bool) {
	if len(deadSlugs) == 0 || content == "" {
		return content, false
	}
	changed := false
	out := wikiLinkRE.ReplaceAllStringFunc(content, func(match string) string {
		sub := wikiLinkRE.FindStringSubmatch(match)
		if len(sub) < 2 {
			return match
		}
		slug := sub[1]
		if _, dead := deadSlugs[slug]; !dead {
			return match
		}
		display := ""
		if len(sub) >= 3 {
			display = strings.TrimSpace(sub[2])
		}

		// (1) Try fuzzy resolve before falling back to strip. The
		// resolver consults display-text reverse lookup, hyphen-
		// normalized equality, and bigram similarity in that order;
		// returns "" only when no candidate is safe.
		if resolved, ok := resolveDeadSlug(slug, display, liveSlugs, titleToSlug); ok && resolved != slug {
			changed = true
			if display != "" {
				return "[[" + resolved + "|" + display + "]]"
			}
			return "[[" + resolved + "]]"
		}

		// (2) Strip — best-effort plain text. Prefer the LLM-supplied
		// display text; otherwise humanize the slug's last path segment
		// so the prose stays readable.
		changed = true
		if display != "" {
			return display
		}
		parts := strings.Split(slug, "/")
		label := parts[len(parts)-1]
		label = strings.ReplaceAll(label, "-", " ")
		return label
	})
	return out, changed
}
