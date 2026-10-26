package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// TestModelCandidatesCannotClaimReservedPageAddresses 保留系统地址仅用于摘要和索引，模型候选按真实类型与标题获得普通知识地址。
func TestModelCandidatesCannotClaimReservedPageAddresses(t *testing.T) {
	ctx := context.Background()
	svc := &liteService{}
	for _, kind := range []string{"entity", "concept"} {
		for _, reserved := range []string{"summary/source-a", "index", "index/catalog"} {
			got := svc.claimWikiIdentitySlug(ctx, "library", kind, "Recovery Rules", reserved, false, nil)
			if got != kind+"/recovery-rules" {
				t.Errorf("%s candidate retained reserved address %q: got=%q", kind, reserved, got)
			}
		}
	}
	// 精确已有身份可以跨 entity/concept 复用；普通地址空间不属于本次规范范围。
	if got := svc.claimWikiIdentitySlug(ctx, "library", "entity", "Person", "concept/person", true, nil); got != "concept/person" {
		t.Fatalf("cross-type exact identity changed: %s", got)
	}
	if got := svc.claimWikiIdentitySlug(ctx, "library", "concept", "Person", "custom/person", false, nil); got != "custom/person" {
		t.Fatalf("unrelated namespace changed: %s", got)
	}
}

// TestReservedMapCandidateKeepsOtherSourceSummaryAndPublishesRealContribution 验证来源B误用来源A摘要地址时，B正文真正写入概念页，且回执只确认实际概念贡献。
func TestReservedMapCandidateKeepsOtherSourceSummaryAndPublishesRealContribution(t *testing.T) {
	ctx := context.Background()
	s, j, sources := contractFixture(t, 2)
	reserved := "summary/" + sources[0].SourceID
	const actual = "concept/recovery-rules"
	model := &contractModel{respond: func(r GenerateRequest) (GenerateResult, error) {
		switch r.Stage {
		case "map":
			if r.SourceVersionID == sources[0].ID {
				return GenerateResult{Text: `{"entities":[],"concepts":[]}`, FinishReason: "stop"}, nil
			}
			return GenerateResult{Text: fmt.Sprintf(`{"entities":[],"concepts":[{"name":"Recovery Rules","slug":%q,"description":"离线会话的有效条件","details":"离线会话遵循单独有效条件。"}]}`, reserved), FinishReason: "stop"}, nil
		case "summary":
			if r.SourceVersionID == sources[0].ID {
				return GenerateResult{Text: "SUMMARY: 来源A会话规则\n# 来源A摘要\n服务端校验在线会话。", FinishReason: "stop"}, nil
			}
			return GenerateResult{Text: "SUMMARY: 来源B离线会话规则\n# 来源B摘要\n[[concept/recovery-rules|恢复规则]]：离线会话遵循单独有效条件。", FinishReason: "stop"}, nil
		case "cite":
			return GenerateResult{Text: `{"citations":{"concept/recovery-rules":["c000"]},"new_slugs":[]}`, FinishReason: "stop"}, nil
		case "reduce":
			input := r.Messages[len(r.Messages)-1].Content
			if !strings.Contains(input, "<slug>concept/recovery-rules</slug>") || !strings.Contains(input, "离线会话遵循单独有效条件") {
				t.Errorf("source B content not synthesized into actual concept: %s", input)
			}
			return GenerateResult{Text: "SUMMARY: 离线会话恢复规则\n# 恢复规则\n离线会话遵循单独有效条件。", FinishReason: "stop"}, nil
		}
		return normalReply(r)
	}}
	result, err := Run(ctx, s, Input{JobID: j.ID, Generator: model})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != "completed" {
		t.Fatalf("reserved candidate lost valid contribution: state=%s", result.State)
	}
	concept, err := s.GetPageBySlug(ctx, j.LibraryID, actual)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(concept.Content, "离线会话遵循单独有效条件") || len(concept.Evidence.Sources) != 1 || concept.Evidence.Sources[0].SourceVersionID != sources[1].ID {
		t.Fatalf("source B not materialized: %+v", concept)
	}
	summary, err := s.GetPageBySlug(ctx, j.LibraryID, reserved)
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Evidence.Sources) != 1 || summary.Evidence.Sources[0].SourceVersionID != sources[0].ID || summary.Content != "# 来源A摘要\n服务端校验在线会话。" {
		t.Fatalf("source B contaminated source A summary: %+v", summary)
	}
	payload, err := s.LoadLitePlan(ctx, j.ID, j.Attempt, sources[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	var plan documentPlan
	if err = json.Unmarshal([]byte(payload), &plan); err != nil {
		t.Fatal(err)
	}
	updates := plan.Updates[actual]
	if len(updates) != 1 || len(plan.Updates[reserved]) != 0 {
		t.Fatalf("reserved contribution survived plan: %+v", plan.Updates)
	}
	applied, err := s.HasLiteContribution(ctx, j.ID, hash(marshal([]string{"source-contribution", sources[1].ID, actual, "addition"})))
	if err != nil || !applied {
		t.Fatalf("actual concept contribution has no receipt: applied=%v err=%v", applied, err)
	}
	applied, err = s.HasLiteContribution(ctx, j.ID, hash(marshal([]string{"source-contribution", sources[1].ID, reserved, "addition"})))
	if err != nil || applied {
		t.Fatalf("unprocessed reserved contribution has receipt: applied=%v err=%v", applied, err)
	}
}

// TestCitationDiscoveredCandidateCannotClaimIndex 验证引用阶段新发现的知识也通过共享身份入口避开索引地址。
func TestCitationDiscoveredCandidateCannotClaimIndex(t *testing.T) {
	ctx := context.Background()
	s, j, _ := contractFixture(t, 1)
	model := &contractModel{respond: func(r GenerateRequest) (GenerateResult, error) {
		if r.Stage == "cite" {
			return GenerateResult{Text: `{"citations":{"concept/session":["c000"]},"new_slugs":[{"type":"concept","name":"Offline Policy","slug":"index","description":"离线有效条件","details":"离线会话遵循单独有效条件。","source_chunks":["c000"]}]}`, FinishReason: "stop"}, nil
		}
		if r.Stage == "reduce" && strings.Contains(r.Messages[len(r.Messages)-1].Content, "<slug>concept/offline-policy</slug>") {
			return GenerateResult{Text: "SUMMARY: 离线会话有效条件\n# 离线策略\n离线会话遵循单独有效条件。", FinishReason: "stop"}, nil
		}
		return normalReply(r)
	}}
	result, err := Run(ctx, s, Input{JobID: j.ID, Generator: model})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != "completed" {
		t.Fatalf("citation candidate did not complete: %s", result.State)
	}
	page, err := s.GetPageBySlug(ctx, j.LibraryID, "concept/offline-policy")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(page.Content, "离线会话遵循单独有效条件") || len(page.Evidence.Sources) != 1 {
		t.Fatalf("citation-discovered concept lost content or evidence: %+v", page)
	}
	index, err := s.GetPageBySlug(ctx, j.LibraryID, "index")
	if err != nil {
		t.Fatal(err)
	}
	if index.PageType != "index" {
		t.Fatalf("model concept occupied system index: %+v", index)
	}
}

// TestRestoredReservedUpdateUsesSharedIdentityNormalization 验证旧计划恢复重排时也规范保留地址，跨类型精确已有身份继续复用。
func TestRestoredReservedUpdateUsesSharedIdentityNormalization(t *testing.T) {
	ctx := context.Background()
	s, j, _ := contractFixture(t, 1)
	svc := &liteService{store: s, job: j, library: j.LibrarySnapshot}
	updates := map[string][]SlugUpdate{"summary/source-a": {{Slug: "summary/source-a", Type: "concept", Item: extractedItem{Name: "Recovery Rules", Slug: "summary/source-a"}}}}
	got := svc.remapSlugUpdatesByIdentity(ctx, j.LibraryID, updates, svc.newBatchContext(nil))
	if len(got["concept/recovery-rules"]) != 1 || len(got["summary/source-a"]) != 0 {
		t.Fatalf("restored candidate retained reserved address: %+v", got)
	}
	existing := []extractedItem{{Name: "Person", Slug: "entity/person"}}
	result := svc.stabilizeExtractedIdentities(ctx, j.LibraryID, "entity", existing, nil, map[string]string{"entity/person": "concept/person"}, svc.newBatchContext(nil))
	if len(result) != 1 || result[0].Slug != "concept/person" {
		t.Fatalf("authoritative cross-type identity changed: %+v", result)
	}
}
