package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	types "github.com/JieWaZi/neux-knowledge/internal/domain"
	"gorm.io/gorm"
)

func auditStore(t *testing.T) (*Store, string, string, *types.Library) {
	t.Helper()
	dir := t.TempDir()
	dbPath, rawPath := filepath.Join(dir, "knowledge.db"), filepath.Join(dir, "raw")
	s, err := Open(context.Background(), dbPath, rawPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	rec, err := s.CreateLibrary(context.Background(), types.CreateLibraryInput{RequestID: "library", Name: "核查", Kind: "general", WikiLanguage: "zh-CN"})
	if err != nil {
		t.Fatal(err)
	}
	l, err := s.GetLibrary(context.Background(), rec.ResourceID)
	if err != nil {
		t.Fatal(err)
	}
	return s, dbPath, rawPath, l
}

func TestAuditExportCursorAndNoPersistentSideEffects(t *testing.T) {
	ctx := context.Background()
	s, _, _, l := auditStore(t)
	pages := make([]types.WikiPage, 205)
	for i := range pages {
		pages[i] = types.WikiPage{ID: fmt.Sprintf("p%04d", i), KnowledgeBaseID: l.ID, Slug: fmt.Sprintf("concept/%d", i), Title: "知识", PageType: "concept", Status: "published", Content: strings.Repeat("知", 50), Version: 1, WikiLanguage: l.WikiLanguage}
	}
	if err := s.db.CreateInBatches(pages, 100).Error; err != nil {
		t.Fatal(err)
	}
	counts := map[string]int64{}
	for _, table := range []string{"knowledge_receipts", "wiki_page_issues", "wiki_page_revisions", "knowledge_jobs"} {
		var n int64
		if err := s.db.Table(table).Count(&n).Error; err != nil {
			t.Fatal(err)
		}
		counts[table] = n
	}
	report, err := s.LintLibrary(ctx, l.ID)
	if err != nil {
		t.Fatal(err)
	}
	if report.PageCount != 205 || len(report.Issues) != 205 {
		t.Fatalf("cursor skipped pages: %d %d", report.PageCount, len(report.Issues))
	}
	bundle, err := s.ExportMarkdown(ctx, types.ExportInput{LibraryID: l.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Files) != 205 {
		t.Fatalf("export truncated pages: %d", len(bundle.Files))
	}
	for table, want := range counts {
		var n int64
		if err = s.db.Table(table).Count(&n).Error; err != nil || n != want {
			t.Fatalf("read mutated %s: %d != %d: %v", table, n, want, err)
		}
	}
}

func TestMarkdownExportReadsOneSQLiteSnapshot(t *testing.T) {
	s, dbPath, rawPath, l := auditStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pages := []types.WikiPage{{ID: "a", KnowledgeBaseID: l.ID, Slug: "concept/a", Title: "甲", PageType: "concept", Status: "published", Content: "old", Version: 1}, {ID: "b", KnowledgeBaseID: l.ID, Slug: "concept/b", Title: "乙", PageType: "concept", Status: "published", Content: "old", Version: 1}}
	if err := s.db.Create(&pages).Error; err != nil {
		t.Fatal(err)
	}
	writer, err := Open(ctx, dbPath, rawPath)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	readStarted, writerUpdated := make(chan struct{}), make(chan struct{})
	var readOnce, updateOnce sync.Once
	var snapshotTx *sql.Tx
	var queryCount int
	mixedTransaction := false
	if err = s.db.Callback().Query().After("gorm:query").Register("audit-export:snapshot", func(db *gorm.DB) {
		actual, ok := db.Statement.ConnPool.(*sql.Tx)
		if snapshotTx == nil {
			snapshotTx = actual
		}
		if !ok || actual != snapshotTx {
			mixedTransaction = true
		}
		queryCount++
		if db.Statement.Table == "knowledge_libraries" {
			readOnce.Do(func() {
				close(readStarted)
				select {
				case <-writerUpdated:
				case <-ctx.Done():
					db.AddError(ctx.Err())
				}
			})
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer s.db.Callback().Query().Remove("audit-export:snapshot")
	done := make(chan error, 1)
	go func() {
		<-readStarted
		done <- writer.transaction(ctx, func(tx *gorm.DB) error {
			err := tx.Model(&types.WikiPage{}).Where("knowledge_base_id = ?", l.ID).Updates(map[string]any{"content": "new", "version": 2}).Error
			updateOnce.Do(func() { close(writerUpdated) })
			return err
		})
	}()
	bundle, err := s.ExportMarkdown(ctx, types.ExportInput{LibraryID: l.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Files) != 2 {
		t.Fatal("missing snapshot pages")
	}
	if mixedTransaction || queryCount < 3 {
		t.Fatalf("export did not share a real SQLite read transaction: mixed=%v queries=%d", mixedTransaction, queryCount)
	}
	for _, file := range bundle.Files {
		if file.Metadata.PageVersion != 1 || !strings.HasSuffix(file.Markdown, "\n---\nold") {
			t.Fatalf("mixed export snapshot: %+v", file)
		}
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	_ = s.db.Callback().Query().Remove("audit-export:snapshot")
	current, err := s.GetPage(ctx, l.ID, "a")
	if err != nil || current.Version != 2 || current.Content != "new" {
		t.Fatalf("concurrent write did not commit: %+v %v", current, err)
	}
}

func TestLintChecksEveryEvidenceIdentityAndPropagatesReadFailure(t *testing.T) {
	ctx := context.Background()
	s, _, _, l := auditStore(t)
	rec, err := s.ImportSource(ctx, types.ImportSourceInput{RequestID: "source", LibraryID: l.ID, Filename: "source.md", Bytes: []byte("原文")})
	if err != nil {
		t.Fatal(err)
	}
	source, err := s.GetSource(ctx, l.ID, rec.ResourceID)
	if err != nil {
		t.Fatal(err)
	}
	valid := types.SourceRef{SourceID: source.SourceID, SourceVersionID: source.ID, SHA256: source.SHA256}
	invalid := valid
	invalid.SHA256 = "corrupt"
	page := types.WikiPage{ID: "identity", KnowledgeBaseID: l.ID, Slug: "concept/identity", Title: "来源身份", PageType: "concept", Status: "published", Content: strings.Repeat("知", 50), Version: 1, Evidence: types.PageEvidence{Sources: []types.SourceRef{valid, invalid}}}
	if err = s.db.Create(&page).Error; err != nil {
		t.Fatal(err)
	}
	report, err := s.LintLibrary(ctx, l.ID)
	if err != nil {
		t.Fatal(err)
	}
	stale := 0
	for _, issue := range report.Issues {
		if issue.Type == "stale_ref" {
			stale++
		}
	}
	if stale != 1 {
		t.Fatalf("duplicate version masked corrupt evidence identity: %d", stale)
	}
	fault := errors.New("source query unavailable")
	if err = s.db.Callback().Query().After("gorm:query").Register("audit-export:source-error", func(db *gorm.DB) {
		if db.Statement.Table == "knowledge_source_versions" {
			db.AddError(fault)
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer s.db.Callback().Query().Remove("audit-export:source-error")
	report, err = s.LintLibrary(ctx, l.ID)
	if err == nil || !errors.Is(err, fault) || report != nil {
		t.Fatalf("query failure was treated as lint finding: %+v %v", report, err)
	}
}
