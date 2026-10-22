package core

import (
	"github.com/JieWaZi/neux-knowledge/internal/chunker"
	types "github.com/JieWaZi/neux-knowledge/internal/domain"
)

// ChunkingConfig preserves the complete upstream chunking configuration and explicitly rejects unsupported parser engines and table guidance.
type ChunkingConfig = types.ChunkingConfig

// ParserEngineRule preserves the upstream configuration contract for selecting parsers by file type.
type ParserEngineRule = types.ParserEngineRule

// DocumentChunk preserves upstream source text, context headers, sequence numbers, and source rune offsets.
type DocumentChunk = chunker.Chunk

// DocumentChildChunk preserves the association with the parent chunk array; ParentIndex -1 means no parent.
type DocumentChildChunk = chunker.ChildChunk

// DocumentChunks preserves upstream parent and child chunks; flat mode places all results in Children.
type DocumentChunks = chunker.ParentChildResult

// DefaultChunkingConfig returns the complete defaults used by the upstream creation form.
func DefaultChunkingConfig() ChunkingConfig { return types.DefaultChunkingConfig() }

// SplitDocument reuses the upstream algorithm to split text already supplied by the Reader.
// Offsets count runes in the input text; callers must store that same text and must not normalize newlines after locating it.
// A nil configuration uses new-library defaults; explicit configurations follow upstream NormalizeSplitterConfig semantics.
func SplitDocument(text string, cfg *ChunkingConfig) (DocumentChunks, error) {
	if cfg == nil {
		defaults := DefaultChunkingConfig()
		cfg = &defaults
	}
	if err := types.ValidateChunkingConfig(cfg); err != nil {
		return DocumentChunks{}, err
	}
	base := chunker.NormalizeSplitterConfig(chunker.SplitterConfig{
		ChunkSize: cfg.ChunkSize, ChunkOverlap: cfg.ChunkOverlap, Separators: cfg.Separators,
		Strategy: cfg.Strategy, TokenLimit: cfg.TokenLimit, Languages: cfg.Languages,
	})
	if cfg.EnableParentChild {
		parent, child := chunker.DeriveParentChildConfigs(base, cfg.ParentChunkSize, cfg.ChildChunkSize)
		return chunker.SplitParentChild(text, parent, child), nil
	}
	chunks := chunker.Split(text, base)
	out := DocumentChunks{}
	for _, chunk := range chunks {
		out.Children = append(out.Children, DocumentChildChunk{Chunk: chunk, ParentIndex: -1})
	}
	return out, nil
}
