package store

import (
	"context"
	"fmt"
	types "github.com/JieWaZi/neux-knowledge/internal/domain"
	"path/filepath"
	"testing"
)

// BenchmarkDocumentRetrieval 验证库范围候选检索；造数不计时，不代表真实模型或冷磁盘表现。
func BenchmarkDocumentRetrieval(b *testing.B) {
	for _, size := range []int{10000, 100000, 1000000} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			ctx := context.Background()
			dir := b.TempDir()
			s, err := Open(ctx, filepath.Join(dir, "bench.db"), filepath.Join(dir, "raw"))
			if err != nil {
				b.Fatal(err)
			}
			defer s.Close()
			receipt, err := s.CreateLibrary(ctx, types.CreateLibraryInput{RequestID: "bench", Name: "bench", Kind: "general", WikiLanguage: "zh-CN"})
			if err != nil {
				b.Fatal(err)
			}
			vector := make([]float32, 64)
			vector[0] = 1
			sql := `WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<?) INSERT INTO knowledge_embedding_chunks(id,library_id,source_version_id,chunk_id,chunk_type,state,content,fingerprint,dimensions) SELECT printf('row-%09d',x),CASE WHEN x%10=0 THEN ? ELSE 'other' END,'source',printf('row-%09d',x),'child','indexed',CASE WHEN x%1000=0 THEN 'rare 数据库 索引' ELSE 'ordinary 文档 内容' END,'space',64 FROM n`
			if err = s.db.Exec(sql, size, receipt.ResourceID).Error; err != nil {
				b.Fatal(err)
			}
			if err = s.db.Exec(`INSERT INTO knowledge_retrieval_fts(rowid,content,source_id,chunk_id,knowledge_id,knowledge_base_id) SELECT rowid,CASE WHEN content LIKE 'rare%' THEN ? ELSE ? END,chunk_id,chunk_id,source_version_id,library_id FROM knowledge_embedding_chunks`, tokenizeCJKBigram("rare 数据库 索引"), tokenizeCJKBigram("ordinary 文档 内容")).Error; err != nil {
				b.Fatal(err)
			}
			if err = s.db.Exec(`CREATE VIRTUAL TABLE knowledge_retrieval_vec_64 USING vec0(embedding float[64] distance_metric=cosine)`).Error; err != nil {
				b.Fatal(err)
			}
			if err = s.db.Exec(`INSERT INTO knowledge_retrieval_vec_64(rowid,embedding) SELECT rowid,? FROM knowledge_embedding_chunks`, vectorBytes(vector)).Error; err != nil {
				b.Fatal(err)
			}
			var plan []struct{ Detail string } // 真实 SQLite 查询规划。
			if err = s.db.Raw("EXPLAIN QUERY PLAN SELECT c.id,-1000000.0*bm25(knowledge_retrieval_fts) AS score FROM knowledge_retrieval_fts JOIN knowledge_embedding_chunks c ON c.rowid=knowledge_retrieval_fts.rowid WHERE knowledge_retrieval_fts MATCH ? AND c.library_id=? ORDER BY score DESC,c.id LIMIT ?", sanitizeFTS5Query("rare"), receipt.ResourceID, 5).Scan(&plan).Error; err != nil {
				b.Fatal(err)
			}
			for _, row := range plan {
				b.Log(row.Detail)
			}
			for _, mode := range []string{"keyword", "vector", "hybrid"} {
				b.Run(mode, func(b *testing.B) {
					var v []float32
					query := "rare"
					if mode != "keyword" {
						v = vector
					}
					if mode == "vector" {
						query = ""
					}
					b.ReportAllocs()
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						hits, err := s.SearchDocumentChunks(ctx, receipt.ResourceID, query, v, "space", 5)
						if err != nil || len(hits) != 5 {
							b.Fatalf("hits=%d %v", len(hits), err)
						}
					}
				})
			}
		})
	}
}
