package wiki

import (
	"path"
	"strings"
)

// ParseOutLinks projects outgoing links from actual Markdown bodies without accepting caller-fabricated relation fields.
func ParseOutLinks(content string) []string {
	// Reuse the link scanner's code-span detection so links in example code do not become relations.
	_, used := computeForbiddenSpans(content)
	safe := map[string]bool{}
	for slug := range used {
		safe[normalizeSlug(slug)] = true
	}
	matches := wikiLinkRegex.FindAllStringSubmatch(content, -1)
	seen := make(map[string]bool)
	var links []string

	for _, match := range matches {
		if len(match) > 1 {
			slug := strings.TrimSpace(match[1])
			// Handle [[slug|display name]] format — slug is the first part
			if parts := strings.SplitN(slug, "|", 2); len(parts) == 2 {
				slug = strings.TrimSpace(parts[0])
			}
			slug = normalizeSlug(slug)
			if slug != "" && safe[slug] && !seen[slug] {
				seen[slug] = true
				links = append(links, slug)
			}
		}
	}
	return links
}

// RewritePageLinks updates actual links within the same Markdown scanning boundaries, preserving examples in code and other links.
// An empty newSlug removes link syntax while retaining the alias or readable page name.
func RewritePageLinks(content, oldSlug, newSlug string) string {
	spans, _ := computeForbiddenSpans(content)
	var out strings.Builder
	last, blockedEnd := 0, 0
	for _, sp := range spans {
		raw := content[sp.start:sp.end]
		match := wikiLinkRegex.FindStringIndex(raw)
		if match == nil || match[0] != 0 || match[1] != len(raw) {
			if sp.end > blockedEnd {
				blockedEnd = sp.end
			}
			continue
		}
		if sp.start < blockedEnd || sp.start < last {
			continue
		}
		inner := raw[2 : len(raw)-2]
		if normalizeSlug(extractWikiSlug(inner)) != normalizeSlug(oldSlug) {
			continue
		}
		display := ""
		if pipe := strings.IndexByte(inner, '|'); pipe >= 0 {
			display = inner[pipe:]
		}
		replacement := "[[" + newSlug + display + "]]"
		if newSlug == "" {
			replacement = strings.ReplaceAll(path.Base(oldSlug), "-", " ")
			if display != "" {
				replacement = display[1:]
			}
		}
		out.WriteString(content[last:sp.start])
		out.WriteString(replacement)
		last = sp.end
	}
	if last == 0 {
		return content
	}
	out.WriteString(content[last:])
	return out.String()
}
