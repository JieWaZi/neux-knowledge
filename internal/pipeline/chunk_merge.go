package pipeline

import (
	"sort"
	"strings"
)

// JoinChunkContent preserves and joins the actual text of two chunks, safely removing containment and genuine boundary overlap.
//
// JoinChunkContent joins two current chunk bodies without relying on parser
// offsets. Exact containment is collapsed, a real suffix/prefix overlap is
// removed, and otherwise both bodies are retained with separator between
// them. The conservative fallback intentionally prefers small duplication
// over silently dropping edited content.
func JoinChunkContent(acc, next, separator string) string {
	if acc == "" {
		return next
	}
	if next == "" {
		return acc
	}
	if ContainsChunkContent(acc, next) {
		return acc
	}
	if ContainsChunkContent(next, acc) {
		return next
	}

	accRunes := []rune(acc)
	nextRunes := []rune(next)
	maxOverlap := minInt(len(accRunes), len(nextRunes))
	// Editable chunks may be much larger than parser-produced chunks. Bound
	// suffix matching so an adversarial 200 KB edit cannot turn retrieval into
	// quadratic work. Parser overlap windows are normally far below this cap;
	// larger unmatched overlap is safely retained as duplication.
	if maxOverlap > defaultSearchSpan {
		maxOverlap = defaultSearchSpan
	}
	for overlap := maxOverlap; overlap >= minOverlapRunes; overlap-- {
		if runeSlicesEqual(accRunes[len(accRunes)-overlap:], nextRunes[:overlap]) {
			return acc + string(nextRunes[overlap:])
		}
	}
	return acc + separator + next
}

// ContainsChunkContent checks whether another text safely contains a complete chunk, avoiding false matches on short words.
//
// ContainsChunkContent reports whether the complete current body is safely
// represented by another body. Very short substrings are not treated as
// containment because common words and punctuation would create false drops.
func ContainsChunkContent(container, contained string) bool {
	if container == "" || contained == "" {
		return false
	}
	if container == contained {
		return true
	}
	return len([]rune(contained)) >= minOverlapRunes && strings.Contains(container, contained)
}

func runeSlicesEqual(left, right []rune) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

// This shared text-overlap logic supports document reconstruction (reconstructContent),
// knowledge graph content merging (graph mergeChunkContents), and similar paths. Chat retrieval allows
// users to edit chunks; use JoinChunkContent above to avoid relying on source offsets.
//
// Position-based overlap formulas previously used throughout the code (offset = len(content) - (EndAt -
// lastEndAt), for example) assume len([]rune(Content)) == EndAt-StartAt. Two kinds of
// data violate this invariant, causing misaligned joins, lost characters, or duplication:
//  1. Parent-child chunkers insert headers into split tables. These headers have zero width
//     (start == end) and cannot be represented by offsets, so content is longer than EndAt-StartAt.
//  2. Content may retain HTML entities (such as &#34; / &gt;) that are longer than the source span.
//
// Match overlaps by text instead: find the first occurrence of a suffix of the accumulated text
// within a window at the start of the next segment, then append after it. Positions (StartAt/EndAt)
// only estimate the search window size; they no longer determine where text is cut.

const (
	// minOverlapRunes is the shortest suffix considered for matching. Shorter suffixes, such as table separators |---|,
	// are ignored because they are prone to false matches.
	minOverlapRunes = 12
	// defaultSearchSpan is the minimum search window size, allowing genuine overlaps to be
	// detected even when positions are missing or zero.
	defaultSearchSpan = 400
)

// AppendWithOverlap appends next to acc, removing their overlapping text.
//
// positionOverlap estimates overlap from StartAt/EndAt (lastEnd - curStart) and only
// sizes the search window; text matching handles inserted headers and HTML entity length differences.
// If no text overlap is found, append without trimming to preserve content.
//
// When positionOverlap <= 0, the segments are adjacent or disjoint and have no overlap to deduplicate.
// Text matching with the minimum headSlack of 320 could otherwise find genuinely repeated
// content from the suffix of acc in the opening window of next, such as a sentence repeated in a document,
// and incorrectly delete the entire opening as an inserted header, irreversibly losing content. Append directly;
// callers can remove duplicate inserted headers afterward.
func AppendWithOverlap(acc, next string, positionOverlap int) string {
	if acc == "" {
		return next
	}
	if next == "" {
		return acc
	}
	if positionOverlap <= 0 {
		return acc + next
	}

	accRunes := []rune(acc)
	nextRunes := []rune(next)

	span := positionOverlap

	maxK := minInt(len(accRunes), len(nextRunes))
	if cap := maxInt(span*3, defaultSearchSpan); maxK > cap {
		maxK = cap
	}
	// Maximum prefix to skip before overlapping text, such as synthetic inserted headers.
	headSlack := maxInt(span*2, 320)

	for k := maxK; k >= minOverlapRunes; k-- {
		needle := accRunes[len(accRunes)-k:]
		if pos := indexRunes(nextRunes, needle, headSlack); pos >= 0 {
			return acc + string(nextRunes[pos+k:])
		}
	}
	return acc + next
}

// AppendWithExactOverlap joins acc and next using the exact positional overlap when the caller
// has verified the offsets: compare the last overlap characters of acc with the first overlap characters of next
// and trim only if they match exactly. A zero overlap appends directly.
//
// Unlike AppendWithOverlap, this does not estimate: that function searches a window for the longest suffix match
// to handle inserted headers and HTML entity length differences, which can mistake repeated periodic text
// (tables or logs) for overlap and trim real content. Trusted offsets make the overlap known without searching.
//
// On a mismatch, return ok=false so the caller can decide whether to fall back to AppendWithOverlap.
func AppendWithExactOverlap(acc, next string, overlap int) (string, bool) {
	if acc == "" {
		return next, true
	}
	if next == "" {
		return acc, true
	}
	if overlap < 0 {
		return "", false
	}
	if overlap == 0 {
		return acc + next, true
	}

	accRunes := []rune(acc)
	nextRunes := []rune(next)
	if overlap > len(accRunes) || overlap > len(nextRunes) {
		return "", false
	}
	if !runeSlicesEqual(accRunes[len(accRunes)-overlap:], nextRunes[:overlap]) {
		return "", false
	}
	return acc + string(nextRunes[overlap:]), true
}

// MergeTextChunks sorts by StartAt (then ChunkIndex for ties), then uses AppendWithOverlap
// to reconstruct the chunks into complete text. gapSep separates segments with nonadjacent positions
// (a gap), such as "\n"; an empty string concatenates them directly.
//
// Callers must filter chunk types first, for example to retain only text chunks; this function does not inspect ChunkType.
func MergeTextChunks(chunks []*Chunk, gapSep string) string {
	if len(chunks) == 0 {
		return ""
	}

	sorted := make([]*Chunk, len(chunks))
	copy(sorted, chunks)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].StartAt == sorted[j].StartAt {
			return sorted[i].ChunkIndex < sorted[j].ChunkIndex
		}
		return sorted[i].StartAt < sorted[j].StartAt
	})

	merged := ""
	mergedEnd := -1
	for _, c := range sorted {
		if c == nil || c.Content == "" {
			continue
		}
		if merged == "" {
			merged = c.Content
			if c.EndAt > 0 {
				mergedEnd = c.EndAt
			}
			continue
		}

		// A gap or missing position (EndAt==0) creates a separate paragraph without trimming overlap.
		if c.StartAt > mergedEnd || c.EndAt == 0 {
			if gapSep != "" {
				merged += gapSep
			}
			merged += c.Content
			if c.EndAt > 0 {
				mergedEnd = c.EndAt
			}
			continue
		}

		// For partial overlap or adjacent segments, match text to remove overlap before appending.
		if c.EndAt > mergedEnd {
			merged = AppendWithOverlap(merged, c.Content, mergedEnd-c.StartAt)
			mergedEnd = c.EndAt
		}
		// Otherwise, the previous segment completely covers this one; skip it.
	}

	return merged
}

// indexRunes returns the rune index of the first occurrence of needle in haystack whose start is at most
// maxStart. It returns -1 if no match exists.
func indexRunes(haystack, needle []rune, maxStart int) int {
	if len(needle) == 0 || len(needle) > len(haystack) {
		return -1
	}
	limit := len(haystack) - len(needle)
	if maxStart < limit {
		limit = maxStart
	}
	for i := 0; i <= limit; i++ {
		match := true
		for j := 0; j < len(needle); j++ {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
