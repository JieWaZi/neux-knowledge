package store

import (
	"context"
	"errors"
	"testing"

	types "github.com/JieWaZi/neux-knowledge/internal/domain"
)

// TestLitePageCheckpointGuardsResume 验证已提交阶段恢复仍校验任务闸门、人工覆盖和同版本维护的读取时间。
func TestLitePageCheckpointGuardsResume(t *testing.T) {
	for _, mode := range []string{"committed", "unchanged-existing", "manual", "maintenance", "structure", "permission", "cancelled", "attempt"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			s, library, _, job := sourceWorkFixture(t)
			const key = "index-checkpoint"
			page := types.WikiPage{ID: "checkpoint-index", KnowledgeBaseID: library.ID, Slug: "index", Title: "Wiki Index", PageType: "index", Status: "published", Content: "# Wiki Index\n当前知识。"}
			expected := 0
			if mode == "unchanged-existing" {
				if _, err := s.WriteAgentPage(ctx, types.WritePageInput{RequestID: "seed-index", LibraryID: library.ID, StructureVersion: library.StructureVersion, Page: page}); err != nil {
					t.Fatal(err)
				}
				stored, err := s.GetPage(ctx, library.ID, page.ID)
				if err != nil {
					t.Fatal(err)
				}
				page, expected = *stored, stored.Version
			}
			if _, err := s.PublishLite(ctx, "publish-index", job.ID, job.Attempt, &types.Publication{Maintenance: true, Pages: []types.PageMutation{{Page: page, ExpectedVersion: expected}}}, key); err != nil {
				t.Fatal(err)
			}
			snapshot, err := s.GetPage(ctx, library.ID, page.ID)
			if err != nil {
				t.Fatal(err)
			}
			var want types.ErrorCode
			switch mode {
			case "manual":
				manual := *snapshot
				manual.Content += "人工新事实。"
				if _, err = s.WritePage(ctx, types.WritePageInput{RequestID: "manual-index", LibraryID: library.ID, StructureVersion: library.StructureVersion, ExpectedVersion: manual.Version, Page: manual}); err != nil {
					t.Fatal(err)
				}
				snapshot, err = s.GetPage(ctx, library.ID, page.ID)
				if err != nil {
					t.Fatal(err)
				}
				want = types.ErrVersionConflict
			case "maintenance":
				maintained := *snapshot
				maintained.Content += "链接维护。"
				if err = s.UpdateWikiMaintenance(ctx, job.ID, job.Attempt, &maintained, maintained.Version); err != nil {
					t.Fatal(err)
				}
				want = types.ErrVersionConflict
			case "structure":
				if err = s.db.Model(&types.Library{}).Where("id = ?", library.ID).UpdateColumn("structure_version", library.StructureVersion+1).Error; err != nil {
					t.Fatal(err)
				}
				want = types.ErrVersionConflict
			case "permission":
				if err = s.db.Model(&types.Library{}).Where("id = ?", library.ID).UpdateColumn("policy_version", library.PolicyVersion+1).Error; err != nil {
					t.Fatal(err)
				}
				want = types.ErrScopeViolation
			case "cancelled":
				if err = s.db.Model(&types.Job{}).Where("id = ?", job.ID).UpdateColumn("cancel_requested", true).Error; err != nil {
					t.Fatal(err)
				}
				want = types.ErrCancelled
			case "attempt":
				if err = s.db.Model(&types.Job{}).Where("id = ?", job.ID).UpdateColumn("attempt", job.Attempt+1).Error; err != nil {
					t.Fatal(err)
				}
				want = types.ErrStaleAttempt
			}
			applied, err := s.HasCurrentLitePageCheckpoint(ctx, job.ID, job.Attempt, key, snapshot)
			if want != "" {
				var failure *types.Error
				if applied || !errors.As(err, &failure) || failure.Code != want {
					t.Fatalf("checkpoint bypassed %s: applied=%v err=%v", mode, applied, err)
				}
			} else if err != nil || !applied {
				t.Fatalf("committed stage was not resumable: applied=%v err=%v", applied, err)
			}
			if mode == "maintenance" {
				fresh, e := s.GetPage(ctx, library.ID, page.ID)
				if e != nil {
					t.Fatal(e)
				}
				applied, err = s.HasCurrentLitePageCheckpoint(ctx, job.ID, job.Attempt, key, fresh)
				if err != nil || !applied || fresh.Version != snapshot.Version || fresh.Content != snapshot.Content+"链接维护。" {
					t.Fatalf("fresh maintenance snapshot was not retained: applied=%v page=%+v err=%v", applied, fresh, err)
				}
			}
		})
	}
}
