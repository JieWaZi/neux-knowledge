package pipeline

import (
	"context"
	"fmt"
	"strings"
	"testing"

	types "github.com/JieWaZi/neux-knowledge/internal/domain"
)

func TestArchivedDisappearedTopicDoesNotRestoreOldEvidence(t *testing.T) {
	ctx := context.Background()
	s, j, sources := contractFixture(t, 1)
	if _, err := Run(ctx, s, Input{JobID: j.ID, Generator: &contractModel{respond: normalReply}}); err != nil {
		t.Fatal(err)
	}
	page, err := s.GetPageBySlug(ctx, j.LibraryID, "concept/session")
	if err != nil {
		t.Fatal(err)
	}
	lib, err := s.GetLibrary(ctx, j.LibraryID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeletePage(ctx, types.DeletePageInput{RequestID: "archive-page", LibraryID: j.LibraryID, PageID: page.ID, ExpectedVersion: page.Version, StructureVersion: lib.StructureVersion}); err != nil {
		t.Fatal(err)
	}
	imported, err := s.ImportSource(ctx, types.ImportSourceInput{RequestID: "source-v2", LibraryID: j.LibraryID, SourceID: sources[0].SourceID, ExpectedVersion: sources[0].Version, Filename: sources[0].Filename, MIME: sources[0].MIME, Bytes: []byte("# 已转移的资料\n本资料只记录备份和恢复，不再包含会话管理主题。")})
	if err != nil {
		t.Fatal(err)
	}
	accept := func(request string, ids []string) *types.Job {
		t.Helper()
		lib, err := s.GetLibrary(ctx, j.LibraryID)
		if err != nil {
			t.Fatal(err)
		}
		rec, err := s.AcceptJob(ctx, types.AcceptJobInput{RequestID: request, LibraryID: lib.ID, ExpectedVersion: lib.Version, StructureVersion: lib.StructureVersion, SourceVersionIDs: ids, Permission: j.Permission, RulesVersion: "rules", ConfigDigest: request})
		if err != nil {
			t.Fatal(err)
		}
		job, err := s.GetJob(ctx, rec.JobID)
		if err != nil {
			t.Fatal(err)
		}
		return job
	}
	next := accept("accept-v2", []string{imported.ResourceID})
	empty := &contractModel{respond: func(r GenerateRequest) (GenerateResult, error) {
		if r.Stage == "map" {
			return GenerateResult{Text: `{"entities":[],"concepts":[]}`, FinishReason: "stop"}, nil
		}
		if r.Stage == "summary" {
			return GenerateResult{Text: "SUMMARY: 备份资料\n本资料只记录备份和恢复。", FinishReason: "stop"}, nil
		}
		return normalReply(r)
	}}
	if _, err := Run(ctx, s, Input{JobID: next.ID, Generator: empty}); err != nil {
		t.Fatal(err)
	}
	fresh, err := s.ImportSource(ctx, types.ImportSourceInput{RequestID: "fresh-source", LibraryID: j.LibraryID, Filename: "new.md", MIME: "text/markdown", Bytes: []byte("# 会话管理\n此新资料重新说明会话管理规则。")})
	if err != nil {
		t.Fatal(err)
	}
	last := accept("accept-fresh", []string{fresh.ResourceID})
	if _, err := Run(ctx, s, Input{JobID: last.ID, Generator: &contractModel{respond: normalReply}}); err != nil {
		t.Fatal(err)
	}
	resurrected, err := s.GetPageBySlug(ctx, j.LibraryID, "concept/session")
	if err != nil {
		t.Fatal(err)
	}
	if resurrected.Status != "published" {
		t.Fatalf("not republished: %+v", resurrected)
	}
	for _, ref := range resurrected.Evidence.Sources {
		if ref.SourceVersionID == sources[0].ID {
			t.Fatalf("resurrected page still cites obsolete source v1 absent from current topic: %+v", resurrected.Evidence)
		}
	}
}

func TestArchivedDisappearedTopicPreservesSiblingAndProtection(t *testing.T) {
	for _, protected := range []bool{false, true} {
		t.Run(map[bool]string{false: "sibling", true: "protected"}[protected], func(t *testing.T) {
			ctx := context.Background()
			s, j, sources := contractFixture(t, 2)
			initial := &contractModel{respond: func(r GenerateRequest) (GenerateResult, error) {
				if r.Stage == "summary" {
					if r.SourceVersionID == sources[0].ID {
						return GenerateResult{Text: "SUMMARY: 来源一旧版\n旧地址由来源一提供。"}, nil
					}
					return GenerateResult{Text: "SUMMARY: 来源二\n另一来源的独立规则。"}, nil
				}
				if r.Stage == "reduce" {
					return GenerateResult{Text: "SUMMARY: 会话知识\n旧地址由来源一提供；另一来源的独立规则。"}, nil
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
			lib, err := s.GetLibrary(ctx, j.LibraryID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.DeletePage(ctx, types.DeletePageInput{RequestID: "archive-shared", LibraryID: j.LibraryID, PageID: page.ID, ExpectedVersion: page.Version, StructureVersion: lib.StructureVersion}); err != nil {
				t.Fatal(err)
			}
			before, err := s.GetPage(ctx, j.LibraryID, page.ID)
			if err != nil {
				t.Fatal(err)
			}
			if protected {
				lib, err = s.GetLibrary(ctx, j.LibraryID)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := s.WritePage(ctx, types.WritePageInput{RequestID: "protect-shared", LibraryID: j.LibraryID, StructureVersion: lib.StructureVersion, ExpectedVersion: before.Version, Page: *before}); err != nil {
					t.Fatal(err)
				}
				before, err = s.GetPage(ctx, j.LibraryID, page.ID)
				if err != nil {
					t.Fatal(err)
				}
			}
			imported, err := s.ImportSource(ctx, types.ImportSourceInput{RequestID: "withdraw-topic", LibraryID: j.LibraryID, SourceID: sources[0].SourceID, ExpectedVersion: sources[0].Version, Filename: sources[0].Filename, MIME: sources[0].MIME, Bytes: []byte("# 新版资料\n只记录备份，不再提及原主题。")})
			if err != nil {
				t.Fatal(err)
			}
			lib, err = s.GetLibrary(ctx, j.LibraryID)
			if err != nil {
				t.Fatal(err)
			}
			accepted, err := s.AcceptJob(ctx, types.AcceptJobInput{RequestID: "accept-withdraw-topic", LibraryID: lib.ID, ExpectedVersion: lib.Version, StructureVersion: lib.StructureVersion, SourceVersionIDs: []string{imported.ResourceID}, Permission: j.Permission, RulesVersion: "rules", ConfigDigest: "v2"})
			if err != nil {
				t.Fatal(err)
			}
			sawContext := false
			model := &contractModel{respond: func(r GenerateRequest) (GenerateResult, error) {
				switch r.Stage {
				case "map":
					return GenerateResult{Text: `{"entities":[],"concepts":[]}`}, nil
				case "summary":
					return GenerateResult{Text: "SUMMARY: 备份资料\n新版仅记录备份。"}, nil
				case "reduce":
					input := r.Messages[len(r.Messages)-1].Content
					sawContext = strings.Contains(input, "旧地址由来源一提供。") && strings.Contains(input, "另一来源的独立规则。")
					return GenerateResult{Text: "SUMMARY: 另一来源规则\n另一来源的独立规则。"}, nil
				}
				return normalReply(r)
			}}
			result, err := Run(ctx, s, Input{JobID: accepted.JobID, Generator: model})
			if err != nil {
				t.Fatal(err)
			}
			after, err := s.GetPage(ctx, j.LibraryID, page.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !sawContext || after.Status != "archived" {
				t.Fatalf("missing old/sibling context or archive revived: context=%v page=%+v", sawContext, after)
			}
			if protected {
				if result.State != "failed" || after.Content != before.Content || after.Version != before.Version {
					t.Fatalf("protected archive changed or source falsely completed: result=%+v page=%+v", result, after)
				}
			} else {
				if result.State != "completed" || len(after.Evidence.Sources) != 1 || after.Evidence.Sources[0].SourceVersionID != sources[1].ID {
					t.Fatalf("sibling evidence lost or old evidence retained: result=%+v evidence=%+v", result, after.Evidence)
				}
			}
		})
	}
}

// TestLiteRetryDoesNotReplayCorrectedRetraction 防止纠正撤回上下文后旧计划恢复重复合成已成功页。
func TestLiteRetryDoesNotReplayCorrectedRetraction(t *testing.T) {
	ctx := context.Background()
	s, j, sources := contractFixture(t, 1)
	if _, err := Run(ctx, s, Input{JobID: j.ID, Generator: &contractModel{respond: normalReply}}); err != nil {
		t.Fatal(err)
	}
	imported, err := s.ImportSource(ctx, types.ImportSourceInput{RequestID: "retraction-v2", LibraryID: j.LibraryID, SourceID: sources[0].SourceID, ExpectedVersion: sources[0].Version, Filename: sources[0].Filename, MIME: sources[0].MIME, Bytes: []byte("# 恢复资料\n本版仅记录恢复，不再涉及原先的会话管理。")})
	if err != nil {
		t.Fatal(err)
	}
	lib, err := s.GetLibrary(ctx, j.LibraryID)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := s.AcceptJob(ctx, types.AcceptJobInput{RequestID: "accept-retraction-v2", LibraryID: lib.ID, ExpectedVersion: lib.Version, StructureVersion: lib.StructureVersion, SourceVersionIDs: []string{imported.ResourceID}, Permission: j.Permission, RulesVersion: "rules", ConfigDigest: "v2"})
	if err != nil {
		t.Fatal(err)
	}
	job, err := s.GetJob(ctx, accepted.JobID)
	if err != nil {
		t.Fatal(err)
	}
	failRecovery := true
	retractions := 0
	model := &contractModel{respond: func(r GenerateRequest) (GenerateResult, error) {
		switch r.Stage {
		case "map":
			return GenerateResult{Text: `{"entities":[],"concepts":[{"name":"恢复机制","slug":"concept/recovery","description":"恢复条件","details":"本版仅描述恢复。"}]}`}, nil
		case "summary":
			return GenerateResult{Text: "SUMMARY: 恢复资料\n本版仅描述恢复机制。"}, nil
		case "cite":
			return GenerateResult{Text: `{"citations":{"concept/recovery":["c000"]},"new_slugs":[]}`}, nil
		case "reduce":
			input := r.Messages[len(r.Messages)-1].Content
			if strings.Contains(input, "<slug>concept/session</slug>") {
				retractions++
				return GenerateResult{Text: "SUMMARY: 已撤回旧来源\n# 会话管理\n旧来源已不再支持本主题。"}, nil
			}
			if failRecovery {
				return GenerateResult{}, fmt.Errorf("status 403: controlled recovery failure")
			}
		}
		return normalReply(r)
	}}
	if err := s.EnsureOrganizeSourceWork(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	work, err := s.ListOrganizeSourceWork(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	svc := &liteService{store: s, job: job, library: job.LibrarySnapshot, input: Input{Generator: model, Reader: MarkdownReader{}}}
	if err := svc.processBatch(ctx, work); err != nil {
		t.Fatal(err)
	}
	work, err = s.ListOrganizeSourceWork(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.finalize(ctx, work); err != nil {
		t.Fatal(err)
	}
	job, err = s.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RetryJob(ctx, types.JobActionInput{RequestID: "retry-recovery-only", JobID: job.ID, ExpectedVersion: job.Version}); err != nil {
		t.Fatal(err)
	}
	failRecovery = false
	result, err := Run(ctx, s, Input{JobID: job.ID, Generator: model})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != "completed" || retractions != 1 {
		t.Fatalf("successful retraction replayed: state=%s calls=%d", result.State, retractions)
	}
}
