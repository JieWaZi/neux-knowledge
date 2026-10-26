package pipeline

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	types "github.com/JieWaZi/neux-knowledge/internal/domain"
	"github.com/JieWaZi/neux-knowledge/internal/wiki"
)

// TestDocumentSummaryParseFailureInvalidatesRawCache 普通摘要解析失败后，同一尝试重新进入必须取得新响应并保留真实失败阶段。
func TestDocumentSummaryParseFailureInvalidatesRawCache(t *testing.T) {
	ctx := context.Background()
	s, j, sources := contractFixture(t, 1)
	if err := s.EnsureOrganizeSourceWork(ctx, j.ID); err != nil {
		t.Fatal(err)
	}
	lib, err := s.GetLibrary(ctx, j.LibraryID)
	if err != nil {
		t.Fatal(err)
	}
	lib.IndexingStrategy.WikiEnabled = false
	n := 0
	model := &contractModel{respond: func(r GenerateRequest) (GenerateResult, error) {
		n++
		if n == 1 {
			return GenerateResult{Text: `{"summary":`, FinishReason: "stop"}, nil
		}
		return GenerateResult{Text: `{"summary":"有效文档摘要"}`, FinishReason: "stop"}, nil
	}}
	svc := &liteService{store: s, job: j, library: lib, input: Input{Reader: MarkdownReader{}, Generator: model, PrepareDocument: func(_ context.Context, _ string, a ReadArtifact) (ReadArtifact, error) { return a, nil }}}
	work, err := s.ListOrganizeSourceWork(ctx, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.mapDocument(ctx, work[0], nil); err == nil {
		t.Fatal("first malformed response unexpectedly accepted")
	}
	if _, err := svc.mapDocument(ctx, work[0], nil); err != nil {
		t.Errorf("retry must consume corrected model reply instead of cached parse failure: calls=%d error=%v source=%s", n, err, sources[0].ID)
	}
	if n != 2 {
		t.Errorf("model calls=%d, want 2", n)
	}
}

// TestWikiSummaryLengthLimitKeepsTruncatedContentUnpublished Wiki 摘要耗尽续写仍被截断时，不发布片段或把来源标记为完成。
func TestWikiSummaryLengthLimitKeepsTruncatedContentUnpublished(t *testing.T) {
	s, j, _ := contractFixture(t, 1)
	model := &contractModel{respond: func(r GenerateRequest) (GenerateResult, error) {
		if r.Stage == "summary" {
			return GenerateResult{Text: "SUMMARY: 文档摘要\n# 来源摘要\n| 条件 | 行为 |\n|---|---|\n| 在线 | 校", FinishReason: "length"}, nil
		}
		return normalReply(r)
	}}
	result, err := Run(context.Background(), s, Input{JobID: j.ID, Generator: model})
	if err != nil {
		t.Fatal(err)
	}
	if result.State == "completed" {
		t.Error("truncated wiki summary incorrectly completed and published")
	}
	stages, _ := s.ListStages(context.Background(), j.ID)
	for _, st := range stages {
		if st.Kind == "summary" && st.Outcome == "succeeded" {
			t.Errorf("truncated summary succeeded: completeness=%s result=%q", st.Completeness, st.ResultText)
		}
	}
}

// TestMapAcceptsBOMFencedJSON Map 与普通摘要都接受 BOM 包裹的标准 JSON 代码块。
func TestMapAcceptsBOMFencedJSON(t *testing.T) {
	raw := "\uFEFF```json\n{\"entities\":[],\"concepts\":[]}\n```"
	var got combinedExtraction
	if err := ParseLLMJsonResponse(raw, &got); err != nil {
		t.Fatalf("shared JSON parser rejected wrapper: %v", err)
	}
	if err := json.Unmarshal([]byte(cleanLLMJSON(raw)), &got); err != nil {
		t.Errorf("map parser rejected valid model JSON wrapper: %v", err)
	}
}

// TestMapBOMFencedJSONCompletesOrganization 合法 Map 结果带 BOM 和代码块时，完整整理仍能成功。
func TestMapBOMFencedJSONCompletesOrganization(t *testing.T) {
	s, j, _ := contractFixture(t, 1)
	model := &contractModel{respond: func(r GenerateRequest) (GenerateResult, error) {
		out, err := normalReply(r)
		if r.Stage == "map" {
			out.Text = "\uFEFF```json\n" + out.Text + "\n```"
		}
		return out, err
	}}
	result, err := Run(context.Background(), s, Input{JobID: j.ID, Generator: model})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != "completed" {
		t.Errorf("valid JSON with BOM+fence incorrectly failed locally: state=%s", result.State)
	}
}

// TestMapParseFailureInvalidatesRawStage 普通 Map 解析失败会让同输入重新请求模型，而不是命中坏缓存。
func TestMapParseFailureInvalidatesRawStage(t *testing.T) {
	ctx := context.Background()
	s, j, sources := contractFixture(t, 1)
	lib, err := s.GetLibrary(ctx, j.LibraryID)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	model := &contractModel{respond: func(r GenerateRequest) (GenerateResult, error) {
		n++
		if n == 1 {
			return GenerateResult{Text: `{"entities":`, FinishReason: "stop"}, nil
		}
		return normalReply(r)
	}}
	svc := &liteService{store: s, job: j, library: lib, input: Input{Generator: model}}
	prompt := wiki.StagePrompt{User: "same immutable test input"}
	raw, err := svc.generatePrompt(ctx, "map", sources[0].ID, []string{sources[0].ID}, prompt)
	if err != nil {
		t.Fatal(err)
	}
	var got combinedExtraction
	err = json.Unmarshal([]byte(cleanLLMJSON(raw)), &got)
	if err == nil {
		t.Fatal("test malformed JSON parsed")
	}
	svc.markParseError(ctx, "map", sources[0].ID, raw, err)
	raw, err = svc.generatePrompt(ctx, "map", sources[0].ID, []string{sources[0].ID}, prompt)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("invalid stage reused: calls=%d raw=%s", n, raw)
	}
}

// TestEmptyContinuationRejectsTruncatedPrefix 空续写不能把先前已知截断的页面前缀升级为完整结果。
func TestEmptyContinuationRejectsTruncatedPrefix(t *testing.T) {
	ctx := context.Background()
	s, j, sources := contractFixture(t, 1)
	n := 0
	model := &contractModel{respond: func(r GenerateRequest) (GenerateResult, error) {
		n++
		if n == 1 {
			return GenerateResult{Text: "SUMMARY: 提示\n# 页面\n未完待", FinishReason: "length"}, nil
		}
		return GenerateResult{Text: "", FinishReason: "stop"}, nil
	}}
	svc := &liteService{store: s, job: j, input: Input{Generator: model}}
	raw, err := svc.generatePrompt(ctx, "reduce", sources[0].ID, []string{sources[0].ID}, wiki.StagePrompt{User: "rewrite"})
	if err == nil {
		t.Errorf("empty continuation validated known partial prefix: %q", raw)
	}
}

// TestMapRestoresImageURLAfterParsingJSON 原件图片地址含引号时，先解析模型 JSON，再无损还原详情与别名中的嵌套字符串。
func TestMapRestoresImageURLAfterParsingJSON(t *testing.T) {
	ctx := context.Background()
	s, j, sources := contractFixture(t, 1)
	url := `https://example.com/image.svg?label="RAG"`
	model := &contractModel{respond: func(r GenerateRequest) (GenerateResult, error) {
		return GenerateResult{Text: `{"entities":[],"concepts":[{"name":"架构","slug":"concept/arch","aliases":["![别名图](wkimg:0001)"],"description":"架构说明","details":"![图](wkimg:0001)"}]}`, FinishReason: "stop"}, nil
	}}
	lib, err := s.GetLibrary(ctx, j.LibraryID)
	if err != nil {
		t.Fatal(err)
	}
	svc := &liteService{store: s, job: j, library: lib, input: Input{Generator: model}}
	_, concepts, _, err := svc.extractUpstream(ctx, sources[0], "![图]("+url+")", "Chinese", nil, nil, svc.newBatchContext(nil), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(concepts) != 1 || concepts[0].Details != "![图]("+url+")" || len(concepts[0].Aliases) != 1 || concepts[0].Aliases[0] != "![别名图]("+url+")" {
		t.Fatalf("restored nested strings=%+v", concepts)
	}
}

// TestMapInvalidImageJSONDoesNotReuseFailedRaw 图片占位符保留在失败原文中，解析失败仍准确失效同尝试缓存。
func TestMapInvalidImageJSONDoesNotReuseFailedRaw(t *testing.T) {
	ctx := context.Background()
	s, j, sources := contractFixture(t, 1)
	n := 0
	model := &contractModel{respond: func(r GenerateRequest) (GenerateResult, error) {
		n++
		if n == 1 {
			return GenerateResult{Text: `{"entities":[],"concepts":[{"details":"![图](wkimg:0001)"}`, FinishReason: "stop"}, nil
		}
		return normalReply(r)
	}}
	lib, err := s.GetLibrary(ctx, j.LibraryID)
	if err != nil {
		t.Fatal(err)
	}
	svc := &liteService{store: s, job: j, library: lib, input: Input{Generator: model}}
	content := `![图](https://example.com/image.svg?label="RAG")`
	if _, _, _, err := svc.extractUpstream(ctx, sources[0], content, "Chinese", nil, nil, svc.newBatchContext(nil), true); err == nil {
		t.Fatal("malformed model JSON accepted")
	}
	if _, _, _, err := svc.extractUpstream(ctx, sources[0], content, "Chinese", nil, nil, svc.newBatchContext(nil), true); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("known parse failure reused: model calls=%d", n)
	}
	stages, err := s.ListStages(ctx, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	failed := false
	for _, st := range stages {
		if st.Kind == "map" && st.Outcome == "failed" && strings.Contains(st.ResultText, "wkimg:0001") {
			failed = true
		}
	}
	if !failed {
		t.Fatal("raw failed model response not retained")
	}
}

// TestWikiSummaryLengthContinuesToCompletePage Wiki 摘要截断后携带本次片段续写，完成的正文才允许发布。
func TestWikiSummaryLengthContinuesToCompletePage(t *testing.T) {
	s, j, sources := contractFixture(t, 1)
	n := 0
	model := &contractModel{respond: func(r GenerateRequest) (GenerateResult, error) {
		if r.Stage == "summary" {
			n++
			if n == 1 {
				return GenerateResult{Text: "SUMMARY: 完整摘要\n# 来源摘要\n第一段", FinishReason: "length"}, nil
			}
			if len(r.Messages) != 3 || r.Messages[1].Role != "assistant" || !strings.Contains(r.Messages[1].Content, "第一段") {
				t.Errorf("missing summary continuation context: %+v", r.Messages)
			}
			return GenerateResult{Text: "与第二段。", FinishReason: "stop"}, nil
		}
		return normalReply(r)
	}}
	result, err := Run(context.Background(), s, Input{JobID: j.ID, Generator: model})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != "completed" {
		t.Fatalf("state=%s", result.State)
	}
	page, err := s.GetPageBySlug(context.Background(), j.LibraryID, "summary/"+sources[0].SourceID)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 || page.Content != "# 来源摘要\n第一段与第二段。" {
		t.Fatalf("summary calls=%d content=%q", n, page.Content)
	}
}

// TestTaxonomyParseFailureInvalidatesSingleSourceCache 验证目录调用未填单来源请求字段时，仍按真实阶段来源失效缓存。
func TestTaxonomyParseFailureInvalidatesSingleSourceCache(t *testing.T) {
	ctx := context.Background()
	s, j, sources := contractFixture(t, 1)
	n := 0
	model := &contractModel{respond: func(r GenerateRequest) (GenerateResult, error) {
		n++
		if n == 1 {
			return GenerateResult{Text: `{"assignments":"invalid-shape"}`, FinishReason: "stop"}, nil
		}
		return GenerateResult{Text: `{"assignments":[{"slug":"concept/session","path":["账号"]}]}`, FinishReason: "stop"}, nil
	}}
	svc := &liteService{store: s, job: j, input: Input{Generator: model}}
	var planned taxonomyAssignments
	if err := svc.generateJSON(ctx, "taxonomy", "", []string{sources[0].ID}, "same directory request", nil, &planned); err == nil {
		t.Fatal("incorrect assignment shape accepted")
	}
	if err := svc.generateJSON(ctx, "taxonomy", "", []string{sources[0].ID}, "same directory request", nil, &planned); err != nil {
		t.Fatal(err)
	}
	if n != 2 || len(planned.paths()["concept/session"]) != 1 {
		t.Fatalf("model calls=%d assignments=%+v", n, planned.paths())
	}
}

// TestStructuredImageRestorePreservesJSONNumbers 验证结构化图片地址还原不把大整数转换成浮点数，也不改写其他 JSON 字段。
func TestStructuredImageRestorePreservesJSONNumbers(t *testing.T) {
	ctx := context.Background()
	s, j, sources := contractFixture(t, 1)
	model := &contractModel{respond: func(r GenerateRequest) (GenerateResult, error) {
		return GenerateResult{Text: `{"number":9007199254740993,"nested":{"image":"![图](wkimg:0001)","flag":true}}`, FinishReason: "stop"}, nil
	}}
	svc := &liteService{store: s, job: j, input: Input{Generator: model}}
	var target map[string]json.RawMessage
	err := svc.generateJSON(ctx, "map", sources[0].ID, []string{sources[0].ID}, "{{.Content}}", map[string]string{"Content": `![图](https://example.com/a?label="RAG")`}, &target)
	if err != nil {
		t.Fatal(err)
	}
	if string(target["number"]) != "9007199254740993" {
		t.Fatalf("number changed: %s", target["number"])
	}
	var nested map[string]any
	if err = json.Unmarshal(target["nested"], &nested); err != nil {
		t.Fatal(err)
	}
	if nested["image"] != `![图](https://example.com/a?label="RAG")` || nested["flag"] != true {
		t.Fatalf("nested value changed: %+v", nested)
	}
}

// TestLegacyTruncatedDocumentPlanIsRebuilt 验证缺少完整输出验证版本的旧计划不会把截断摘要送往发布，损坏来源身份也不能借重组绕过校验。
func TestLegacyTruncatedDocumentPlanIsRebuilt(t *testing.T) {
	for _, mode := range []string{"truncated", "wrong-source"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			s, j, sources := contractFixture(t, 1)
			if err := s.EnsureOrganizeSourceWork(ctx, j.ID); err != nil {
				t.Fatal(err)
			}
			lib, err := s.GetLibrary(ctx, j.LibraryID)
			if err != nil {
				t.Fatal(err)
			}
			source := sources[0]
			raw, err := s.ReadRaw(ctx, j.LibraryID, source.ID)
			if err != nil {
				t.Fatal(err)
			}
			artifact, err := (MarkdownReader{}).Read(ctx, ReadRequest{SourceID: source.SourceID, SourceVersionID: source.ID, SHA256: source.SHA256, MIME: source.MIME, Filename: source.Filename, Raw: raw, Scope: "all"})
			if err != nil {
				t.Fatal(err)
			}
			if mode == "wrong-source" {
				artifact.SHA256 = "different-source"
			}
			slug := "summary/" + source.SourceID
			page, _ := newPage(j.LibraryID, slug, "summary", nil)
			page.Title = source.Filename
			page.Content = "# 旧截断摘要\n未完待"
			page.Summary = "旧截断摘要"
			legacy := documentPlan{Artifact: artifact, Summary: types.PageMutation{Page: page}, Updates: map[string][]SlugUpdate{slug: {{Slug: slug, Type: "summary", KnowledgeID: source.SourceID, SourceVersionID: source.ID, SummaryLine: page.Summary, SummaryBody: page.Content}}}}
			if err = s.SaveLitePlan(ctx, j.ID, j.Attempt, source.ID, string(marshal(legacy))); err != nil {
				t.Fatal(err)
			}
			model := &contractModel{respond: normalReply}
			svc := &liteService{store: s, job: j, library: lib, input: Input{Reader: MarkdownReader{}, Generator: model}}
			work, err := s.ListOrganizeSourceWork(ctx, j.ID)
			if err != nil {
				t.Fatal(err)
			}
			result, err := svc.mapDocument(ctx, work[0], nil)
			if mode == "wrong-source" {
				if err == nil || len(model.calls) != 0 {
					t.Fatalf("corrupt legacy identity was hidden by regeneration: err=%v calls=%d", err, len(model.calls))
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(model.calls) == 0 || result.summary.Page.Content != "# 文档摘要\n[[concept/session|会话管理]]区分在线与离线。" {
				t.Fatalf("truncated plan reused: calls=%d content=%q", len(model.calls), result.summary.Page.Content)
			}
		})
	}
}

// TestValidatedDocumentPlanReusesCompletedModelResults 验证新生成的完整计划可直接恢复，不再次读取原件或消耗模型请求。
func TestValidatedDocumentPlanReusesCompletedModelResults(t *testing.T) {
	ctx := context.Background()
	s, j, _ := contractFixture(t, 1)
	if err := s.EnsureOrganizeSourceWork(ctx, j.ID); err != nil {
		t.Fatal(err)
	}
	lib, err := s.GetLibrary(ctx, j.LibraryID)
	if err != nil {
		t.Fatal(err)
	}
	model := &contractModel{respond: normalReply}
	svc := &liteService{store: s, job: j, library: lib, input: Input{Reader: MarkdownReader{}, Generator: model}}
	work, err := s.ListOrganizeSourceWork(ctx, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	first, err := svc.mapDocument(ctx, work[0], nil)
	if err != nil {
		t.Fatal(err)
	}
	priorCalls := len(model.calls)
	svc.input.Reader = nil
	restored, err := svc.mapDocument(ctx, work[0], nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(model.calls) != priorCalls || restored.summary.Page.Content != first.summary.Page.Content {
		t.Fatalf("complete plan regenerated or changed: calls=%d before=%d content=%q", len(model.calls), priorCalls, restored.summary.Page.Content)
	}
}

// TestStructuredStagesRejectLengthStoppedJSON 验证可解析但已被模型标为截断的结构化响应仍不可流出，原文以失败和截断诊断保留。
func TestStructuredStagesRejectLengthStoppedJSON(t *testing.T) {
	for _, kind := range []string{"map", "cite", "dedup", "taxonomy"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			s, j, sources := contractFixture(t, 1)
			model := &contractModel{respond: func(r GenerateRequest) (GenerateResult, error) {
				return GenerateResult{Text: `{"entities":[],"concepts":[],"citations":{},"new_slugs":[],"merges":{},"assignments":[]}`, FinishReason: "length"}, nil
			}}
			svc := &liteService{store: s, job: j, input: Input{Generator: model}}
			output, err := svc.generatePrompt(ctx, kind, sources[0].ID, []string{sources[0].ID}, wiki.StagePrompt{User: "structured stage"})
			if err == nil || output != "" {
				t.Fatalf("truncated JSON flowed out as success: kind=%s output=%q err=%v", kind, output, err)
			}
			stages, err := s.ListStages(ctx, j.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(stages) != 1 || stages[0].Kind != kind || stages[0].Outcome != "failed" || stages[0].Completeness != "truncated" || stages[0].ResultText == "" {
				t.Fatalf("truncated raw response not recorded accurately: %+v", stages)
			}
		})
	}
}

// TestLengthStoppedMapDoesNotFreezeValidatedPlan 验证候选与旧提取器均只返回截断 JSON 时，不冻结带当前验证版本的文档计划。
func TestLengthStoppedMapDoesNotFreezeValidatedPlan(t *testing.T) {
	ctx := context.Background()
	s, j, sources := contractFixture(t, 1)
	model := &contractModel{respond: func(r GenerateRequest) (GenerateResult, error) {
		if r.Stage == "map" {
			return GenerateResult{Text: `{"entities":[],"concepts":[]}`, FinishReason: "length"}, nil
		}
		return normalReply(r)
	}}
	result, err := Run(ctx, s, Input{JobID: j.ID, Generator: model})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != "failed" {
		t.Fatalf("truncated extraction completed: %s", result.State)
	}
	payload, err := s.LoadLitePlan(ctx, j.ID, j.Attempt, sources[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if payload != "" {
		t.Fatalf("truncated extraction froze a verified plan: %s", payload)
	}
}
