package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	types "github.com/JieWaZi/neux-knowledge/internal/domain"
)

// TestLiteRetryKeepsAppliedPagesAcrossRecovery 防止一个页面失败后重复生成已成功的页面与文档提取结果。
func TestLiteRetryKeepsAppliedPagesAcrossRecovery(t *testing.T) {
	for _, count := range []int{1, 2} {
		t.Run(fmt.Sprintf("sources-%d", count), func(t *testing.T) { checkAppliedPagesAcrossRecovery(t, count, false, false) })
	}
	t.Run("manual-summary", func(t *testing.T) { checkAppliedPagesAcrossRecovery(t, 1, true, false) })
	t.Run("explicit-retry", func(t *testing.T) { checkAppliedPagesAcrossRecovery(t, 1, false, true) })
}

func checkAppliedPagesAcrossRecovery(t *testing.T, sourceCount int, manualSummary, retryAttempt bool) {
	t.Helper()
	ctx := context.Background()
	s, j, sources := contractFixture(t, sourceCount)
	if err := s.EnsureOrganizeSourceWork(ctx, j.ID); err != nil {
		t.Fatal(err)
	}
	failing := true
	model := &contractModel{respond: func(r GenerateRequest) (GenerateResult, error) {
		if r.SourceVersionID != sources[0].ID && (r.Stage == "map" || r.Stage == "cite") {
			return normalReply(r)
		}
		switch r.Stage {
		case "map":
			return GenerateResult{Text: `{"entities":[],"concepts":[{"name":"会话管理","slug":"concept/session","description":"会话规则","details":"校验在线会话"},{"name":"恢复机制","slug":"concept/recovery","description":"恢复规则","details":"恢复会话"}]}`}, nil
		case "cite":
			return GenerateResult{Text: `{"citations":{"concept/session":["c000"],"concept/recovery":["c000"]},"new_slugs":[]}`}, nil
		case "summary":
			if r.SourceVersionID == sources[0].ID {
				return GenerateResult{Text: "SUMMARY: 会话与恢复\n# 文档摘要\n[[concept/session|在线验证]]与[[concept/recovery|继续阅读]]。"}, nil
			}
			return normalReply(r)
		case "reduce":
			if strings.Contains(r.Messages[len(r.Messages)-1].Content, "<slug>concept/recovery</slug>") && failing {
				return GenerateResult{}, fmt.Errorf("status 503: controlled page generation failure")
			}
		}
		return normalReply(r)
	}}
	svc := &liteService{store: s, job: j, library: j.LibrarySnapshot, input: Input{Generator: model, Reader: MarkdownReader{}}}
	work, _ := s.ListOrganizeSourceWork(ctx, j.ID)
	if err := svc.processBatch(ctx, work); err != nil {
		t.Fatal(err)
	}
	good, err := s.GetPageBySlug(ctx, j.LibraryID, "concept/session")
	if err != nil {
		t.Fatal(err)
	}
	if retryAttempt {
		if _, e := s.PublishLite(ctx, "settle-before-explicit-retry", j.ID, j.Attempt, &types.Publication{Sources: []types.SourceConclusion{{SourceVersionID: sources[0].ID, State: "failed", Diagnostic: "recovery page failed"}}, Final: true}); e != nil {
			t.Fatal(e)
		}
		j, _ = s.GetJob(ctx, j.ID)
		if _, e := s.RetryJob(ctx, types.JobActionInput{RequestID: "retry-same-job", JobID: j.ID, ExpectedVersion: j.Version}); e != nil {
			t.Fatal(e)
		}
		j, _ = s.GetJob(ctx, j.ID)
		if e := s.EnsureOrganizeSourceWork(ctx, j.ID); e != nil {
			t.Fatal(e)
		}
	}
	work, _ = s.ListOrganizeSourceWork(ctx, j.ID)
	pending := []*types.OrganizeSourceWork{}
	for _, w := range work {
		if w.State == "pending" {
			pending = append(pending, w)
		}
	}
	if len(pending) != 1 || pending[0].SourceVersionID != sources[0].ID {
		t.Fatalf("wrong pending source contributions: %+v", work)
	}
	work = pending
	const manualBody = "# 人工保留的摘要\n这一段不能被恢复计划覆盖。"
	if manualSummary {
		page, e := s.GetPageBySlug(ctx, j.LibraryID, "summary/"+sources[0].SourceID)
		if e != nil {
			t.Fatal(e)
		}
		lib, e := s.GetLibrary(ctx, j.LibraryID)
		if e != nil {
			t.Fatal(e)
		}
		page.Content = manualBody
		if _, e = s.WritePage(ctx, types.WritePageInput{RequestID: "manual-summary", LibraryID: j.LibraryID, StructureVersion: lib.StructureVersion, ExpectedVersion: page.Version, Page: *page}); e != nil {
			t.Fatal(e)
		}
	}
	// 新建 Service 模拟退出后恢复，不能依赖进程内 map 记住成功结果。
	failing = false
	svc = &liteService{store: s, job: j, library: j.LibrarySnapshot, input: Input{Generator: model, Reader: MarkdownReader{}}}
	if err := svc.processBatch(ctx, work); err != nil {
		t.Fatal(err)
	}
	after, err := s.GetPageBySlug(ctx, j.LibraryID, "concept/session")
	if err != nil {
		t.Fatal(err)
	}
	if after.Version != good.Version {
		t.Fatalf("successful page regenerated: before=%d after=%d", good.Version, after.Version)
	}
	summary, err := s.GetPageBySlug(ctx, j.LibraryID, "summary/"+sources[0].SourceID)
	if err != nil {
		t.Fatal(err)
	}
	if manualSummary && summary.Content != manualBody {
		t.Fatalf("manual summary was overwritten: %s", summary.Content)
	}
	if !manualSummary && !strings.Contains(summary.Content, "[[concept/recovery|继续阅读]]") {
		t.Fatalf("recovered page lost original explicit summary link: %s", summary.Content)
	}

	counts := map[string]int{}
	for _, r := range model.calls {
		counts[r.Stage]++
		if r.Stage == "reduce" && strings.Contains(r.Messages[len(r.Messages)-1].Content, "<slug>concept/session</slug>") {
			counts["successful_page"]++
		}
	}
	if counts["map"] != sourceCount || counts["summary"] != sourceCount || counts["cite"] != sourceCount || counts["successful_page"] != 1 {
		t.Fatalf("successful work called model again: %+v", counts)
	}
	work, _ = s.ListOrganizeSourceWork(ctx, j.ID)
	for _, w := range work {
		if w.State != "done" {
			t.Fatalf("retry did not finish: %+v", w)
		}
	}
}

// TestLiteGuardRefusalDoesNotSpendSixModelPasses 防止确定性的表格保护拒写被当作整份资料的瞬时故障。
func TestLiteGuardRefusalDoesNotSpendSixModelPasses(t *testing.T) {
	ctx := context.Background()
	s, j, sources := contractFixture(t, 1)
	page := types.WikiPage{KnowledgeBaseID: j.LibraryID, Slug: "concept/session", Title: "会话管理", PageType: "concept", Content: "# 会话管理\n| 条件 | 行为 |\n|---|---|\n| 在线 | 校验 |\n| 离线 | 本地 |", Status: "published"}
	if _, err := s.PublishLite(ctx, "seed", j.ID, j.Attempt, &types.Publication{Pages: []types.PageMutation{{Page: page}}}); err != nil {
		t.Fatal(err)
	}
	model := &contractModel{respond: normalReply}
	result, err := Run(ctx, s, Input{JobID: j.ID, Generator: model})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != "failed" {
		t.Fatalf("state=%s", result.State)
	}
	count := 0
	for _, r := range model.calls {
		if r.Stage == "reduce" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("deterministic refusal regenerated %d times", count)
	}
	source, _ := s.GetSource(ctx, j.LibraryID, sources[0].ID)
	if !strings.Contains(source.Diagnostic, "concept/session") || !strings.Contains(source.Diagnostic, "table") {
		t.Fatalf("missing page rejection diagnostic: %s", source.Diagnostic)
	}
}

// TestLiteSharedPageAcrossBatchesHasOneVersion 验证不同来源分批更新同页后，只有最终内容而无中间历史。
func TestLiteSharedPageAcrossBatchesHasOneVersion(t *testing.T) {
	ctx := context.Background()
	s, j, _ := contractFixture(t, 3)
	if err := s.EnsureOrganizeSourceWork(ctx, j.ID); err != nil {
		t.Fatal(err)
	}
	model := &contractModel{respond: normalReply}
	svc := &liteService{store: s, job: j, library: j.LibrarySnapshot, input: Input{Generator: model, Reader: MarkdownReader{}}}
	work, err := s.ListOrganizeSourceWork(ctx, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range work {
		if err := svc.processBatch(ctx, []*types.OrganizeSourceWork{w}); err != nil {
			t.Fatal(err)
		}
	}
	page, err := s.GetPageBySlug(ctx, j.LibraryID, "concept/session")
	if err != nil {
		t.Fatal(err)
	}
	if page.Version != 1 || len(page.Evidence.Sources) != 3 {
		t.Fatalf("page version=%d, sources=%d", page.Version, len(page.Evidence.Sources))
	}
	_, n, err := s.ListRevisions(ctx, j.LibraryID, page.ID, 0, 100)
	if err != nil || n != 0 {
		t.Fatalf("intermediate revisions: %d %v", n, err)
	}
}

// TestLiteRetryPreservesSemanticContributionAfterPlanRebuild 防止旧计划重组时描述或引用变化重复应用已成功来源贡献。
func TestLiteRetryPreservesSemanticContributionAfterPlanRebuild(t *testing.T) {
	ctx := context.Background()
	s, j, sources := contractFixture(t, 1)
	failed := true
	generation := 1
	successfulCalls := 0
	model := &contractModel{respond: func(r GenerateRequest) (GenerateResult, error) {
		switch r.Stage {
		case "map":
			return GenerateResult{Text: fmt.Sprintf(`{"entities":[],"concepts":[{"name":"会话管理","slug":"concept/session","description":"会话规则%d","details":"提取描述%d"},{"name":"恢复机制","slug":"concept/recovery","description":"恢复规则","details":"恢复会话"}]}`, generation, generation)}, nil
		case "cite":
			return GenerateResult{Text: `{"citations":{"concept/session":["c000"],"concept/recovery":["c000"]},"new_slugs":[]}`}, nil
		case "reduce":
			input := r.Messages[len(r.Messages)-1].Content
			if strings.Contains(input, "<slug>concept/session</slug>") {
				successfulCalls++
			}
			if strings.Contains(input, "<slug>concept/recovery</slug>") && failed {
				return GenerateResult{}, fmt.Errorf("status 403: controlled failure")
			}
		}
		return normalReply(r)
	}}
	if err := s.EnsureOrganizeSourceWork(ctx, j.ID); err != nil {
		t.Fatal(err)
	}
	work, err := s.ListOrganizeSourceWork(ctx, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	svc := &liteService{store: s, job: j, library: j.LibrarySnapshot, input: Input{Generator: model, Reader: MarkdownReader{}}}
	if err := svc.processBatch(ctx, work); err != nil {
		t.Fatal(err)
	}
	original, err := s.GetPageBySlug(ctx, j.LibraryID, "concept/session")
	if err != nil {
		t.Fatal(err)
	}
	payload, err := s.LoadLitePlan(ctx, j.ID, j.Attempt, sources[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	var legacy documentPlan
	if err := json.Unmarshal([]byte(payload), &legacy); err != nil {
		t.Fatal(err)
	}
	legacy.ModelValidationVersion = 0
	time.Sleep(2 * time.Millisecond)
	if err := s.SaveLitePlan(ctx, j.ID, j.Attempt, sources[0].ID, string(marshal(legacy))); err != nil {
		t.Fatal(err)
	}
	work, err = s.ListOrganizeSourceWork(ctx, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.finalize(ctx, work); err != nil {
		t.Fatal(err)
	}
	current, err := s.GetJob(ctx, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RetryJob(ctx, types.JobActionInput{RequestID: "semantic-retry", JobID: j.ID, ExpectedVersion: current.Version}); err != nil {
		t.Fatal(err)
	}
	failed = false
	generation = 2
	result, err := Run(ctx, s, Input{JobID: j.ID, Generator: model})
	if err != nil {
		t.Fatal(err)
	}
	after, err := s.GetPageBySlug(ctx, j.LibraryID, "concept/session")
	if err != nil {
		t.Fatal(err)
	}
	if result.State != "completed" || successfulCalls != 1 || after.Version != original.Version || after.Content != original.Content {
		t.Fatalf("semantic contribution replayed: state=%s calls=%d before=%+v after=%+v", result.State, successfulCalls, original, after)
	}
}

// TestLiteRetryAdoptsLegacyContributionAfterPlanRebuild 验证只凭已提交旧内容回执继承成功贡献，而非重新生成的描述。
func TestLiteRetryAdoptsLegacyContributionAfterPlanRebuild(t *testing.T) {
	ctx := context.Background()
	s, j, sources := contractFixture(t, 1)
	generation := 1
	goodCalls := 0
	model := &contractModel{respond: func(r GenerateRequest) (GenerateResult, error) {
		switch r.Stage {
		case "map":
			return GenerateResult{Text: fmt.Sprintf(`{"entities":[],"concepts":[{"name":"会话管理","slug":"concept/session","description":"会话描述%d","details":"提取描述%d"},{"name":"恢复机制","slug":"concept/recovery","description":"恢复规则","details":"恢复会话"}]}`, generation, generation)}, nil
		case "cite":
			return GenerateResult{Text: `{"citations":{"concept/session":["c000"],"concept/recovery":["c000"]},"new_slugs":[]}`}, nil
		case "reduce":
			if strings.Contains(r.Messages[len(r.Messages)-1].Content, "<slug>concept/session</slug>") {
				goodCalls++
			}
		}
		return normalReply(r)
	}}
	if err := s.EnsureOrganizeSourceWork(ctx, j.ID); err != nil {
		t.Fatal(err)
	}
	work, err := s.ListOrganizeSourceWork(ctx, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	svc := &liteService{store: s, job: j, library: j.LibrarySnapshot, input: Input{Generator: model, Reader: MarkdownReader{}}}
	result, err := svc.mapDocument(ctx, work[0], nil)
	if err != nil {
		t.Fatal(err)
	}
	contribution := result.updates["concept/session"][0]
	page := types.WikiPage{ID: "legacy-applied-page", KnowledgeBaseID: j.LibraryID, Slug: "concept/session", Title: "会话管理", PageType: "concept", Status: "published", Content: "已经提交的当前来源会话规则。", Evidence: types.PageEvidence{Sources: []types.SourceRef{documentRef(sources[0])}}}
	oldKey := "lite/" + j.ID + "/applied/" + hash(marshal(contribution))
	if _, err := s.PublishLite(ctx, "legacy-applied-page", j.ID, j.Attempt, &types.Publication{Pages: []types.PageMutation{{Page: page}}, SourceVersionIDs: []string{sources[0].ID}}, oldKey); err != nil {
		t.Fatal(err)
	}
	legacy := documentPlan{Artifact: result.artifact, Updates: result.updates, Summary: result.summary, Description: result.description}
	time.Sleep(2 * time.Millisecond)
	if err := s.SaveLitePlan(ctx, j.ID, j.Attempt, sources[0].ID, string(marshal(legacy))); err != nil {
		t.Fatal(err)
	}
	current, err := s.GetJob(ctx, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.FailJob(ctx, types.FailJobInput{JobActionInput: types.JobActionInput{RequestID: "legacy-partial-fail", JobID: j.ID, ExpectedVersion: current.Version}, Diagnostic: "other page interrupted", Sources: []types.SourceConclusion{{SourceVersionID: sources[0].ID, State: "failed", Diagnostic: "other page interrupted"}}}); err != nil {
		t.Fatal(err)
	}
	current, err = s.GetJob(ctx, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RetryJob(ctx, types.JobActionInput{RequestID: "retry-legacy-contribution", JobID: j.ID, ExpectedVersion: current.Version}); err != nil {
		t.Fatal(err)
	}
	generation = 2
	// 新计划保存后进程退出，尚未来得及进入贡献过滤；下次恢复仍须认出旧回执。
	resumedJob, e := s.GetJob(ctx, j.ID)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.EnsureOrganizeSourceWork(ctx, j.ID); e != nil {
		t.Fatal(e)
	}
	resumedWork, e := s.ListOrganizeSourceWork(ctx, j.ID)
	if e != nil {
		t.Fatal(e)
	}
	resumed := &liteService{store: s, job: resumedJob, library: resumedJob.LibrarySnapshot, input: Input{Generator: model, Reader: MarkdownReader{}}}
	currentPages, e := resumed.pages(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = resumed.mapDocument(ctx, resumedWork[0], currentPages); e != nil {
		t.Fatal(e)
	}
	final, err := Run(ctx, s, Input{JobID: j.ID, Generator: model})
	if err != nil {
		t.Fatal(err)
	}
	after, err := s.GetPage(ctx, j.LibraryID, page.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.State != "completed" || goodCalls != 0 || after.Content != page.Content {
		t.Fatalf("legacy contribution replayed: state=%s calls=%d page=%+v", final.State, goodCalls, after)
	}
	applied, err := s.HasLiteContribution(ctx, j.ID, contributionHash(contribution))
	if err != nil || !applied {
		t.Fatalf("legacy contribution was not adopted: %v %v", applied, err)
	}
}
