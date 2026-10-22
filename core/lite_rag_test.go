package core_test

import (
	"context"
	"fmt"
	"github.com/JieWaZi/neux-knowledge/core"
	"strings"
	"testing"
	"time"
)

// directModelFunc replaces only the actual external model while retaining the core database and index.
type directModelFunc func(context.Context, core.GenerateRequest) (core.GenerateResult, error)

func (f directModelFunc) Generate(ctx context.Context, r core.GenerateRequest) (core.GenerateResult, error) {
	return f(ctx, r)
}
func TestLiteRAGSummaryKeepsUpstreamRolesBudgetAndActualIndex(t *testing.T) {
	for _, truncated := range []bool{false, true} {
		t.Run(fmt.Sprint(truncated), func(t *testing.T) {
			ctx := context.Background()
			e, _ := open(t)
			strategy := core.IndexingStrategy{KeywordEnabled: true}
			created, err := e.CreateLibrary(ctx, core.CreateLibraryInput{RequestID: "rag", Name: "检索资料", Kind: "general", WikiLanguage: "zh-CN", IndexingStrategy: &strategy})
			if err != nil {
				t.Fatal(err)
			}
			lib, _ := e.GetLibrary(ctx, created.ResourceID)
			imported, err := e.ImportSource(ctx, core.ImportSourceInput{RequestID: "raw", LibraryID: lib.ID, Filename: "不应进入模型的文件名.md", MIME: "text/markdown", Bytes: []byte("授权允许时保留事实，离线会话具有独立的适用条件和有效期限。")})
			if err != nil {
				t.Fatal(err)
			}
			accepted, err := e.AcceptJob(ctx, core.AcceptJobInput{RequestID: "accept", LibraryID: lib.ID, ExpectedVersion: lib.Version, StructureVersion: lib.StructureVersion, SourceVersionIDs: []string{imported.ResourceID}, Permission: core.PermissionSnapshot{PolicyVersion: lib.PolicyVersion, Operations: []string{"organize"}}, RulesVersion: "lite", ConfigDigest: "rag"})
			if err != nil {
				t.Fatal(err)
			}
			if truncated {
				if err = e.EnsureOrganizeSourceWork(ctx, accepted.JobID); err != nil {
					t.Fatal(err)
				}
				for i := 0; i < 5; i++ {
					work, _ := e.ListOrganizeSourceWork(ctx, accepted.JobID)
					if _, err = e.DeferOrganizeSourceWork(ctx, core.SourceWorkActionInput{RequestID: fmt.Sprint("failure", i), JobID: accepted.JobID, JobAttempt: 1, SourceVersionID: imported.ResourceID, ExpectedVersion: work[0].Version, Diagnostic: "prior model failure", NotBefore: time.Now().Add(-time.Minute).UnixMilli()}); err != nil {
						t.Fatal(err)
					}
				}
			}
			calls := 0
			model := directModelFunc(func(_ context.Context, r core.GenerateRequest) (core.GenerateResult, error) {
				calls++
				if r.Stage != "document_summary" || r.MaxTokens != 1024 || r.Thinking || r.Temperature != .3 {
					t.Errorf("ordinary summary contract=%+v", r)
				}
				if len(r.Messages) != 2 || r.Messages[0].Role != "system" || r.Messages[1].Role != "user" {
					t.Errorf("roles=%+v", r.Messages)
				}
				if !strings.Contains(r.Messages[0].Content, "保留离线条件") || strings.Contains(r.Messages[1].Content, "不应进入模型的文件名") {
					t.Error("summary instructions or real document input were not scoped")
				}
				reason := "stop"
				if truncated {
					reason = "length"
				}
				return core.GenerateResult{Text: `{"summary":"文档给出授权与离线会话条件。","gist":"会话授权","topics":["授权","离线"],"doc_type":"guide","typical_question":"离线会话何时有效？"}`, FinishReason: reason}, nil
			})
			result, err := e.RunOrganize(ctx, core.OrganizeInput{JobID: accepted.JobID, Generator: model, Instruction: "保留离线条件", PrepareDocument: func(ctx context.Context, id string, a core.ReadArtifact) (core.ReadArtifact, error) {
				return e.PrepareIndexedDocument(ctx, accepted.JobID, id, a, core.DocumentEmbedder{})
			}})
			if err != nil {
				t.Fatal(err)
			}
			source, _ := e.GetSource(ctx, lib.ID, imported.ResourceID)
			_, pages, _ := e.ListPages(ctx, core.PageQuery{LibraryID: lib.ID, Limit: 10})
			chunks, _ := e.ListDocumentChunks(ctx, lib.ID, source.ID)
			if calls != 1 || pages != 0 || len(chunks) == 0 {
				t.Fatalf("calls=%d pages=%d chunks=%d", calls, pages, len(chunks))
			}
			if truncated {
				if result.State != "failed" || source.Description != "" || source.Profile != nil {
					t.Fatalf("truncated profile was published: %+v", source)
				}
			} else if result.State != "completed" || source.Profile == nil || source.Description == "" {
				t.Fatalf("ordinary profile not published: %+v", source)
			}
		})
	}
}

// blockingReader waits for cancellation to simulate an actual reader that has not returned.
type blockingReader struct {
	started chan struct{} /* Signal that the Reader has actually started. */
}

func (r blockingReader) Read(ctx context.Context, _ core.ReadRequest) (core.ReadArtifact, error) {
	close(r.started)
	<-ctx.Done()
	return core.ReadArtifact{}, ctx.Err()
}
func TestLiteInternalCancellationDuringReadingThenRetryCallsModel(t *testing.T) {
	e, _ := open(t)
	lib := library(t, e, "retry-reading")
	imported, err := e.ImportSource(context.Background(), core.ImportSourceInput{RequestID: "reading-raw", LibraryID: lib.ID, Filename: "read.md", MIME: "text/markdown", Bytes: []byte("会话验证必须区分在线与离线条件，来源提供真实依据。")})
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := e.AcceptJob(context.Background(), core.AcceptJobInput{RequestID: "reading-job", LibraryID: lib.ID, ExpectedVersion: lib.Version, StructureVersion: lib.StructureVersion, SourceVersionIDs: []string{imported.ResourceID}, Permission: core.PermissionSnapshot{PolicyVersion: lib.PolicyVersion, Operations: []string{"organize"}}, RulesVersion: "lite", ConfigDigest: "reading"})
	if err != nil {
		t.Fatal(err)
	}
	model := directModelFunc(func(_ context.Context, r core.GenerateRequest) (core.GenerateResult, error) {
		switch r.Stage {
		case "map":
			return core.GenerateResult{Text: `{"entities":[],"concepts":[]}`, FinishReason: "stop"}, nil
		case "summary":
			return core.GenerateResult{Text: "SUMMARY: 会话条件\n# 摘要\n会话条件区分在线与离线。", FinishReason: "stop"}, nil
		case "index":
			return core.GenerateResult{Text: "# 会话资料\n收录会话条件。", FinishReason: "stop"}, nil
		}
		return core.GenerateResult{}, fmt.Errorf("unexpected stage %s", r.Stage)
	})
	reader := blockingReader{started: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := e.RunOrganize(ctx, core.OrganizeInput{JobID: accepted.JobID, Reader: reader, Generator: model})
		done <- err
	}()
	select {
	case <-reader.started:
	case <-time.After(5 * time.Second):
		t.Fatal("Reader never started")
	}
	job, _ := e.GetJob(context.Background(), accepted.JobID)
	_, err = e.CancelOrganize(context.Background(), core.JobActionInput{RequestID: "internal-cancel", JobID: job.ID, ExpectedVersion: job.Version})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("reading did not cancel")
	}
	job, _ = e.GetJob(context.Background(), job.ID)
	if job.State != "cancelled" {
		t.Fatalf("internal cancellation did not settle: %+v", job)
	}
	if _, err = e.RetryJob(context.Background(), core.JobActionInput{RequestID: "retry", JobID: job.ID, ExpectedVersion: job.Version}); err != nil {
		t.Fatal(err)
	}
	result, err := e.RunOrganize(context.Background(), core.OrganizeInput{JobID: job.ID, Generator: model})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != "completed" {
		t.Fatalf("retry stranded: %+v", result)
	}
}
