package pipeline

import (
	"encoding/json"
	"errors"
	"strings"

	types "github.com/JieWaZi/neux-knowledge/internal/domain"
)

var (
	errEmptySummaryOutput       = invalid("summary model returned empty output")
	errSummaryOutputNotParsable = invalid("summary model returned JSON without a usable summary")
)

// documentSummaryResult is what one profiling call yields: the short summary
// stored in knowledge.Description plus the structured profile, when the model
// returned parseable JSON.
type documentSummaryResult struct {
	// Summary is the generated short document summary.
	Summary string
	// Profile is the optional structured document profile.
	Profile *types.KnowledgeProfile
}

// documentProfileOutput mirrors the JSON contract of generate_summary.yaml.
type documentProfileOutput struct {
	// Summary is the generated short document summary.
	Summary string `json:"summary"`
	// Gist describes the document's main subject.
	Gist string `json:"gist"`
	// Topics contains the document's topic keywords.
	Topics []string `json:"topics"`
	// DocType is the document type.
	DocType string `json:"doc_type"`
	// TypicalQuestion is a representative question the document can answer.
	TypicalQuestion string `json:"typical_question"`
}

// normalizeSummaryReplyContent strips a UTF-8 BOM, the surrounding whitespace
// and one markdown code fence from a summary reply, so shape detection and
// parsing look at the same text. A fence without a newline carries no payload
// we can classify, so it normalizes to the empty string.
func normalizeSummaryReplyContent(content string) string {
	content = strings.TrimSpace(strings.TrimPrefix(content, "\uFEFF"))
	if rest, ok := strings.CutPrefix(content, "```"); ok {
		// Drop the fence's info string (```json) and the newline after it.
		idx := strings.IndexByte(rest, '\n')
		if idx < 0 {
			return ""
		}
		content = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(rest[idx+1:]), "```"))
	}
	return content
}

// summaryReplyIsJSONShaped reports whether the reply really is a structured
// JSON payload, either directly or inside a markdown code fence. Only such
// replies are held to the structured contract below; anything else is a
// legacy plain-text summary.
//
// An object opening ("{") always counts, parseable or not: half an object is
// exactly the reply that must never be stored as a summary. A bracket opening
// is ambiguous — "[1] …" and "[Document summary] …" are prose with a leading citation,
// not broken JSON — so it only counts when the reply is a JSON array or opens
// an array of objects/strings.
func summaryReplyIsJSONShaped(content string) bool {
	content = normalizeSummaryReplyContent(content)
	if content == "" {
		return false
	}
	switch content[0] {
	case '{':
		return true
	case '[':
		var arr []json.RawMessage
		if json.Unmarshal([]byte(content), &arr) == nil {
			return true
		}
		first := strings.TrimLeft(content[1:], " \t\r\n")
		return strings.HasPrefix(first, "{") ||
			strings.HasPrefix(first, "[") ||
			strings.HasPrefix(first, `"`)
	default:
		return false
	}
}

// parseDocumentSummaryOutput accepts both the structured JSON reply and a
// legacy plain-text summary (custom templates, older models). Plain text is
// stored as the summary with no profile, so nothing that worked before
// regresses; only the knowledge-base aggregation loses that document's topics.
//
// A reply that opens like JSON is held to the JSON contract instead: a broken
// object (output cut off at MaxTokens, an unescaped newline inside a string)
// or an object with no usable text returns errSummaryOutputNotParsable. The
// raw reply used to be stored as the summary, which put half a JSON object in
// knowledges.description, marked the summary completed so Asynq never retried
// it, and embedded the same fragment into the RAG index.
func parseDocumentSummaryOutput(content string) (*documentSummaryResult, error) {
	// Some providers prefix the reply with a UTF-8 BOM; TrimSpace alone would
	// leave it in place, hide the "{" and let half an object through as prose.
	content = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(content), "\uFEFF"))
	if content == "" {
		return nil, errEmptySummaryOutput
	}
	jsonShaped := summaryReplyIsJSONShaped(content)
	var out documentProfileOutput
	if err := ParseLLMJsonResponse(content, &out); err != nil {
		if jsonShaped {
			return nil, errors.Join(errSummaryOutputNotParsable, err)
		}
		// Legacy plain text: keep it as the summary with no profile.
		return &documentSummaryResult{Summary: content}, nil
	}
	summary := strings.TrimSpace(out.Summary)
	profile := (&types.KnowledgeProfile{
		Gist:            out.Gist,
		Topics:          out.Topics,
		DocType:         out.DocType,
		TypicalQuestion: out.TypicalQuestion,
	}).Normalize()
	if summary == "" && profile != nil {
		summary = profile.Gist
	}
	if summary == "" {
		if jsonShaped {
			// JSON without usable text is as unusable as broken JSON: the
			// caller must fail the attempt instead of storing the object.
			return nil, errSummaryOutputNotParsable
		}
		// Plain text that happened to contain balanced JSON punctuation.
		return &documentSummaryResult{Summary: content}, nil
	}
	return &documentSummaryResult{Summary: summary, Profile: profile}, nil
}
