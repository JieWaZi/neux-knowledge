package store

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"gorm.io/gorm"

	types "github.com/JieWaZi/neux-knowledge/internal/domain"
)

func TestWikiMaintenanceCascadeRollsBackEveryRow(t *testing.T) {
	for _, operation := range []string{"rename", "delete"} {
		t.Run(operation, func(t *testing.T) {
			ctx := context.Background()
			s, l, _, _ := sourceWorkFixture(t)
			create := func(request, slug, body string) *types.WikiPage {
				rec, err := s.WriteAgentPage(ctx, types.WritePageInput{RequestID: request, LibraryID: l.ID, StructureVersion: l.StructureVersion, Page: types.WikiPage{Slug: slug, Title: slug, PageType: "concept", Content: body}})
				if err != nil {
					t.Fatal(err)
				}
				p, err := s.GetPage(ctx, l.ID, rec.ResourceID)
				if err != nil {
					t.Fatal(err)
				}
				return p
			}
			target := create("target", "concept/target", "target")
			linked := create("linked", "concept/linked", "[[concept/target|显示]]")
			failID := linked.ID
			if operation == "delete" {
				failID = target.ID
			}
			// Fail after at least one cascading row write to prove no partial bodies, history, folder versions, or receipts commit.
			trigger := fmt.Sprintf("CREATE TRIGGER maintenance_failure BEFORE UPDATE ON wiki_pages WHEN OLD.id = '%s' BEGIN SELECT RAISE(ABORT, 'injected maintenance failure'); END", failID)
			if err := s.db.Exec(trigger).Error; err != nil {
				t.Fatal(err)
			}
			var err error
			if operation == "rename" {
				_, err = s.RenamePage(ctx, types.RenamePageInput{RequestID: "failed", LibraryID: l.ID, PageID: target.ID, ExpectedVersion: target.Version, StructureVersion: l.StructureVersion, NewSlug: "concept/new"})
			} else {
				_, err = s.DeletePage(ctx, types.DeletePageInput{RequestID: "failed", LibraryID: l.ID, PageID: target.ID, ExpectedVersion: target.Version, StructureVersion: l.StructureVersion})
			}
			if err == nil {
				t.Fatal("injected failure was ignored")
			}
			for _, want := range []*types.WikiPage{target, linked} {
				actual, err := s.GetPage(ctx, l.ID, want.ID)
				if err != nil {
					t.Fatal(err)
				}
				if actual.Version != want.Version || actual.Slug != want.Slug || actual.Status != want.Status || actual.Content != want.Content {
					t.Fatalf("partial write: %+v", actual)
				}
				_, n, err := s.ListRevisions(ctx, l.ID, want.ID, 0, 10)
				if err != nil || n != 0 {
					t.Fatalf("partial revision count=%d %v", n, err)
				}
			}
			current, err := s.GetLibrary(ctx, l.ID)
			if err != nil || current.StructureVersion != l.StructureVersion {
				t.Fatalf("partial structure %+v %v", current, err)
			}
			if _, err = s.GetReceipt(ctx, "failed"); err == nil {
				t.Fatal("failed mutation committed receipt")
			}
		})
	}
}

func TestArchiveReplayKeepsLegacyInputDigest(t *testing.T) {
	ctx := context.Background()
	s, l, _, _ := sourceWorkFixture(t)
	// 旧版持久摘要只包含这六个字段；升级不能改变同一普通归档请求的身份。
	legacy := struct {
		RequestID             string // 旧请求身份。
		LibraryID             string // 原知识库。
		PageID                string // 原页面身份。
		ExpectedVersion       int    // 原页面版本。
		StructureVersion      int64  // 原目录版本。
		ExpectedPolicyVersion int64  // 原策略版本。
	}{"legacy-archive", l.ID, "old-page", 1, l.StructureVersion, l.PolicyVersion}
	prior, err := s.mutate(ctx, legacy.RequestID, "page.delete", l.ID, legacy, func(*gorm.DB) (*types.Receipt, error) {
		return &types.Receipt{ResourceID: legacy.PageID, Version: 2, State: "saved"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.DeletePage(ctx, types.DeletePageInput{RequestID: legacy.RequestID, LibraryID: legacy.LibraryID, PageID: legacy.PageID, ExpectedVersion: legacy.ExpectedVersion, StructureVersion: legacy.StructureVersion, ExpectedPolicyVersion: legacy.ExpectedPolicyVersion})
	if err != nil || got.CreatedAt != prior.CreatedAt {
		t.Fatalf("upgrade invalidated original archive receipt: %+v %v", got, err)
	}
}

func TestAgentEvidenceUsesPersistedChunkLocator(t *testing.T) {
	ctx := context.Background()
	s, l, source, job := sourceWorkFixture(t)
	locator := types.SourceLocator{Kind: "line", StartLine: 1, EndLine: 2}
	chunk := types.EmbeddingChunkIndex{ID: "agent-evidence-chunk", ChunkID: "first", ChunkType: "parent", Content: "真实原文：可靠摘录", Locator: locator, State: "parsed"}
	if err := s.SaveDocumentChunks(ctx, job.ID, source.ID, []types.EmbeddingChunkIndex{chunk}); err != nil {
		t.Fatal(err)
	}
	ref := types.SourceRef{SourceID: source.SourceID, SourceVersionID: source.ID, SHA256: source.SHA256, Excerpt: "可靠摘录", ExcerptHash: hash([]byte("可靠摘录")), Locator: locator, ClaimKind: "source_statement"}
	in := types.WritePageInput{RequestID: "evidence", LibraryID: l.ID, StructureVersion: l.StructureVersion, Page: types.WikiPage{Slug: "concept/evidence", Title: "证据", PageType: "concept", Content: "核查内容", Evidence: types.PageEvidence{Sources: []types.SourceRef{ref}}}}
	if _, err := s.WriteAgentPage(ctx, in); err != nil {
		t.Fatal(err)
	}
	in.RequestID = "forged-locator"
	in.Page.Slug = "concept/forged"
	in.Page.Evidence.Sources[0].Locator.StartLine = 99
	if _, err := s.WriteAgentPage(ctx, in); err == nil {
		t.Fatal("accepted invented source location")
	}
}

func TestWikiMaintenancePreservesHistoricalIssueRows(t *testing.T) {
	ctx := context.Background()
	s, l, _, _ := sourceWorkFixture(t)
	r, err := s.WriteAgentPage(ctx, types.WritePageInput{RequestID: "page", LibraryID: l.ID, StructureVersion: l.StructureVersion, Page: types.WikiPage{Slug: "concept/old", Title: "历史页面", PageType: "concept", Content: "正文"}})
	if err != nil {
		t.Fatal(err)
	}
	// Inject historical rows from before the upgrade to verify maintenance compatibility without adding an issue-management entry point.
	historical := types.WikiPageIssue{ID: "historical", KnowledgeBaseID: l.ID, Slug: "concept/old", IssueType: "other", Description: "已有记录", Status: "ignored", ReportedBy: "historical-agent"}
	if err = s.db.Create(&historical).Error; err != nil {
		t.Fatal(err)
	}
	_, err = s.RenamePage(ctx, types.RenamePageInput{RequestID: "rename", LibraryID: l.ID, PageID: r.ResourceID, ExpectedVersion: 1, StructureVersion: l.StructureVersion, NewSlug: "concept/new"})
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.GetPage(ctx, l.ID, r.ResourceID)
	if err != nil {
		t.Fatal(err)
	}
	l, err = s.GetLibrary(ctx, l.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.DeletePage(ctx, types.DeletePageInput{RequestID: "archive", LibraryID: l.ID, PageID: p.ID, ExpectedVersion: p.Version, StructureVersion: l.StructureVersion})
	if err != nil {
		t.Fatal(err)
	}
	var actual types.WikiPageIssue
	if err = s.db.Where("id = ?", historical.ID).Take(&actual).Error; err != nil {
		t.Fatal(err)
	}
	if actual.Slug != "concept/new" || actual.Status != historical.Status || actual.Description != historical.Description || actual.ReportedBy != historical.ReportedBy {
		t.Fatalf("historical issue lost or rewritten: %+v", actual)
	}
}

func TestWikiMaintenanceRecordsOneCommittedActivityPerRequest(t *testing.T) {
	for _, operation := range []string{"page.agent-write", "page.replace-text", "page.rename", "page.delete"} {
		t.Run(operation, func(t *testing.T) {
			ctx := types.WithActivityActor(context.Background(), types.ActivityActor{UserID: "maintainer", Role: "editor"})
			s, l, _, _ := sourceWorkFixture(t)
			r, err := s.WriteAgentPage(ctx, types.WritePageInput{RequestID: "target", LibraryID: l.ID, StructureVersion: l.StructureVersion, Page: types.WikiPage{Slug: "concept/old", Title: "Old", PageType: "concept", Content: "before"}})
			if err != nil {
				t.Fatal(err)
			}
			p, err := s.GetPage(ctx, l.ID, r.ResourceID)
			if err != nil {
				t.Fatal(err)
			}
			_, err = s.WriteAgentPage(ctx, types.WritePageInput{RequestID: "incoming", LibraryID: l.ID, StructureVersion: l.StructureVersion, Page: types.WikiPage{Slug: "incoming", Title: "Incoming", PageType: "concept", Content: "[[concept/old]]"}})
			if err != nil {
				t.Fatal(err)
			}
			invoke := func() (*types.Receipt, error) {
				switch operation {
				case "page.agent-write":
					updated := *p
					updated.Content = "after"
					return s.WriteAgentPage(ctx, types.WritePageInput{RequestID: "maintenance", LibraryID: l.ID, StructureVersion: l.StructureVersion, ExpectedVersion: p.Version, Page: updated})
				case "page.replace-text":
					return s.ReplacePageText(ctx, types.ReplacePageTextInput{RequestID: "maintenance", LibraryID: l.ID, StructureVersion: l.StructureVersion, ExpectedVersion: p.Version, PageID: p.ID, OldText: "before", NewText: "after"})
				case "page.rename":
					return s.RenamePage(ctx, types.RenamePageInput{RequestID: "maintenance", LibraryID: l.ID, StructureVersion: l.StructureVersion, ExpectedVersion: p.Version, PageID: p.ID, NewSlug: "concept/new"})
				default:
					return s.DeletePage(ctx, types.DeletePageInput{RequestID: "maintenance", LibraryID: l.ID, StructureVersion: l.StructureVersion, ExpectedVersion: p.Version, PageID: p.ID})
				}
			}
			for i := 0; i < 2; i++ {
				if _, err = invoke(); err != nil {
					t.Fatal(err)
				}
			}
			var activities []types.AuditLog
			if err = s.db.Where("request_id = ?", "maintenance").Find(&activities).Error; err != nil {
				t.Fatal(err)
			}
			if len(activities) != 1 {
				t.Fatalf("committed/replayed activity count=%d; want 1", len(activities))
			}
			activity := activities[0]
			if activity.Operation != operation || activity.Action != types.AuditActionWikiContentChanged || activity.ScopeID != l.ID || activity.TargetType != "wiki" || activity.TargetID != l.ID || activity.Outcome != types.AuditOutcomeSuccess || activity.ActorUserID != "maintainer" || activity.ActorRole != "editor" {
				t.Fatalf("inaccurate maintenance activity: %+v", activity)
			}
			wantCount := float64(1)
			if operation == "page.rename" || operation == "page.delete" {
				wantCount = 2
			}
			var details map[string]any
			if err = json.Unmarshal(activity.Details, &details); err != nil || details["count"] != wantCount {
				t.Fatalf("affected page count lost: %s error=%v", activity.Details, err)
			}
			if wantCount == 1 && details["title"] != "Old" {
				t.Fatalf("single-page title missing: %s", activity.Details)
			}
			if wantCount == 2 {
				titles, ok := details["titles"].([]any)
				if !ok || len(titles) != 2 || !((titles[0] == "Old" && titles[1] == "Incoming") || (titles[0] == "Incoming" && titles[1] == "Old")) {
					t.Fatalf("changed and soft-deleted page titles missing: %s", activity.Details)
				}
			}
		})
	}
}

// TestWikiActivityTitlePreviewIsBounded 验证多页回执只保存有限名称并保留真实变更数量。
func TestWikiActivityTitlePreviewIsBounded(t *testing.T) {
	s, l, _, _ := sourceWorkFixture(t)
	ctx := context.Background()
	ids := []string{}
	for _, slug := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		r, err := s.WriteAgentPage(ctx, types.WritePageInput{RequestID: "preview-" + slug, LibraryID: l.ID, StructureVersion: l.StructureVersion, Page: types.WikiPage{Slug: slug, Title: "标题" + slug, PageType: "concept", Content: "不应进入活动的正文"}})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, r.ResourceID)
	}
	pageQueries := 0
	if err := s.db.Callback().Query().After("gorm:query").Register("activity-title-query-budget", func(db *gorm.DB) {
		if db.Statement.Table != "wiki_pages" {
			return
		}
		pageQueries++
		if db.RowsAffected > 5 || len(db.Statement.Selects) != 2 || db.Statement.Selects[0] != "id" || db.Statement.Selects[1] != "title" {
			t.Errorf("标题预览读取超出有界元数据: rows=%d selects=%v", db.RowsAffected, db.Statement.Selects)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.db.Callback().Query().Remove("activity-title-query-budget") })
	if err := s.transaction(ctx, func(tx *gorm.DB) error {
		return appendMutationActivity(ctx, tx, &types.Receipt{RequestID: "preview-many", Operation: "page.agent-write", LibraryID: l.ID, PageIDs: ids}, nil)
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListLibraryActivity(ctx, l.ID, &types.ActivityQuery{Limit: 1})
	if err != nil || len(rows) != 1 {
		t.Fatalf("活动读取: %v %v", rows, err)
	}
	var details map[string]any
	if err = json.Unmarshal(rows[0].Details, &details); err != nil {
		t.Fatal(err)
	}
	if pageQueries != 1 {
		t.Fatalf("多页活动不应逐页查询: queries=%d", pageQueries)
	}
	titles, ok := details["titles"].([]any)
	if !ok || len(titles) != 5 || details["count"] != float64(8) || titles[0] != "标题a" || titles[4] != "标题e" {
		t.Fatalf("预览无界或变更数量不实: %s", rows[0].Details)
	}
}
