package store

import (
	"context"
	types "github.com/JieWaZi/neux-knowledge/internal/domain"
	"gorm.io/gorm"
	"strings"
	"testing"
)

// TestDocumentKeywordSearchUsesChineseTermsAndBM25 防止退回全文子串扫描或相同分数排序。
func TestDocumentKeywordSearchUsesChineseTermsAndBM25(t *testing.T) {
	ctx := context.Background()
	s, lib, src, job := sourceWorkFixture(t)
	rows := []types.EmbeddingChunkIndex{
		{ID: "z-focused", ChunkID: "z-focused", ChunkType: "child", State: "parsed", Content: "数据库 索引 索引"},
		{ID: "a-noisy", ChunkID: "a-noisy", ChunkType: "child", State: "parsed", Content: "数据库 管理 教程 介绍 备份 配置 事务 用户 权限 使用 说明"},
	}
	if err := s.SaveDocumentChunks(ctx, job.ID, src.ID, rows); err != nil {
		t.Fatal(err)
	}
	hits, err := s.SearchDocumentChunks(ctx, lib.ID, "数据库 索引", nil, "", 5)
	if err != nil || len(hits) != 2 || hits[0].Chunk.ID != "z-focused" || hits[0].Score <= hits[1].Score {
		t.Fatalf("BM25 多词结果=%+v err=%v", hits, err)
	}
	for _, q := range []string{`" OR *`, "!!!"} {
		hits, err = s.SearchDocumentChunks(ctx, lib.ID, q, nil, "", 5)
		if err != nil || len(hits) != 0 {
			t.Fatalf("标点被当成查询语法: %q %+v %v", q, hits, err)
		}
	}
}

// TestDocumentHybridSearchCombinesLexicalAndSemantic 防止开启向量后丢弃关键词召回。
func TestDocumentHybridSearchCombinesLexicalAndSemantic(t *testing.T) {
	ctx := context.Background()
	s, lib, src, job := sourceWorkFixture(t)
	rows := []types.EmbeddingChunkIndex{
		{ID: "lexical", ChunkID: "lexical", ChunkType: "child", State: "pending", Content: "特殊代号", EmbeddingModelID: "m", Fingerprint: "fp", Dimensions: 2, Vector: []float32{0, 1}},
		{ID: "semantic", ChunkID: "semantic", ChunkType: "child", State: "pending", Content: "其他含义", EmbeddingModelID: "m", Fingerprint: "fp", Dimensions: 2, Vector: []float32{1, 0}},
	}
	if err := s.SaveDocumentChunks(ctx, job.ID, src.ID, rows); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteDocumentChunks(ctx, job.ID, src.ID, rows); err != nil {
		t.Fatal(err)
	}
	hits, err := s.SearchDocumentChunks(ctx, lib.ID, "特殊代号", []float32{1, 0}, "fp", 2)
	if err != nil || len(hits) != 2 || hits[0].Chunk.ID != "lexical" {
		t.Fatalf("混合检索丢失关键词: %+v %v", hits, err)
	}
}

// TestDocumentVectorTopKStaysInLibrary 防止全局 Top-K 后过滤导致目标库漏召回。
func TestDocumentVectorTopKStaysInLibrary(t *testing.T) {
	ctx := context.Background()
	s, lib, src, job := sourceWorkFixture(t)
	rows := []types.EmbeddingChunkIndex{{ID: "target", ChunkID: "target", ChunkType: "child", State: "pending", Content: "目标", EmbeddingModelID: "m", Fingerprint: "fp", Dimensions: 2, Vector: []float32{0, 1}}}
	if err := s.SaveDocumentChunks(ctx, job.ID, src.ID, rows); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteDocumentChunks(ctx, job.ID, src.ID, rows); err != nil {
		t.Fatal(err)
	}
	// 无关库使用相同维度但不同模型空间，不能改变 A 的召回或触发 A 的模型冲突。
	foreign := types.EmbeddingChunkIndex{ID: "foreign", ChunkID: "foreign", LibraryID: "other", SourceVersionID: "outside", ChunkType: "child", State: "indexed", Content: "别库", Fingerprint: "other-space", Dimensions: 2, Vector: []float32{1, 0}}
	if err := s.db.Create(&foreign).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Transaction(func(tx *gorm.DB) error { return writeDocumentVector(tx, foreign) }); err != nil {
		t.Fatal(err)
	}
	hits, err := s.SearchDocumentChunks(ctx, lib.ID, "", []float32{1, 0}, "fp", 1)
	if err != nil || len(hits) != 1 || hits[0].Chunk.ID != "target" {
		t.Fatalf("库范围检索=%+v %v", hits, err)
	}
}

// TestDocumentIndexLifecycleKeepsIndexesAtomic 防止替换、重建及删除留下旧关键词或向量。
func TestDocumentIndexLifecycleKeepsIndexesAtomic(t *testing.T) {
	for _, action := range []string{"replace", "rebuild", "purge", "library", "reopen"} {
		t.Run(action, func(t *testing.T) {
			ctx := context.Background()
			s, lib, src, job := sourceWorkFixture(t)
			rows := []types.EmbeddingChunkIndex{{ID: "persisted", ChunkID: "persisted", ChunkType: "child", State: "pending", Content: "保密索引内容", Fingerprint: "fp", EmbeddingModelID: "m", Dimensions: 2, Vector: []float32{1, 0}}}
			if err := s.SaveDocumentChunks(ctx, job.ID, src.ID, rows); err != nil {
				t.Fatal(err)
			}
			if err := s.CompleteDocumentChunks(ctx, job.ID, src.ID, rows); err != nil {
				t.Fatal(err)
			}
			if action == "reopen" {
				path := strings.TrimSuffix(s.processorDir, ".processors")
				raw := s.raw.Name()
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
				var err error
				s, err = Open(ctx, path, raw)
				if err != nil {
					t.Fatal(err)
				}
				defer s.Close()
				hits, err := s.SearchDocumentChunks(ctx, lib.ID, "保密索引", []float32{1, 0}, "fp", 2)
				if err != nil || len(hits) != 1 {
					t.Fatalf("重开丢失检索: %+v %v", hits, err)
				}
				return
			}
			if action == "replace" {
				rows[0].Content = "替换后的新正文"
				if err := s.SaveDocumentChunks(ctx, job.ID, src.ID, rows); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := s.FailJob(ctx, types.FailJobInput{JobActionInput: types.JobActionInput{RequestID: "finish-before-delete", JobID: job.ID, ExpectedVersion: job.Version}, Diagnostic: "fixture"}); err != nil {
					t.Fatal(err)
				}
				switch action {
				case "purge":
					preview, err := s.PreviewSourcePurge(ctx, types.PurgePreviewInput{LibraryID: lib.ID, SourceID: src.SourceID})
					if err != nil {
						t.Fatal(err)
					}
					if _, err = s.ConfirmSourcePurge(ctx, types.ConfirmSourcePurgeInput{RequestID: "purge-index", LibraryID: lib.ID, SourceID: src.SourceID, PreviewDigest: preview.Digest, Confirmed: true}); err != nil {
						t.Fatal(err)
					}
				case "library":
					if _, err := s.DeleteLibrary(ctx, types.DeleteLibraryInput{RequestID: "delete-index", LibraryID: lib.ID, ExpectedVersion: lib.Version}); err != nil {
						t.Fatal(err)
					}
				case "rebuild":
					if _, err := s.AcceptJob(ctx, types.AcceptJobInput{RequestID: "rebuild-index", LibraryID: lib.ID, ExpectedVersion: lib.Version, StructureVersion: lib.StructureVersion, SourceVersionIDs: []string{src.ID}, Permission: types.PermissionSnapshot{PolicyVersion: lib.PolicyVersion, Operations: []string{"organize"}}, RulesVersion: "rules", ConfigDigest: "config", Rebuild: true}); err != nil {
						t.Fatal(err)
					}
				}
			}
			for _, table := range []string{"knowledge_retrieval_fts", "knowledge_retrieval_vec_2"} {
				var count int64
				if err := s.db.Raw("SELECT COUNT(*) FROM " + table).Scan(&count).Error; err != nil || count != 0 {
					t.Fatalf("%s 残留索引=%d %v", table, count, err)
				}
			}
		})
	}
}

// TestDocumentIndexFailureRollsBackVectorAndKeyword 防止向量写入失败仍发布可搜索状态。
func TestDocumentIndexFailureRollsBackVectorAndKeyword(t *testing.T) {
	ctx := context.Background()
	s, lib, src, job := sourceWorkFixture(t)
	rows := []types.EmbeddingChunkIndex{{ID: "broken", ChunkID: "broken", ChunkType: "child", State: "pending", Content: "不能发布", Fingerprint: "fp", Dimensions: 2, Vector: []float32{1}}}
	if err := s.SaveDocumentChunks(ctx, job.ID, src.ID, rows); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteDocumentChunks(ctx, job.ID, src.ID, rows); err == nil {
		t.Fatal("损坏向量被接受")
	}
	for _, v := range [][]float32{nil, {1, 0}} {
		hits, err := s.SearchDocumentChunks(ctx, lib.ID, "不能发布", v, "fp", 5)
		if err != nil || len(hits) != 0 {
			t.Fatalf("失败状态被发布: %+v %v", hits, err)
		}
	}
}
