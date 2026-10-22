package pipeline

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

const minTextContentRunes = 10

var (
	mdImageRefRE         = regexp.MustCompile(`!\[[^\]]*\]\([^)]*\)`)
	imageOriginalBlockRE = regexp.MustCompile(`(?is)<image_original\b[^>]*>.*?</image_original>`)
	htmlImgTagRE         = regexp.MustCompile(`(?i)<img\b[^>]*/?>`)
	imageWrapperTagRE    = regexp.MustCompile(`(?i)</?image[a-z_]*\b[^>]*/?>`)
)

func stripImageMarkup(s string) string {
	s = imageOriginalBlockRE.ReplaceAllString(s, "")
	s = mdImageRefRE.ReplaceAllString(s, "")
	s = htmlImgTagRE.ReplaceAllString(s, "")
	s = imageWrapperTagRE.ReplaceAllString(s, "")
	return s
}

func extractRealText(content string) string {
	return strings.TrimSpace(stripImageMarkup(content))
}

func hasSufficientTextContent(content string) bool {
	return realTextRuneCount(content) >= minTextContentRunes
}

func realTextRuneCount(content string) int {
	return utf8.RuneCountInString(extractRealText(content))
}
