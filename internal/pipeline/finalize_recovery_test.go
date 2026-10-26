package pipeline

import (
	"context"
	"errors"
	"testing"

	types "github.com/JieWaZi/neux-knowledge/internal/domain"
)

// TestLiteFinalizeReusesArchivedIndexIdentity 防止归档索引被当作新页创建，并保留人工保护。
func TestLiteFinalizeReusesArchivedIndexIdentity(t *testing.T) {
	for _, protected := range []bool{false, true} {
		t.Run(map[bool]string{false: "generated", true: "protected"}[protected], func(t *testing.T) {
			ctx := context.Background()
			s, j, _ := contractFixture(t, 1)
			page := types.WikiPage{ID: "archived-index", KnowledgeBaseID: j.LibraryID, Slug: "index", Title: "Wiki Index", PageType: "index", Status: "archived", Content: "# 原来的索引"}
			if _, err := s.WriteAgentPage(ctx, types.WritePageInput{RequestID: "seed-archived-index", LibraryID: j.LibraryID, StructureVersion: j.StructureVersion, Page: page}); err != nil {
				t.Fatal(err)
			}
			before, err := s.GetPageBySlug(ctx, j.LibraryID, "index")
			if err != nil {
				t.Fatal(err)
			}
			if protected {
				if _, err = s.WritePage(ctx, types.WritePageInput{RequestID: "protect-index", LibraryID: j.LibraryID, StructureVersion: j.StructureVersion, ExpectedVersion: before.Version, Page: *before}); err != nil {
					t.Fatal(err)
				}
				before, err = s.GetPageBySlug(ctx, j.LibraryID, "index")
				if err != nil {
					t.Fatal(err)
				}
			}
			result, runErr := Run(ctx, s, Input{JobID: j.ID, Generator: &contractModel{respond: normalReply}})
			after, err := s.GetPageBySlug(ctx, j.LibraryID, "index")
			if err != nil {
				t.Fatal(err)
			}
			if protected {
				var refusal *types.Error
				if !errors.As(runErr, &refusal) || refusal.Code != types.ErrManualProtected {
					t.Fatalf("expected manual protection, got %v", runErr)
				}
				if after.ID != before.ID || after.Status != before.Status || after.Content != before.Content || after.Version != before.Version {
					t.Fatalf("protected archived index changed: before=%+v after=%+v", before, after)
				}
				return
			}
			if runErr != nil || result == nil || result.State != "completed" {
				t.Fatalf("normal source could not finalize with archived index: result=%+v err=%v", result, runErr)
			}
			if after.ID != before.ID || after.Status != "published" || after.Version != before.Version+1 {
				t.Fatalf("archived index identity was not reused: before=%+v after=%+v", before, after)
			}
		})
	}
}

// TestLiteFinalizeResumesAfterIndexPublication 防止索引已提交后的恢复撞幂等回执，并继续拒绝已被人工替换的版本。
func TestLiteFinalizeResumesAfterIndexPublication(t *testing.T) {
	for _, superseded := range []bool{false, true} {
		t.Run(map[bool]string{false: "unchanged", true: "superseded"}[superseded], func(t *testing.T) {
			base := context.Background()
			ctx, cancel := context.WithCancel(base)
			defer cancel()
			s, j, sources := contractFixture(t, 1)
			indexCalls := 0
			model := &contractModel{respond: func(r GenerateRequest) (GenerateResult, error) {
				if r.Stage == "index" {
					indexCalls++
					if indexCalls > 1 {
						return GenerateResult{Text: "# Wiki Index\n重复应用同一份资料的变化。", FinishReason: "stop"}, nil
					}
				}
				return normalReply(r)
			}}
			_, firstErr := Run(ctx, s, Input{JobID: j.ID, Generator: model, OnPublished: func(c context.Context, _ *types.Receipt) {
				if _, err := s.GetPageBySlug(c, j.LibraryID, "index"); err == nil {
					cancel()
				}
			}})
			if !errors.Is(firstErr, context.Canceled) {
				t.Fatalf("expected controlled interruption after index commit, got %v", firstErr)
			}
			before, err := s.GetPageBySlug(base, j.LibraryID, "index")
			if err != nil {
				t.Fatal(err)
			}
			source, err := s.GetSource(base, j.LibraryID, sources[0].ID)
			if err != nil || source.State != "processed" {
				t.Fatalf("source was not committed before interruption: source=%+v err=%v", source, err)
			}
			if superseded {
				lib, e := s.GetLibrary(base, j.LibraryID)
				if e != nil {
					t.Fatal(e)
				}
				before.Content = "# 人工更新的索引\n必须保留。"
				if _, e = s.WritePage(base, types.WritePageInput{RequestID: "manual-index-after-interruption", LibraryID: j.LibraryID, StructureVersion: lib.StructureVersion, ExpectedVersion: before.Version, Page: *before}); e != nil {
					t.Fatal(e)
				}
				before, err = s.GetPageBySlug(base, j.LibraryID, "index")
				if err != nil {
					t.Fatal(err)
				}
			}
			result, runErr := Run(base, s, Input{JobID: j.ID, Generator: model})
			if indexCalls != 1 {
				t.Errorf("committed index changes generated %d times", indexCalls)
			}
			after, err := s.GetPageBySlug(base, j.LibraryID, "index")
			if err != nil {
				t.Fatal(err)
			}
			if superseded {
				var conflict *types.Error
				if !errors.As(runErr, &conflict) || conflict.Code != types.ErrVersionConflict {
					t.Fatalf("expected superseded-version conflict, got %v", runErr)
				}
			} else if runErr != nil || result == nil || result.State != "completed" {
				t.Fatalf("resume after index commit did not finalize: result=%+v err=%v", result, runErr)
			}
			if after.ID != before.ID || after.Version != before.Version || after.Content != before.Content {
				t.Fatalf("recovery changed committed index content or version: before=%+v after=%+v", before, after)
			}
		})
	}
}
