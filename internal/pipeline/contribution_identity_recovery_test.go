package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	types "github.com/JieWaZi/neux-knowledge/internal/domain"
	"strings"
	"testing"
	"time"
)

// TestMultipleHistoricalVersionsAllReachRetraction 防止同源多版本遗留事实只撤回一版却删除全部证据。
func TestMultipleHistoricalVersionsAllReachRetraction(t *testing.T) {
	for _, mixedSummary := range []bool{false, true} {
		t.Run(map[bool]string{false: "exact-summary", true: "legacy-mixed-summary"}[mixedSummary], func(t *testing.T) {
			ctx := context.Background()
			s, j, sources := contractFixture(t, 2, []byte("# 第一版资料\nFIRST_OLD_FACT"), []byte("# 独立兄弟来源\nSIBLING_FACT"))
			first := &contractModel{respond: func(r GenerateRequest) (GenerateResult, error) {
				if r.Stage == "summary" && r.SourceVersionID == sources[0].ID {
					return GenerateResult{Text: "SUMMARY: 第一版\nFIRST_OLD_FACT"}, nil
				}
				if r.Stage == "summary" {
					return GenerateResult{Text: "SUMMARY: 兄弟来源\nSIBLING_FACT"}, nil
				}
				if r.Stage == "reduce" {
					return GenerateResult{Text: "SUMMARY: 共同主题\nFIRST_OLD_FACT\nSIBLING_FACT"}, nil
				}
				return normalReply(r)
			}}
			if _, err := Run(ctx, s, Input{JobID: j.ID, Generator: first}); err != nil {
				t.Fatal(err)
			}
			accept := func(id, request string) *types.Job {
				lib, e := s.GetLibrary(ctx, j.LibraryID)
				if e != nil {
					t.Fatal(e)
				}
				rec, e := s.AcceptJob(ctx, types.AcceptJobInput{RequestID: request, LibraryID: lib.ID, ExpectedVersion: lib.Version, StructureVersion: lib.StructureVersion, SourceVersionIDs: []string{id}, Permission: j.Permission, RulesVersion: "rules", ConfigDigest: request})
				if e != nil {
					t.Fatal(e)
				}
				next, e := s.GetJob(ctx, rec.JobID)
				if e != nil {
					t.Fatal(e)
				}
				return next
			}
			imported, e := s.ImportSource(ctx, types.ImportSourceInput{RequestID: "source-v2", LibraryID: j.LibraryID, SourceID: sources[0].SourceID, ExpectedVersion: 1, Filename: sources[0].Filename, MIME: sources[0].MIME, Bytes: []byte("# 第二版\nSECOND_OLD_FACT")})
			if e != nil {
				t.Fatal(e)
			}
			v2, e := s.GetSource(ctx, j.LibraryID, imported.ResourceID)
			if e != nil {
				t.Fatal(e)
			}
			j2 := accept(v2.ID, "accept-v2")
			v2Raw, e := s.ReadRaw(ctx, j.LibraryID, v2.ID)
			if e != nil {
				t.Fatal(e)
			}
			readV2, e := (MarkdownReader{}).Read(ctx, ReadRequest{SourceID: v2.SourceID, SourceVersionID: v2.ID, SHA256: v2.SHA256, MIME: v2.MIME, Filename: v2.Filename, Raw: v2Raw, Scope: "all"})
			if e != nil {
				t.Fatal(e)
			}
			page, e := s.GetPageBySlug(ctx, j.LibraryID, "concept/session")
			if e != nil {
				t.Fatal(e)
			}
			summary, e := s.GetPageBySlug(ctx, j.LibraryID, "summary/"+sources[0].SourceID)
			if e != nil {
				t.Fatal(e)
			}
			// Seed the exact legacy append-only state: v1 and v2 evidence remain on one published page.
			page.Content = "FIRST_OLD_FACT\nSECOND_OLD_FACT\nSIBLING_FACT"
			page.Evidence.Sources = append(page.Evidence.Sources, documentRef(v2))
			page.SourceRefs = nil
			summary.Content = "SECOND_OLD_FACT"
			summary.Evidence.Sources = []types.SourceRef{documentRef(v2)}
			if mixedSummary {
				summary.Evidence.Sources = append([]types.SourceRef{documentRef(sources[0])}, summary.Evidence.Sources...)
			}
			summary.SourceRefs = nil
			if _, e = s.PublishLite(ctx, "legacy-v2-append", j2.ID, j2.Attempt, &types.Publication{Pages: []types.PageMutation{{Page: *page, ExpectedVersion: page.Version}, {Page: *summary, ExpectedVersion: summary.Version}}, SourceVersionIDs: []string{v2.ID}, Sources: []types.SourceConclusion{{SourceVersionID: v2.ID, State: "processed", Complete: true, Coverage: readV2.Coverage}}, Final: true}); e != nil {
				t.Fatal(e)
			}
			page, e = s.GetPageBySlug(ctx, j.LibraryID, "concept/session")
			if e != nil {
				t.Fatal(e)
			}
			lib, e := s.GetLibrary(ctx, j.LibraryID)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = s.DeletePage(ctx, types.DeletePageInput{RequestID: "archive-legacy", LibraryID: lib.ID, PageID: page.ID, ExpectedVersion: page.Version, StructureVersion: lib.StructureVersion}); e != nil {
				t.Fatal(e)
			}
			imported, e = s.ImportSource(ctx, types.ImportSourceInput{RequestID: "source-v3", LibraryID: j.LibraryID, SourceID: v2.SourceID, ExpectedVersion: 2, Filename: v2.Filename, MIME: v2.MIME, Bytes: []byte("# 第三版\n只剩备份资料")})
			if e != nil {
				t.Fatal(e)
			}
			j3 := accept(imported.ResourceID, "accept-v3")
			deleted := ""
			third := &contractModel{respond: func(r GenerateRequest) (GenerateResult, error) {
				switch r.Stage {
				case "map":
					return GenerateResult{Text: `{"entities":[],"concepts":[]}`}, nil
				case "summary":
					return GenerateResult{Text: "SUMMARY: 第三版\n只剩备份资料"}, nil
				case "reduce":
					input := r.Messages[len(r.Messages)-1].Content
					start, end := strings.Index(input, "<deleted_documents>"), strings.Index(input, "</deleted_documents>")
					if start >= 0 && end > start {
						deleted = input[start:end]
					}
					body := "SIBLING_FACT"
					for _, fact := range []string{"FIRST_OLD_FACT", "SECOND_OLD_FACT"} {
						if !strings.Contains(deleted, fact) {
							body += "\n" + fact
						}
					}
					return GenerateResult{Text: "SUMMARY: 撤回结果\n" + body}, nil
				}
				return normalReply(r)
			}}
			result, e := Run(ctx, s, Input{JobID: j3.ID, Generator: third})
			if e != nil {
				t.Fatal(e)
			}
			after, e := s.GetPageBySlug(ctx, j.LibraryID, "concept/session")
			if e != nil {
				t.Fatal(e)
			}
			t.Logf("result=%s deleted=%q after=%q evidence=%+v", result.State, deleted, after.Content, after.Evidence.Sources)
			if !strings.Contains(deleted, "FIRST_OLD_FACT") || !strings.Contains(deleted, "SECOND_OLD_FACT") {
				t.Errorf("not all prior versions reached deleted_documents")
			}
			if strings.Contains(after.Content, "OLD_FACT") {
				t.Errorf("old fact kept after all old source evidence was removed")
			}
		})
	}
}

// TestPlanRebuildPreservesAdditionAcrossEntityConcept 防止精确复用同页后类型漂移重复合成成功贡献。
func TestPlanRebuildPreservesAdditionAcrossEntityConcept(t *testing.T) {
	ctx := context.Background()
	s, j, sources := contractFixture(t, 1)
	generation, goodCalls := 1, 0
	model := &contractModel{respond: func(r GenerateRequest) (GenerateResult, error) {
		switch r.Stage {
		case "map":
			if generation == 1 {
				return GenerateResult{Text: `{"entities":[{"name":"会话管理","slug":"entity/session","description":"会话规则","details":"会话原始贡献"}],"concepts":[{"name":"恢复机制","slug":"concept/recovery","description":"恢复规则","details":"恢复会话"}]}`}, nil
			}
			return GenerateResult{Text: `{"entities":[],"concepts":[{"name":"会话管理","slug":"concept/session","description":"会话规则更新","details":"同一来源相同主题"},{"name":"恢复机制","slug":"concept/recovery","description":"恢复规则","details":"恢复会话"}]}`}, nil
		case "cite":
			return GenerateResult{Text: `{"citations":{"entity/session":["c000"],"concept/session":["c000"],"concept/recovery":["c000"]},"new_slugs":[]}`}, nil
		case "reduce":
			input := r.Messages[len(r.Messages)-1].Content
			if strings.Contains(input, "<slug>entity/session</slug>") {
				goodCalls++
				return GenerateResult{Text: fmt.Sprintf("SUMMARY: 会话规则\n同一来源重复合成次数=%d", goodCalls)}, nil
			}
			if strings.Contains(input, "<slug>concept/recovery</slug>") && generation == 1 {
				return GenerateResult{}, fmt.Errorf("status 403: controlled failure")
			}
		}
		return normalReply(r)
	}}
	if e := s.EnsureOrganizeSourceWork(ctx, j.ID); e != nil {
		t.Fatal(e)
	}
	work, e := s.ListOrganizeSourceWork(ctx, j.ID)
	if e != nil {
		t.Fatal(e)
	}
	svc := &liteService{store: s, job: j, library: j.LibrarySnapshot, input: Input{Generator: model, Reader: MarkdownReader{}}}
	if e = svc.processBatch(ctx, work); e != nil {
		t.Fatal(e)
	}
	before, e := s.GetPageBySlug(ctx, j.LibraryID, "entity/session")
	if e != nil {
		t.Fatal(e)
	}
	payload, e := s.LoadLitePlan(ctx, j.ID, j.Attempt, sources[0].ID)
	if e != nil {
		t.Fatal(e)
	}
	var legacy documentPlan
	if e = json.Unmarshal([]byte(payload), &legacy); e != nil {
		t.Fatal(e)
	}
	legacy.ModelValidationVersion = 0
	time.Sleep(2 * time.Millisecond)
	if e = s.SaveLitePlan(ctx, j.ID, j.Attempt, sources[0].ID, string(marshal(legacy))); e != nil {
		t.Fatal(e)
	}
	work, e = s.ListOrganizeSourceWork(ctx, j.ID)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = svc.finalize(ctx, work); e != nil {
		t.Fatal(e)
	}
	current, e := s.GetJob(ctx, j.ID)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.RetryJob(ctx, types.JobActionInput{RequestID: "retry-changed-type", JobID: j.ID, ExpectedVersion: current.Version}); e != nil {
		t.Fatal(e)
	}
	generation = 2
	result, e := Run(ctx, s, Input{JobID: j.ID, Generator: model})
	if e != nil {
		t.Fatal(e)
	}
	after, e := s.GetPageBySlug(ctx, j.LibraryID, "entity/session")
	if e != nil {
		t.Fatal(e)
	}
	t.Logf("state=%s goodCalls=%d before=%q after=%q", result.State, goodCalls, before.Content, after.Content)
	if result.State != "completed" || goodCalls != 1 || after.Content != before.Content {
		t.Errorf("successful same-source-version/page contribution replayed across entity-concept change")
	}
}

// TestRebuiltPlanAdoptsOnlyCommittedAdditionReceipts 验证跨类型重组兼容旧语义与完整内容回执，缺失回执时仍执行实际贡献。
func TestRebuiltPlanAdoptsOnlyCommittedAdditionReceipts(t *testing.T) {
	for _, mode := range []string{"typed-entity", "typed-concept", "payload", "uncommitted"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			s, j, sources := contractFixture(t, 1)
			oldKind, newKind := "entity", "concept"
			if mode == "typed-concept" {
				oldKind, newKind = "concept", "entity"
			}
			generation, reduceCalls := 1, 0
			model := &contractModel{respond: func(r GenerateRequest) (GenerateResult, error) {
				if r.Stage == "map" {
					kind := oldKind
					if generation == 2 {
						kind = newKind
					}
					candidate := fmt.Sprintf(`[{"name":"会话管理","slug":"%s/session","description":"会话规则%d","details":"同一不可变来源"}]`, kind, generation)
					if kind == "entity" {
						return GenerateResult{Text: `{"entities":` + candidate + `,"concepts":[]}`}, nil
					}
					return GenerateResult{Text: `{"entities":[],"concepts":` + candidate + `}`}, nil
				}
				if r.Stage == "cite" {
					return GenerateResult{Text: `{"citations":{},"new_slugs":[]}`}, nil
				}
				if r.Stage == "reduce" {
					reduceCalls++
					return GenerateResult{Text: "SUMMARY: 本次贡献\n确实应用了未确认的贡献。"}, nil
				}
				return normalReply(r)
			}}
			if e := s.EnsureOrganizeSourceWork(ctx, j.ID); e != nil {
				t.Fatal(e)
			}
			work, e := s.ListOrganizeSourceWork(ctx, j.ID)
			if e != nil {
				t.Fatal(e)
			}
			svc := &liteService{store: s, job: j, library: j.LibrarySnapshot, input: Input{Generator: model, Reader: MarkdownReader{}}}
			mapped, e := svc.mapDocument(ctx, work[0], nil)
			if e != nil {
				t.Fatal(e)
			}
			slug := oldKind + "/session"
			contribution := mapped.updates[slug][0]
			page := types.WikiPage{ID: "committed-type-page", KnowledgeBaseID: j.LibraryID, Slug: slug, Title: "会话管理", PageType: oldKind, Status: "published", Content: "已经确认的旧计划贡献。", Evidence: types.PageEvidence{Sources: []types.SourceRef{documentRef(sources[0])}}}
			keys := []string{}
			if mode != "uncommitted" {
				previousHash := hash(marshal([]string{"source-contribution", sources[0].ID, slug, oldKind}))
				if mode == "payload" {
					previousHash = hash(marshal(contribution))
				}
				keys = append(keys, fmt.Sprintf("lite/%s/%d/applied/%s", j.ID, j.Attempt, previousHash))
			}
			if _, e = s.PublishLite(ctx, "legacy-type-page", j.ID, j.Attempt, &types.Publication{Pages: []types.PageMutation{{Page: page}}, SourceVersionIDs: []string{sources[0].ID}}, keys...); e != nil {
				t.Fatal(e)
			}
			legacy := documentPlan{Artifact: mapped.artifact, Updates: mapped.updates, Summary: mapped.summary, Description: mapped.description}
			time.Sleep(2 * time.Millisecond)
			if e = s.SaveLitePlan(ctx, j.ID, j.Attempt, sources[0].ID, string(marshal(legacy))); e != nil {
				t.Fatal(e)
			}
			current, e := s.GetJob(ctx, j.ID)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = s.FailJob(ctx, types.FailJobInput{JobActionInput: types.JobActionInput{RequestID: "type-partial-fail", JobID: j.ID, ExpectedVersion: current.Version}, Diagnostic: "other page interrupted", Sources: []types.SourceConclusion{{SourceVersionID: sources[0].ID, State: "failed", Diagnostic: "other page interrupted"}}}); e != nil {
				t.Fatal(e)
			}
			current, e = s.GetJob(ctx, j.ID)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = s.RetryJob(ctx, types.JobActionInput{RequestID: "type-retry", JobID: j.ID, ExpectedVersion: current.Version}); e != nil {
				t.Fatal(e)
			}
			generation = 2
			result, e := Run(ctx, s, Input{JobID: j.ID, Generator: model})
			if e != nil {
				t.Fatal(e)
			}
			after, e := s.GetPageBySlug(ctx, j.LibraryID, slug)
			if e != nil {
				t.Fatal(e)
			}
			if mode == "uncommitted" {
				if result.State != "completed" || reduceCalls != 1 || after.Content == page.Content {
					t.Fatalf("uncommitted contribution dropped: state=%s calls=%d body=%q", result.State, reduceCalls, after.Content)
				}
			} else if result.State != "completed" || reduceCalls != 0 || after.Content != page.Content {
				t.Fatalf("committed legacy contribution replayed: state=%s calls=%d body=%q", result.State, reduceCalls, after.Content)
			}
			applied, e := s.HasLiteContribution(ctx, j.ID, hash(marshal([]string{"source-contribution", sources[0].ID, slug, "addition"})))
			if e != nil || !applied {
				t.Fatalf("stable addition receipt absent: %v %v", applied, e)
			}
		})
	}
}
