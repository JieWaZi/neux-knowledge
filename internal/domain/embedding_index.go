package types

// EmbeddingChunkIndex stores upstream chunk text, parent-child relationships, and actual vector indexing state.
type EmbeddingChunkIndex struct {
	Content          string        `json:"content"`                        // 实际分块正文。
	ContextHeader    string        `json:"contextHeader,omitempty"`        // 分块所属的标题路径。
	ParentID         string        `json:"parentId,omitempty"`             // 子块对应的持久父块标识。
	ChunkType        string        `json:"chunkType"`                      // 父块或子块；只为子块生成向量。
	Start            int           `json:"start"`                          // 原文中的起始字符偏移。
	End              int           `json:"end"`                            // 原文中的结束字符偏移。
	Sequence         int           `json:"sequence"`                       // 同级分块顺序。
	Locator          SourceLocator `json:"locator" gorm:"serializer:json"` // 阅读器确认的来源位置。
	Fingerprint      string        `json:"-"`                              // 服务地址及模型指纹，不包含凭据。
	Dimensions       int           `json:"dimensions"`                     // 已校验的向量维度。
	Vector           []float32     `json:"-" gorm:"-"`                     // 仅承载本次索引输入；持久向量只保存在 vec0 虚表。
	ID               string        `json:"id" gorm:"primaryKey"`           // 稳定索引行标识。
	LibraryID        string        `json:"libraryId" gorm:"index"`         // 所属知识库标识。
	SourceVersionID  string        `json:"sourceVersionId" gorm:"index"`   // 不可变来源版本标识。
	ChunkID          string        `json:"chunkId" gorm:"index"`           // 持久分块标识。
	EmbeddingModelID string        `json:"embeddingModelId"`               // 所用嵌入模型标识。
	State            string        `json:"state"`                          // 解析、等待向量、已索引或失败状态。
	Diagnostic       string        `json:"diagnostic"`                     // 最近一次处理诊断。
	CreatedAt        int64         `json:"createdAt"`                      // 登记时间，单位为 Unix 毫秒。
	UpdatedAt        int64         `json:"updatedAt"`                      // 更新时间，单位为 Unix 毫秒。
}

func (EmbeddingChunkIndex) TableName() string { return "knowledge_embedding_chunks" }

// EnqueueEmbeddingChunksInput registers chunks awaiting embedding; actual vectors are written in Phase 2.
type EnqueueEmbeddingChunksInput struct {
	LibraryID        string   // Owning library.
	SourceVersionID  string   // Immutable source version.
	ChunkIDs         []string // Reader chunk identifier.
	EmbeddingModelID string   // Expected embedding model.
}

// DocumentChunkHit stores retrieval rank and actual original chunks that can be traced back to their source.
type DocumentChunkHit struct {
	Chunk     EmbeddingChunkIndex `json:"chunk"`     // 同一版本的真实持久分块。
	Score     float64             `json:"score"`     // 向量、关键词或融合相关性得分。
	MatchType MatchType           `json:"matchType"` // 实际召回路径：0 为向量，1 为关键词；融合保留向量优先的来源。
}

// MatchType 表示实际召回算法，沿用上游的向量与关键词类型。
type MatchType int

const (
	// MatchTypeEmbedding 表示向量相似度召回。
	MatchTypeEmbedding MatchType = iota
	// MatchTypeKeywords 表示全文关键词召回。
	MatchTypeKeywords
)
