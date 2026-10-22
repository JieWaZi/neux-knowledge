package pipeline

import (
	"encoding/json"
	"regexp"
	"strings"
)

// jsonCodeFenceRE extracts a JSON payload wrapped in a Markdown code fence.
// Compiled once: ParseLLMJsonResponse runs on the graph-extraction path.
var jsonCodeFenceRE = regexp.MustCompile("```(?:json)?\\s*([\\s\\S]*?)```")

func ParseLLMJsonResponse(content string, target interface{}) error {
	// First, try to parse directly as JSON
	err := json.Unmarshal([]byte(content), target)
	if err == nil {
		return nil
	}

	// If direct parsing fails, try to extract JSON from code blocks
	matches := jsonCodeFenceRE.FindStringSubmatch(content)
	if len(matches) >= 2 {
		// Extract the JSON content within the code block
		jsonContent := strings.TrimSpace(matches[1])
		if fenceErr := json.Unmarshal([]byte(jsonContent), target); fenceErr == nil {
			return nil
		}
	}

	// Last resort: models often wrap the payload in prose ("Sure, here is
	// the JSON: {...}"). Scan for a balanced object/array so trailing
	// commentary — including bracket-like text such as "[1]" — cannot
	// truncate the payload.
	if extracted := ExtractBalancedJSON(content); extracted != "" {
		if scanErr := json.Unmarshal([]byte(extracted), target); scanErr == nil {
			return nil
		}
	}

	// Report the direct-parse failure, which is the most descriptive one.
	return err
}

// ExtractBalancedJSON returns the first balanced JSON object or array embedded
// in s, or an empty string when there is none. Whichever bracket type opens
// first wins, and quoted strings are skipped so braces inside string literals
// do not unbalance the scan. The result is not validated as JSON; callers must
// still unmarshal it.
func ExtractBalancedJSON(s string) string {
	objStart := strings.IndexByte(s, '{')
	arrStart := strings.IndexByte(s, '[')
	var open, closeCh byte
	var start int
	switch {
	case objStart < 0 && arrStart < 0:
		return ""
	case objStart < 0:
		open, closeCh, start = '[', ']', arrStart
	case arrStart < 0:
		open, closeCh, start = '{', '}', objStart
	case objStart < arrStart:
		open, closeCh, start = '{', '}', objStart
	default:
		open, closeCh, start = '[', ']', arrStart
	}

	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case open:
			depth++
		case closeCh:
			depth--
			if depth == 0 {
				return strings.TrimSpace(s[start : i+1])
			}
		}
	}
	return ""
}
