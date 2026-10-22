package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	types "github.com/JieWaZi/neux-knowledge/internal/domain"
	"github.com/JieWaZi/neux-knowledge/internal/store"
)

// contractModel is the only external model stub; the database and full pipeline use real implementations.
type contractModel struct {
	mu      sync.Mutex                                    // Protects observations of concurrent calls.
	calls   []GenerateRequest                             // Actual core requests.
	respond func(GenerateRequest) (GenerateResult, error) // Controlled native model responses.
}

func (g *contractModel) Generate(_ context.Context, r GenerateRequest) (GenerateResult, error) {
	g.mu.Lock()
	g.calls = append(g.calls, r)
	g.mu.Unlock()
	return g.respond(r)
}
func contractFixture(t *testing.T, count int, raw ...[]byte) (*store.Store, *types.Job, []*types.SourceVersion) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	s, err := store.Open(ctx, filepath.Join(dir, "core.db"), filepath.Join(dir, "raw"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	rec, err := s.CreateLibrary(ctx, types.CreateLibraryInput{RequestID: "library", Name: "实际资料库", Kind: "general", WikiLanguage: "zh-CN"})
	if err != nil {
		t.Fatal(err)
	}
	l, err := s.GetLibrary(ctx, rec.ResourceID)
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	sources := []*types.SourceVersion{}
	for i := 0; i < count; i++ {
		body := []byte(fmt.Sprintf("# 会话管理%d\n服务端校验在线会话，离线会话遵循单独有效条件。", i))
		if len(raw) > 0 {
			body = raw[0]
		}
		rec, err = s.ImportSource(ctx, types.ImportSourceInput{RequestID: fmt.Sprintf("source%d", i), LibraryID: l.ID, Filename: fmt.Sprintf("文档%d.md", i), MIME: "text/markdown", Bytes: body})
		if err != nil {
			t.Fatal(err)
		}
		source, e := s.GetSource(ctx, l.ID, rec.ResourceID)
		if e != nil {
			t.Fatal(e)
		}
		sources = append(sources, source)
		ids = append(ids, source.ID)
	}
	rec, err = s.AcceptJob(ctx, types.AcceptJobInput{RequestID: "accept", LibraryID: l.ID, ExpectedVersion: l.Version, StructureVersion: l.StructureVersion, SourceVersionIDs: ids, Permission: types.PermissionSnapshot{PolicyVersion: l.PolicyVersion, Operations: []string{"organize"}}, RulesVersion: "lite-direct", ConfigDigest: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	j, err := s.GetJob(ctx, rec.JobID)
	if err != nil {
		t.Fatal(err)
	}
	return s, j, sources
}
func normalReply(r GenerateRequest) (GenerateResult, error) {
	switch r.Stage {
	case "map":
		return GenerateResult{Text: `{"entities":[],"concepts":[{"name":"会话管理","slug":"concept/session","description":"在线和离线会话规则","details":"保留来源各自适用条件。"}]}`, FinishReason: "stop"}, nil
	case "dedup":
		return GenerateResult{Text: `{"merges":{}}`, FinishReason: "stop"}, nil
	case "summary":
		return GenerateResult{Text: "SUMMARY: 会话适用条件\n# 文档摘要\n[[concept/session|会话管理]]区分在线与离线。", FinishReason: "stop"}, nil
	case "cite":
		return GenerateResult{Text: `{"citations":{"concept/session":["c000"]},"new_slugs":[]}`, FinishReason: "stop"}, nil
	case "taxonomy":
		return GenerateResult{Text: `{"assignments":[{"slug":"concept/session","path":["账号","会话"]}]}`, FinishReason: "stop"}, nil
	case "reduce":
		return GenerateResult{Text: "SUMMARY: 会话适用条件\n# 会话管理\n在线验证与离线验证具有各自来源条件。", FinishReason: "stop", Usage: &types.ModelUsage{InputTokens: 10, OutputTokens: 20, TotalTokens: 30}}, nil
	case "index":
		return GenerateResult{Text: "# 会话知识\n本库管理在线和离线会话条件。", FinishReason: "stop"}, nil
	}
	return GenerateResult{}, fmt.Errorf("unexpected stage %s", r.Stage)
}
func TestLiteDirectTextPublishesWithoutHostExecution(t *testing.T) {
	s, j, sources := contractFixture(t, 1)
	model := &contractModel{respond: normalReply}
	result, err := Run(context.Background(), s, Input{JobID: j.ID, Generator: model})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != "completed" {
		t.Fatalf("state=%s", result.State)
	}
	pages, total, err := s.ListPages(context.Background(), types.PageQuery{LibraryID: j.LibraryID, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 {
		t.Fatalf("pages=%d: %+v", total, pages)
	}
	page, err := s.GetPageBySlug(context.Background(), j.LibraryID, "concept/session")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Evidence.Sources) != 1 || page.Evidence.Sources[0].SourceVersionID != sources[0].ID {
		t.Fatalf("evidence=%+v", page.Evidence)
	}
	stages, err := s.ListStages(context.Background(), j.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, stage := range stages {
		if stage.ResultText == "" {
			t.Fatalf("stage did not store direct model result: %+v", stage)
		}
	}
	for _, req := range model.calls {
		if req.Thinking || req.Temperature != .3 || req.MaxTokens != 32768 {
			t.Fatalf("model defaults=%+v", req)
		}
		if len(req.Messages) > 2 || req.Messages[len(req.Messages)-1].Role != "user" {
			t.Fatalf("request reused prior context: %+v", req.Messages)
		}
		for _, message := range req.Messages {
			if strings.Contains(message.Content, "allycli") || strings.Contains(message.Content, "Host file delivery") {
				t.Fatal("model was told to use removed host protocol")
			}
		}
	}
}
func TestLiteMalformedPassZeroUsesRawLegacyFallback(t *testing.T) {
	s, j, _ := contractFixture(t, 1)
	var mu sync.Mutex
	maps := 0
	model := &contractModel{respond: func(r GenerateRequest) (GenerateResult, error) {
		if r.Stage == "map" {
			mu.Lock()
			maps++
			n := maps
			mu.Unlock()
			if n == 1 {
				return GenerateResult{Text: `{"entities":`, FinishReason: "stop"}, nil
			}
		}
		return normalReply(r)
	}}
	result, err := Run(context.Background(), s, Input{JobID: j.ID, Generator: model})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != "completed" || maps != 2 {
		t.Fatalf("state=%s maps=%d", result.State, maps)
	}
	stages, _ := s.ListStages(context.Background(), j.ID)
	failed := 0
	for _, stage := range stages {
		if stage.Kind == "map" && stage.Outcome == "failed" {
			failed++
			if !strings.Contains(stage.Diagnostic, "JSON") {
				t.Fatalf("parse failure missing: %+v", stage)
			}
		}
	}
	if failed != 1 {
		t.Fatalf("failed parse stages=%d", failed)
	}
}
func TestLiteReduceFailureKeepsSourceSummaryAndStripsDeadLink(t *testing.T) {
	s, j, _ := contractFixture(t, 1)
	model := &contractModel{respond: func(r GenerateRequest) (GenerateResult, error) {
		if r.Stage == "reduce" {
			return GenerateResult{}, fmt.Errorf("status 403: provider denied")
		}
		return normalReply(r)
	}}
	result, err := Run(context.Background(), s, Input{JobID: j.ID, Generator: model})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != "completed" {
		t.Fatalf("upstream addition failure should not requeue source: %s", result.State)
	}
	pages, _, _ := s.ListPages(context.Background(), types.PageQuery{LibraryID: j.LibraryID, Limit: 20})
	for _, page := range pages {
		if page.Slug == "concept/session" {
			t.Fatal("failed new page was published")
		}
		if page.PageType == "summary" && strings.Contains(page.Content, "[[concept/session") {
			t.Fatalf("dead summary link survived: %s", page.Content)
		}
	}
}
func TestLiteRawParsersDoNotRequireHostJSONEnvelope(t *testing.T) {
	var result combinedExtraction
	if err := json.Unmarshal([]byte(cleanLLMJSON("```json\n{\"entities\":[],\"concepts\":[]}\n```")), &result); err != nil {
		t.Fatal(err)
	}
}

func TestLiteReparseReplacesStableSourceContribution(t *testing.T) {
	s, j, original := contractFixture(t, 1)
	first := &contractModel{respond: func(r GenerateRequest) (GenerateResult, error) {
		switch r.Stage {
		case "map":
			return GenerateResult{Text: `{"entities":[],"concepts":[{"name":"会话管理","slug":"concept/session","description":"会话","details":"旧规则"},{"name":"旧主题","slug":"concept/stale","description":"旧主题","details":"旧主题事实"}]}`, FinishReason: "stop"}, nil
		case "summary":
			return GenerateResult{Text: "SUMMARY: 旧版资料\n# 原件摘要\n旧规则说明", FinishReason: "stop"}, nil
		}
		return normalReply(r)
	}}
	if _, err := Run(context.Background(), s, Input{JobID: j.ID, Generator: first}); err != nil {
		t.Fatal(err)
	}
	imported, err := s.ImportSource(context.Background(), types.ImportSourceInput{RequestID: "replace", LibraryID: j.LibraryID, SourceID: original[0].SourceID, ExpectedVersion: original[0].Version, Filename: "修订版.md", MIME: "text/markdown", Bytes: []byte("# 会话管理\n新规则只保留在线会话，旧主题已从来源撤回。")})
	if err != nil {
		t.Fatal(err)
	}
	current, err := s.GetSource(context.Background(), j.LibraryID, imported.ResourceID)
	if err != nil {
		t.Fatal(err)
	}
	lib, _ := s.GetLibrary(context.Background(), j.LibraryID)
	accepted, err := s.AcceptJob(context.Background(), types.AcceptJobInput{RequestID: "reparse", LibraryID: lib.ID, ExpectedVersion: lib.Version, StructureVersion: lib.StructureVersion, SourceVersionIDs: []string{current.ID}, Permission: types.PermissionSnapshot{PolicyVersion: lib.PolicyVersion, Operations: []string{"organize"}}, RulesVersion: "lite-direct", ConfigDigest: "next"})
	if err != nil {
		t.Fatal(err)
	}
	second := &contractModel{respond: func(r GenerateRequest) (GenerateResult, error) {
		if r.Stage == "summary" {
			return GenerateResult{Text: "SUMMARY: 新版资料\n# 原件摘要\n新规则说明", FinishReason: "stop"}, nil
		}
		if r.Stage == "reduce" {
			return GenerateResult{Text: "SUMMARY: 新规则\n# 新版知识\n新规则已替换旧贡献。", FinishReason: "stop"}, nil
		}
		return normalReply(r)
	}}
	if _, err = Run(context.Background(), s, Input{JobID: accepted.JobID, Generator: second}); err != nil {
		t.Fatal(err)
	}
	session, err := s.GetPageBySlug(context.Background(), lib.ID, "concept/session")
	if err != nil {
		t.Fatal(err)
	}
	if len(session.Evidence.Sources) != 1 || session.Evidence.Sources[0].SourceVersionID != current.ID {
		t.Fatalf("reparse kept old source version: %+v", session.Evidence)
	}
	stale, err := s.GetPageBySlug(context.Background(), lib.ID, "concept/stale")
	if err != nil {
		t.Fatal(err)
	}
	if len(stale.Evidence.Sources) != 0 {
		t.Fatalf("stale topic still attributed to removed contribution: %+v", stale.Evidence)
	}
	summaries, _, err := s.ListPages(context.Background(), types.PageQuery{LibraryID: lib.ID, PageType: "summary", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 1 || summaries[0].Slug != "summary/"+slugify(original[0].SourceID) || !strings.Contains(summaries[0].Content, "新规则说明") {
		t.Fatalf("stable source summary was not overwritten: %+v", summaries)
	}
	sawSwap, sawStale := false, false
	for _, r := range second.calls {
		if r.Stage != "reduce" {
			continue
		}
		prompt := r.Messages[len(r.Messages)-1].Content
		if strings.Contains(prompt, "旧规则说明") {
			sawSwap = true
		}
		if strings.Contains(prompt, "旧主题") && strings.Contains(prompt, "新规则只保留在线会话") {
			sawStale = true
		}
	}
	if !sawSwap || !sawStale {
		t.Fatalf("upstream retract branches missing: swap=%v stale=%v", sawSwap, sawStale)
	}
}
func TestLiteSixSourcesUseSerialFiveDocumentBatches(t *testing.T) {
	s, j, _ := contractFixture(t, 6)
	model := &contractModel{respond: normalReply}
	if _, err := Run(context.Background(), s, Input{JobID: j.ID, Generator: model}); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	reduceBeforeSixth := false
	indexCalls := 0
	for _, r := range model.calls {
		if r.Stage == "reduce" && len(seen) == 5 {
			reduceBeforeSixth = true
		}
		if r.Stage == "map" {
			seen[r.SourceVersionID] = true
			if len(seen) == 6 && !reduceBeforeSixth {
				t.Fatal("sixth document entered before first five document batch reduced")
			}
		}
		if r.Stage == "index" {
			indexCalls++
		}
	}
	if len(seen) != 6 || indexCalls != 1 {
		t.Fatalf("sources=%d index calls=%d", len(seen), indexCalls)
	}
}
func TestLiteUserInstructionIsScopedToItsJob(t *testing.T) {
	for _, instruction := range []string{"仅保留在线与离线适用条件。", ""} {
		s, j, _ := contractFixture(t, 1)
		model := &contractModel{respond: normalReply}
		if _, err := Run(context.Background(), s, Input{JobID: j.ID, Generator: model, Instruction: instruction}); err != nil {
			t.Fatal(err)
		}
		for _, r := range model.calls {
			if r.Stage != "map" && r.Stage != "summary" && r.Stage != "reduce" && r.Stage != "index" {
				continue
			}
			text := ""
			for _, m := range r.Messages {
				text += m.Content
			}
			if instruction != "" && !strings.Contains(text, instruction) {
				t.Fatalf("task instruction missing from %s", r.Stage)
			}
			if instruction == "" && strings.Contains(text, "仅保留在线与离线适用条件。") {
				t.Fatalf("previous job instruction leaked into %s", r.Stage)
			}
		}
	}
}

func TestLiteCitationBatchFailureDoesNotCancelSiblings(t *testing.T) {
	raw := []byte(strings.Repeat("第一段事实", 2400) + "\n" + strings.Repeat("第二段事实", 2400) + "\n" + strings.Repeat("第三段事实", 2400))
	s, j, _ := contractFixture(t, 1, raw)
	model := &contractModel{respond: func(r GenerateRequest) (GenerateResult, error) {
		if r.Stage == "cite" {
			text := r.Messages[len(r.Messages)-1].Content
			if strings.Contains(text, "第三段事实") {
				return GenerateResult{}, fmt.Errorf("status 403: citation failed")
			}
			return GenerateResult{Text: `{"citations":{"concept/session":["c000","unknown"]},"new_slugs":[]}`, FinishReason: "stop"}, nil
		}
		return normalReply(r)
	}}
	if _, err := Run(context.Background(), s, Input{JobID: j.ID, Generator: model}); err != nil {
		t.Fatal(err)
	}
	page, err := s.GetPageBySlug(context.Background(), j.LibraryID, "concept/session")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Evidence.Sources) != 2 {
		t.Fatalf("expected two successful citation batches, evidence=%+v", page.Evidence)
	}
	for _, ref := range page.Evidence.Sources {
		if strings.Contains(ref.Excerpt, "第三段事实") {
			t.Fatal("failed citation was fabricated as supporting evidence")
		}
	}
	calls := 0
	for _, r := range model.calls {
		if r.Stage == "cite" {
			calls++
		}
	}
	if calls != 3 {
		t.Fatalf("citation calls=%d", calls)
	}
}
func TestLitePageRewriteLengthContinuationRefusesFragment(t *testing.T) {
	s, j, _ := contractFixture(t, 1)
	model := &contractModel{respond: normalReply}
	if _, err := Run(context.Background(), s, Input{JobID: j.ID, Generator: model}); err != nil {
		t.Fatal(err)
	}
	old, _ := s.GetPageBySlug(context.Background(), j.LibraryID, "concept/session")
	lib, _ := s.GetLibrary(context.Background(), j.LibraryID)
	accepted, err := s.AcceptJob(context.Background(), types.AcceptJobInput{RequestID: "rebuild", Rebuild: true, LibraryID: lib.ID, ExpectedVersion: lib.Version, StructureVersion: lib.StructureVersion, SourceVersionIDs: j.SourceVersionIDs, Permission: j.Permission, RulesVersion: "lite-direct", ConfigDigest: "rebuild"})
	if err != nil {
		t.Fatal(err)
	}
	truncated := &contractModel{respond: func(r GenerateRequest) (GenerateResult, error) {
		if r.Stage == "reduce" {
			return GenerateResult{Text: "不完整片段", FinishReason: "length", Usage: &types.ModelUsage{InputTokens: 1, OutputTokens: 2, TotalTokens: 3}}, nil
		}
		return normalReply(r)
	}}
	if _, err = Run(context.Background(), s, Input{JobID: accepted.JobID, Generator: truncated}); err != nil {
		t.Fatal(err)
	}
	current, _ := s.GetPageBySlug(context.Background(), lib.ID, old.Slug)
	if current.Content != old.Content {
		t.Fatalf("truncated rewrite replaced existing page: %s", current.Content)
	}
	calls := 0
	for _, r := range truncated.calls {
		if r.Stage == "reduce" {
			if len(r.Messages) != 2+2*calls {
				t.Fatalf("continuation context=%d round=%d", len(r.Messages), calls)
			}
			calls++
		}
	}
	if calls != 4 {
		t.Fatalf("continuation calls=%d", calls)
	}
	stages, _ := s.ListStages(context.Background(), accepted.JobID)
	for _, stage := range stages {
		if stage.Kind == "reduce" {
			if stage.Outcome != "failed" || stage.Completeness != "truncated" || stage.Usage == nil || stage.Usage.TotalTokens != 12 {
				t.Fatalf("rewrite diagnostics=%+v", stage)
			}
		}
	}
}
func TestLiteSourceFailureBudgetSurvivesRecovery(t *testing.T) {
	s, j, _ := contractFixture(t, 1)
	if err := s.EnsureOrganizeSourceWork(context.Background(), j.ID); err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 5; n++ {
		work, _ := s.ListOrganizeSourceWork(context.Background(), j.ID)
		_, err := s.DeferOrganizeSourceWork(context.Background(), types.SourceWorkActionInput{RequestID: fmt.Sprintf("prior-failure%d", n), JobID: j.ID, JobAttempt: j.Attempt, SourceVersionID: work[0].SourceVersionID, ExpectedVersion: work[0].Version, Diagnostic: "previous process model failure", NotBefore: time.Now().Add(-time.Minute).UnixMilli()})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := s.EnsureOrganizeSourceWork(context.Background(), j.ID); err != nil {
		t.Fatal(err)
	}
	model := &contractModel{respond: func(r GenerateRequest) (GenerateResult, error) {
		if r.Stage == "summary" {
			return GenerateResult{}, fmt.Errorf("status 403: denied")
		}
		return normalReply(r)
	}}
	result, err := Run(context.Background(), s, Input{JobID: j.ID, Generator: model})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != "failed" {
		t.Fatalf("state=%s", result.State)
	}
	work, _ := s.ListOrganizeSourceWork(context.Background(), j.ID)
	if work[0].FailureCount != 6 || work[0].State != "dead" {
		t.Fatalf("failure budget was reset: %+v", work[0])
	}
}
func TestLiteWaitsForPreviousLibraryLockInsteadOfDroppingJob(t *testing.T) {
	s, j, _ := contractFixture(t, 1)
	release, acquired, err := s.LockProcessor(context.Background(), "lite/library/"+j.LibraryID, 1)
	if err != nil || !acquired {
		t.Fatalf("lock=%v %v", acquired, err)
	}
	done := make(chan error, 1)
	model := &contractModel{respond: normalReply}
	go func() { _, err := Run(context.Background(), s, Input{JobID: j.ID, Generator: model}); done <- err }()
	select {
	case err := <-done:
		release()
		t.Fatalf("accepted job was dropped on library lock conflict: %v", err)
	case <-time.After(60 * time.Millisecond):
	}
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("job did not resume after previous writer released lock")
	}
	current, _ := s.GetJob(context.Background(), j.ID)
	if current.State != "completed" {
		t.Fatalf("state=%s", current.State)
	}
}

func TestLiteSummaryLineMatchesUpstreamSingleLineContract(t *testing.T) {
	for _, raw := range []string{"SUMMARY: 条件摘要", "SUMMARY： 条件摘要"} {
		summary, body := splitSummaryLine(raw)
		if summary != "条件摘要" || body != "" {
			t.Fatalf("summary=%q body=%q", summary, body)
		}
	}
}

func TestLiteFinalizationLinksSiblingTitlesAndPersistsBacklinks(t *testing.T) {
	s, j, sources := contractFixture(t, 1, []byte("# 会话管理\n会话管理使用令牌检查。令牌检查核验真实令牌，并区分在线与离线会话条件。"))
	model := &contractModel{respond: func(r GenerateRequest) (GenerateResult, error) {
		switch r.Stage {
		case "map":
			return GenerateResult{Text: `{"entities":[],"concepts":[{"name":"会话管理","slug":"concept/session","description":"会话管理规则"},{"name":"令牌检查","slug":"concept/token-check","aliases":["Token校验"],"description":"实际核验令牌"}]}`, FinishReason: "stop"}, nil
		case "cite":
			return GenerateResult{Text: `{"citations":{"concept/session":["c000"],"concept/token-check":["c000"]},"new_slugs":[]}`, FinishReason: "stop"}, nil
		case "taxonomy":
			return GenerateResult{Text: `{"assignments":[]}`, FinishReason: "stop"}, nil
		case "summary":
			return GenerateResult{Text: "SUMMARY: 会话规则\n# 摘要\n会话管理使用令牌检查。", FinishReason: "stop"}, nil
		case "reduce":
			if strings.Contains(r.Messages[len(r.Messages)-1].Content, "<slug>concept/session</slug>") {
				return GenerateResult{Text: "SUMMARY: 会话规则\n# 会话管理\n会话管理使用令牌检查，也称Token校验。", FinishReason: "stop"}, nil
			}
			return GenerateResult{Text: "SUMMARY: 检查规则\n# 令牌检查\n令牌检查核验实际令牌。", FinishReason: "stop"}, nil
		default:
			return normalReply(r)
		}
	}}
	if _, err := Run(context.Background(), s, Input{JobID: j.ID, Generator: model}); err != nil {
		t.Fatal(err)
	}
	sourcePage, err := s.GetPageBySlug(context.Background(), j.LibraryID, "concept/session")
	if err != nil {
		t.Fatal(err)
	}
	target, err := s.GetPageBySlug(context.Background(), j.LibraryID, "concept/token-check")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sourcePage.Content, "[[concept/token-check|令牌检查]]") || len(sourcePage.OutLinks) != 1 || sourcePage.OutLinks[0] != target.Slug {
		t.Fatalf("actual finalize did not persist link: %+v", sourcePage)
	}
	incoming := false
	for _, slug := range target.InLinks {
		incoming = incoming || slug == sourcePage.Slug
	}
	if !incoming {
		t.Fatalf("backlink missing: %+v", target.InLinks)
	}
	if len(target.Evidence.Sources) != 1 || target.Evidence.Sources[0].SourceVersionID != sources[0].ID {
		t.Fatalf("actual source evidence missing: %+v", target.Evidence)
	}
	if strings.Contains(sourcePage.Content, "[[concept/token-check|Token校验]]") {
		t.Fatal("production title-only matching unexpectedly included aliases")
	}
}

func TestIndexTitleStaysFixedWhileIncrementalIntroAndDirectoryChange(t *testing.T) {
	ctx := context.Background()
	s, j, _ := contractFixture(t, 1)
	for round := 0; round < 2; round++ {
		model := &contractModel{respond: func(r GenerateRequest) (GenerateResult, error) {
			if r.Stage == "index" {
				body := "# 模型动态标题\n\n原有会话范围。"
				if round == 1 {
					body += "新增文档的范围。"
				}
				return GenerateResult{Text: body, FinishReason: "stop"}, nil
			}
			return normalReply(r)
		}}
		if _, err := Run(ctx, s, Input{JobID: j.ID, Generator: model}); err != nil {
			t.Fatal(err)
		}
		page, err := s.GetPageBySlug(ctx, j.LibraryID, "index")
		if err != nil {
			t.Fatal(err)
		}
		want := "# Wiki Index\n\n原有会话范围。"
		if round == 1 {
			want += "新增文档的范围。"
		}
		if page.Title != "Wiki Index" || page.Content != want || page.Summary != want {
			t.Fatalf("index round %d: title=%q content=%q summary=%q", round, page.Title, page.Content, page.Summary)
		}
		view, err := s.GetIndexView(ctx, j.LibraryID, []string{"summary"}, 50, "")
		if err != nil || view.Intro != want || view.Groups[0].Total != int64(round+1) {
			t.Fatalf("index view round %d: %+v err=%v", round, view, err)
		}
		ordinary, err := s.GetPageBySlug(ctx, j.LibraryID, "concept/session")
		if err != nil || ordinary.Title != "会话管理" {
			t.Fatalf("ordinary title changed: %+v %v", ordinary, err)
		}
		if round == 1 {
			break
		}
		lib, _ := s.GetLibrary(ctx, j.LibraryID)
		// Simulate a dynamic metadata title left by a previous model; the next organization run must normalize it too.
		page.Title = "旧索引名称"
		if _, err = s.WritePage(ctx, types.WritePageInput{RequestID: "legacy-index-name", LibraryID: lib.ID, StructureVersion: lib.StructureVersion, ExpectedVersion: page.Version, Page: *page}); err != nil {
			t.Fatal(err)
		}
		receipt, err := s.ImportSource(ctx, types.ImportSourceInput{RequestID: "next-source", LibraryID: lib.ID, Filename: "新增资料.md", MIME: "text/markdown", Bytes: []byte("新增文档说明会话适用范围。")})
		if err != nil {
			t.Fatal(err)
		}
		lib, _ = s.GetLibrary(ctx, lib.ID)
		rec, err := s.AcceptJob(ctx, types.AcceptJobInput{RequestID: "next-job", LibraryID: lib.ID, ExpectedVersion: lib.Version, StructureVersion: lib.StructureVersion, SourceVersionIDs: []string{receipt.ResourceID}, Permission: types.PermissionSnapshot{PolicyVersion: lib.PolicyVersion, Operations: []string{"organize"}}, RulesVersion: "lite-direct", ConfigDigest: "fixture"})
		if err != nil {
			t.Fatal(err)
		}
		j, err = s.GetJob(ctx, rec.JobID)
		if err != nil {
			t.Fatal(err)
		}
	}
}

// TestLiteLinkFinalizationRechecksPublishGuard prevents page link updates after authorization is revoked following index publication.
func TestLiteLinkFinalizationRechecksPublishGuard(t *testing.T) {
	s, j, _ := contractFixture(t, 1)
	ctx := context.Background()
	revoked := false
	before := map[string]string{}
	denied := fmt.Errorf("credential revoked")
	model := &contractModel{respond: func(r GenerateRequest) (GenerateResult, error) {
		if r.Stage == "summary" {
			return GenerateResult{Text: "SUMMARY: 会话适用条件\n# 文档摘要\n会话管理区分在线与离线。", FinishReason: "stop"}, nil
		}
		return normalReply(r)
	}}
	_, err := Run(ctx, s, Input{JobID: j.ID, Generator: model,
		PublishGuard: func(context.Context) (func(), error) {
			if revoked {
				return nil, denied
			}
			return func() {}, nil
		},
		OnPublished: func(ctx context.Context, _ *types.Receipt) {
			pages, _, e := s.ListPages(ctx, types.PageQuery{LibraryID: j.LibraryID, Limit: 100})
			if e != nil {
				t.Fatal(e)
			}
			for _, page := range pages {
				if page.PageType == "index" {
					revoked = true
				}
			}
			if revoked {
				for _, page := range pages {
					before[page.ID] = page.Content
				}
			}
		},
	})
	if err != denied {
		t.Fatalf("expected revoked publication failure, got %v", err)
	}
	if len(before) != 3 {
		t.Fatalf("index publication not reached: %v", before)
	}
	pages, _, err := s.ListPages(ctx, types.PageQuery{LibraryID: j.LibraryID, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, page := range pages {
		if page.Content != before[page.ID] {
			t.Errorf("revoked credential changed %s links: before=%q after=%q", page.Slug, before[page.ID], page.Content)
		}
	}
}

// TestLiteLinkFinalizationRejectsStructureConflict prevents an old task from updating links after a manual folder change.
func TestLiteLinkFinalizationRejectsStructureConflict(t *testing.T) {
	s, j, _ := contractFixture(t, 1)
	ctx := context.Background()
	before := map[string]string{}
	model := &contractModel{respond: func(r GenerateRequest) (GenerateResult, error) {
		if r.Stage == "summary" {
			return GenerateResult{Text: "SUMMARY: 会话适用条件\n# 文档摘要\n会话管理区分在线与离线。", FinishReason: "stop"}, nil
		}
		return normalReply(r)
	}}
	_, err := Run(ctx, s, Input{JobID: j.ID, Generator: model, OnPublished: func(ctx context.Context, _ *types.Receipt) {
		pages, _, e := s.ListPages(ctx, types.PageQuery{LibraryID: j.LibraryID, Limit: 100})
		if e != nil {
			t.Fatal(e)
		}
		for _, page := range pages {
			if page.PageType != "index" {
				continue
			}
			lib, e := s.GetLibrary(ctx, j.LibraryID)
			if e != nil {
				t.Fatal(e)
			}
			_, e = s.WriteTheme(ctx, types.WriteThemeInput{RequestID: "manual-structure-change", LibraryID: lib.ID, StructureVersion: lib.StructureVersion, Theme: types.WikiFolder{Name: "人工目录"}})
			if e != nil {
				t.Fatal(e)
			}
			for _, p := range pages {
				before[p.ID] = p.Content
			}
		}
	}})
	var domainErr *types.Error
	if !errors.As(err, &domainErr) || domainErr.Code != types.ErrVersionConflict {
		t.Fatalf("expected structure conflict, got %v", err)
	}
	if len(before) != 3 {
		t.Fatalf("index publication not reached: %v", before)
	}
	pages, _, err := s.ListPages(ctx, types.PageQuery{LibraryID: j.LibraryID, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, page := range pages {
		if page.Content != before[page.ID] {
			t.Errorf("stale structure changed %s links: before=%q after=%q", page.Slug, before[page.ID], page.Content)
		}
	}
}
