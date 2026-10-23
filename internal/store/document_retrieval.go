package store

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode"

	types "github.com/JieWaZi/neux-knowledge/internal/domain"
	"gorm.io/gorm"
)

// createDocumentRetrieval 随全新业务结构创建索引，不迁移或回填旧数据库。
func createDocumentRetrieval(tx *gorm.DB) error {
	statements := []string{
		`CREATE VIRTUAL TABLE knowledge_retrieval_fts USING fts5(content,source_id,chunk_id,knowledge_id,knowledge_base_id, content='', contentless_delete=1, tokenize='unicode61')`,
		`CREATE INDEX idx_knowledge_retrieval_scope ON knowledge_embedding_chunks(library_id,chunk_type,state,fingerprint,dimensions)`,
	}
	for _, stmt := range statements {
		if err := tx.Exec(stmt).Error; err != nil {
			return err
		}
	}
	return nil
}

// validateDocumentRetrieval 拒绝缺失索引，不能将损坏状态当作无搜索结果。
func validateDocumentRetrieval(tx *gorm.DB) error {
	for _, name := range []string{"knowledge_retrieval_fts", "idx_knowledge_retrieval_scope"} {
		var count int64
		if err := tx.Raw("SELECT COUNT(*) FROM sqlite_master WHERE name=?", name).Scan(&count).Error; err != nil {
			return err
		}
		if count != 1 {
			return fail(types.ErrDatabase, "schema.validate", "document retrieval schema is missing")
		}
	}
	var dims []int
	if err := tx.Model(&types.EmbeddingChunkIndex{}).Where("chunk_type='child' AND state='indexed'").Distinct("dimensions").Pluck("dimensions", &dims).Error; err != nil {
		return err
	}
	for _, dim := range dims {
		var ddl string
		if err := tx.Raw("SELECT sql FROM sqlite_master WHERE type='table' AND name=?", documentVectorTable(dim)).Scan(&ddl).Error; err != nil {
			return err
		}
		if !strings.Contains(ddl, "USING vec0(") {
			return fail(types.ErrDatabase, "schema.validate", "document vector schema is missing")
		}
	}
	return nil
}

// documentVectorTable 按实际维度命名，维度只来自已校验的向量长度。
func documentVectorTable(dim int) string { return fmt.Sprintf("knowledge_retrieval_vec_%d", dim) }

func vectorBytes(v []float32) []byte {
	b := make([]byte, len(v)*4)
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(x))
	}
	return b
}

// writeDocumentVector 与分块状态在同一事务提交；删除由同事务清理函数覆盖来源及整库路径。
func writeDocumentVector(tx *gorm.DB, row types.EmbeddingChunkIndex) error {
	if row.ChunkType != "child" {
		return nil
	}
	if row.Dimensions < 1 || row.Dimensions != len(row.Vector) {
		return fail(types.ErrInvalid, "index.vector", "invalid vector dimensions")
	}
	for _, x := range row.Vector {
		if math.IsNaN(float64(x)) || math.IsInf(float64(x), 0) {
			return fail(types.ErrInvalid, "index.vector", "nonfinite vector")
		}
	}
	table := documentVectorTable(row.Dimensions)
	if err := tx.Exec(fmt.Sprintf(`CREATE VIRTUAL TABLE IF NOT EXISTS %s USING vec0(embedding float[%d] distance_metric=cosine)`, table, row.Dimensions)).Error; err != nil {
		return err
	}

	return tx.Exec(fmt.Sprintf(`INSERT INTO %s(rowid,embedding) SELECT rowid,? FROM knowledge_embedding_chunks WHERE id=?`, table), vectorBytes(row.Vector), row.ID).Error
}

// rankedDocument 仅返回候选身份与得分，最后有界读取正文，不扫描 JSON 向量。
type rankedDocument struct {
	ID        string          // 稳定分块身份。
	Score     float64         // 单路相关性或归一化融合得分。
	MatchType types.MatchType // 向量为 0，关键词为 1。
}

func (s *Store) searchDocumentIndex(ctx context.Context, lib, query string, vector []float32, fp string, limit int) ([]types.DocumentChunkHit, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	var result []types.DocumentChunkHit
	err := s.transaction(ctx, func(tx *gorm.DB) error {
		result = nil
		if _, err := getLibrary(tx, lib, true); err != nil {
			return err
		}
		var words, semantic []rankedDocument
		pattern := sanitizeFTS5Query(query)
		if pattern != "" {
			sql := `SELECT c.id,? AS match_type, -1000000.0*bm25(knowledge_retrieval_fts) AS score FROM knowledge_retrieval_fts JOIN knowledge_embedding_chunks c ON c.rowid=knowledge_retrieval_fts.rowid WHERE knowledge_retrieval_fts MATCH ? AND c.library_id=? ORDER BY score DESC,c.id LIMIT ?`
			if err := tx.Raw(sql, types.MatchTypeKeywords, pattern, lib, limit).Scan(&words).Error; err != nil {
				return err
			}
		}
		if len(vector) > 0 {
			for _, x := range vector {
				if math.IsNaN(float64(x)) || math.IsInf(float64(x), 0) {
					return fail(types.ErrInvalid, "index.search", "nonfinite query vector")
				}
			}
			var incompatible int64
			if err := tx.Model(&types.EmbeddingChunkIndex{}).Where("library_id=? AND chunk_type='child' AND state='indexed' AND (fingerprint<>? OR dimensions<>?)", lib, fp, len(vector)).Count(&incompatible).Error; err != nil {
				return err
			}
			if incompatible > 0 {
				return fail(types.ErrVersionConflict, "index.search", "embedding endpoint, model or dimensions changed; rebuild is required")
			}
			table := documentVectorTable(len(vector))
			var exists int64
			if err := tx.Raw("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&exists).Error; err != nil {
				return err
			}
			if exists > 0 {
				sql := fmt.Sprintf(`SELECT c.id,1-v.distance AS score FROM %s v JOIN knowledge_embedding_chunks c ON c.rowid=v.rowid WHERE v.embedding MATCH ? AND k=? AND v.rowid IN (SELECT rowid FROM knowledge_embedding_chunks WHERE library_id=? AND chunk_type='child' AND state='indexed' AND fingerprint=? AND dimensions=?) ORDER BY v.distance,c.id`, table)
				if err := tx.Raw(sql, vectorBytes(vector), limit, lib, fp, len(vector)).Scan(&semantic).Error; err != nil {
					return err
				}
			}
		}
		ranked := fuseDocumentRanks(semantic, words)
		if len(ranked) > limit {
			ranked = ranked[:limit]
		}
		if len(ranked) == 0 {
			return nil
		}
		ids := make([]string, len(ranked))
		for i, row := range ranked {
			ids[i] = row.ID
		}
		var chunks []types.EmbeddingChunkIndex
		if err := tx.Where("library_id=? AND id IN ?", lib, ids).Find(&chunks).Error; err != nil {
			return err
		}
		byID := map[string]types.EmbeddingChunkIndex{}
		for _, row := range chunks {
			byID[row.ID] = row
		}
		for _, hit := range ranked {
			row, ok := byID[hit.ID]
			if !ok {
				return fail(types.ErrDatabase, "index.search", "ranked chunk is missing")
			}
			result = append(result, types.DocumentChunkHit{Chunk: row, Score: hit.Score, MatchType: hit.MatchType})
		}
		return nil
	})
	return result, err
}

// fuseDocumentRanks 沿 Lite 默认参数归一化：k=60，向量权重 0.7，关键词权重 0.3。
func fuseDocumentRanks(v, w []rankedDocument) []rankedDocument {
	if len(w) == 0 {
		return v
	}
	if len(v) == 0 {
		maxScore := 0.0
		for _, row := range w {
			if row.Score > maxScore {
				maxScore = row.Score
			}
		}
		if maxScore > 1 {
			for i := range w {
				w[i].Score /= maxScore
			}
		}
		return w
	}
	scores := map[string]float64{}
	kinds := map[string]types.MatchType{}
	for _, row := range w {
		kinds[row.ID] = types.MatchTypeKeywords
	}
	for _, row := range v {
		kinds[row.ID] = types.MatchTypeEmbedding
	}
	for i, row := range v {
		scores[row.ID] += 0.7 / float64(61+i)
	}
	for i, row := range w {
		scores[row.ID] += 0.3 / float64(61+i)
	}
	rows := make([]rankedDocument, 0, len(scores))
	for id, score := range scores {
		rows = append(rows, rankedDocument{ID: id, Score: score * 61, MatchType: kinds[id]})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Score == rows[j].Score {
			return rows[i].ID < rows[j].ID
		}
		return rows[i].Score > rows[j].Score
	})
	return rows
}

// tokenizeCJKBigram 将连续汉字切为重叠双字词，保留非汉字词语。
func tokenizeCJKBigram(text string) string {
	var parts []string
	var currentCJK []rune
	var currentNonCJK strings.Builder

	flushCJK := func() {
		if len(currentCJK) == 0 {
			return
		}
		if len(currentCJK) == 1 {
			parts = append(parts, string(currentCJK[0]))
		} else {
			for i := 0; i < len(currentCJK)-1; i++ {
				parts = append(parts, string(currentCJK[i])+string(currentCJK[i+1]))
			}
		}
		currentCJK = currentCJK[:0]
	}

	flushNonCJK := func() {
		if currentNonCJK.Len() > 0 {
			parts = append(parts, currentNonCJK.String())
			currentNonCJK.Reset()
		}
	}

	for _, r := range text {
		// unicode.Han covers Chinese characters
		if unicode.Is(unicode.Han, r) {
			flushNonCJK()
			currentCJK = append(currentCJK, r)
		} else if unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) {
			// Delimiters
			flushCJK()
			flushNonCJK()
		} else {
			// Alphanumeric or other languages
			flushCJK()
			currentNonCJK.WriteRune(r)
		}
	}
	flushCJK()
	flushNonCJK()

	return strings.Join(parts, " ")
}

// sanitizeFTS5Query 将用户文本转为安全的多词 OR 查询，只匹配正文。
func sanitizeFTS5Query(q string) string {
	q = strings.TrimSpace(q)
	if q == "" {
		return q
	}

	// Tokenize input query with bigrams (same as during indexing)
	tokenized := tokenizeCJKBigram(q)
	fields := strings.Fields(tokenized)

	var parts []string
	for _, f := range fields {
		if f == "" {
			continue
		}
		term := `"` + strings.ReplaceAll(f, `"`, `""`) + `"`
		// The index holds CJK text as bigrams, so a lone Han character
		// (the 股 of "A股") was never a token; match bigrams starting with it.
		if runes := []rune(f); len(runes) == 1 && unicode.Is(unicode.Han, runes[0]) {
			term += "*"
		}
		parts = append(parts, term)
	}

	if len(parts) == 0 {
		return ""
	}

	// Use OR because we want fuzzy match across multiple bigrams/words.
	// Only the content column is searched: the FTS table also indexes
	// source, chunk, knowledge and KB IDs, so a query for "2023" could match
	// a UUID fragment.
	return "content : (" + strings.Join(parts, " OR ") + ")"
}

// deleteDocumentChunks 在权威行删除前有界清理对应向量；失败回滚整个业务事务。
func deleteDocumentChunks(tx *gorm.DB, libraryID, sourceID string) error {
	scope := tx.Model(&types.EmbeddingChunkIndex{}).Where("library_id=?", libraryID)
	if sourceID != "" {
		scope = scope.Where("source_version_id=?", sourceID)
	}
	var last int64
	for {
		var batch []struct {
			RowID      int64  // 当前待清理的 SQLite 行身份。
			Dimensions int    // 对应向量表维度。
			State      string // 决定是否存在已发布向量。
		}
		q := scope.Session(&gorm.Session{}).Select("rowid AS row_id,dimensions,state").Where("rowid>? AND chunk_type='child'", last).Order("rowid").Limit(200)
		if err := q.Find(&batch).Error; err != nil {
			return err
		}
		if len(batch) == 0 {
			break
		}
		for _, row := range batch {
			if row.State == "indexed" {
				if err := tx.Exec(fmt.Sprintf("DELETE FROM %s WHERE rowid=?", documentVectorTable(row.Dimensions)), row.RowID).Error; err != nil {
					return err
				}
			}
			if err := tx.Exec("DELETE FROM knowledge_retrieval_fts WHERE rowid=?", row.RowID).Error; err != nil {
				return err
			}
			last = row.RowID
		}
	}
	return scope.Delete(&types.EmbeddingChunkIndex{}).Error
}

// syncFTS5Insert 沿事务内显式同步模式写入关键词索引，不引入触发器或专用 SQL 分词函数。
func syncFTS5Insert(tx *gorm.DB, row types.EmbeddingChunkIndex) error {
	if row.ChunkType != "child" {
		return nil
	}
	return tx.Exec(`INSERT INTO knowledge_retrieval_fts(rowid,content,source_id,chunk_id,knowledge_id,knowledge_base_id) SELECT rowid,?,chunk_id,chunk_id,source_version_id,library_id FROM knowledge_embedding_chunks WHERE id=?`, tokenizeCJKBigram(row.Content), row.ID).Error
}
