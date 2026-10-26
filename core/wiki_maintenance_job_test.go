package core_test

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/JieWaZi/neux-knowledge/core"
)

func TestWikiMaintenanceAcceptsWithoutReprocessingSources(t *testing.T) {
	ctx := context.Background()
	e, _ := open(t)
	lib := library(t, e, "maintenance")
	input := core.AcceptJobInput{RequestID: "maintain", LibraryID: lib.ID, ExpectedVersion: lib.Version, StructureVersion: lib.StructureVersion, Permission: core.PermissionSnapshot{PolicyVersion: lib.PolicyVersion, Operations: []string{"maintain"}}, RulesVersion: "wiki-fixer", ConfigDigest: "frozen-config"}
	if err := json.Unmarshal([]byte(`{"maintenance":true}`), &input); err != nil {
		t.Fatal(err)
	}
	receipt, err := e.AcceptJob(ctx, input)
	if err != nil || receipt.State != "accepted" || receipt.JobID == "" {
		t.Fatalf("Wiki maintenance needs a persistent task without selecting originals: %+v %v", receipt, err)
	}
}

func maintenanceInput(lib *core.Library, request string) core.AcceptJobInput {
	return core.AcceptJobInput{Maintenance: true, RequestID: request, LibraryID: lib.ID, ExpectedVersion: lib.Version, StructureVersion: lib.StructureVersion, Permission: core.PermissionSnapshot{PolicyVersion: lib.PolicyVersion, Operations: []string{"maintain"}}, RulesVersion: "wiki-fixer", ConfigDigest: "config"}
}

func TestWikiMaintenanceQueueReportAndRestartUseExistingJobSchema(t *testing.T) {
	ctx := context.Background()
	e, options := open(t)
	lib := library(t, e, "maintenance-queue")
	src := source(t, e, lib, "unprocessed")
	first, err := e.AcceptJob(ctx, maintenanceInput(lib, "first-maintenance"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := e.AcceptJob(ctx, maintenanceInput(lib, "second-maintenance"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ready, err := e.ActivateQueuedJob(ctx, second.JobID); err != nil || ready {
		t.Fatalf("later maintenance jumped queue: %v %v", ready, err)
	}
	job, ready, err := e.ActivateQueuedJob(ctx, first.JobID)
	if err != nil || !ready || job.State != "processing" || !core.IsMaintenanceJob(job) {
		t.Fatalf("maintenance activation: %+v %v %v", job, ready, err)
	}
	stages, err := e.ListStages(ctx, job.ID)
	if err != nil || len(stages) != 1 || stages[0].Kind != "wiki_maintenance" || stages[0].State != "running" {
		t.Fatalf("persistent ACP stage: %+v %v", stages, err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := core.Open(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	job, ready, err = reopened.ActivateQueuedJob(ctx, job.ID)
	if err != nil || !ready || job.State != "processing" {
		t.Fatalf("restart lost maintenance: %+v %v", job, err)
	}
	input := core.JobActionInput{RequestID: "complete", JobID: job.ID, ExpectedVersion: job.Version}
	_, err = reopened.CompleteMaintenanceJob(ctx, input, "")
	code(t, err, core.ErrInvalid)
	completed, err := reopened.CompleteMaintenanceJob(ctx, input, "已核查目录与来源；归并两处重复知识，保留一个未解决问题。")
	if err != nil || completed.State != "completed" {
		t.Fatalf("maintenance completion: %+v %v", completed, err)
	}
	replay, err := reopened.CompleteMaintenanceJob(ctx, input, "已核查目录与来源；归并两处重复知识，保留一个未解决问题。")
	if err != nil || replay.CreatedAt != completed.CreatedAt {
		t.Fatalf("completion replay: %+v %v", replay, err)
	}
	stages, err = reopened.ListStages(ctx, job.ID)
	if err != nil || len(stages) != 1 || stages[0].Outcome != "succeeded" || stages[0].ResultText != "已核查目录与来源；归并两处重复知识，保留一个未解决问题。" {
		t.Fatalf("lost actual report: %+v %v", stages, err)
	}
	unchanged, err := reopened.GetSource(ctx, lib.ID, src.ID)
	if err != nil || unchanged.State != "pending" {
		t.Fatalf("maintenance reprocessed original: %+v %v", unchanged, err)
	}
	if _, ready, err := reopened.ActivateQueuedJob(ctx, second.JobID); err != nil || !ready {
		t.Fatalf("completed head did not yield queue: %v %v", ready, err)
	}
}

func TestWikiMaintenanceRejectsSourcesRebuildAndExpandedPermission(t *testing.T) {
	e, _ := open(t)
	lib := library(t, e, "maintenance-input")
	src := source(t, e, lib, "input")
	for _, input := range []core.AcceptJobInput{
		func() core.AcceptJobInput {
			in := maintenanceInput(lib, "with-source")
			in.SourceVersionIDs = []string{src.ID}
			return in
		}(),
		func() core.AcceptJobInput { in := maintenanceInput(lib, "with-rebuild"); in.Rebuild = true; return in }(),
		func() core.AcceptJobInput {
			in := maintenanceInput(lib, "with-expanded-permission")
			in.Permission.Operations = []string{"organize", "maintain"}
			return in
		}(),
	} {
		_, err := e.AcceptJob(context.Background(), input)
		code(t, err, core.ErrInvalid)
	}
	input := maintenanceInput(lib, "ordinary-empty")
	input.Maintenance = false
	input.Permission.Operations = []string{"organize"}
	receipt, err := e.AcceptJob(context.Background(), input)
	if err != nil || receipt.State != "no-op" {
		t.Fatalf("empty original selection broadened scope: %+v %v", receipt, err)
	}
}

func TestWikiMaintenanceTransactionScopeRejectsQueuedCancelledAndCrossLibraryWrites(t *testing.T) {
	ctx := context.Background()
	e, _ := open(t)
	lib := library(t, e, "maintenance-scope")
	other := library(t, e, "maintenance-other")
	p := page(t, e, lib, "concept/original")
	outside := page(t, e, other, "concept/outside")
	receipt, err := e.AcceptJob(ctx, maintenanceInput(lib, "scope-job"))
	if err != nil {
		t.Fatal(err)
	}
	scope := core.WithMaintenanceJob(ctx, receipt.JobID, 1)
	input := core.ReplacePageTextInput{RequestID: "queued-write", LibraryID: lib.ID, PageID: p.ID, ExpectedVersion: p.Version, StructureVersion: lib.StructureVersion, OldText: "原正文", NewText: "已核实正文"}
	_, err = e.ReplacePageText(scope, input)
	code(t, err, core.ErrVersionConflict)
	job, ready, err := e.ActivateQueuedJob(ctx, receipt.JobID)
	if err != nil || !ready {
		t.Fatal(job, ready, err)
	}
	input.RequestID = "active-write"
	_, err = e.ReplacePageText(scope, input)
	if err != nil {
		t.Fatal(err)
	}
	foreign := input
	foreign.RequestID = "cross-library"
	foreign.LibraryID = other.ID
	foreign.PageID = outside.ID
	foreign.StructureVersion = other.StructureVersion
	_, err = e.ReplacePageText(scope, foreign)
	code(t, err, core.ErrScopeViolation)
	_, err = e.ImportSource(scope, core.ImportSourceInput{RequestID: "no-originals", LibraryID: lib.ID, Filename: "forbidden.md", Bytes: []byte("forbidden")})
	code(t, err, core.ErrScopeViolation)
	_, err = e.CancelOrganize(ctx, core.JobActionInput{RequestID: "cancel", JobID: job.ID, ExpectedVersion: job.Version})
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.ReplacePageText(scope, input)
	code(t, err, core.ErrCancelled)
	input.RequestID = "late-write"
	input.ExpectedVersion++
	input.OldText = "已核实正文"
	input.NewText = "不应写入"
	_, err = e.ReplacePageText(scope, input)
	code(t, err, core.ErrCancelled)
	got, err := e.GetPage(ctx, lib.ID, p.ID)
	if err != nil || got.Content != "已核实正文" {
		t.Fatalf("late response changed page: %+v %v", got, err)
	}
	_, err = e.CompleteMaintenanceJob(ctx, core.JobActionInput{RequestID: "late-complete", JobID: job.ID, ExpectedVersion: job.Version}, "invalid success")
	code(t, err, core.ErrCancelled)
}

func TestWikiMaintenanceRetryRetainsFailedStageAndRejectsOldAttempt(t *testing.T) {
	ctx := context.Background()
	e, _ := open(t)
	lib := library(t, e, "maintenance-retry")
	p := page(t, e, lib, "concept/retry")
	receipt, err := e.AcceptJob(ctx, maintenanceInput(lib, "retry-accept"))
	if err != nil {
		t.Fatal(err)
	}
	first, ready, err := e.ActivateQueuedJob(ctx, receipt.JobID)
	if err != nil || !ready {
		t.Fatal(first, ready, err)
	}
	failure, err := e.FailJob(ctx, core.FailJobInput{JobActionInput: core.JobActionInput{RequestID: "maintenance-failure", JobID: first.ID, ExpectedVersion: first.Version}, Diagnostic: "model unavailable"})
	if err != nil || failure.State != "failed" {
		t.Fatalf("failed maintenance: %+v %v", failure, err)
	}
	stages, err := e.ListStages(ctx, first.ID)
	if err != nil || len(stages) != 1 || stages[0].State != "finished" || stages[0].Outcome != "failed" || stages[0].Diagnostic != "model unavailable" {
		t.Fatalf("failed stage remains active: %+v %v", stages, err)
	}
	_, err = e.RetryJob(ctx, core.JobActionInput{RequestID: "retry-maintenance", JobID: first.ID, ExpectedVersion: failure.Version})
	if err != nil {
		t.Fatal(err)
	}
	second, ready, err := e.ActivateQueuedJob(ctx, first.ID)
	if err != nil || !ready || second.Attempt != first.Attempt+1 {
		t.Fatal(second, ready, err)
	}
	_, err = e.ReplacePageText(core.WithMaintenanceJob(ctx, first.ID, first.Attempt), core.ReplacePageTextInput{RequestID: "old-attempt-write", LibraryID: lib.ID, PageID: p.ID, ExpectedVersion: p.Version, StructureVersion: lib.StructureVersion, OldText: "原正文", NewText: "旧尝试的迟到结果"})
	code(t, err, core.ErrStaleAttempt)
	_, err = e.CompleteMaintenanceJob(ctx, core.JobActionInput{RequestID: "old-version-complete", JobID: first.ID, ExpectedVersion: first.Version}, "stale success")
	code(t, err, core.ErrVersionConflict)
	_, err = e.CompleteMaintenanceJob(ctx, core.JobActionInput{RequestID: "retried-complete", JobID: second.ID, ExpectedVersion: second.Version}, "核查完成，无需更改。")
	if err != nil {
		t.Fatal(err)
	}
	stages, err = e.ListStages(ctx, first.ID)
	if err != nil || len(stages) != 2 {
		t.Fatalf("retry lost stage audit: %+v %v", stages, err)
	}
	byAttempt := map[int]*core.Stage{}
	for _, stage := range stages {
		byAttempt[stage.JobAttempt] = stage
	}
	if byAttempt[first.Attempt].Outcome != "failed" || byAttempt[second.Attempt].Outcome != "succeeded" {
		t.Fatalf("retry overwrote outcome: %+v", stages)
	}
}

func TestWikiMaintenanceScopeRejectsRevokedPolicy(t *testing.T) {
	ctx := context.Background()
	e, _ := open(t)
	lib := library(t, e, "maintenance-policy")
	p := page(t, e, lib, "concept/policy")
	receipt, err := e.AcceptJob(ctx, maintenanceInput(lib, "policy-accept"))
	if err != nil {
		t.Fatal(err)
	}
	job, ready, err := e.ActivateQueuedJob(ctx, receipt.JobID)
	if err != nil || !ready {
		t.Fatal(job, ready, err)
	}
	lib, err = e.GetLibrary(ctx, lib.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.UpdateLibrary(ctx, core.UpdateLibraryInput{RequestID: "policy-revoked", LibraryID: lib.ID, ExpectedVersion: lib.Version, Name: lib.Name, PolicyVersion: lib.PolicyVersion + 1})
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.ReplacePageText(core.WithMaintenanceJob(ctx, job.ID, job.Attempt), core.ReplacePageTextInput{RequestID: "revoked-write", LibraryID: lib.ID, PageID: p.ID, ExpectedVersion: p.Version, StructureVersion: lib.StructureVersion, OldText: "原正文", NewText: "撤权后的迟到结果"})
	code(t, err, core.ErrScopeViolation)
	_, err = e.CompleteMaintenanceJob(ctx, core.JobActionInput{RequestID: "revoked-complete", JobID: job.ID, ExpectedVersion: job.Version}, "invalid success")
	code(t, err, core.ErrScopeViolation)
}

func TestWikiMaintenanceActivitySeparatesTaskScopeFromActualChangedPages(t *testing.T) {
	ctx := context.Background()
	e, _ := open(t)
	lib := library(t, e, "maintenance-activity")
	first := page(t, e, lib, "concept/first")
	second := page(t, e, lib, "concept/second")
	receipt, err := e.AcceptJob(ctx, maintenanceInput(lib, "activity-maintenance"))
	if err != nil {
		t.Fatal(err)
	}
	job, ready, err := e.ActivateQueuedJob(ctx, receipt.JobID)
	if err != nil || !ready {
		t.Fatal(job, ready, err)
	}
	rows, _, err := e.ListLibraryActivity(ctx, lib.ID, core.ActivityQuery{Action: core.AuditAction("wiki.maintenance")})
	if err != nil || len(rows) != 1 || rows[0].Outcome != core.AuditOutcomeAccepted || rows[0].TargetType != "wiki_maintenance_task" {
		t.Fatalf("maintenance acceptance claims content changed: %+v %v", rows, err)
	}
	var accepted map[string]any
	if err := json.Unmarshal(rows[0].Details, &accepted); err != nil {
		t.Fatal(err)
	}
	if accepted["scope"] != "existing_wiki" || accepted["total"] != nil || accepted["changed_pages"] != nil {
		t.Fatalf("acceptance invents processed pages: %s", rows[0].Details)
	}
	scope := core.WithMaintenanceJob(ctx, job.ID, job.Attempt)
	for index, mutation := range []core.ReplacePageTextInput{
		{RequestID: "first-change", LibraryID: lib.ID, PageID: first.ID, ExpectedVersion: first.Version, StructureVersion: lib.StructureVersion, OldText: "原正文", NewText: "更新正文"},
		{RequestID: "repeat-first-change", LibraryID: lib.ID, PageID: first.ID, ExpectedVersion: first.Version + 1, StructureVersion: lib.StructureVersion, OldText: "更新正文", NewText: "再次更新"},
		{RequestID: "second-change", LibraryID: lib.ID, PageID: second.ID, ExpectedVersion: second.Version, StructureVersion: lib.StructureVersion, OldText: "原正文", NewText: "第二页更新"},
	} {
		changed, err := e.ReplacePageText(scope, mutation)
		if err != nil || changed.JobID != job.ID || changed.JobAttempt != job.Attempt {
			t.Fatalf("tool receipt %d lost its actual task: %+v %v", index, changed, err)
		}
	}
	_, err = e.CompleteMaintenanceJob(ctx, core.JobActionInput{RequestID: "activity-complete", JobID: job.ID, ExpectedVersion: job.Version}, "实际报告")
	if err != nil {
		t.Fatal(err)
	}
	rows, _, err = e.ListLibraryActivity(ctx, lib.ID, core.ActivityQuery{Action: core.AuditAction("wiki.maintenance")})
	if err != nil || len(rows) != 2 || rows[0].Outcome != core.AuditOutcomeSuccess {
		t.Fatalf("maintenance result missing: %+v %v", rows, err)
	}
	var completed map[string]any
	if err := json.Unmarshal(rows[0].Details, &completed); err != nil {
		t.Fatal(err)
	}
	if completed["changed_pages"] != float64(2) || completed["total"] != nil {
		t.Fatalf("distinct actual changes are not source totals or tool-call counts: %s", rows[0].Details)
	}
	failed, err := e.AcceptJob(ctx, maintenanceInput(lib, "activity-failure-accept"))
	if err != nil {
		t.Fatal(err)
	}
	failureJob, ready, err := e.ActivateQueuedJob(ctx, failed.JobID)
	if err != nil || !ready {
		t.Fatal(failureJob, ready, err)
	}
	_, err = e.FailOrganize(ctx, failureJob.ID, failureJob.Attempt, "actual failure")
	if err != nil {
		t.Fatal(err)
	}
	rows, _, err = e.ListLibraryActivity(ctx, lib.ID, core.ActivityQuery{Action: core.AuditAction("wiki.maintenance"), Outcome: core.AuditOutcomeFailed})
	if err != nil || len(rows) != 1 || rows[0].TargetType != "wiki_maintenance_task" {
		t.Fatalf("maintenance failure claims update success: %+v %v", rows, err)
	}
}

func TestWikiMaintenanceThemeReceiptCountsOnlyPagesActuallyWritten(t *testing.T) {
	ctx := context.Background()
	e, _ := open(t)
	lib := library(t, e, "maintenance-theme-count")
	parent, err := e.WriteTheme(ctx, core.WriteThemeInput{RequestID: "theme-parent", LibraryID: lib.ID, StructureVersion: lib.StructureVersion, Theme: core.Theme{Name: "旧目录"}})
	if err != nil {
		t.Fatal(err)
	}
	lib, _ = e.GetLibrary(ctx, lib.ID)
	child, err := e.WriteTheme(ctx, core.WriteThemeInput{RequestID: "theme-child", LibraryID: lib.ID, StructureVersion: lib.StructureVersion, Theme: core.Theme{Name: "子目录", ParentID: parent.ResourceID}})
	if err != nil {
		t.Fatal(err)
	}
	lib, _ = e.GetLibrary(ctx, lib.ID)
	other, err := e.WriteTheme(ctx, core.WriteThemeInput{RequestID: "theme-other", LibraryID: lib.ID, StructureVersion: lib.StructureVersion, Theme: core.Theme{Name: "其他目录"}})
	if err != nil {
		t.Fatal(err)
	}
	pages := []*core.Page{}
	for index, folderID := range []string{parent.ResourceID, child.ResourceID, other.ResourceID} {
		lib, _ = e.GetLibrary(ctx, lib.ID)
		created, err := e.WritePage(ctx, core.WritePageInput{RequestID: "theme-page-" + folderID, LibraryID: lib.ID, StructureVersion: lib.StructureVersion, Page: core.Page{FolderID: folderID, Slug: []string{"concept/parent", "concept/child", "concept/other"}[index], Title: "目录正文", PageType: "concept", Content: "实际保存的正文"}})
		if err != nil {
			t.Fatal(err)
		}
		p, err := e.GetPage(ctx, lib.ID, created.ResourceID)
		if err != nil {
			t.Fatal(err)
		}
		pages = append(pages, p)
	}
	lib, _ = e.GetLibrary(ctx, lib.ID)
	accepted, err := e.AcceptJob(ctx, maintenanceInput(lib, "theme-maintenance"))
	if err != nil {
		t.Fatal(err)
	}
	job, ready, err := e.ActivateQueuedJob(ctx, accepted.JobID)
	if err != nil || !ready {
		t.Fatal(job, ready, err)
	}
	scope := core.WithMaintenanceJob(ctx, job.ID, job.Attempt)
	receipt, err := e.WriteTheme(scope, core.WriteThemeInput{RequestID: "theme-rename", LibraryID: lib.ID, StructureVersion: lib.StructureVersion, Theme: core.Theme{ID: parent.ResourceID, Name: "新目录"}})
	if err != nil {
		t.Fatal(err)
	}
	written := map[string]bool{}
	for _, id := range receipt.PageIDs {
		written[id] = true
	}
	if len(receipt.PageIDs) != 2 || !written[pages[0].ID] || !written[pages[1].ID] || written[pages[2].ID] {
		t.Fatalf("theme receipt lost actual cascading page writes: %+v", receipt)
	}
	for index, before := range pages {
		after, err := e.GetPage(ctx, lib.ID, before.ID)
		if err != nil {
			t.Fatal(err)
		}
		if index < 2 && (slices.Equal(after.CategoryPath, before.CategoryPath) || after.WikiPath == before.WikiPath || !after.UpdatedAt.After(before.UpdatedAt)) {
			t.Fatalf("reported page was not actually written: before=%+v after=%+v", before, after)
		}
		if index == 2 && (!after.UpdatedAt.Equal(before.UpdatedAt) || !slices.Equal(after.CategoryPath, before.CategoryPath) || after.WikiPath != before.WikiPath) {
			t.Fatalf("unrelated directory page changed: before=%+v after=%+v", before, after)
		}
	}
	lib, _ = e.GetLibrary(ctx, lib.ID)
	empty, err := e.WriteTheme(scope, core.WriteThemeInput{RequestID: "theme-empty", LibraryID: lib.ID, StructureVersion: lib.StructureVersion, Theme: core.Theme{Name: "空目录"}})
	if err != nil || len(empty.PageIDs) != 0 {
		t.Fatalf("empty directory invented page writes: %+v %v", empty, err)
	}
	_, err = e.CompleteMaintenanceJob(ctx, core.JobActionInput{RequestID: "theme-completed", JobID: job.ID, ExpectedVersion: job.Version}, "已调整目录。")
	if err != nil {
		t.Fatal(err)
	}
	rows, _, err := e.ListLibraryActivity(ctx, lib.ID, core.ActivityQuery{Action: core.AuditActionWikiMaintenance, Outcome: core.AuditOutcomeSuccess})
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
	var details map[string]any
	if err := json.Unmarshal(rows[0].Details, &details); err != nil {
		t.Fatal(err)
	}
	if details["changed_pages"] != float64(2) {
		t.Fatalf("actual directory cascade must count once per written page: %s", rows[0].Details)
	}
}
