package store

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	types "github.com/JieWaZi/neux-knowledge/internal/domain"
	"gorm.io/gorm"
)

func TestPageDirectoryProjectsOnlyNavigationFieldsAndBatches500(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	s, err := Open(ctx, filepath.Join(root, "knowledge.db"), filepath.Join(root, "raw"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	receipt, err := s.CreateLibrary(ctx, types.CreateLibraryInput{RequestID: "directory", Name: "目录", Kind: "general", WikiLanguage: "zh-CN"})
	if err != nil {
		t.Fatal(err)
	}
	lib := receipt.ResourceID
	now := time.Now()
	rows := make([]types.WikiPage, 0, 503)
	for i := range 500 {
		rows = append(rows, types.WikiPage{ID: fmt.Sprintf("p-%03d", i), KnowledgeBaseID: lib, Slug: fmt.Sprintf("concept/%03d", i), Title: fmt.Sprintf("页面 %03d", i), Summary: "目录简介", PageType: "concept", Status: "published", Content: strings.Repeat("大型正文", 2000), Version: 2, FolderID: "folder", CategoryPath: []string{"目录"}, SortOrder: i, UpdatedAt: now})
	}
	rows = append(rows, types.WikiPage{ID: "archived", KnowledgeBaseID: lib, Slug: "archived", Status: "archived", UpdatedAt: now}, types.WikiPage{ID: "deleted", KnowledgeBaseID: lib, Slug: "deleted", Status: "published", DeletedAt: gorm.DeletedAt{Time: now, Valid: true}}, types.WikiPage{ID: "foreign", KnowledgeBaseID: "another-library", Slug: "foreign", Status: "published"})
	if err = s.db.CreateInBatches(rows, 25).Error; err != nil {
		t.Fatal(err)
	}
	var statements []string
	if err = s.db.Callback().Query().After("gorm:query").Register("test:directory-projection", func(db *gorm.DB) {
		if db.Statement.Table == "wiki_pages" {
			statements = append(statements, db.Statement.SQL.String())
		}
	}); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	items, total, err := s.ListPageDirectory(ctx, lib, 0, 1000)
	directoryElapsed := time.Since(started)
	if err != nil || total != 500 || len(items) != 500 {
		t.Fatalf("items=%d total=%d err=%v", len(items), total, err)
	}
	if len(statements) != 2 {
		t.Fatalf("directory made %d page queries: %v", len(statements), statements)
	}
	for _, sql := range statements {
		if strings.Contains(sql, "SELECT *") || strings.Contains(sql, "`content`") || strings.Contains(sql, "`evidence`") || strings.Contains(sql, "source_refs") {
			t.Fatalf("heavy projection: %s", sql)
		}
	}
	if !strings.Contains(statements[1], "`title`") || !strings.Contains(statements[1], "`folder_id`") {
		t.Fatal(statements)
	}
	if items[0].ID != "p-000" || items[0].FolderID != "folder" || items[0].Summary != "目录简介" || len(items[0].CategoryPath) != 1 {
		t.Fatalf("metadata lost: %+v", items[0])
	}
	encoded, _ := json.Marshal(items)
	if len(encoded) > 200000 || strings.Contains(string(encoded), `"content"`) || strings.Contains(string(encoded), `"evidence"`) {
		t.Fatalf("heavy directory: %d bytes", len(encoded))
	}
	t.Logf("500 entries: %d bytes; SQL: %v", len(encoded), statements)
	started = time.Now()
	fullBytes := 0
	for offset := 0; offset < 500; offset += 100 {
		full, _, e := s.ListPages(ctx, types.PageQuery{LibraryID: lib, Offset: offset, Limit: 100})
		if e != nil {
			t.Fatal(e)
		}
		raw, _ := json.Marshal(full)
		fullBytes += len(raw)
	}
	t.Logf("fixture comparison: full=%d bytes / 5 reads / %s; directory=%d bytes / 1 read / %s", fullBytes, time.Since(started), len(encoded), directoryElapsed)
	counts, e := s.CountPagesByTheme(ctx, lib)
	if e != nil || counts["folder"] != 500 {
		t.Fatal("folder counts", counts, e)
	}
	next, count, err := s.ListPageDirectory(ctx, lib, 499, 1)
	if err != nil || count != 500 || len(next) != 1 || next[0].ID != "p-499" {
		t.Fatalf("pagination %+v %d %v", next, count, err)
	}
	empty, count, err := s.ListPageDirectory(ctx, lib, 500, 1000)
	if err != nil || count != 500 || len(empty) != 0 {
		t.Fatalf("tail %+v %d %v", empty, count, err)
	}
	page, err := s.GetPage(ctx, lib, "p-000")
	if err != nil || len(page.Content) != len(rows[0].Content) {
		t.Fatal("detail body lost", err)
	}
	if _, _, err = s.ListPageDirectory(ctx, "missing-library", 0, 1000); err == nil {
		t.Fatal("missing library accepted")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err = s.ListPageDirectory(cancelled, lib, 0, 1000); err == nil {
		t.Fatal("cancel ignored")
	}
}
