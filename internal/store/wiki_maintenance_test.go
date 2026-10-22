package store

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

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
		})
	}
}
