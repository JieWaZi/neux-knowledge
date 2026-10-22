package core_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/JieWaZi/neux-knowledge/core"
)

func TestActivityIsDurableScopedAndIdempotent(t *testing.T) {
	e, opt := open(t)
	ctx := core.WithActivityActor(context.Background(), core.ActivityActor{UserID: "actual-user", Trigger: "user"})
	in := core.CreateLibraryInput{RequestID: "activity-create", Name: "活动库", Kind: "general", WikiLanguage: "zh-CN", Purpose: "DO-NOT-LOG-BODY"}
	rec, err := e.CreateLibrary(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.CreateLibrary(ctx, in); err != nil {
		t.Fatal(err)
	}
	lib, _ := e.GetLibrary(ctx, rec.ResourceID)
	other := library(t, e, "activity-other")
	_, err = e.UpdateLibrary(ctx, core.UpdateLibraryInput{RequestID: "activity-update", LibraryID: lib.ID, ExpectedVersion: lib.Version, Name: lib.Name, PolicyVersion: lib.PolicyVersion, Purpose: "DO-NOT-LOG-TOKEN"})
	if err != nil {
		t.Fatal(err)
	}
	raw := core.ImportSourceInput{RequestID: "activity-upload", LibraryID: lib.ID, Filename: "notes.md", Bytes: []byte("DO-NOT-LOG-RAW-CONTENT")}
	if _, err = e.ImportSource(ctx, raw); err != nil {
		t.Fatal(err)
	}
	if _, err = e.ImportSource(ctx, raw); err != nil {
		t.Fatal(err)
	}
	rows, cursor, err := e.ListLibraryActivity(ctx, lib.ID, core.ActivityQuery{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].ID <= rows[1].ID || cursor != rows[1].ID {
		t.Fatalf("incorrect cursor page: %+v cursor=%d", rows, cursor)
	}
	older, next, err := e.ListLibraryActivity(ctx, lib.ID, core.ActivityQuery{AfterID: cursor})
	if err != nil {
		t.Fatal(err)
	}
	if len(older) != 1 || next != older[0].ID || older[0].Action != core.AuditActionKBCreated {
		t.Fatalf("idempotent writes duplicated or cursor skipped: %+v", older)
	}
	for _, row := range append(rows, older...) {
		if row.ScopeID != lib.ID || row.ActorUserID != "actual-user" || row.ActorRole != "" {
			t.Fatalf("scope or actor incorrect: %+v", row)
		}
		data, _ := json.Marshal(row)
		if strings.Contains(string(data), "DO-NOT-LOG") {
			t.Fatalf("content/credential leaked: %s", data)
		}
	}
	filtered, _, err := e.ListLibraryActivity(ctx, lib.ID, core.ActivityQuery{Action: core.AuditActionKBUpdated, Outcome: core.AuditOutcomeSuccess, ActorUserID: "actual-user"})
	if err != nil || len(filtered) != 1 {
		t.Fatalf("filters lost: %+v %v", filtered, err)
	}
	foreign, _, err := e.ListLibraryActivity(ctx, other.ID, core.ActivityQuery{ActorUserID: "actual-user"})
	if err != nil || len(foreign) != 0 {
		t.Fatalf("cross-library data leaked: %+v %v", foreign, err)
	}
	empty, zero, err := e.ListLibraryActivity(ctx, lib.ID, core.ActivityQuery{AfterID: older[0].ID})
	if err != nil || len(empty) != 0 || zero != 0 {
		t.Fatalf("empty cursor wrong: %+v %d %v", empty, zero, err)
	}
	if err = e.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := core.Open(ctx, opt)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	all, _, err := reopened.ListLibraryActivity(ctx, lib.ID, core.ActivityQuery{})
	if err != nil || len(all) != 3 {
		t.Fatalf("activities not durable: %+v %v", all, err)
	}
}

func TestActivityAcceptedIsNotSuccessAndFailureKeepsInitiator(t *testing.T) {
	e, _ := open(t)
	ctx := core.WithActivityActor(context.Background(), core.ActivityActor{UserID: "initiator", Trigger: "user"})
	l := library(t, e, "activity-failure")
	raw := source(t, e, l, "activity-failure")
	in := core.AcceptJobInput{RequestID: "activity-accept", LibraryID: l.ID, ExpectedVersion: l.Version, StructureVersion: l.StructureVersion, SourceVersionIDs: []string{raw.ID}, Permission: core.PermissionSnapshot{PolicyVersion: l.PolicyVersion, Operations: []string{"organize"}}, RulesVersion: "rules", ConfigDigest: "config"}
	rec, err := e.AcceptJob(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.AcceptJob(ctx, in); err != nil {
		t.Fatal(err)
	}
	accepted, _, err := e.ListLibraryActivity(ctx, l.ID, core.ActivityQuery{Action: core.AuditActionKnowledgeReparseStarted})
	if err != nil || len(accepted) != 1 || accepted[0].Outcome != core.AuditOutcomeAccepted {
		t.Fatalf("accepted task presented as complete: %+v %v", accepted, err)
	}
	j, _ := e.GetJob(ctx, rec.JobID)
	fail := core.FailJobInput{JobActionInput: core.JobActionInput{RequestID: "activity-failed", JobID: j.ID, ExpectedVersion: j.Version}, Diagnostic: "DO-NOT-LOG-secret-token", Sources: []core.SourceConclusion{{SourceVersionID: raw.ID, State: "failed", Diagnostic: "DO-NOT-LOG-body"}}}
	if _, err = e.FailJob(context.Background(), fail); err != nil {
		t.Fatal(err)
	}
	if _, err = e.FailJob(context.Background(), fail); err != nil {
		t.Fatal(err)
	}
	events, _, err := e.ListLibraryActivity(ctx, l.ID, core.ActivityQuery{Action: core.AuditActionKnowledgeReparseStarted})
	if err != nil || len(events) != 2 {
		t.Fatalf("terminal event missing/duplicated: %+v %v", events, err)
	}
	if events[0].Outcome != core.AuditOutcomeFailed || events[0].ActorUserID != "initiator" {
		t.Fatalf("background lost actual actor: %+v", events[0])
	}
	data, _ := json.Marshal(events)
	if strings.Contains(string(data), "DO-NOT-LOG") {
		t.Fatalf("failure detail leaked: %s", data)
	}
}

func TestExecutionDefaultsActivityIsNarrowAndIdempotent(t *testing.T) {
	e, _ := open(t)
	l := library(t, e, "default-activity")
	ctx := core.WithActivityActor(context.Background(), core.ActivityActor{UserID: "actual-user"})
	_, err := e.UpdateLibrary(ctx, core.UpdateLibraryInput{RequestID: "defaults-and-core", LibraryID: l.ID, ExpectedVersion: l.Version, Name: l.Name, PolicyVersion: l.PolicyVersion})
	if err != nil {
		t.Fatal(err)
	}
	if err = e.RecordLibraryExecutionDefaultsActivity(ctx, l.ID, "defaults-and-core"); err != nil {
		t.Fatal(err)
	}
	if err = e.RecordLibraryExecutionDefaultsActivity(ctx, l.ID, "defaults-and-core"); err != nil {
		t.Fatal(err)
	}
	after, _ := e.GetLibrary(ctx, l.ID)
	if after.Version != l.Version+1 {
		t.Fatal("host defaults audit changed core library version")
	}
	rows, _, err := e.ListLibraryActivity(ctx, l.ID, core.ActivityQuery{Action: core.AuditActionKBUpdated})
	if err != nil || len(rows) != 2 {
		t.Fatalf("operation identity collided or duplicated: %+v %v", rows, err)
	}
	if rows[0].Operation != "library.execution_defaults" || !strings.Contains(string(rows[0].Details), "execution_defaults") || rows[0].ActorUserID != "actual-user" {
		t.Fatalf("defaults operation attribution lost: %+v", rows[0])
	}
	other := library(t, e, "defaults-other")
	err = e.RecordLibraryExecutionDefaultsActivity(ctx, other.ID, "defaults-and-core")
	code(t, err, core.ErrIdempotencyConflict)
}

func TestActivityUpdateOnlyReportsActuallyChangedFields(t *testing.T) {
	e, _ := open(t)
	ctx := context.Background()
	lib := library(t, e, "actual-changed-fields")
	cfg := *lib.ChunkingConfig
	cfg.ParentChunkSize++
	_, err := e.UpdateLibrary(ctx, core.UpdateLibraryInput{RequestID: "only-chunk-change", LibraryID: lib.ID, ExpectedVersion: lib.Version, Name: lib.Name, Purpose: lib.Purpose, Archived: lib.Archived, PolicyVersion: lib.PolicyVersion, ChunkingConfig: &cfg})
	if err != nil {
		t.Fatal(err)
	}
	rows, _, err := e.ListLibraryActivity(ctx, lib.ID, core.ActivityQuery{Action: core.AuditActionKBUpdated})
	if err != nil || len(rows) != 1 {
		t.Fatalf("missing or duplicate update event: %+v %v", rows, err)
	}
	var details struct {
		// ChangedFields contains only business fields actually changed within the transaction.
		ChangedFields []string `json:"changed_fields"`
	}
	if err = json.Unmarshal(rows[0].Details, &details); err != nil {
		t.Fatal(err)
	}
	if len(details.ChangedFields) != 1 || details.ChangedFields[0] != "chunking_config" {
		t.Fatalf("unchanged name/archive fields were falsely claimed: %s", rows[0].Details)
	}
}
