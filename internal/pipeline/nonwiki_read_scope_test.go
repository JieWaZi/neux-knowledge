package pipeline

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	types "github.com/JieWaZi/neux-knowledge/internal/domain"
	"github.com/JieWaZi/neux-knowledge/internal/store"
)

// TestNonWikiRunDoesNotReadWikiPages 用真实 SQLite 的不可读页面表验证普通资料整理和收尾没有 Wiki 正文消费者。
func TestNonWikiRunDoesNotReadWikiPages(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "core.db")
	s, err := store.Open(ctx, dbPath, filepath.Join(dir, "raw"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	rec, err := s.CreateLibrary(ctx, types.CreateLibraryInput{RequestID: "library", Name: "普通资料库", Kind: "general", WikiLanguage: "zh-CN", IndexingStrategy: &types.IndexingStrategy{KeywordEnabled: true}, WikiConfig: &types.WikiConfig{IngestBatchSize: 1}})
	if err != nil {
		t.Fatal(err)
	}
	library, err := s.GetLibrary(ctx, rec.ResourceID)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for i := 0; i < 2; i++ {
		rec, err = s.ImportSource(ctx, types.ImportSourceInput{RequestID: fmt.Sprintf("source-%d", i), LibraryID: library.ID, Filename: fmt.Sprintf("普通资料%d.md", i), Bytes: []byte(fmt.Sprintf("真实资料%d需要提取摘要和关键词索引，无需读取或生成知识页面。", i))})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, rec.ResourceID)
	}
	rec, err = s.AcceptJob(ctx, types.AcceptJobInput{RequestID: "accept", LibraryID: library.ID, ExpectedVersion: library.Version, StructureVersion: library.StructureVersion, SourceVersionIDs: ids, Permission: types.PermissionSnapshot{PolicyVersion: library.PolicyVersion, Operations: []string{"organize"}}, RulesVersion: "rules", ConfigDigest: "config"})
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// 页面表仅在测试副本中移除；任何页面读取都会由 SQLite 直接报错，覆盖空库也会发出查询的情况。
	if _, err = db.Exec("DROP TABLE wiki_pages"); err != nil {
		t.Fatal(err)
	}
	prepared := 0
	model := &contractModel{respond: func(r GenerateRequest) (GenerateResult, error) {
		if r.Stage != "document_summary" {
			return GenerateResult{}, fmt.Errorf("普通资料整理调用了 Wiki 阶段 %s", r.Stage)
		}
		return GenerateResult{Text: `{"summary":"普通资料摘要","profile":{"topics":["知识整理"]}}`, FinishReason: "stop"}, nil
	}}
	result, err := Run(ctx, s, Input{JobID: rec.JobID, Generator: model, PrepareDocument: func(_ context.Context, _ string, a ReadArtifact) (ReadArtifact, error) {
		prepared++
		return a, nil
	}})
	if err != nil {
		t.Fatalf("non-Wiki run read the unavailable Wiki table: %v", err)
	}
	if result.State != "completed" || prepared != len(ids) || len(model.calls) != len(ids) {
		t.Fatalf("ordinary document processing changed: state=%s prepared=%d calls=%d", result.State, prepared, len(model.calls))
	}
	for _, id := range ids {
		source, err := s.GetSource(ctx, library.ID, id)
		if err != nil || source.State != "processed" || source.SummaryStatus != "completed" || source.Description != "普通资料摘要" {
			t.Fatalf("source conclusion lost: source=%+v err=%v", source, err)
		}
	}
}
