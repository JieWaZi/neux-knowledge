package pipeline

import (
	"context"
	"testing"

	types "github.com/JieWaZi/neux-knowledge/internal/domain"
)

// TestLiteRebuildReusesArchivedIdentity 防止目录不展示归档页时重新创建相同地址并触发唯一约束。
func TestLiteRebuildReusesArchivedIdentity(t *testing.T) {
	for _, protected := range []bool{false, true} {
		t.Run(map[bool]string{false: "generated", true: "protected"}[protected], func(t *testing.T) {
			ctx := context.Background()
			s, j, sources := contractFixture(t, 1)
			page := types.WikiPage{ID: "archived-page", KnowledgeBaseID: j.LibraryID, Slug: "concept/session", Title: "会话管理", PageType: "concept", Status: "archived", Content: "旧版归档内容", Evidence: types.PageEvidence{Sources: []types.SourceRef{documentRef(sources[0])}}}
			if _, err := s.WriteAgentPage(ctx, types.WritePageInput{RequestID: "seed-archive", LibraryID: j.LibraryID, StructureVersion: j.StructureVersion, Page: page}); err != nil {
				t.Fatal(err)
			}
			before, err := s.GetPageBySlug(ctx, j.LibraryID, page.Slug)
			if err != nil {
				t.Fatal(err)
			}
			if protected {
				lib, err := s.GetLibrary(ctx, j.LibraryID)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = s.WritePage(ctx, types.WritePageInput{RequestID: "protect", LibraryID: lib.ID, StructureVersion: lib.StructureVersion, ExpectedVersion: before.Version, Page: *before}); err != nil {
					t.Fatal(err)
				}
				before, err = s.GetPageBySlug(ctx, j.LibraryID, page.Slug)
				if err != nil {
					t.Fatal(err)
				}
			}
			if err = s.EnsureOrganizeSourceWork(ctx, j.ID); err != nil {
				t.Fatal(err)
			}
			svc := &liteService{store: s, job: j, library: j.LibrarySnapshot, input: Input{Generator: &contractModel{respond: normalReply}, Reader: MarkdownReader{}}}
			work, err := s.ListOrganizeSourceWork(ctx, j.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err = svc.processBatch(ctx, work); err != nil {
				t.Fatal(err)
			}
			after, err := s.GetPageBySlug(ctx, j.LibraryID, page.Slug)
			if err != nil {
				t.Fatal(err)
			}
			work, err = s.ListOrganizeSourceWork(ctx, j.ID)
			if err != nil {
				t.Fatal(err)
			}
			if protected {
				if after.ID != before.ID || after.Status != "archived" || after.Content != before.Content || after.Version != before.Version {
					t.Fatalf("protected archive changed: %+v", after)
				}
				source, err := s.GetSource(ctx, j.LibraryID, sources[0].ID)
				if err != nil {
					t.Fatal(err)
				}
				if source.State != "failed" {
					t.Fatalf("protected archive should fail permanently, source=%+v work=%+v", source, work)
				}
			} else {
				if after.ID != before.ID || after.Status != "published" || after.Content == before.Content || after.Version != before.Version+1 {
					t.Fatalf("archive identity was not updated: before=%+v after=%+v work=%+v", before, after, work)
				}
				if work[0].State != "done" {
					t.Fatalf("archived page requeued: %+v", work[0])
				}
			}
		})
	}
}
