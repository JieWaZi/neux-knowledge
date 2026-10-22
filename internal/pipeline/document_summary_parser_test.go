package pipeline

import (
	types "github.com/JieWaZi/neux-knowledge/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestParseDocumentSummaryOutput(t *testing.T) {
	fullJSON := `{"summary":"Two sentences.","gist":"A gist","topics":["a","b"],` +
		`"doc_type":"policy","typical_question":"Why?"}`
	fullProfile := &types.KnowledgeProfile{
		Gist:            "A gist",
		Topics:          []string{"a", "b"},
		DocType:         "policy",
		TypicalQuestion: "Why?",
	}
	// The reported failure shape: a 209-rune reply cut off at MaxTokens in the
	// middle of a string. It used to be stored raw as knowledge.Description,
	// marked completed (so Asynq never retried) and embedded as the summary chunk.
	truncatedJSON := `{"summary": "` + strings.Repeat("截", 130) + `", "gist": "` + strings.Repeat("断", 54)

	tests := []struct {
		// name identifies the test scenario.
		name string
		// content is the raw model output.
		content string
		// want is the document summary expected to be saved.
		want string
		// profile is the normalized source profile expected to be saved.
		profile *types.KnowledgeProfile
		// wantErr is the stable error expected when rejecting this output.
		wantErr error
	}{
		{
			name:    "structured JSON keeps summary and profile",
			content: fullJSON,
			want:    "Two sentences.",
			profile: fullProfile,
		},
		{
			name:    "fenced JSON keeps summary and profile",
			content: "```json\n" + fullJSON + "\n```",
			want:    "Two sentences.",
			profile: fullProfile,
		},
		{
			name:    "gist fills a missing summary",
			content: `{"gist":"Only a gist","topics":["x"]}`,
			want:    "Only a gist",
			profile: &types.KnowledgeProfile{Gist: "Only a gist", Topics: []string{"x"}},
		},
		{
			// The template's refusal reply is valid JSON with usable text, so
			// it keeps working (it carries no profile fields).
			name: "JSON refusal with an empty profile is kept",
			content: `{"summary": "No textual content was extractable from this document.",` +
				` "gist": "", "topics": [], "doc_type": "", "typical_question": ""}`,
			want: "No textual content was extractable from this document.",
		},
		{
			name:    "truncated JSON is rejected, never stored raw",
			content: truncatedJSON,
			wantErr: errSummaryOutputNotParsable,
		},
		{
			// A UTF-8 BOM is not whitespace: before the shape check stripped
			// it, the leading "{" stayed hidden and this half object was
			// handed back as the summary.
			name:    "BOM-prefixed truncated JSON is rejected",
			content: "\uFEFF" + truncatedJSON,
			wantErr: errSummaryOutputNotParsable,
		},
		{
			name:    "BOM-prefixed structured JSON still parses",
			content: "\uFEFF" + fullJSON,
			want:    "Two sentences.",
			profile: fullProfile,
		},
		{
			// Prose that opens with a bracketed citation is not broken JSON.
			name:    "bracketed prose keeps the legacy fallback",
			content: "[文档摘要] 本文件说明请假制度与年度额度。",
			want:    "[文档摘要] 本文件说明请假制度与年度额度。",
		},
		{
			name:    "numbered prose keeps the legacy fallback",
			content: "[1] 本文件说明请假制度与年度额度。",
			want:    "[1] 本文件说明请假制度与年度额度。",
		},
		{
			// A real JSON array is structured output that does not satisfy the
			// object contract, so it is rejected rather than stored raw.
			name:    "JSON array without the object contract is rejected",
			content: `["a","b"]`,
			wantErr: errSummaryOutputNotParsable,
		},
		{
			name:    "truncated JSON array is rejected",
			content: `[{"summary": "half a doc`,
			wantErr: errSummaryOutputNotParsable,
		},
		{
			name:    "JSON with an unescaped newline is rejected",
			content: "{\"summary\": \"First line\nSecond line\", \"gist\": \"g\"}",
			wantErr: errSummaryOutputNotParsable,
		},
		{
			name:    "JSON without summary or gist is rejected",
			content: `{"topics":["a"],"doc_type":"policy"}`,
			wantErr: errSummaryOutputNotParsable,
		},
		{
			name:    "whitespace-only reply is rejected",
			content: " \n\t ",
			wantErr: errEmptySummaryOutput,
		},
		{
			// Custom templates and older models answer in prose; that fallback
			// is deliberate and must survive.
			name:    "plain text keeps the legacy fallback",
			content: "  This document explains leave policy.  ",
			want:    "This document explains leave policy.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := parseDocumentSummaryOutput(tt.content)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				require.Nil(t, out, "a rejected reply must never be handed back as a summary")
				return
			}
			require.NoError(t, err)
			require.NotNil(t, out)
			assert.Equal(t, tt.want, out.Summary)
			assert.Equal(t, tt.profile, out.Profile)
		})
	}
}
