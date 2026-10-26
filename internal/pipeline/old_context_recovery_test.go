package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	types "github.com/JieWaZi/neux-knowledge/internal/domain"
	"github.com/JieWaZi/neux-knowledge/internal/store"
	"strings"
	"testing"
)

type oldContributionReader func(context.Context, ReadRequest) (ReadArtifact, error)

func (f oldContributionReader) Read(ctx context.Context, r ReadRequest) (ReadArtifact, error) {
	return f(ctx, r)
}

func oldContributionFixture(t *testing.T, raw []byte) (*store.Store, *types.Job, *types.SourceVersion, *types.WikiPage) {
	t.Helper()
	ctx := context.Background()
	s, j, sources := contractFixture(t, 1, raw)
	old := sources[0]
	a, e := (MarkdownReader{}).Read(ctx, ReadRequest{SourceID: old.SourceID, SourceVersionID: old.ID, SHA256: old.SHA256, MIME: old.MIME, Filename: old.Filename, Raw: raw, Scope: "all"})
	if e != nil {
		t.Fatal(e)
	}
	page := types.WikiPage{ID: "old-contribution", KnowledgeBaseID: j.LibraryID, Slug: "concept/session", Title: "会话管理", PageType: "concept", Status: "published", Content: "OLD_RETIRED_FACT", Evidence: types.PageEvidence{Sources: []types.SourceRef{documentRef(old)}}}
	if _, e = s.PublishLite(ctx, "seed-without-summary", j.ID, j.Attempt, &types.Publication{Pages: []types.PageMutation{{Page: page}}, SourceVersionIDs: []string{old.ID}, Sources: []types.SourceConclusion{{SourceVersionID: old.ID, State: "processed", Complete: true, Coverage: a.Coverage}}, Final: true}); e != nil {
		t.Fatal(e)
	}
	before, e := s.GetPageBySlug(ctx, j.LibraryID, page.Slug)
	if e != nil {
		t.Fatal(e)
	}
	rec, e := s.ImportSource(ctx, types.ImportSourceInput{RequestID: "old-context-v2", LibraryID: j.LibraryID, SourceID: old.SourceID, ExpectedVersion: old.Version, Filename: old.Filename, MIME: old.MIME, Bytes: []byte("# 新版资料\n只记录备份恢复。")})
	if e != nil {
		t.Fatal(e)
	}
	lib, e := s.GetLibrary(ctx, j.LibraryID)
	if e != nil {
		t.Fatal(e)
	}
	accepted, e := s.AcceptJob(ctx, types.AcceptJobInput{RequestID: "accept-context-v2", LibraryID: lib.ID, ExpectedVersion: lib.Version, StructureVersion: lib.StructureVersion, SourceVersionIDs: []string{rec.ResourceID}, Permission: j.Permission, RulesVersion: "rules", ConfigDigest: "old-context"})
	if e != nil {
		t.Fatal(e)
	}
	next, e := s.GetJob(ctx, accepted.JobID)
	if e != nil {
		t.Fatal(e)
	}
	return s, next, old, before
}

// TestOldRetractionContextRestoresRawOrPreservesPage 验证缺失摘要时真实旧原文完整回退，无法恢复则失败并保留旧页证据。
func TestOldRetractionContextRestoresRawOrPreservesPage(t *testing.T) {
	for _, mode := range []string{"raw", "unsupported", "large"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			raw := []byte("# 旧版资料\nOLD_RETIRED_FACT")
			if mode == "large" {
				raw = []byte("# 旧版资料\nOLD_RETIRED_FACT\n" + strings.Repeat("旧", maxContentForWiki) + "\nOLD_TAIL_FACT")
			}
			s, j, old, before := oldContributionFixture(t, raw)
			reduceCalls := 0
			model := &contractModel{respond: func(r GenerateRequest) (GenerateResult, error) {
				switch r.Stage {
				case "map":
					return GenerateResult{Text: `{"entities":[],"concepts":[]}`}, nil
				case "summary":
					return GenerateResult{Text: "SUMMARY: 备份资料\n新版只记录备份恢复。"}, nil
				case "reduce":
					reduceCalls++
					prompt := r.Messages[len(r.Messages)-1].Content
					start, end := strings.Index(prompt, "<deleted_documents>"), strings.Index(prompt, "</deleted_documents>")
					if start < 0 || end <= start || !strings.Contains(prompt[start:end], "OLD_RETIRED_FACT") {
						t.Error("old raw facts missing from deletion context")
					}
					if mode == "large" && !strings.Contains(prompt[start:end], "OLD_TAIL_FACT") {
						t.Error("large old raw input was truncated")
					}
					return GenerateResult{Text: "SUMMARY: 已撤回\n本主题旧事实已撤回。"}, nil
				}
				return normalReply(r)
			}}
			reader := oldContributionReader(func(ctx context.Context, r ReadRequest) (ReadArtifact, error) {
				if r.SourceVersionID == old.ID && mode == "unsupported" {
					return ReadArtifact{SourceVersionID: r.SourceVersionID, SHA256: r.SHA256, Scope: r.Scope, Status: "unsupported", Coverage: json.RawMessage(`{"complete":false}`), Diagnostic: "old format unavailable"}, nil
				}
				return (MarkdownReader{}).Read(ctx, r)
			})
			result, e := Run(ctx, s, Input{JobID: j.ID, Generator: model, Reader: reader})
			if e != nil {
				t.Fatal(e)
			}
			after, e := s.GetPageBySlug(ctx, j.LibraryID, before.Slug)
			if e != nil {
				t.Fatal(e)
			}
			if mode == "raw" || mode == "large" {
				if result.State != "completed" || reduceCalls != 1 || len(after.Evidence.Sources) != 0 {
					t.Fatalf("raw recovery failed: result=%+v reduce=%d page=%+v", result, reduceCalls, after)
				}
				return
			}
			source, e := s.GetSource(ctx, j.LibraryID, j.SourceVersionIDs[0])
			if e != nil {
				t.Fatal(e)
			}
			if result.State != "failed" || reduceCalls != 0 || after.Content != before.Content || after.Version != before.Version || len(after.Evidence.Sources) != 1 || !strings.Contains(source.Diagnostic, old.ID) {
				t.Fatalf("uncovered old facts lost evidence or falsely completed: result=%+v reduce=%d diagnostic=%s before=%+v after=%+v", result, reduceCalls, source.Diagnostic, before, after)
			}
		})
	}
}

// TestOldRetractionContextPreservesRecoveryErrorClass 防止旧材料恢复包装把临时存储错误变成确定性来源失败。
func TestOldRetractionContextPreservesRecoveryErrorClass(t *testing.T) {
	for _, code := range []types.ErrorCode{types.ErrStorage, types.ErrCandidateInvalid} {
		t.Run(string(code), func(t *testing.T) {
			ctx := context.Background()
			s, j, sources := contractFixture(t, 1)
			old := sources[0]
			rec, err := s.ImportSource(ctx, types.ImportSourceInput{RequestID: "classification-v2", LibraryID: j.LibraryID, SourceID: old.SourceID, ExpectedVersion: old.Version, Filename: old.Filename, MIME: old.MIME, Bytes: []byte("# 新版资料\n本版已经换版。")})
			if err != nil {
				t.Fatal(err)
			}
			current, err := s.GetSource(ctx, j.LibraryID, rec.ResourceID)
			if err != nil {
				t.Fatal(err)
			}
			cause := &types.Error{Code: code, Operation: "reader.recovery", Detail: "controlled old-material recovery failure"}
			reader := oldContributionReader(func(context.Context, ReadRequest) (ReadArtifact, error) { return ReadArtifact{}, cause })
			svc := &liteService{store: s, job: j, library: j.LibrarySnapshot, input: Input{Reader: reader}}
			oldPage := &types.WikiPage{Slug: "concept/old-topic", PageType: "concept", Evidence: types.PageEvidence{Sources: []types.SourceRef{documentRef(old)}}}
			updates := map[string][]SlugUpdate{"summary/" + old.SourceID: {{Slug: "summary/" + old.SourceID, Type: "summary", KnowledgeID: old.SourceID, SourceVersionID: current.ID}}}
			_, failures, err := svc.reconcileArchivedSourceVersions(ctx, updates, []*types.WikiPage{oldPage}, map[string]*types.SourceVersion{current.ID: current})
			if err != nil {
				t.Fatal(err)
			}
			failure := failures[current.ID]
			var classified *types.Error
			if !errors.As(failure, &classified) || classified.Code != code {
				t.Fatalf("recovery error class changed: want=%s got=%v", code, failure)
			}
			if !errors.Is(failure, cause) || !strings.Contains(failure.Error(), old.ID) || !strings.Contains(failure.Error(), current.ID) {
				t.Fatalf("recovery cause or source diagnostic lost: %v", failure)
			}
		})
	}
}
