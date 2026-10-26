package pipeline

import (
	"fmt"

	"sort"
	"strings"
)

func insertWikiTaxonomyPath(root *wikiTaxonomyNode, path []string) {
	if root == nil || len(path) == 0 {
		return
	}
	cur := root
	for _, part := range path {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if cur.children == nil {
			cur.children = make(map[string]*wikiTaxonomyNode)
		}
		child := cur.children[part]
		if child == nil {
			child = &wikiTaxonomyNode{}
			cur.children[part] = child
		}
		cur = child
	}
}

func appendWikiTaxonomyNode(buf *strings.Builder, label string, node *wikiTaxonomyNode, depth int) {
	if label != "" {
		fmt.Fprintf(buf, "%s%s\n", strings.Repeat("  ", depth), label)
	}
	if node == nil || len(node.children) == 0 {
		return
	}
	keys := make([]string, 0, len(node.children))
	for k := range node.children {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		appendWikiTaxonomyNode(buf, k, node.children[k], depth+1)
	}
}

func formatExistingTaxonomyForPrompt(paths [][]string) string {
	if len(paths) == 0 {
		return ""
	}
	root := &wikiTaxonomyNode{}
	for _, path := range paths {
		insertWikiTaxonomyPath(root, path)
	}
	if len(root.children) == 0 {
		return ""
	}
	var buf strings.Builder
	keys := make([]string, 0, len(root.children))
	for k := range root.children {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		appendWikiTaxonomyNode(&buf, k, root.children[k], 0)
	}
	return strings.TrimSpace(buf.String())
}

func cleanLLMJSON(s string) string {
	s = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "\uFEFF"))
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)
	return sanitizeJSONString(s)
}

func sanitizeJSONString(s string) string {
	var buf strings.Builder
	buf.Grow(len(s))
	inString := false
	escape := false
	for _, r := range s {
		if escape {
			if r == '\n' {
				buf.WriteString(`n`)
			} else if r == '\r' {
				buf.WriteString(`r`)
			} else if r == '\t' {
				buf.WriteString(`t`)
			} else {
				buf.WriteRune(r)
			}
			escape = false
			continue
		}
		if r == '\\' {
			escape = true
			buf.WriteRune(r)
			continue
		}
		if r == '"' {
			inString = !inString
			buf.WriteRune(r)
			continue
		}
		if inString {
			if r == '\n' {
				buf.WriteString(`\n`)
				continue
			}
			if r == '\r' {
				buf.WriteString(`\r`)
				continue
			}
			if r == '\t' {
				buf.WriteString(`\t`)
				continue
			}
		}
		buf.WriteRune(r)
	}
	return buf.String()
}
