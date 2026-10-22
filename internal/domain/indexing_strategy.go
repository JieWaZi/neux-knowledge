package types

import (
	"database/sql/driver"
	"encoding/json"
)

// IndexingStrategy controls the library's indexing and organization pipelines; each switch is independent.
type IndexingStrategy struct {
	// VectorEnabled enables semantic vector indexing and retrieval.
	VectorEnabled bool `json:"vectorEnabled" yaml:"vector_enabled"`
	// KeywordEnabled enables keyword retrieval.
	KeywordEnabled bool `json:"keywordEnabled" yaml:"keyword_enabled"`
	// WikiEnabled enables document Wiki organization.
	WikiEnabled bool `json:"wikiEnabled" yaml:"wiki_enabled"`
	// GraphEnabled enables knowledge graph extraction.
	GraphEnabled bool `json:"graphEnabled" yaml:"graph_enabled"`
}

// DefaultIndexingStrategy is the local core default: keywords enabled, vectors/Wiki/graph disabled.
func DefaultIndexingStrategy() IndexingStrategy {
	return IndexingStrategy{VectorEnabled: false, KeywordEnabled: true, WikiEnabled: false, GraphEnabled: false}
}

// DefaultGeneralLibraryIndexingStrategy is the local organization default for general libraries: Wiki and keywords enabled, vectors disabled.
func DefaultGeneralLibraryIndexingStrategy() IndexingStrategy {
	return IndexingStrategy{VectorEnabled: false, KeywordEnabled: true, WikiEnabled: true, GraphEnabled: false}
}

// NeedsEmbedding reports whether a local embedding model is required; the SQLite keyword path does not call embeddings.
// Upstream sends both vectors and keywords through the retrieval engine's BatchIndex, so its condition is vector or keyword.
func (s IndexingStrategy) NeedsEmbedding() bool {
	return s.VectorEnabled
}

// IsZero reports whether all pipelines are disabled.
func (s IndexingStrategy) IsZero() bool {
	return !s.VectorEnabled && !s.KeywordEnabled && !s.WikiEnabled && !s.GraphEnabled
}

// Value implements driver.Valuer.
func (s IndexingStrategy) Value() (driver.Value, error) { return json.Marshal(s) }

// Scan implements sql.Scanner; an empty column falls back to DefaultIndexingStrategy.
func (s *IndexingStrategy) Scan(value interface{}) error {
	if value == nil {
		*s = DefaultIndexingStrategy()
		return nil
	}
	b, ok := value.([]byte)
	if !ok {
		*s = DefaultIndexingStrategy()
		return nil
	}
	if err := json.Unmarshal(b, s); err != nil {
		*s = DefaultIndexingStrategy()
		return err
	}
	return nil
}

// HasAnyIndexing retains upstream validation requiring at least one enabled pipeline.
// The local core supports Wiki, vectors, and keywords; the unintegrated graph pipeline cannot satisfy this condition alone.
func (s IndexingStrategy) HasAnyIndexing() bool {
	return s.VectorEnabled || s.KeywordEnabled || s.WikiEnabled
}
