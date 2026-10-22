package pipeline

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var (
	mdImageURLRE            = regexp.MustCompile(`!\[[^\]]*\]\(([^)]*)\)`)
	imageURLAttrRE          = regexp.MustCompile(`(?i)<image\b[^>]*\surl\s*=\s*(?:"([^"]*)"|'([^']*)')`)
	imagePlaceholderTokenRE = regexp.MustCompile(`wkimg:[A-Za-z0-9_-]+`)
)

func maskTemplateDataImageURLs(data map[string]string) (map[string]string, map[string]string) {
	if len(data) == 0 {
		return data, nil
	}

	masked := make(map[string]string, len(data))
	urlToToken := make(map[string]string)
	tokenToURL := make(map[string]string)

	keys := make([]string, 0, len(data))
	for key := range data {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	for _, key := range keys {
		masked[key] = maskImageURLsWithState(data[key], urlToToken, tokenToURL)
	}

	return masked, tokenToURL
}

// maskImageURLs replaces image URLs with low-entropy placeholders. It only
// freezes URLs; alt/caption text remains in place for the LLM to edit.
func maskImageURLs(s string) (string, map[string]string) {
	urlToToken := make(map[string]string)
	tokenToURL := make(map[string]string)
	return maskImageURLsWithState(s, urlToToken, tokenToURL), tokenToURL
}

func maskImageURLsWithState(s string, urlToToken, tokenToURL map[string]string) string {
	urls := collectMaskableImageURLs(s)
	if len(urls) == 0 {
		return s
	}

	for _, url := range urls {
		if _, ok := urlToToken[url]; ok {
			continue
		}
		token := fmt.Sprintf("wkimg:%04d", len(tokenToURL)+1)
		urlToToken[url] = token
		tokenToURL[token] = url
	}

	replaceURLs := append([]string(nil), urls...)
	sort.SliceStable(replaceURLs, func(i, j int) bool {
		return len(replaceURLs[i]) > len(replaceURLs[j])
	})

	masked := s
	for _, url := range replaceURLs {
		masked = strings.ReplaceAll(masked, url, urlToToken[url])
	}
	return masked
}

func collectMaskableImageURLs(s string) []string {
	seen := make(map[string]struct{})
	var urls []string

	addURL := func(url string) {
		url = strings.TrimSpace(url)
		if url == "" {
			return
		}
		if _, ok := seen[url]; ok {
			return
		}
		seen[url] = struct{}{}
		urls = append(urls, url)
	}

	for _, match := range mdImageURLRE.FindAllStringSubmatch(s, -1) {
		addURL(match[1])
	}
	for _, match := range imageURLAttrRE.FindAllStringSubmatch(s, -1) {
		if match[1] != "" {
			addURL(match[1])
			continue
		}
		addURL(match[2])
	}

	return urls
}

// unmaskImageURLs restores known placeholders and drops any corrupted or
// invented image placeholders so broken image links never reach storage.
func unmaskImageURLs(out string, urlMap map[string]string) string {
	out = mdImageURLRE.ReplaceAllStringFunc(out, func(match string) string {
		parts := mdImageURLRE.FindStringSubmatch(match)
		if len(parts) != 2 {
			return match
		}
		url := strings.TrimSpace(parts[1])
		if realURL, ok := urlMap[url]; ok {
			idx := strings.LastIndex(match, "(")
			if idx < 0 {
				return match
			}
			return match[:idx+1] + realURL + ")"
		}
		if strings.HasPrefix(url, "wkimg:") {
			return ""
		}
		return match
	})

	return replaceImagePlaceholderTokensOutsideMarkdown(out, urlMap)
}

func replaceImagePlaceholderTokensOutsideMarkdown(s string, urlMap map[string]string) string {
	matches := mdImageURLRE.FindAllStringIndex(s, -1)
	if len(matches) == 0 {
		return replaceImagePlaceholderTokens(s, urlMap)
	}

	var b strings.Builder
	last := 0
	for _, match := range matches {
		if match[0] > last {
			b.WriteString(replaceImagePlaceholderTokens(s[last:match[0]], urlMap))
		}
		b.WriteString(s[match[0]:match[1]])
		last = match[1]
	}
	if last < len(s) {
		b.WriteString(replaceImagePlaceholderTokens(s[last:], urlMap))
	}
	return b.String()
}

func replaceImagePlaceholderTokens(s string, urlMap map[string]string) string {
	return imagePlaceholderTokenRE.ReplaceAllStringFunc(s, func(token string) string {
		if realURL, ok := urlMap[token]; ok {
			return realURL
		}
		return ""
	})
}

// stripImageMarkup removes image-only placeholders (Markdown image refs,
// <img> tags, <image_original> redundancy blocks) and unwraps the
// <image>/<image_caption>/<image_ocr> XML wrappers produced by the search
// enrichment layer, leaving any OCR or caption text as plain inline text.
//
// This shape matters: when VLM OCR succeeds on a scanned PDF page, the
// extracted text reaches downstream code wrapped in <image_ocr> tags inside
// an <image> block. A naive "strip the whole <image>...</image> block"
// approach would discard the OCR text — the exact opposite of what we want.
