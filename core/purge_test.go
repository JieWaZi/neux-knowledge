package core_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JieWaZi/neux-knowledge/core"
)

func TestSourcePurgePreservesManualKnowledgeAndScrubsHistory(t *testing.T) {
	ctx := context.Background()
	e, opt := open(t)
	l := library(t, e, "purge")
	s := source(t, e, l, "RAWSECRET")
	other := source(t, e, l, "other")
	refs := evidence(s)
	refs.Sources = append(refs.Sources, evidence(other).Sources...)
	p := writeAuditPage(t, e, l, core.Page{Slug: "concept/preserved", Title: "人工知识", PageType: "concept", Content: "历史RAWSECRET", Summary: "RAWSECRET", Evidence: refs, PageMetadata: core.JSON(`{"private":"RAWSECRET","sourcePurge":{"needsReview":true,"unavailableSourceIds":["RAWSECRET"],"unavailableVersionIds":["RAWSECRET"],"purgeIds":["RAWSECRET"]}}`)})
	currentLibrary, _ := e.GetLibrary(ctx, l.ID)
	p.Content = "人工保留的知识正文。"
	_, err := e.WritePage(ctx, core.WritePageInput{RequestID: "purge-current", LibraryID: l.ID, StructureVersion: currentLibrary.StructureVersion, ExpectedVersion: p.Version, Page: *p})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := e.PreviewSourcePurge(ctx, core.PurgePreviewInput{LibraryID: l.ID, SourceID: s.SourceID})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(preview)
	if strings.Contains(string(encoded), "RAWSECRET") {
		t.Fatal("preview leaked original text or filename")
	}
	if len(preview.Versions) != 1 || len(preview.Pages) != 1 || !preview.Pages[0].PreserveBody || preview.RevisionCount != 1 {
		t.Fatalf("incomplete preview %+v", preview)
	}
	confirm := core.ConfirmSourcePurgeInput{RequestID: "confirm-purge", LibraryID: l.ID, SourceID: s.SourceID, PreviewDigest: preview.Digest, Confirmed: true}
	rec, err := e.ConfirmSourcePurge(ctx, confirm)
	if err != nil {
		t.Fatal(err)
	}
	if rec.State != "accepted" {
		t.Fatalf("confirmation pretended filesystem deletion: %+v", rec)
	}
	again, err := e.ConfirmSourcePurge(ctx, confirm)
	if err != nil || again.ResourceID != rec.ResourceID {
		t.Fatalf("confirmation replay failed: %+v %v", again, err)
	}
	_, err = e.ReadRaw(ctx, l.ID, s.ID)
	code(t, err, core.ErrNotFound)
	p, err = e.GetPage(ctx, l.ID, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if p.Content != "人工保留的知识正文。" || p.Summary != "" || len(p.Evidence.Sources) != 1 || p.Evidence.Sources[0].SourceID != other.SourceID {
		t.Fatalf("manual or other-source knowledge lost: %+v", p)
	}
	if !strings.Contains(string(p.PageMetadata), "needsReview") || !strings.Contains(string(p.PageMetadata), s.SourceID) || strings.Contains(string(p.PageMetadata), "RAWSECRET") {
		t.Fatal("unavailable source marker missing or leaked source text")
	}
	revisions, _, err := e.ListRevisions(ctx, l.ID, p.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, rev := range revisions {
		full, err := e.GetRevision(ctx, l.ID, p.ID, rev.Version)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(full)
		if strings.Contains(string(b), "RAWSECRET") {
			t.Fatal("history retained removed source text")
		}
	}
	_, err = e.RecoverSourcePurge(ctx, core.RecoverSourcePurgeInput{RequestID: "recover-purge", LibraryID: l.ID, PurgeID: rec.ResourceID, ExpectedPolicyVersion: l.PolicyVersion})
	if err != nil {
		t.Fatal(err)
	}
	status, err := e.GetSourcePurge(ctx, l.ID, rec.ResourceID)
	if err != nil || status.State != "completed" {
		t.Fatalf("purge status=%+v %v", status, err)
	}
	_, err = e.ImportSource(ctx, core.ImportSourceInput{RequestID: "src-RAWSECRET", LibraryID: l.ID, Filename: "RAWSECRET.md", MIME: "text/markdown", Bytes: []byte("# 原件\nRAWSECRET")})
	if err != nil {
		t.Fatalf("original import receipt replay failed: %v", err)
	}
	_, err = e.ReadRaw(ctx, l.ID, s.ID)
	code(t, err, core.ErrNotFound)
	if _, err = os.Stat(filepath.Join(opt.RawDir, s.RawKey)); !os.IsNotExist(err) {
		t.Fatal("import receipt replay recreated purged raw object")
	}
	_, err = e.ImportSource(ctx, core.ImportSourceInput{RequestID: "resurrect", LibraryID: l.ID, SourceID: s.SourceID, ExpectedVersion: s.Version, Filename: "resurrect.md", Bytes: []byte("RAWSECRET")})
	if err == nil {
		t.Fatal("purged source identity resurrected")
	}
}

func TestSourcePurgeConfirmationRequiresUnchangedImpactAndExplicitConsent(t *testing.T) {
	ctx := context.Background()
	e, _ := open(t)
	l := library(t, e, "purge-cas")
	s := source(t, e, l, "cas")
	preview, err := e.PreviewSourcePurge(ctx, core.PurgePreviewInput{LibraryID: l.ID, SourceID: s.SourceID})
	if err != nil {
		t.Fatal(err)
	}
	in := core.ConfirmSourcePurgeInput{RequestID: "no-consent", LibraryID: l.ID, SourceID: s.SourceID, PreviewDigest: preview.Digest}
	_, err = e.ConfirmSourcePurge(ctx, in)
	code(t, err, core.ErrInvalid)
	writeAuditPage(t, e, l, core.Page{Slug: "new-reference", Title: "新增引用", PageType: "concept", Content: "知识", Evidence: evidence(s)})
	in.RequestID = "stale-confirm"
	in.Confirmed = true
	_, err = e.ConfirmSourcePurge(ctx, in)
	code(t, err, core.ErrVersionConflict)
	if _, err = e.ReadRaw(ctx, l.ID, s.ID); err != nil {
		t.Fatal("stale confirmation deleted raw")
	}
	preview, err = e.PreviewSourcePurge(ctx, core.PurgePreviewInput{LibraryID: l.ID, SourceID: s.SourceID})
	if err != nil {
		t.Fatal(err)
	}
	current, _ := e.GetLibrary(ctx, l.ID)
	_, err = e.UpdateLibrary(ctx, core.UpdateLibraryInput{RequestID: "purge-policy", LibraryID: l.ID, ExpectedVersion: current.Version, Name: current.Name, PolicyVersion: current.PolicyVersion + 1})
	if err != nil {
		t.Fatal(err)
	}
	in.RequestID = "revoked-confirm"
	in.PreviewDigest = preview.Digest
	_, err = e.ConfirmSourcePurge(ctx, in)
	code(t, err, core.ErrVersionConflict)
}

func TestSourcePurgeAllVersionsAndSharedRaw(t *testing.T) {
	ctx := context.Background()
	e, opt := open(t)
	l := library(t, e, "purge-versions")
	other := library(t, e, "purge-shared")
	var sourceID string
	versions := []string{}
	for i := 0; i < 2; i++ {
		rec, err := e.ImportSource(ctx, core.ImportSourceInput{RequestID: fmt.Sprintf("version-%d", i), LibraryID: l.ID, SourceID: sourceID, ExpectedVersion: int64(i), Filename: "shared.md", Bytes: []byte("shared content")})
		if err != nil {
			t.Fatal(err)
		}
		v, _ := e.GetSource(ctx, l.ID, rec.ResourceID)
		sourceID = v.SourceID
		versions = append(versions, v.ID)
	}
	shared, err := e.ImportSource(ctx, core.ImportSourceInput{RequestID: "other-shared", LibraryID: other.ID, Filename: "other.md", Bytes: []byte("shared content")})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := e.PreviewSourcePurge(ctx, core.PurgePreviewInput{LibraryID: l.ID, SourceID: sourceID})
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Versions) != 2 || preview.SharedBlobCount != 1 {
		t.Fatalf("shared/all-version preview incorrect: %+v", preview)
	}
	rec, err := e.ConfirmSourcePurge(ctx, core.ConfirmSourcePurgeInput{RequestID: "shared-confirm", LibraryID: l.ID, SourceID: sourceID, PreviewDigest: preview.Digest, Confirmed: true})
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Close(); err != nil {
		t.Fatal(err)
	}
	e, err = core.Open(ctx, opt)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	_, err = e.RecoverSourcePurge(ctx, core.RecoverSourcePurgeInput{RequestID: "shared-recover", LibraryID: l.ID, PurgeID: rec.ResourceID, ExpectedPolicyVersion: l.PolicyVersion})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range versions {
		_, err = e.GetSource(ctx, l.ID, id)
		code(t, err, core.ErrNotFound)
	}
	raw, err := e.ReadRaw(ctx, other.ID, shared.ResourceID)
	if err != nil || string(raw) != "shared content" {
		t.Fatalf("purge deleted other library raw: %q %v", raw, err)
	}
	status, err := e.GetSourcePurge(ctx, l.ID, rec.ResourceID)
	if err != nil || status.RetainedSharedBlobCount != 1 || status.State != "completed" {
		t.Fatalf("shared preservation not explicit: %+v %v", status, err)
	}
}

func TestSourcePurgeLintRetainsUnavailableVersionIdentity(t *testing.T) {
	ctx := context.Background()
	e, _ := open(t)
	l := library(t, e, "purge-lint")
	s := source(t, e, l, "PURGED_LINT_SECRET")
	page := writeAuditPage(t, e, l, core.Page{Slug: "concept/retained", Title: "人工保留", PageType: "concept", Content: "人工保留的正文需要核查。", Evidence: evidence(s)})
	preview, err := e.PreviewSourcePurge(ctx, core.PurgePreviewInput{LibraryID: l.ID, SourceID: s.SourceID})
	if err != nil {
		t.Fatal(err)
	}
	confirmed, err := e.ConfirmSourcePurge(ctx, core.ConfirmSourcePurgeInput{RequestID: "lint-confirm", LibraryID: l.ID, SourceID: s.SourceID, PreviewDigest: preview.Digest, Confirmed: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.RecoverSourcePurge(ctx, core.RecoverSourcePurgeInput{RequestID: "lint-recover", LibraryID: l.ID, PurgeID: confirmed.ResourceID, ExpectedPolicyVersion: l.PolicyVersion})
	if err != nil {
		t.Fatal(err)
	}
	report, err := e.LintLibrary(ctx, l.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, issue := range report.Issues {
		if issue.Type == "stale_ref" && issue.PageID == page.ID && issue.TargetID == s.ID {
			found++
		}
	}
	if found != 1 {
		t.Fatalf("purge review marker omitted stable unavailable version identity: %+v", report)
	}
	encoded, _ := json.Marshal(report)
	if strings.Contains(string(encoded), "PURGED_LINT_SECRET") {
		t.Fatal("purged source material entered lint diagnostics")
	}
	bundle, err := e.ExportMarkdown(ctx, core.ExportInput{LibraryID: l.ID})
	if err != nil || len(bundle.Files) != 1 {
		t.Fatalf("purged page export failed: %+v %v", bundle, err)
	}
	marker := bundle.Files[0].Metadata.SourcePurgeReview
	if marker == nil || !marker.NeedsReview || len(marker.UnavailableVersionIDs) != 1 || marker.UnavailableVersionIDs[0] != s.ID {
		t.Fatalf("export omitted review identity: %+v", marker)
	}
	encoded, _ = json.Marshal(bundle)
	if strings.Contains(string(encoded), "PURGED_LINT_SECRET") {
		t.Fatal("purged source material entered export metadata")
	}
}
