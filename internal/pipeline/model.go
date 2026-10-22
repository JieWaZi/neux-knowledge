package pipeline

import (
	"context"
	"strings"
	"time"

	types "github.com/JieWaZi/neux-knowledge/internal/domain"
	"github.com/JieWaZi/neux-knowledge/internal/wiki"
)

func (s *liteService) generate(ctx context.Context, stage, sourceID string, sourceIDs []string, tpl string, data map[string]string) (string, error) {
	masked, urlMap := maskTemplateDataImageURLs(data)
	prompt, err := wiki.RenderLitePrompt(tpl, masked)
	if err != nil {
		return "", err
	}
	output, err := s.generatePrompt(ctx, stage, sourceID, sourceIDs, prompt)
	return unmaskImageURLs(output, urlMap), err
}
func (s *liteService) generatePrompt(ctx context.Context, kind, sourceID string, sourceIDs []string, prompt wiki.StagePrompt) (string, error) {
	messages := []ModelMessage{}
	if prompt.System != "" {
		messages = append(messages, ModelMessage{Role: "system", Content: prompt.System})
	}
	messages = append(messages, ModelMessage{Role: "user", Content: prompt.User})
	input := marshal(messages)
	name := kind + "/" + hash(marshal(sourceIDs)) + "/" + hash(input)
	st, err := s.store.BeginModelStage(ctx, s.job.ID, s.job.Attempt, name, kind, sourceIDs, input)
	if err != nil {
		return "", err
	}
	result := GenerateResult{}
	var callErr error
	var totalUsage *types.ModelUsage
	var output strings.Builder
	canContinue := kind == "reduce"
	for round := 0; round <= 3; round++ {
		for attempt := 1; attempt <= wikiLLMMaxAttempts; attempt++ {
			req := GenerateRequest{JobID: s.job.ID, SourceVersionID: sourceID, StageID: st.ID, Stage: kind, Messages: append([]ModelMessage{}, messages...), Temperature: .3, Thinking: false, MaxTokens: maxTokensForStage(kind)}
			result, callErr = s.input.Generator.Generate(ctx, req)
			if result.Usage != nil {
				if totalUsage == nil {
					totalUsage = &types.ModelUsage{}
				}
				totalUsage.InputTokens += result.Usage.InputTokens
				totalUsage.OutputTokens += result.Usage.OutputTokens
				totalUsage.TotalTokens += result.Usage.TotalTokens
			}
			if callErr == nil {
				break
			}
			if !isTransientLLMError(ctx, callErr) || attempt == wikiLLMMaxAttempts {
				break
			}
			if callErr = waitContext(ctx, wikiLLMBackoffBase<<uint(attempt-1)); callErr != nil {
				break
			}
		}
		if callErr != nil {
			output.WriteString(result.Text)
			break
		}
		if round > 0 && strings.EqualFold(strings.TrimSpace(result.Text), "(complete)") {
			result.FinishReason = "stop"
			break
		}
		output.WriteString(result.Text)
		if !canContinue || !isLengthStop(result.FinishReason) {
			break
		}
		if round == 3 {
			callErr = invalid("wiki page rewrite still truncated after three continuations")
			break
		}
		messages = append(messages, ModelMessage{Role: "assistant", Content: result.Text}, ModelMessage{Role: "user", Content: wiki.WikiPageModifyContinuationPrompt})
	}
	if callErr == nil && kind == "document_summary" && isLengthStop(result.FinishReason) {
		callErr = invalid("document summary response was truncated")
	}
	if callErr == nil && strings.TrimSpace(output.String()) == "" {
		callErr = invalid("model returned empty output")
	}
	diagnostic := ""
	if callErr != nil {
		diagnostic = callErr.Error()
	}
	// Cancellation must retain diagnostics already returned; bounded cleanup runs independently of the cancelled work context.
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	finishErr := s.store.FinishModelStage(cleanup, st.ID, output.String(), result.FinishReason, diagnostic, totalUsage, result.Diagnostics)
	if callErr != nil {
		return "", callErr
	}
	if finishErr != nil {
		return "", finishErr
	}
	return output.String(), nil
}
func isLengthStop(reason string) bool {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "length", "max_tokens", "max_output_tokens":
		return true
	}
	return false
}

func (s *liteService) markParseError(ctx context.Context, kind, sourceID, raw string, cause error) {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	_ = s.store.RecordModelParseFailure(cleanup, s.job.ID, kind, sourceID, raw, cause.Error())
}

func maxTokensForStage(kind string) int {
	if kind == "document_summary" {
		return 1024
	}
	return wikiLLMMaxTokens
}
