package core_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/JieWaZi/neux-knowledge/core"
)

func duplicatePurge(t *testing.T, e *core.Engine, l *core.Library, old, canonical *core.Page) core.PurgePageInput {
	t.Helper()
	return core.PurgePageInput{RequestID: "purge", LibraryID: l.ID, PageID: old.ID, ExpectedVersion: old.Version, ExpectedUpdatedAt: old.UpdatedAt.Format(time.RFC3339Nano), StructureVersion: l.StructureVersion, ExpectedPolicyVersion: l.PolicyVersion, CanonicalPageID: canonical.ID, CanonicalExpectedVersion: canonical.Version, CanonicalExpectedUpdatedAt: canonical.UpdatedAt.Format(time.RFC3339Nano)}
}

func TestPurgeArchivedDuplicateKeepsCanonicalOriginalsAndReceipt(t *testing.T) {
	e, _ := open(t)
	ctx := context.Background()
	l := library(t, e, "purge")
	original := source(t, e, l, "evidence")
	a := page(t, e, l, "concept/duplicate")
	b := page(t, e, l, "concept/canonical")
	c := page(t, e, l, "concept/referrer")
	l, _ = e.GetLibrary(ctx, l.ID)
	in := duplicatePurge(t, e, l, a, b)
	in.RequestID = "archive"
	if _, err := e.DeletePage(ctx, core.DeletePageInput(in)); err != nil {
		t.Fatal(err)
	}
	a, _ = e.GetPage(ctx, l.ID, a.ID)
	l, _ = e.GetLibrary(ctx, l.ID)
	// 归档后新增的真实入链也须在永久删除事务内指向规范页。
	if _, err := e.ReplacePageText(ctx, core.ReplacePageTextInput{RequestID: "late-reference", LibraryID: l.ID, PageID: c.ID, ExpectedVersion: c.Version, StructureVersion: l.StructureVersion, OldText: c.Content, NewText: "[[concept/duplicate|重复页]]"}); err != nil {
		t.Fatal(err)
	}
	in = duplicatePurge(t, e, l, a, b)
	receipt, err := e.PurgePage(ctx, in)
	if err != nil || receipt.Operation != "page.purge" {
		t.Fatal(receipt, err)
	}
	_, err = e.GetPage(ctx, l.ID, a.ID)
	code(t, err, core.ErrNotFound)
	_, err = e.GetRevision(ctx, l.ID, a.ID, 1)
	code(t, err, core.ErrNotFound)
	got, err := e.GetPage(ctx, l.ID, b.ID)
	if err != nil || got.Content != b.Content || got.Version != b.Version {
		t.Fatalf("canonical changed: %+v %v", got, err)
	}
	c, err = e.GetPage(ctx, l.ID, c.ID)
	if err != nil || c.Content != "[[concept/canonical|重复页]]" {
		t.Fatalf("lost navigation: %+v %v", c, err)
	}
	if _, err := e.GetSource(ctx, l.ID, original.ID); err != nil {
		t.Fatal("original removed", err)
	}
	if replay, err := e.PurgePage(ctx, in); err != nil || replay.CreatedAt != receipt.CreatedAt {
		t.Fatalf("lost purge receipt: %+v %v", replay, err)
	}
	activities, _, err := e.ListLibraryActivity(ctx, l.ID, core.ActivityQuery{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, activity := range activities {
		if activity.Operation == "page.purge" {
			var details map[string]any
			if err := json.Unmarshal(activity.Details, &details); err != nil {
				t.Fatal(err)
			}
			found = details["canonicalPageId"] == b.ID && details["title"] == a.Title
		}
	}
	if !found {
		t.Fatal("purge activity lost the removed page title or canonical destination")
	}
	in.CanonicalPageID = c.ID
	_, err = e.PurgePage(ctx, in)
	code(t, err, core.ErrIdempotencyConflict)
}

func TestPurgeDuplicateNormalizesIncomingLinkIdentity(t *testing.T) {
	e, _ := open(t)
	ctx := context.Background()
	l := library(t, e, "link-case")
	a := page(t, e, l, "Concept/Old Page")
	b := page(t, e, l, "canonical")
	c := page(t, e, l, "referrer")
	l, _ = e.GetLibrary(ctx, l.ID)
	if _, err := e.DeletePage(ctx, core.DeletePageInput{RequestID: "archive", LibraryID: l.ID, PageID: a.ID, ExpectedVersion: a.Version, StructureVersion: l.StructureVersion}); err != nil {
		t.Fatal(err)
	}
	a, _ = e.GetPage(ctx, l.ID, a.ID)
	l, _ = e.GetLibrary(ctx, l.ID)
	if _, err := e.ReplacePageText(ctx, core.ReplacePageTextInput{RequestID: "reference", LibraryID: l.ID, PageID: c.ID, ExpectedVersion: c.Version, StructureVersion: l.StructureVersion, OldText: c.Content, NewText: "[[Concept/Old Page|旧页]] and `[[Concept/Old Page]]`"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.PurgePage(ctx, duplicatePurge(t, e, l, a, b)); err != nil {
		t.Fatal(err)
	}
	c, _ = e.GetPage(ctx, l.ID, c.ID)
	if c.Content != "[[canonical|旧页]] and `[[Concept/Old Page]]`" {
		t.Fatalf("link normalization or code boundary lost: %s", c.Content)
	}
}

func TestPurgeDuplicateRejectsCanonicalDescendantCycle(t *testing.T) {
	e, _ := open(t)
	ctx := context.Background()
	l := library(t, e, "cycle")
	a := page(t, e, l, "duplicate")
	c := page(t, e, l, "child")
	b := page(t, e, l, "canonical")
	for _, update := range []*core.Page{c, b} {
		if update.ID == c.ID {
			update.ParentSlug = a.Slug
		} else {
			update.ParentSlug = c.Slug
		}
		l, _ = e.GetLibrary(ctx, l.ID)
		if _, err := e.WritePage(ctx, core.WritePageInput{RequestID: update.ID, LibraryID: l.ID, StructureVersion: l.StructureVersion, ExpectedVersion: update.Version, Page: *update}); err != nil {
			t.Fatal(err)
		}
	}
	b, _ = e.GetPage(ctx, l.ID, b.ID)
	l, _ = e.GetLibrary(ctx, l.ID)
	in := core.DeletePageInput(duplicatePurge(t, e, l, a, b))
	_, err := e.DeletePage(ctx, in)
	code(t, err, core.ErrInvalid)
	c, _ = e.GetPage(ctx, l.ID, c.ID)
	if c.ParentSlug != a.Slug {
		t.Fatal("rejected consolidation changed the parent graph")
	}
}

func TestPurgeDuplicateRequiresArchivedTargetAndFreshCanonical(t *testing.T) {
	for _, scenario := range []string{"published", "target-timestamp", "canonical-version", "canonical-timestamp", "self", "policy", "foreign"} {
		t.Run(scenario, func(t *testing.T) {
			e, _ := open(t)
			ctx := context.Background()
			l := library(t, e, scenario)
			a := page(t, e, l, "duplicate")
			b := page(t, e, l, "canonical")
			l, _ = e.GetLibrary(ctx, l.ID)
			if scenario != "published" {
				if _, err := e.DeletePage(ctx, core.DeletePageInput{RequestID: "archive", LibraryID: l.ID, PageID: a.ID, ExpectedVersion: a.Version, StructureVersion: l.StructureVersion}); err != nil {
					t.Fatal(err)
				}
				a, _ = e.GetPage(ctx, l.ID, a.ID)
				l, _ = e.GetLibrary(ctx, l.ID)
			}
			in := duplicatePurge(t, e, l, a, b)
			switch scenario {
			case "target-timestamp":
				in.ExpectedUpdatedAt = a.UpdatedAt.Add(-time.Second).Format(time.RFC3339Nano)
			case "canonical-version":
				in.CanonicalExpectedVersion++
			case "canonical-timestamp":
				in.CanonicalExpectedUpdatedAt = b.UpdatedAt.Add(-time.Second).Format(time.RFC3339Nano)
			case "self":
				in.CanonicalPageID = a.ID
			case "policy":
				in.ExpectedPolicyVersion++
			case "foreign":
				other := library(t, e, "outside")
				in.CanonicalPageID = page(t, e, other, "outside").ID
			}
			if _, err := e.PurgePage(ctx, in); err == nil {
				t.Fatal("unsafe permanent deletion accepted")
			}
			if _, err := e.GetPage(ctx, l.ID, a.ID); err != nil {
				t.Fatal("rejected deletion removed target", err)
			}
		})
	}
}

func TestPurgeDuplicateKeepsMaintenanceAttemptAndCancellationBarrier(t *testing.T) {
	for _, scenario := range []string{"active", "cancelled", "stale-attempt"} {
		t.Run(scenario, func(t *testing.T) {
			e, _ := open(t)
			ctx := context.Background()
			l := library(t, e, scenario)
			a := page(t, e, l, "duplicate")
			b := page(t, e, l, "canonical")
			l, _ = e.GetLibrary(ctx, l.ID)
			if _, err := e.DeletePage(ctx, core.DeletePageInput{RequestID: "archive", LibraryID: l.ID, PageID: a.ID, ExpectedVersion: a.Version, StructureVersion: l.StructureVersion}); err != nil {
				t.Fatal(err)
			}
			a, _ = e.GetPage(ctx, l.ID, a.ID)
			l, _ = e.GetLibrary(ctx, l.ID)
			receipt, err := e.AcceptJob(ctx, maintenanceInput(l, "maintenance"))
			if err != nil {
				t.Fatal(err)
			}
			job, ready, err := e.ActivateQueuedJob(ctx, receipt.JobID)
			if err != nil || !ready {
				t.Fatal(job, err)
			}
			attempt := job.Attempt
			if scenario == "cancelled" {
				if _, err := e.CancelOrganize(ctx, core.JobActionInput{RequestID: "cancel", JobID: job.ID, ExpectedVersion: job.Version}); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "stale-attempt" {
				attempt++
			}
			got, err := e.PurgePage(core.WithMaintenanceJob(ctx, job.ID, attempt), duplicatePurge(t, e, l, a, b))
			if scenario == "active" {
				if err != nil || got.JobID != job.ID {
					t.Fatalf("maintenance lost purge ownership: %+v %v", got, err)
				}
			} else {
				if err == nil {
					t.Fatal("revoked or stale attempt permanently deleted page")
				}
				if _, err := e.GetPage(ctx, l.ID, a.ID); err != nil {
					t.Fatal("rejected callback still removed duplicate", err)
				}
			}
		})
	}
}

func TestArchiveDuplicateRetargetsLinksWithCanonicalGuard(t *testing.T) {
	e, _ := open(t)
	ctx := context.Background()
	l := library(t, e, "archive-merge")
	a := page(t, e, l, "duplicate")
	b := page(t, e, l, "canonical")
	c := page(t, e, l, "referrer")
	l, _ = e.GetLibrary(ctx, l.ID)
	if _, err := e.ReplacePageText(ctx, core.ReplacePageTextInput{RequestID: "reference", LibraryID: l.ID, PageID: c.ID, ExpectedVersion: c.Version, StructureVersion: l.StructureVersion, OldText: c.Content, NewText: "[[duplicate]]"}); err != nil {
		t.Fatal(err)
	}
	in := core.DeletePageInput(duplicatePurge(t, e, l, a, b))
	in.RequestID = "stale-archive"
	in.CanonicalExpectedUpdatedAt = b.UpdatedAt.Add(-time.Second).Format(time.RFC3339Nano)
	_, err := e.DeletePage(ctx, in)
	code(t, err, core.ErrVersionConflict)
	in.RequestID = "archive"
	in.CanonicalExpectedUpdatedAt = b.UpdatedAt.Format(time.RFC3339Nano)
	if _, err := e.DeletePage(ctx, in); err != nil {
		t.Fatal(err)
	}
	c, _ = e.GetPage(ctx, l.ID, c.ID)
	if c.Content != "[[canonical]]" {
		t.Fatalf("archive lost the canonical destination: %+v", c)
	}
}
