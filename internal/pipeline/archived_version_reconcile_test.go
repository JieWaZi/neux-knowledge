package pipeline

import (
	"context"
	"strings"
	"testing"

	types "github.com/JieWaZi/neux-knowledge/internal/domain"
)

// TestArchivedPageRebuildRetractsPriorVersion 验证归档页换版撤回旧贡献，并保留其他来源及人工修改。
func TestArchivedPageRebuildRetractsPriorVersion(t *testing.T) {
	for _, mode := range []string{"replaced", "protected", "superseded", "summary-already-replaced"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			s, j, sources := contractFixture(t, 2)
			initial := &contractModel{respond: func(r GenerateRequest) (GenerateResult, error) {
				if r.Stage == "summary" {
					if r.SourceVersionID == sources[0].ID {
						return GenerateResult{Text: "SUMMARY: 原件旧版\n旧地址由来源一提供。"}, nil
					}
					return GenerateResult{Text: "SUMMARY: 另一来源\n另一来源的独立规则。"}, nil
				}
				if r.Stage == "reduce" {
					return GenerateResult{Text: "SUMMARY: 共同页面\n旧地址由来源一提供；另一来源的独立规则。"}, nil
				}
				return normalReply(r)
			}}
			if _, err := Run(ctx, s, Input{JobID: j.ID, Generator: initial}); err != nil {
				t.Fatal(err)
			}
			page, err := s.GetPageBySlug(ctx, j.LibraryID, "concept/session")
			if err != nil {
				t.Fatal(err)
			}
			summary, err := s.GetPageBySlug(ctx, j.LibraryID, "summary/"+sources[0].SourceID)
			if err != nil {
				t.Fatal(err)
			}
			for _, p := range []*types.WikiPage{page, summary} {
				lib, e := s.GetLibrary(ctx, j.LibraryID)
				if e != nil {
					t.Fatal(e)
				}
				if _, e = s.DeletePage(ctx, types.DeletePageInput{RequestID: "archive-" + p.ID, LibraryID: j.LibraryID, PageID: p.ID, ExpectedVersion: p.Version, StructureVersion: lib.StructureVersion}); e != nil {
					t.Fatal(e)
				}
			}
			before, err := s.GetPage(ctx, j.LibraryID, page.ID)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "protected" {
				lib, e := s.GetLibrary(ctx, j.LibraryID)
				if e != nil {
					t.Fatal(e)
				}
				if _, e = s.WritePage(ctx, types.WritePageInput{RequestID: "protect-archive", LibraryID: j.LibraryID, StructureVersion: lib.StructureVersion, ExpectedVersion: before.Version, Page: *before}); e != nil {
					t.Fatal(e)
				}
				before, err = s.GetPage(ctx, j.LibraryID, page.ID)
				if err != nil {
					t.Fatal(err)
				}
			}
			imported, err := s.ImportSource(ctx, types.ImportSourceInput{RequestID: "source-v2", LibraryID: j.LibraryID, SourceID: sources[0].SourceID, ExpectedVersion: sources[0].Version, Filename: sources[0].Filename, MIME: sources[0].MIME, Bytes: []byte("# 会话管理\n新地址由来源一提供，替换旧地址。")})
			if err != nil {
				t.Fatal(err)
			}
			current, err := s.GetSource(ctx, j.LibraryID, imported.ResourceID)
			if err != nil {
				t.Fatal(err)
			}
			lib, err := s.GetLibrary(ctx, j.LibraryID)
			if err != nil {
				t.Fatal(err)
			}
			accepted, err := s.AcceptJob(ctx, types.AcceptJobInput{RequestID: "rebuild-v2", LibraryID: lib.ID, ExpectedVersion: lib.Version, StructureVersion: lib.StructureVersion, SourceVersionIDs: []string{current.ID}, Permission: j.Permission, RulesVersion: "rules", ConfigDigest: "v2", Rebuild: true})
			if err != nil {
				t.Fatal(err)
			}
			j, err = s.GetJob(ctx, accepted.JobID)
			if err != nil {
				t.Fatal(err)
			}
			sawRetract := false
			model := &contractModel{respond: func(r GenerateRequest) (GenerateResult, error) {
				if r.Stage == "reduce" {
					input := r.Messages[len(r.Messages)-1].Content
					start, end := strings.Index(input, "<deleted_documents>"), strings.Index(input, "</deleted_documents>")
					sawRetract = start >= 0 && end > start && strings.Contains(input[start:end], "旧地址由来源一提供。") && strings.Contains(input, "另一来源的独立规则。")
					if mode == "superseded" {
						currentLib, e := s.GetLibrary(ctx, j.LibraryID)
						if e != nil {
							return GenerateResult{}, e
						}
						manual := clonePage(*before)
						manual.Content += "人工新事实。"
						if _, e = s.WritePage(ctx, types.WritePageInput{RequestID: "manual-during-reduce", LibraryID: j.LibraryID, StructureVersion: currentLib.StructureVersion, ExpectedVersion: before.Version, Page: manual}); e != nil {
							return GenerateResult{}, e
						}
					}
					if sawRetract {
						return GenerateResult{Text: "SUMMARY: 新旧贡献替换\n新地址由来源一提供；另一来源的独立规则。"}, nil
					}
					return GenerateResult{Text: "SUMMARY: 缺少撤回\n旧地址由来源一提供；新地址由来源一提供；另一来源的独立规则。"}, nil
				}
				return normalReply(r)
			}}
			if err = s.EnsureOrganizeSourceWork(ctx, j.ID); err != nil {
				t.Fatal(err)
			}
			svc := &liteService{store: s, job: j, library: j.LibrarySnapshot, input: Input{Generator: model, Reader: MarkdownReader{}}}
			work, err := s.ListOrganizeSourceWork(ctx, j.ID)
			if err != nil {
				t.Fatal(err)
			}
			pages, err := svc.pages(ctx)
			if err != nil {
				t.Fatal(err)
			}
			// 预先保存不含归档撤回的计划，模拟旧失败任务恢复，修复不能只放在新Map阶段。
			if _, err = svc.mapDocument(ctx, work[0], pages); err != nil {
				t.Fatal(err)
			}
			if mode == "summary-already-replaced" {
				newSummary, e := s.GetPage(ctx, j.LibraryID, summary.ID)
				if e != nil {
					t.Fatal(e)
				}
				newSummary.Content = "新版来源摘要。"
				newSummary.Evidence.Sources = []types.SourceRef{documentRef(current)}
				newSummary.SourceRefs = nil
				newSummary.Status = "published"
				if _, e = s.PublishLite(ctx, "already-published-new-summary", j.ID, j.Attempt, &types.Publication{Pages: []types.PageMutation{{Page: *newSummary, ExpectedVersion: newSummary.Version}}, SourceVersionIDs: []string{current.ID}}); e != nil {
					t.Fatal(e)
				}
			}
			if err = svc.processBatch(ctx, work); err != nil {
				t.Fatal(err)
			}
			after, err := s.GetPage(ctx, j.LibraryID, page.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !sawRetract {
				t.Error("archived source v1 summary was not retracted while keeping sibling context")
			}
			if mode == "replaced" || mode == "summary-already-replaced" {
				if after.ID != before.ID || after.Version != before.Version+1 || after.Status != "published" || strings.Contains(after.Content, "旧地址") || !strings.Contains(after.Content, "另一来源") {
					t.Fatalf("archive version not replaced: %+v", after)
				}
				versions := map[string]bool{}
				for _, ref := range after.Evidence.Sources {
					versions[ref.SourceVersionID] = true
				}
				if versions[sources[0].ID] || !versions[current.ID] || !versions[sources[1].ID] {
					t.Fatalf("wrong version evidence after archive rebuild: %+v", after.Evidence)
				}
			} else if mode == "protected" {
				if after.Content != before.Content || after.Version != before.Version || after.Status != "archived" {
					t.Fatalf("protected archived body changed: %+v", after)
				}
			} else {
				if after.Content != before.Content+"人工新事实。" || after.Version != before.Version+1 || after.Status != "archived" {
					t.Fatalf("late archive rewrite replaced manual content: %+v", after)
				}
			}
		})
	}
}
