package types

// EmbeddingChunkIndex stores upstream chunk text, parent-child relationships, and actual vector indexing state.
type EmbeddingChunkIndex struct {
	Content          string        `json:"content"`                        // Actual chunk content, rather than a page summary.
	ContextHeader    string        `json:"contextHeader,omitempty"`        // Heading path context of the upstream chunk.
	ParentID         string        `json:"parentId,omitempty"`             // Persistent parent chunk identifier for a child chunk.
	ChunkType        string        `json:"chunkType"`                      // parent or child; parent chunks are not embedded separately.
	Start            int           `json:"start"`                          // Upstream-confirmed starting rune offset in the original text.
	End              int           `json:"end"`                            // Upstream-confirmed ending rune offset in the original text.
	Sequence         int           `json:"sequence"`                       // Upstream order among sibling chunks.
	Locator          SourceLocator `json:"locator" gorm:"serializer:json"` // Source location confirmed by the reader.
	Fingerprint      string        `json:"-"`                              // Normalized service address and model fingerprint, without credentials.
	Dimensions       int           `json:"dimensions"`                     // Validated vector dimensions.
	Vector           []float32     `json:"-" gorm:"serializer:json"`       // Actual vector; only the indexed state is searchable.
	ID               string        `json:"id" gorm:"primaryKey"`           // Stable index row identifier.
	LibraryID        string        `json:"libraryId" gorm:"index"`         // Owning library.
	SourceVersionID  string        `json:"sourceVersionId" gorm:"index"`   // Immutable source version.
	ChunkID          string        `json:"chunkId" gorm:"index"`           // Reader chunk identifier.
	EmbeddingModelID string        `json:"embeddingModelId"`               // Expected embedding model identifier.
	State            string        `json:"state"`                          // pending, indexed, or failed.
	Diagnostic       string        `json:"diagnostic"`                     // Most recent processing note.
	CreatedAt        int64         `json:"createdAt"`                      // Registration time in Unix milliseconds.
	UpdatedAt        int64         `json:"updatedAt"`                      // Update time in Unix milliseconds.
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
	Chunk EmbeddingChunkIndex `json:"chunk"` // Actual persistent original chunk from the same version.
	Score float64             `json:"score"` // Vector similarity or keyword score.
}
