package pipeline

import (
	"context"
	"strings"
	"testing"

	types "github.com/JieWaZi/neux-knowledge/internal/domain"
)

// TestSummaryRegenerationUsesCurrentSource 摘要是当前单份来源的派生内容，不把旧归纳表的编号和排版当成事实身份。
func TestSummaryRegenerationUsesCurrentSource(t *testing.T) {
	for _, mode := range []string{"table-to-prose", "renumbered", "unrelated-identity", "source-deleted", "manual", "superseded", "truncated"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			raw := "# 服务规则\n服务A禁止访问，服务B允许访问。\n| 序号 | 告警名称 |\n|---|---|\n| 1 | 告警甲 |\n| 2 | 告警乙 |\n"
			oldBody := "| 编号 | 服务 | 权限 |\n|---|---|---|\n| 1 | 服务A | 禁止访问 |\n| 2 | 服务B | 允许访问 |\n"
			candidate := "# 服务规则\n服务A禁止访问，服务B允许访问。"
			if mode == "renumbered" {
				candidate = "| 服务 | 权限 |\n|---|---|\n| 服务A | 禁止访问 |\n| 服务B | 允许访问 |\n"
			}
			if mode == "unrelated-identity" {
				candidate = "| 需求编号 | 规则 |\n|---|---|\n| RULE-A | 服务A禁止访问 |\n| RULE-B | 服务B允许访问 |\n"
			}
			s, j, sources := contractFixture(t, 1, []byte(raw))
			initial := &contractModel{respond: func(r GenerateRequest) (GenerateResult, error) {
				if r.Stage == "summary" {
					return GenerateResult{Text: "SUMMARY: 来源服务规则\n" + oldBody, FinishReason: "stop"}, nil
				}
				return normalReply(r)
			}}
			if _, err := Run(ctx, s, Input{JobID: j.ID, Generator: initial}); err != nil {
				t.Fatal(err)
			}
			before, err := s.GetPageBySlug(ctx, j.LibraryID, "summary/"+sources[0].SourceID)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "manual" {
				lib, e := s.GetLibrary(ctx, j.LibraryID)
				if e != nil {
					t.Fatal(e)
				}
				if _, e = s.WritePage(ctx, types.WritePageInput{RequestID: "protect-summary", LibraryID: j.LibraryID, StructureVersion: lib.StructureVersion, ExpectedVersion: before.Version, Page: *before}); e != nil {
					t.Fatal(e)
				}
				before, err = s.GetPage(ctx, j.LibraryID, before.ID)
				if err != nil {
					t.Fatal(err)
				}
			}
			current := sources[0]
			if mode == "source-deleted" {
				imported, e := s.ImportSource(ctx, types.ImportSourceInput{RequestID: "source-only-a", LibraryID: j.LibraryID, SourceID: current.SourceID, ExpectedVersion: current.Version, Filename: current.Filename, MIME: current.MIME, Bytes: []byte("# 当前服务规则\n仅保留服务A禁止访问的规则。")})
				if e != nil {
					t.Fatal(e)
				}
				current, e = s.GetSource(ctx, j.LibraryID, imported.ResourceID)
				if e != nil {
					t.Fatal(e)
				}
				candidate = "# 当前服务规则\n仅保留服务A禁止访问的规则。"
			}
			lib, err := s.GetLibrary(ctx, j.LibraryID)
			if err != nil {
				t.Fatal(err)
			}
			accepted, err := s.AcceptJob(ctx, types.AcceptJobInput{RequestID: "regenerate", LibraryID: lib.ID, ExpectedVersion: lib.Version, StructureVersion: lib.StructureVersion, SourceVersionIDs: []string{current.ID}, Permission: j.Permission, RulesVersion: "rules", ConfigDigest: "current-summary", Rebuild: true})
			if err != nil {
				t.Fatal(err)
			}
			j, err = s.GetJob(ctx, accepted.JobID)
			if err != nil {
				t.Fatal(err)
			}
			summaryCalls := 0
			model := &contractModel{respond: func(r GenerateRequest) (GenerateResult, error) {
				if r.Stage == "summary" {
					summaryCalls++
					if mode == "superseded" {
						currentLib, e := s.GetLibrary(ctx, j.LibraryID)
						if e != nil {
							return GenerateResult{}, e
						}
						manual := clonePage(*before)
						manual.Content += "\n人工补充。"
						if _, e = s.WritePage(ctx, types.WritePageInput{RequestID: "concurrent-manual", LibraryID: j.LibraryID, StructureVersion: currentLib.StructureVersion, ExpectedVersion: before.Version, Page: manual}); e != nil {
							return GenerateResult{}, e
						}
					}
					reason := "stop"
					if mode == "truncated" {
						reason = "length"
					}
					return GenerateResult{Text: "SUMMARY: 当前服务规则\n" + candidate, FinishReason: reason}, nil
				}
				if r.Stage == "summary_repair" {
					t.Error("derived summary was forced through table-row repair")
				}
				return normalReply(r)
			}}
			result := &Result{State: "pending"}
			if mode == "superseded" {
				if err = s.EnsureOrganizeSourceWork(ctx, j.ID); err != nil {
					t.Fatal(err)
				}
				work, e := s.ListOrganizeSourceWork(ctx, j.ID)
				if e != nil {
					t.Fatal(e)
				}
				svc := &liteService{store: s, job: j, library: j.LibrarySnapshot, input: Input{Generator: model, Reader: MarkdownReader{}}}
				if e = svc.processBatch(ctx, work); e != nil {
					t.Fatal(e)
				}
			} else {
				result, err = Run(ctx, s, Input{JobID: j.ID, Generator: model})
				if err != nil {
					t.Fatal(err)
				}
			}
			after, err := s.GetPage(ctx, j.LibraryID, before.ID)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "manual" || mode == "truncated" {
				if result.State != "failed" || after.Content != before.Content || after.Version != before.Version {
					t.Fatalf("failed regeneration changed protected/prior summary: %s %+v", result.State, after)
				}
			} else if mode == "superseded" {
				if after.Content != before.Content+"\n人工补充。" || after.Version != before.Version+1 {
					t.Fatalf("late summary overwrote manual content: %+v", after)
				}
			} else {
				if result.State != "completed" || after.ID != before.ID || after.Version != before.Version+1 || strings.TrimSpace(after.Content) != strings.TrimSpace(candidate) {
					t.Fatalf("valid summary rewrite rejected: mode=%s state=%s summary=%+v", mode, result.State, after)
				}
				if len(after.Evidence.Sources) != 1 || after.Evidence.Sources[0].SourceVersionID != current.ID || after.Evidence.Sources[0].SHA256 != current.SHA256 {
					t.Fatalf("summary evidence was not replaced: %+v", after.Evidence)
				}
				if mode == "unrelated-identity" && strings.Contains(after.Content, "告警") {
					t.Fatalf("unrelated original ordinal table was copied into summary: %s", after.Content)
				}
			}
			if mode != "truncated" && summaryCalls != 1 {
				t.Fatalf("summary regenerated %d times", summaryCalls)
			}
		})
	}
}

// TestSummaryPublicationRequiresExactCurrentArtifact 防止专用摘要发布路径被错误来源、错误地址或不完整阅读结果调用。
func TestSummaryPublicationRequiresExactCurrentArtifact(t *testing.T) {
	for _, mode := range []string{"wrong-source", "wrong-ref", "wrong-sha", "partial", "wrong-slug", "empty"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			s, j, sources := contractFixture(t, 1)
			source := sources[0]
			raw, err := s.ReadRaw(ctx, j.LibraryID, source.ID)
			if err != nil {
				t.Fatal(err)
			}
			artifact, err := (MarkdownReader{}).Read(ctx, ReadRequest{SourceID: source.SourceID, SourceVersionID: source.ID, SHA256: source.SHA256, Filename: source.Filename, MIME: source.MIME, Raw: raw, Scope: "all"})
			if err != nil {
				t.Fatal(err)
			}
			slug := "summary/" + source.SourceID
			update := SlugUpdate{Slug: slug, Type: "summary", SourceVersionID: source.ID, KnowledgeID: source.SourceID, SourceRef: source.SourceID, DocTitle: source.Filename, SummaryBody: "当前来源的摘要正文。", SummaryLine: "当前来源摘要"}
			switch mode {
			case "wrong-source":
				update.KnowledgeID = "another-source"
			case "wrong-ref":
				update.SourceRef = "another-source"
			case "wrong-sha":
				artifact.SHA256 = "wrong"
			case "partial":
				artifact.Status = "partial"
			case "wrong-slug":
				slug = "summary/wrong"
				update.Slug = slug
			case "empty":
				update.SummaryBody = " "
			}
			svc := &liteService{store: s, job: j, library: j.LibrarySnapshot}
			reducer := &upstreamReducer{liteService: svc, sources: map[string]*types.SourceVersion{source.ID: source}, artifacts: map[string]ReadArtifact{source.ID: artifact}}
			_, _, _, _, err = reducer.reduceSlugUpdates(ctx, j.LibraryID, slug, []SlugUpdate{update}, svc.newBatchContext(nil))
			if err == nil {
				t.Fatalf("invalid summary publication accepted: %s", mode)
			}
			_, total, e := s.ListPages(ctx, types.PageQuery{LibraryID: j.LibraryID, Limit: 10})
			if e != nil {
				t.Fatal(e)
			}
			if total != 0 {
				t.Fatalf("invalid summary was persisted: %s", mode)
			}
		})
	}
}
