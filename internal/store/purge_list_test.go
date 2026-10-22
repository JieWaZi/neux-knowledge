package store

import (
	"context"
	types "github.com/JieWaZi/neux-knowledge/internal/domain"
	"testing"
)

func TestListSourcePurgesSurvivesReopenAndScopesLibrary(t *testing.T) {
	ctx := context.Background()
	s, db, raw, l := auditStore(t)
	v := purgeStoreSource(t, s, l)
	p, e := s.PreviewSourcePurge(ctx, types.PurgePreviewInput{LibraryID: l.ID, SourceID: v.SourceID})
	if e != nil {
		t.Fatal(e)
	}
	r, e := s.ConfirmSourcePurge(ctx, types.ConfirmSourcePurgeInput{RequestID: "list-purge", LibraryID: l.ID, SourceID: v.SourceID, PreviewDigest: p.Digest, Confirmed: true})
	if e != nil {
		t.Fatal(e)
	}
	other, e := s.CreateLibrary(ctx, types.CreateLibraryInput{RequestID: "other-list", Name: "other", Kind: "general", WikiLanguage: "zh-CN"})
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	s, e = Open(ctx, db, raw)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	rows, total, e := s.ListSourcePurges(ctx, l.ID, 0, 10)
	if e != nil || total != 1 || len(rows) != 1 || rows[0].ID != r.ResourceID || rows[0].State != "pending" {
		t.Fatalf("lost pending cleanup: %+v %d %v", rows, total, e)
	}
	rows, total, e = s.ListSourcePurges(ctx, other.ResourceID, 0, 10)
	if e != nil || total != 0 || len(rows) != 0 {
		t.Fatalf("cross-library leak: %+v %d %v", rows, total, e)
	}
	rows, total, e = s.ListSourcePurges(ctx, l.ID, 1, 10)
	if e != nil || total != 1 || len(rows) != 0 {
		t.Fatalf("paging: %+v %d %v", rows, total, e)
	}
	if _, _, e = s.ListSourcePurges(ctx, l.ID, -1, 10); e == nil {
		t.Fatal("negative offset accepted")
	}
}
