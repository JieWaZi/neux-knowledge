package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	types "github.com/JieWaZi/neux-knowledge/internal/domain"
)

// TestLiteActivityTracksWritesAndTerminalOutcomes 验证实际Lite发布写入标题及成功、部分失败和失败终态，重放不重复。
func TestLiteActivityTracksWritesAndTerminalOutcomes(t *testing.T) {
	for _, scenario := range []string{"success", "partial", "failed"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			s, library, _, job := sourceWorkFixture(t, "second")
			for n := 0; n < 2; n++ {
				if _, err := s.PublishLite(ctx, "activity-page", job.ID, job.Attempt, &types.Publication{SourceVersionIDs: job.SourceVersionIDs, Pages: []types.PageMutation{{Page: types.WikiPage{Slug: "result", Title: "真实生成页面", PageType: "concept", Content: "不应进入活动的正文"}}}}); err != nil {
					t.Fatal(err)
				}
			}
			writes, err := s.ListLibraryActivity(ctx, library.ID, &types.ActivityQuery{Action: types.AuditActionWikiContentChanged})
			if err != nil || len(writes) != 1 || writes[0].Operation != "lite.publish" {
				t.Fatalf("Lite实际写入活动缺失或重放重复: %v %v", writes, err)
			}
			var details map[string]any
			if err = json.Unmarshal(writes[0].Details, &details); err != nil || details["title"] != "真实生成页面" || details["count"] != float64(1) {
				t.Fatalf("Lite写入标题或数量丢失: %s %v", writes[0].Details, err)
			}
			prior, err := s.ListLibraryActivity(ctx, library.ID, &types.ActivityQuery{Action: types.AuditActionKnowledgeReparseStarted})
			if err != nil || len(prior) != 1 || prior[0].Outcome != types.AuditOutcomeAccepted {
				t.Fatalf("中间写入不应冒充任务完成: %v %v", prior, err)
			}
			conclusions := []types.SourceConclusion{}
			for index, id := range job.SourceVersionIDs {
				conclusion := types.SourceConclusion{SourceVersionID: id, State: "no-change", Complete: true, Coverage: json.RawMessage(`{"confirmed":true}`)}
				if scenario == "failed" || (scenario == "partial" && index == 1) {
					conclusion.State = "failed"
					conclusion.Diagnostic = "模型服务连接失败"
				}
				conclusions = append(conclusions, conclusion)
			}
			for n := 0; n < 2; n++ {
				if _, err := s.PublishLite(ctx, "activity-final", job.ID, job.Attempt, &types.Publication{Sources: conclusions, Final: true}); err != nil {
					t.Fatal(err)
				}
			}
			terminal, err := s.ListLibraryActivity(ctx, library.ID, &types.ActivityQuery{Action: types.AuditActionKnowledgeReparseStarted})
			if err != nil || len(terminal) != 2 || string(terminal[0].Outcome) != scenario {
				t.Fatalf("Lite终态活动缺失或重放重复: %v %v", terminal, err)
			}
			if err = json.Unmarshal(terminal[0].Details, &details); err != nil || details["attempt"] != float64(1) || details["total"] != float64(2) {
				t.Fatalf("终态范围或尝试丢失: %s %v", terminal[0].Details, err)
			}
			final, err := s.GetJob(ctx, job.ID)
			if err != nil {
				t.Fatal(err)
			}
			if scenario != "success" && !strings.Contains(final.Diagnostic, "模型服务连接失败") {
				t.Fatalf("真实失败原因未随任务结算: %+v", final)
			}
			if scenario == "success" && final.Diagnostic != "" {
				t.Fatalf("成功任务残留失败诊断: %s", final.Diagnostic)
			}
		})
	}
}

// TestLiteActivityCancellationSettlesOnce 验证最后一个真实阶段结束或恢复时，同事务追加唯一取消终态。
func TestLiteActivityCancellationSettlesOnce(t *testing.T) {
	for _, recoverStage := range []bool{false, true} {
		t.Run(map[bool]string{false: "finish", true: "recover"}[recoverStage], func(t *testing.T) {
			ctx := context.Background()
			s, library, _, job := sourceWorkFixture(t)
			stage, err := s.BeginModelStage(ctx, job.ID, job.Attempt, "reading", "reading", job.SourceVersionIDs, json.RawMessage(`{}`))
			if err != nil {
				t.Fatal(err)
			}
			current, _ := s.GetJob(ctx, job.ID)
			if _, err = s.CancelJob(ctx, types.JobActionInput{RequestID: "activity-cancel", JobID: job.ID, ExpectedVersion: current.Version}); err != nil {
				t.Fatal(err)
			}
			waiting, _ := s.ListLibraryActivity(ctx, library.ID, &types.ActivityQuery{Action: types.AuditActionKnowledgeParseCanceled})
			if len(waiting) != 1 || waiting[0].Outcome != types.AuditOutcomeAccepted {
				t.Fatalf("取消屏障未真实记录: %v", waiting)
			}
			for n := 0; n < 2; n++ {
				if recoverStage {
					err = s.RecoverLiteStages(ctx, job.ID, job.Attempt)
				} else {
					err = s.FinishModelStage(ctx, stage.ID, "", "stop", "", nil, nil)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			final, _ := s.GetJob(ctx, job.ID)
			if final.State != "cancelled" {
				t.Fatalf("取消未结算: %+v", final)
			}
			rows, err := s.ListLibraryActivity(ctx, library.ID, &types.ActivityQuery{Action: types.AuditActionKnowledgeParseCanceled})
			if err != nil || len(rows) != 2 || rows[0].Outcome != types.AuditOutcomeCanceled {
				t.Fatalf("取消终态缺失或重复: %v %v", rows, err)
			}
		})
	}
}

// TestLiteFailureDiagnosticSummaryIsBounded 验证已有终态来源读取只冻结有限诊断摘要。
func TestLiteFailureDiagnosticSummaryIsBounded(t *testing.T) {
	ctx := context.Background()
	s, _, _, job := sourceWorkFixture(t, "b", "c", "d")
	conclusions := []types.SourceConclusion{}
	for _, id := range job.SourceVersionIDs {
		conclusions = append(conclusions, types.SourceConclusion{SourceVersionID: id, State: "failed", Diagnostic: strings.Repeat("错", 2000)})
	}
	if _, err := s.PublishLite(ctx, "bounded-final", job.ID, job.Attempt, &types.Publication{Sources: conclusions, Final: true}); err != nil {
		t.Fatal(err)
	}
	final, err := s.GetJob(ctx, job.ID)
	if err != nil || len([]rune(final.Diagnostic)) == 0 || len([]rune(final.Diagnostic)) > 1502 {
		t.Fatalf("失败摘要缺失或无界: runes=%d err=%v", len([]rune(final.Diagnostic)), err)
	}
}
