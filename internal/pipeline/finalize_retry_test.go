package pipeline

import (
	"context"
	"strings"
	"testing"

	types "github.com/JieWaZi/neux-knowledge/internal/domain"
)

func TestLiteRetryCarriesUnfinishedFinalize(t *testing.T) {
	ctx := context.Background()
	s, j, _ := contractFixture(t, 1)
	if err := s.EnsureOrganizeSourceWork(ctx, j.ID); err != nil {
		t.Fatal(err)
	}
	work, err := s.ListOrganizeSourceWork(ctx, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	model := &contractModel{respond: normalReply}
	svc := &liteService{store: s, job: j, library: j.LibrarySnapshot, input: Input{Generator: model, Reader: MarkdownReader{}}}
	if err := svc.processBatch(ctx, work); err != nil {
		t.Fatal(err)
	}
	ops, err := s.ListWikiFinalizeOps(ctx, j.ID, j.Attempt)
	if err != nil || len(ops) == 0 {
		t.Fatalf("first attempt ops=%d err=%v", len(ops), err)
	}
	current, err := s.GetJob(ctx, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.FailJob(ctx, types.FailJobInput{JobActionInput: types.JobActionInput{RequestID: "fail-before-finalize", JobID: j.ID, ExpectedVersion: current.Version}, Diagnostic: "index publication interrupted"}); err != nil {
		t.Fatal(err)
	}
	current, err = s.GetJob(ctx, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RetryJob(ctx, types.JobActionInput{RequestID: "retry-finalize", JobID: j.ID, ExpectedVersion: current.Version}); err != nil {
		t.Fatal(err)
	}
	result, err := Run(ctx, s, Input{JobID: j.ID, Generator: model})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("result=%+v previous finalize ops=%d", result, len(ops))
	if _, err := s.GetPageBySlug(ctx, j.LibraryID, "index"); err != nil {
		t.Fatalf("completed retry discarded prior attempt finalize work: %v", err)
	}
}

func TestLiteRetryPreservesCommittedIndex(t *testing.T) {
	ctx := context.Background()
	s, j, _ := contractFixture(t, 1)
	model := &contractModel{respond: normalReply}
	if err := s.EnsureOrganizeSourceWork(ctx, j.ID); err != nil {
		t.Fatal(err)
	}
	work, err := s.ListOrganizeSourceWork(ctx, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	svc := &liteService{store: s, job: j, library: j.LibrarySnapshot, input: Input{Generator: model, Reader: MarkdownReader{}}}
	if err := svc.processBatch(ctx, work); err != nil {
		t.Fatal(err)
	}
	ops, err := s.ListWikiFinalizeOps(ctx, j.ID, j.Attempt)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.rebuildIndex(ctx, ops); err != nil {
		t.Fatal(err)
	}
	before, err := s.GetPageBySlug(ctx, j.LibraryID, "index")
	if err != nil {
		t.Fatal(err)
	}
	current, err := s.GetJob(ctx, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.FailJob(ctx, types.FailJobInput{JobActionInput: types.JobActionInput{RequestID: "fail-after-index", JobID: j.ID, ExpectedVersion: current.Version}, Diagnostic: "links interrupted"}); err != nil {
		t.Fatal(err)
	}
	current, err = s.GetJob(ctx, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RetryJob(ctx, types.JobActionInput{RequestID: "retry-after-index", JobID: j.ID, ExpectedVersion: current.Version}); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(ctx, s, Input{JobID: j.ID, Generator: model}); err != nil {
		t.Fatal(err)
	}
	after, err := s.GetPageBySlug(ctx, j.LibraryID, "index")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	for _, r := range model.calls {
		if r.Stage == "index" {
			calls++
		}
	}
	if calls != 1 || after.Version != before.Version || after.Content != before.Content {
		t.Fatalf("committed index regenerated: calls=%d before=%+v after=%+v", calls, before, after)
	}
}

func TestLiteRetryIndexUsesOnlyUnappliedChanges(t *testing.T) {
	ctx := context.Background()
	s, j, sources := contractFixture(t, 2)
	var indexInputs []string
	model := &contractModel{respond: func(r GenerateRequest) (GenerateResult, error) {
		if r.Stage == "index" {
			indexInputs = append(indexInputs, r.Messages[len(r.Messages)-1].Content)
		}
		return normalReply(r)
	}}
	if err := s.EnsureOrganizeSourceWork(ctx, j.ID); err != nil {
		t.Fatal(err)
	}
	work, err := s.ListOrganizeSourceWork(ctx, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	svc := &liteService{store: s, job: j, library: j.LibrarySnapshot, input: Input{Generator: model, Reader: MarkdownReader{}}}
	if err := svc.processBatch(ctx, work[:1]); err != nil {
		t.Fatal(err)
	}
	ops, err := s.ListWikiFinalizeOps(ctx, j.ID, j.Attempt)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.rebuildIndex(ctx, ops); err != nil {
		t.Fatal(err)
	}
	current, err := s.GetJob(ctx, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.FailJob(ctx, types.FailJobInput{JobActionInput: types.JobActionInput{RequestID: "fail-with-pending-source", JobID: j.ID, ExpectedVersion: current.Version}, Diagnostic: "interrupted", Sources: []types.SourceConclusion{{SourceVersionID: work[1].SourceVersionID, State: "failed", Diagnostic: "interrupted"}}}); err != nil {
		t.Fatal(err)
	}
	current, err = s.GetJob(ctx, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RetryJob(ctx, types.JobActionInput{RequestID: "retry-pending-source", JobID: j.ID, ExpectedVersion: current.Version}); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(ctx, s, Input{JobID: j.ID, Generator: model}); err != nil {
		t.Fatal(err)
	}
	oldTitle, newTitle := "", ""
	for _, source := range sources {
		if source.ID == work[0].SourceVersionID {
			oldTitle = source.Filename
		} else {
			newTitle = source.Filename
		}
	}
	if len(indexInputs) != 2 {
		t.Fatalf("index calls=%d", len(indexInputs))
	}
	if strings.Contains(indexInputs[1], "<document_added>\n<title>"+oldTitle+"</title>") || !strings.Contains(indexInputs[1], "<document_added>\n<title>"+newTitle+"</title>") {
		t.Fatalf("wrong incremental change set: %s", indexInputs[1])
	}
}

func TestLiteRetryReusesLegacyIndexCheckpoint(t *testing.T) {
	ctx := context.Background()
	s, j, _ := contractFixture(t, 1)
	model := &contractModel{respond: normalReply}
	if err := s.EnsureOrganizeSourceWork(ctx, j.ID); err != nil {
		t.Fatal(err)
	}
	work, err := s.ListOrganizeSourceWork(ctx, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	svc := &liteService{store: s, job: j, library: j.LibrarySnapshot, input: Input{Generator: model, Reader: MarkdownReader{}}}
	if err := svc.processBatch(ctx, work); err != nil {
		t.Fatal(err)
	}
	ops, err := s.ListWikiFinalizeOps(ctx, j.ID, j.Attempt)
	if err != nil {
		t.Fatal(err)
	}
	page := types.WikiPage{ID: "legacy-index", KnowledgeBaseID: j.LibraryID, Slug: "index", Title: "Wiki Index", PageType: "index", Status: "published", Content: "# Wiki Index\n变化已处理。"}
	key := svc.key("finalize-index/" + hash(marshal(ops)))
	if _, err := s.PublishLite(ctx, "legacy-index-publication", j.ID, j.Attempt, &types.Publication{Maintenance: true, Pages: []types.PageMutation{{Page: page}}}, key); err != nil {
		t.Fatal(err)
	}
	current, err := s.GetJob(ctx, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.FailJob(ctx, types.FailJobInput{JobActionInput: types.JobActionInput{RequestID: "legacy-finalize-fail", JobID: j.ID, ExpectedVersion: current.Version}, Diagnostic: "links interrupted"}); err != nil {
		t.Fatal(err)
	}
	current, err = s.GetJob(ctx, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RetryJob(ctx, types.JobActionInput{RequestID: "legacy-finalize-retry", JobID: j.ID, ExpectedVersion: current.Version}); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(ctx, s, Input{JobID: j.ID, Generator: model}); err != nil {
		t.Fatal(err)
	}
	after, err := s.GetPageBySlug(ctx, j.LibraryID, "index")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range model.calls {
		if r.Stage == "index" {
			t.Fatal("legacy committed index changes were regenerated")
		}
	}
	if after.Content != page.Content {
		t.Fatalf("legacy index overwritten: %s", after.Content)
	}
}
