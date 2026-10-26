package pipeline

import (
	"context"
	"encoding/json"
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

// generateJSON 先解析模型原文，再还原结构化字符串中的图片地址，避免地址破坏 JSON 转义。
func (s *liteService) generateJSON(ctx context.Context, kind, sourceID string, sourceIDs []string, tpl string, data map[string]string, target any) error {
	masked, urlMap := maskTemplateDataImageURLs(data)
	prompt, err := wiki.RenderLitePrompt(tpl, masked)
	if err != nil {
		return err
	}
	raw, err := s.generatePrompt(ctx, kind, sourceID, sourceIDs, prompt)
	if err != nil {
		return err
	}
	if err = json.Unmarshal([]byte(cleanLLMJSON(raw)), target); err != nil {
		stageSourceID := ""
		if len(sourceIDs) == 1 {
			stageSourceID = sourceIDs[0]
		}
		if recordErr := s.markParseError(ctx, kind, stageSourceID, raw, err); recordErr != nil {
			return recordErr
		}
		return invalid("parse " + kind + " JSON: " + err.Error())
	}
	if len(urlMap) == 0 {
		return nil
	}
	// 仅在已验证的目标类型上恢复地址，保留调用方既有字段和数字类型。
	// 地址先编码成 JSON 字符串片段，因此任意嵌套值中的引号、反斜线和控制字符均不会破坏结构。
	encoded, err := json.Marshal(target)
	if err != nil {
		return err
	}
	escapedURLs := make(map[string]string, len(urlMap))
	for token, url := range urlMap {
		escaped, _ := json.Marshal(url)
		escapedURLs[token] = string(escaped[1 : len(escaped)-1])
	}
	return json.Unmarshal([]byte(unmaskImageURLs(string(encoded), escapedURLs)), target)
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
	if st.State == "finished" && st.Outcome == "succeeded" {
		return st.ResultText, nil
	}
	result := GenerateResult{}
	var callErr error
	var totalUsage *types.ModelUsage
	var output strings.Builder
	canContinue := kind == "reduce" || kind == "summary"
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
		if round > 0 && strings.TrimSpace(result.Text) == "" {
			callErr = invalid("model returned empty continuation for a truncated wiki page")
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
	if callErr == nil && !canContinue && isLengthStop(result.FinishReason) {
		callErr = invalid(kind + " response was truncated")
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
		return "", &modelCallError{cause: callErr}
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

func (s *liteService) markParseError(ctx context.Context, kind, sourceID, raw string, cause error) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	return s.store.RecordModelParseFailure(cleanup, s.job.ID, kind, sourceID, raw, cause.Error())
}

func maxTokensForStage(kind string) int {
	if kind == "document_summary" {
		return 1024
	}
	return wikiLLMMaxTokens
}

// modelCallError 区分模型调用失败与阶段记录或页面存储失败，决定是否值得再次消耗模型预算。
type modelCallError struct {
	cause error // 模型返回的原始失败，保留类型与诊断。
}

func (e *modelCallError) Error() string { return e.cause.Error() }
func (e *modelCallError) Unwrap() error { return e.cause }
