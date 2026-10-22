package remote_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/JieWaZi/neux-knowledge/core"
	"github.com/JieWaZi/neux-knowledge/httpapi"
	"github.com/JieWaZi/neux-knowledge/remote"
)

func customFixture(t *testing.T) (*core.Engine, *core.Library, *remote.Custom, *remote.Custom) {
	t.Helper()
	d := t.TempDir()
	e, err := core.Open(context.Background(), core.Options{DBPath: filepath.Join(d, "db"), RawDir: filepath.Join(d, "raw")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	r, err := e.CreateLibrary(context.Background(), core.CreateLibraryInput{RequestID: "lib", Name: "共享", Kind: "general", WikiLanguage: "zh-CN"})
	if err != nil {
		t.Fatal(err)
	}
	l, _ := e.GetLibrary(context.Background(), r.ResourceID)
	p, _ := httpapi.NewPolicyStore(httpapi.SecurityConfig{Tokens: map[string]httpapi.Principal{"secret": {Subject: "two-clients", Libraries: map[string]httpapi.LibraryGrant{l.ID: {Operations: []string{"read", "write", "organize"}, PolicyVersion: l.PolicyVersion}}}}})
	s := httptest.NewServer(httpapi.New(e, httpapi.Options{Security: p}))
	t.Cleanup(s.Close)
	a, err := remote.NewCustom(remote.Options{BaseURL: s.URL, Token: "secret"}, remote.ResourceRef{Backend: "custom", ConnectionID: "connection", KnowledgeBaseID: l.ID})
	if err != nil {
		t.Fatal(err)
	}
	b, err := remote.NewCustom(remote.Options{BaseURL: s.URL, Token: "secret"}, a.Resource())
	if err != nil {
		t.Fatal(err)
	}
	return e, l, a, b
}
func TestCustomTwoClientsCASReplayAndBinary(t *testing.T) {
	ctx := context.Background()
	e, l, a, b := customFixture(t)
	in := core.WritePageInput{RequestID: "new", LibraryID: l.ID, StructureVersion: l.StructureVersion, Page: core.Page{KnowledgeBaseID: l.ID, Slug: "abc", Title: "共享知识", Content: "初稿", PageType: "concept"}}
	r, err := a.WritePage(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	pa, err := a.GetPage(ctx, r.ResourceID)
	if err != nil {
		t.Fatal(err)
	}
	pb, err := b.GetPage(ctx, r.ResourceID)
	if err != nil {
		t.Fatal(err)
	}
	pa.Content = "甲"
	edit := core.WritePageInput{RequestID: "a-edit", LibraryID: l.ID, ExpectedVersion: pa.Version, StructureVersion: l.StructureVersion, Page: *pa}
	rr, err := a.WritePage(ctx, edit)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := b.WritePage(ctx, edit)
	if err != nil || replay.ResourceID != rr.ResourceID || replay.Version != 2 {
		t.Fatal(replay, err)
	}
	pb.Content = "乙旧稿"
	_, err = b.WritePage(ctx, core.WritePageInput{RequestID: "b-edit", LibraryID: l.ID, ExpectedVersion: pb.Version, StructureVersion: l.StructureVersion, Page: *pb})
	var re *remote.Error
	if !errors.As(err, &re) || re.Code != "version_conflict" {
		t.Fatal(err)
	}
	edit.Page.Content = "同键不同内容"
	_, err = b.WritePage(ctx, edit)
	if !errors.As(err, &re) || re.Code != "idempotency_conflict" {
		t.Fatal(err)
	}
	refreshed, err := b.GetPage(ctx, r.ResourceID)
	if err != nil || refreshed.Content != "甲" || refreshed.Version != 2 {
		t.Fatal(refreshed, err)
	}
	history, err := a.ListRevisions(ctx, r.ResourceID, 0, 50)
	if err != nil || history.Total != 1 {
		t.Fatal(history, err)
	}
	raw := []byte{0, 1, 0xff, 0xef, 0xbb, 0xbf}
	sr, err := a.ImportSource(ctx, core.ImportSourceInput{RequestID: "raw", LibraryID: l.ID, Filename: "a.bin", Bytes: raw})
	if err != nil {
		t.Fatal(err)
	}
	download, err := b.DownloadSource(ctx, sr.ResourceID)
	if err != nil || !bytes.Equal(download.Bytes, raw) || download.Filename != "a.bin" {
		t.Fatal(download, err)
	}
	receipt, err := b.GetReceipt(ctx, "raw")
	if err != nil || receipt.ResourceID != sr.ResourceID {
		t.Fatal(receipt, err)
	}
	_, jobs, _ := e.ListJobs(ctx, l.ID, 0, 50)
	if jobs != 0 {
		t.Fatal("raw upload generated local job")
	}
	_, err = a.Organize(ctx, remote.OrganizeRequest{RequestID: "org", LibraryID: l.ID, ProfileID: "server"})
	if !errors.As(err, &re) || re.Code != "unsupported" {
		t.Fatal(err)
	}
}
func TestCustomUnknownOutcomeNeverRetries(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		conn, _, _ := w.(http.Hijacker).Hijack()
		_ = conn.Close()
	}))
	defer s.Close()
	c, err := remote.NewCustom(remote.Options{BaseURL: s.URL, Token: "secret"}, remote.ResourceRef{Backend: "custom", ConnectionID: "c", KnowledgeBaseID: "k"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.ImportSource(context.Background(), core.ImportSourceInput{RequestID: "stable-id", LibraryID: "k", Filename: "a", Bytes: []byte("x")})
	var unknown *remote.OutcomeUnknownError
	if !errors.As(err, &unknown) || unknown.RequestID != "stable-id" || calls.Load() != 1 {
		t.Fatal(err, calls.Load())
	}
}
func TestCustomRequiresCompleteTargetExceptDiscovery(t *testing.T) {
	for _, ref := range []remote.ResourceRef{{Backend: "remote", ConnectionID: "c", KnowledgeBaseID: "k"}, {Backend: "custom", KnowledgeBaseID: "k"}} {
		if _, err := remote.NewCustom(remote.Options{BaseURL: "http://localhost", Token: "s"}, ref); err == nil {
			t.Fatal("accepted invalid ref", ref)
		}
	}
	c, err := remote.NewCustom(remote.Options{BaseURL: "http://localhost", Token: "s"}, remote.ResourceRef{Backend: "custom", ConnectionID: "c"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.GetLibrary(context.Background())
	var re *remote.Error
	if !errors.As(err, &re) || re.Code != "invalid_resource" {
		t.Fatal(err)
	}
}

func TestCustomOrganizeSendsExplicitRebuild(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if r.Method != "POST" || r.URL.Path != "/api/knowledge/v1/libraries/kb/organize" || body["rebuild"] != true || body["profileId"] != "server" || body["instruction"] != "保留边界与来源" {
			t.Errorf("client lost rebuild: %s %s %+v", r.Method, r.URL, body)
		}
		_ = json.NewEncoder(w).Encode(core.Receipt{RequestID: "rebuild-client", LibraryID: "kb", ResourceID: "job", JobID: "job", Operation: "job.accept", State: "accepted"})
	}))
	defer s.Close()
	c, err := remote.NewCustom(remote.Options{BaseURL: s.URL, Token: "secret"}, remote.ResourceRef{Backend: "custom", ConnectionID: "connection", KnowledgeBaseID: "kb"})
	if err != nil {
		t.Fatal(err)
	}
	var in remote.OrganizeRequest
	_ = json.Unmarshal([]byte(`{"requestId":"rebuild-client","libraryId":"kb","expectedVersion":2,"structureVersion":3,"sourceVersionIds":["source"],"profileId":"server","rebuild":true,"instruction":"保留边界与来源"}`), &in)
	if _, err := c.Organize(context.Background(), in); err != nil {
		t.Fatal(err)
	}
}

func TestCustomReadsActivityWithExactCursorAndSourceChunks(t *testing.T) {
	_, l, client, _ := customFixture(t)
	activity, ok := any(client).(interface {
		ListLibraryActivity(context.Context, core.ActivityQuery) ([]*core.AuditLog, uint64, error)
	})
	if !ok {
		t.Fatal("custom protocol has no activity reader")
	}
	entries, cursor, err := activity.ListLibraryActivity(context.Background(), core.ActivityQuery{Action: core.AuditAction("kb.created"), Limit: 1})
	if err != nil || len(entries) != 1 || entries[0].ScopeID != l.ID || cursor != entries[0].ID {
		t.Fatal(entries, cursor, err)
	}
	chunks, ok := any(client).(interface {
		ListSourceChunks(context.Context, string) ([]core.IndexedDocumentChunk, error)
	})
	if !ok {
		t.Fatal("custom protocol has no source chunks reader")
	}
	raw, err := client.ImportSource(context.Background(), core.ImportSourceInput{RequestID: "unparsed", LibraryID: l.ID, Filename: "source.md", Bytes: []byte("unparsed")})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := chunks.ListSourceChunks(context.Background(), raw.ResourceID)
	if err != nil || rows == nil || len(rows) != 0 {
		t.Fatal("unparsed source should retain a real empty result", rows, err)
	}
	if _, err := chunks.ListSourceChunks(context.Background(), "missing"); err == nil {
		t.Fatal("missing source became empty success")
	}
}

func TestCustomRoutesHistoryThemeArchiveAndScope(t *testing.T) {
	ctx := context.Background()
	_, l, a, _ := customFixture(t)
	themes, err := a.ListThemes(ctx)
	if err != nil || len(themes) != 1 {
		t.Fatal(themes, err)
	}
	tr, err := a.WriteTheme(ctx, core.WriteThemeInput{RequestID: "theme", LibraryID: l.ID, StructureVersion: l.StructureVersion, Theme: core.Theme{KnowledgeBaseID: l.ID, Name: "人工目录", Manual: true}})
	if err != nil {
		t.Fatal(err)
	}
	l, err = a.GetLibrary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	pr, err := a.WritePage(ctx, core.WritePageInput{RequestID: "page", LibraryID: l.ID, StructureVersion: l.StructureVersion, Page: core.Page{KnowledgeBaseID: l.ID, Slug: "parent/child", Title: "页", Content: "第一版", PageType: "concept", FolderID: tr.ResourceID}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := a.GetPageBySlug(ctx, "parent/child")
	if err != nil || p.ID != pr.ResourceID {
		t.Fatal(p, err)
	}
	p.Content = "第二版"
	l, err = a.GetLibrary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.WritePage(ctx, core.WritePageInput{RequestID: "edit2", LibraryID: l.ID, ExpectedVersion: 1, StructureVersion: l.StructureVersion, Page: *p})
	if err != nil {
		t.Fatal(err)
	}
	rev, err := a.GetRevision(ctx, p.ID, 1)
	if err != nil || rev.Content != "第一版" {
		t.Fatal(rev, err)
	}
	_, err = a.RestorePage(ctx, core.RestorePageInput{RequestID: "restore", LibraryID: l.ID, PageID: p.ID, ExpectedVersion: 2, RevisionVersion: 1, StructureVersion: l.StructureVersion})
	if err != nil {
		t.Fatal(err)
	}
	p, err = a.GetPage(ctx, p.ID)
	if err != nil || p.Content != "第一版" || p.FolderID != tr.ResourceID || p.Version != 3 {
		t.Fatal(p, err)
	}
	list, err := a.ListPages(ctx, core.PageQuery{LibraryID: l.ID, Query: "页"})
	if err != nil || list.Total != 1 {
		t.Fatal(list, err)
	}
	sr, err := a.ImportSource(ctx, core.ImportSourceInput{RequestID: "upload2", LibraryID: l.ID, Filename: "b.txt", Bytes: []byte("raw")})
	if err != nil {
		t.Fatal(err)
	}
	source, err := a.GetSource(ctx, sr.ResourceID)
	if err != nil || source.RawKey != "" {
		t.Fatal(source, err)
	}
	_, err = a.ArchiveSource(ctx, core.ArchiveSourceInput{RequestID: "archive", LibraryID: l.ID, SourceVersionID: source.ID, ExpectedVersion: source.RowVersion, Archived: true})
	if err != nil {
		t.Fatal(err)
	}
	source, err = a.GetSource(ctx, source.ID)
	if err != nil || !source.Archived {
		t.Fatal(source, err)
	}
	sources, err := a.ListSources(ctx, 0, 50)
	// Archived sources remain readable by identity; source browsing counts only unarchived originals.
	if err != nil || sources.Total != 0 || len(sources.Items) != 0 {
		t.Fatal(sources, err)
	}
	_, err = a.WritePage(ctx, core.WritePageInput{RequestID: "wrong-kb", LibraryID: "other"})
	var re *remote.Error
	if !errors.As(err, &re) || re.Code != "scope_violation" {
		t.Fatal(err)
	}
	_, err = a.GetJob(ctx, "missing")
	if !errors.As(err, &re) || re.Code != "not_found" {
		t.Fatal(err)
	}
}

func TestCustomConnectionDiscoveryAndCreate(t *testing.T) {
	ctx := context.Background()
	e, l, _, _ := customFixture(t)
	p, _ := httpapi.NewPolicyStore(httpapi.SecurityConfig{Tokens: map[string]httpapi.Principal{"creator": {Subject: "admin", CreateLibrary: true, Libraries: map[string]httpapi.LibraryGrant{l.ID: {Operations: []string{"read"}, PolicyVersion: l.PolicyVersion}}}}})
	s := httptest.NewServer(httpapi.New(e, httpapi.Options{Security: p}))
	defer s.Close()
	c, err := remote.NewCustom(remote.Options{BaseURL: s.URL, Token: "creator"}, remote.ResourceRef{Backend: "custom", ConnectionID: "discovery"})
	if err != nil {
		t.Fatal(err)
	}
	list, err := c.ListLibraries(ctx, 0, 10)
	if err != nil || list.Total != 1 || list.Items[0].ID != l.ID {
		t.Fatal(list, err)
	}
	r, err := c.CreateLibrary(ctx, core.CreateLibraryInput{RequestID: "second", Name: "新库", Kind: "general", WikiLanguage: "zh-CN"})
	if err != nil || r.LibraryID == "" || r.ResourceID != r.LibraryID {
		t.Fatal(r, err)
	}
	list, err = c.ListLibraries(ctx, 0, 10)
	if err != nil || list.Total != 1 {
		t.Fatal("creation silently broadened credential scope", list, err)
	}
}

func TestCustomRejectsUnknownProtocolAndMalformedSuccessfulWrites(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"data":[]}`))
	}))
	defer s.Close()
	c, _ := remote.NewCustom(remote.Options{BaseURL: s.URL, Token: "s"}, remote.ResourceRef{Backend: "custom", ConnectionID: "c", KnowledgeBaseID: "k"})
	_, err := c.ListPages(context.Background(), core.PageQuery{LibraryID: "k"})
	var re *remote.Error
	if !errors.As(err, &re) || re.Code != "protocol_incompatible" {
		t.Fatal(err)
	}
	_, err = c.ImportSource(context.Background(), core.ImportSourceInput{RequestID: "stable", LibraryID: "k", Filename: "x", Bytes: []byte("x")})
	var unknown *remote.OutcomeUnknownError
	if !errors.As(err, &unknown) || unknown.RequestID != "stable" {
		t.Fatal(err)
	}
}

func TestCustomReceiptPreservesOriginalRequestIDWithSeparators(t *testing.T) {
	_, l, c, _ := customFixture(t)
	ctx := context.Background()
	in := core.ImportSourceInput{RequestID: "external/request:1", LibraryID: l.ID, Filename: "x", Bytes: []byte("x")}
	r, err := c.ImportSource(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := c.GetReceipt(ctx, in.RequestID)
	if err != nil || actual.RequestID != in.RequestID || actual.ResourceID != r.ResourceID {
		t.Fatal(actual, err)
	}
}
