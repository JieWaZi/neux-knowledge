package pipeline

import (
	"context"
	"fmt"
	"strings"
	"testing"

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
	page := types.WikiPage{KnowledgeBaseID: j.LibraryID, Slug: "summary/" + sources[0].SourceID, Title: "文档摘要", PageType: "summary", Content: "# 会话管理\n| 条件 | 行为 |\n|---|---|\n| 在线 | 校验 |\n| 离线 | 本地 |", Status: "published", Evidence: types.PageEvidence{Sources: []types.SourceRef{documentRef(sources[0])}}}
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
	if !strings.Contains(source.Diagnostic, "summary/"+sources[0].SourceID) || !strings.Contains(source.Diagnostic, "table") {
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
