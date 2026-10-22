package docparser

import "regexp"

// reLinkedImage matches the nested [![alt](img_url)](link_url) pattern where
// an image is wrapped inside a Markdown link. We unwrap it to just ![alt](img_url)
// so that downstream image-processing regexes only have to handle the flat form.
// The URL groups support one level of balanced parentheses.
var reLinkedImage = regexp.MustCompile(
	`\[!\[([^\]]*)\]\(([^()\s]*(?:\([^)]*\)[^()\s]*)*)\)\]` + // [![alt](img_url)]
		`\([^()\s]*(?:\([^)]*\)[^()\s]*)*\)`, // (link_url) — captured but discarded
)

// UnwrapLinkedImages replaces all [![alt](img_url)](link_url) occurrences in
// the markdown with just ![alt](img_url), stripping the outer link wrapper.
// This should be called before any image-extraction regex so that only the
// flat ![alt](url) form needs to be handled.
func UnwrapLinkedImages(markdown string) string {
	return reLinkedImage.ReplaceAllString(markdown, "![$1]($2)")
}
