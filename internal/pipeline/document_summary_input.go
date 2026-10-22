package pipeline

import (
	"context"
	"log/slog"
)

var errInsufficientSummaryContent = invalid("insufficient text content for summary generation")

// checkSufficientSummaryContent returns errInsufficientSummaryContent if the
// given content does not carry enough real text (after stripping image markup)
// for an LLM summary call, and logs a warning at the call site. Returns nil
// when the content passes the threshold.
//
// Extracted so the threshold gate can be unit-tested without standing up the
// full ProcessSummaryGeneration dependency graph.
func checkSufficientSummaryContent(ctx context.Context, knowledgeID, content string) error {
	realTextLen := realTextRuneCount(content)
	if realTextLen < minTextContentRunes {
		slog.WarnContext(ctx, "summary content has insufficient real text", "knowledge_id", knowledgeID, "real_text_runes", realTextLen, "min", minTextContentRunes)
		return errInsufficientSummaryContent
	}
	return nil
}

// sampleLongContent returns content that fits within maxChars.
// For short content (≤ maxChars), it is returned as-is.
// For long content, it samples: head (60%), tail (20%), and evenly-spaced middle (20%),
// joined by "[...content omitted...]" markers so the LLM knows content was skipped.
func sampleLongContent(content string, maxChars int) string {
	runes := []rune(content)
	if len(runes) <= maxChars {
		return content
	}

	const omitMarker = "\n\n[...content omitted...]\n\n"
	omitRunes := len([]rune(omitMarker))

	// Reserve space for two omit markers (head→middle, middle→tail)
	usable := maxChars - 2*omitRunes
	if usable < 100 {
		// Fallback: just truncate
		return string(runes[:maxChars])
	}

	headLen := usable * 60 / 100
	tailLen := usable * 20 / 100
	midLen := usable - headLen - tailLen

	head := string(runes[:headLen])
	tail := string(runes[len(runes)-tailLen:])

	// Sample middle portion: take a contiguous block from the center of the document
	midStart := len(runes)/2 - midLen/2
	if midStart < headLen {
		midStart = headLen
	}
	midEnd := midStart + midLen
	if midEnd > len(runes)-tailLen {
		midEnd = len(runes) - tailLen
		midStart = midEnd - midLen
		if midStart < headLen {
			midStart = headLen
		}
	}
	middle := string(runes[midStart:midEnd])

	return head + omitMarker + middle + omitMarker + tail
}
