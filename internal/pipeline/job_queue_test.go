package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	types "github.com/JieWaZi/neux-knowledge/internal/domain"
)

func TestLiteQueueWaitsForEarlierJobAndMergesItsWiki(t *testing.T) {
	s, first, _ := contractFixture(t, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	raw, err := s.ImportSource(ctx, types.ImportSourceInput{RequestID: "second-source", LibraryID: first.LibraryID, Filename: "second.md", Bytes: []byte("# 会话管理\n第二份来源增加适用条件。")})
	if err != nil {
		t.Fatal(err)
	}
	lib, _ := s.GetLibrary(ctx, first.LibraryID)
	second, err := s.AcceptJob(ctx, types.AcceptJobInput{RequestID: "second", LibraryID: lib.ID, ExpectedVersion: lib.Version, StructureVersion: lib.StructureVersion, SourceVersionIDs: []string{raw.ResourceID}, Permission: first.Permission, RulesVersion: first.RulesVersion, ConfigDigest: first.ConfigDigest})
	if err != nil {
		t.Fatal("different source must queue", err)
	}
	called := make(chan struct{}, 1)
	secondResult := make(chan error, 1)
	model2 := &contractModel{respond: func(r GenerateRequest) (GenerateResult, error) {
		select {
		case called <- struct{}{}:
		default:
		}
		if r.Stage == "reduce" {
			var text strings.Builder
			for _, message := range r.Messages {
				text.WriteString(message.Content)
			}
			if !strings.Contains(text.String(), "首轮独有事实") {
				t.Error("queued job did not read first job's wiki")
			}
			return GenerateResult{Text: "SUMMARY: 累计会话条件\n# 会话管理\n在线验证与离线验证具有各自来源条件。首轮独有事实。第二轮新增事实。", FinishReason: "stop"}, nil
		}
		return normalReply(r)
	}}
	go func() { _, e := Run(ctx, s, Input{JobID: second.JobID, Generator: model2}); secondResult <- e }()
	select {
	case <-called:
		t.Fatal("later job overtook queue head")
	case <-time.After(100 * time.Millisecond):
	}
	model1 := &contractModel{respond: func(r GenerateRequest) (GenerateResult, error) {
		result, e := normalReply(r)
		if r.Stage == "reduce" {
			result.Text += "\n首轮独有事实。"
		}
		return result, e
	}}
	if _, err = Run(ctx, s, Input{JobID: first.ID, Generator: model1}); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-secondResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("queued job did not continue", ctx.Err())
	}
	page, err := s.GetPageBySlug(ctx, lib.ID, "concept/session")
	if err != nil || !strings.Contains(page.Content, "首轮独有事实") || !strings.Contains(page.Content, "第二轮新增事实") {
		t.Fatal("queue lost wiki updates", page, err)
	}
}

func TestLiteQueuedWaitCancellationPreservesTask(t *testing.T) {
	s, first, _ := contractFixture(t, 1)
	ctx := context.Background()
	raw, err := s.ImportSource(ctx, types.ImportSourceInput{RequestID: "later-source", LibraryID: first.LibraryID, Filename: "later.md", Bytes: []byte("later")})
	if err != nil {
		t.Fatal(err)
	}
	lib, _ := s.GetLibrary(ctx, first.LibraryID)
	second, err := s.AcceptJob(ctx, types.AcceptJobInput{RequestID: "later", LibraryID: lib.ID, ExpectedVersion: lib.Version, StructureVersion: lib.StructureVersion, SourceVersionIDs: []string{raw.ResourceID}, Permission: first.Permission, RulesVersion: first.RulesVersion, ConfigDigest: first.ConfigDigest})
	if err != nil {
		t.Fatal(err)
	}
	waiting, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	model := &contractModel{respond: normalReply}
	_, err = Run(waiting, s, Input{JobID: second.JobID, Generator: model})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("wait ignored cancellation", err)
	}
	job, _ := s.GetJob(ctx, second.JobID)
	if job.State != "queued" || len(model.calls) != 0 {
		t.Fatal("waiting task started or was lost", job, model.calls)
	}
}
