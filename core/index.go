package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"

	types "github.com/JieWaZi/neux-knowledge/internal/domain"
)

// DocumentEmbedder is an actual embedding call frozen by the host from existing configuration; it stores no credential copy.
type DocumentEmbedder struct {
	PublishGuard func(context.Context) (func(), error)                // Recheck host authorization after external embedding and hold the lock through index commit.
	Fingerprint  string                                               // Fingerprint of the service endpoint and model space.
	Embed        func(context.Context, []string) ([][]float32, error) // Reuse the host's existing embedding model service.
}

// DocumentChunkHit exposes actual persisted chunks and retrieval scores.
type DocumentChunkHit = types.DocumentChunkHit

// MatchType 表示命中的实际召回算法。
type MatchType = types.MatchType

const (
	// MatchTypeEmbedding 表示向量召回。
	MatchTypeEmbedding = types.MatchTypeEmbedding
	// MatchTypeKeywords 表示关键词召回。
	MatchTypeKeywords = types.MatchTypeKeywords
)

// IndexedDocumentChunk exposes source chunk state without serializing credentials or vectors.
type IndexedDocumentChunk = types.EmbeddingChunkIndex

// ValidateDocumentEmbedding rejects empty, inconsistent-dimension, zero-norm, and non-finite vectors; request success alone does not establish vector validity.
func (e *Engine) ValidateDocumentEmbedding(ctx context.Context, embedder DocumentEmbedder, texts []string) error {
	_, err := embedDocumentTexts(ctx, embedder, texts)
	return err
}
func embedDocumentTexts(ctx context.Context, embedder DocumentEmbedder, texts []string) ([][]float32, error) {
	if embedder.Embed == nil || embedder.Fingerprint == "" {
		return nil, &Error{Code: ErrInvalid, Operation: "index.embed", Detail: "embedding service is not configured"}
	}
	// Preserve upstream batch sizing, ordered result assignment, and stopping on the first error.
	// The host supplies the model port; the SQLite adapter runs batches sequentially to avoid a separate ants worker pool.
	size := os.Getenv("BATCH_EMBED_SIZE")
	if size == "" {
		size = "5"
	}
	batchSize, err := strconv.Atoi(size)
	if err != nil || batchSize < 1 {
		return nil, &Error{Code: ErrInvalid, Operation: "index.embed", Detail: "invalid BATCH_EMBED_SIZE"}
	}
	vectors := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += batchSize {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		end := min(start+batchSize, len(texts))
		batch, err := embedder.Embed(ctx, texts[start:end])
		if err != nil {
			return nil, &Error{Code: ErrStorage, Operation: "index.embed", Detail: "embedding service failed", Cause: err}
		}
		if len(batch) != end-start {
			return nil, &Error{Code: ErrCandidateInvalid, Operation: "index.embed", Detail: "embedding count mismatch"}
		}
		vectors = append(vectors, batch...)
	}
	if len(vectors) != len(texts) || len(vectors) == 0 {
		return nil, &Error{Code: ErrCandidateInvalid, Operation: "index.embed", Detail: "embedding count mismatch"}
	}
	dimensions := len(vectors[0])
	if dimensions == 0 {
		return nil, &Error{Code: ErrCandidateInvalid, Operation: "index.embed", Detail: "empty embedding"}
	}
	for _, v := range vectors {
		norm := 0.0
		if len(v) != dimensions {
			return nil, &Error{Code: ErrCandidateInvalid, Operation: "index.embed", Detail: "embedding dimensions mismatch"}
		}
		for _, x := range v {
			if math.IsNaN(float64(x)) || math.IsInf(float64(x), 0) {
				return nil, &Error{Code: ErrCandidateInvalid, Operation: "index.embed", Detail: "embedding contains nonfinite values"}
			}
			norm += float64(x) * float64(x)
		}
		if norm == 0 {
			return nil, &Error{Code: ErrCandidateInvalid, Operation: "index.embed", Detail: "zero embedding"}
		}
	}
	return vectors, nil
}

// IndexDocument uses the upstream chunker and configuration frozen at acceptance, stores parent and child chunks, then performs actual embedding and an atomic ready-state commit.
func (e *Engine) IndexDocument(ctx context.Context, jobID, sourceID string, artifact ReadArtifact, embedder DocumentEmbedder) error {
	job, err := e.GetJob(ctx, jobID)
	if err != nil {
		return err
	}
	lib := job.LibrarySnapshot
	if lib == nil {
		lib, err = e.GetLibrary(ctx, job.LibraryID)
		if err != nil {
			return err
		}
	}
	if job.EmbeddingFingerprint != "" && lib.IndexingStrategy.VectorEnabled && job.EmbeddingFingerprint != embedder.Fingerprint {
		return &Error{Code: ErrVersionConflict, Operation: "index.embed", Detail: "embedding configuration changed after acceptance"}
	}
	if artifact.SourceVersionID != sourceID || artifact.Status != "complete" {
		return &Error{Code: ErrCandidateInvalid, Operation: "index.document", Detail: "complete verified source is required"}
	}
	var text strings.Builder
	type boundary struct {
		start, end int           // Coverage boundaries in source-text runes.
		locator    SourceLocator // Source location verified by the Reader.
	}
	ranges := []boundary{}
	for _, block := range artifact.Blocks {
		start := utf8.RuneCountInString(text.String())
		text.WriteString(block.Content)
		ranges = append(ranges, boundary{start, utf8.RuneCountInString(text.String()), block.Locator})
	}
	chunks, err := SplitDocument(text.String(), lib.ChunkingConfig)
	if err != nil {
		return &Error{Code: ErrInvalid, Operation: "index.chunk", Cause: err}
	}
	locate := func(start, end int) SourceLocator {
		var result SourceLocator
		found := 0
		for _, b := range ranges {
			if b.end > start && b.start < end {
				if found == 0 {
					result = b.locator
				} else if result.Kind == "line" && b.locator.Kind == "line" {
					result.EndLine = b.locator.EndLine
				}
				found++
			}
		}
		if found > 1 && result.Kind != "line" {
			return SourceLocator{Kind: "range", Section: fmt.Sprintf("runes:%d-%d", start, end)}
		}
		return result
	}
	identity := func(kind string, seq int) string {
		h := sha256.Sum256([]byte(sourceID + "/" + kind + fmt.Sprint(seq)))
		return hex.EncodeToString(h[:])
	}
	rows := []types.EmbeddingChunkIndex{}
	for _, chunk := range chunks.Parents {
		rows = append(rows, types.EmbeddingChunkIndex{ID: identity("parent", chunk.Seq), ChunkID: identity("parent", chunk.Seq), ChunkType: "parent", Content: chunk.Content, ContextHeader: chunk.ContextHeader, Start: chunk.Start, End: chunk.End, Sequence: chunk.Seq, Locator: locate(chunk.Start, chunk.End), State: "parsed"})
	}
	texts := []string{}
	indices := []int{}
	for _, chunk := range chunks.Children {
		parent := ""
		if chunk.ParentIndex >= 0 && chunk.ParentIndex < len(chunks.Parents) {
			parent = identity("parent", chunks.Parents[chunk.ParentIndex].Seq)
		}
		state := "parsed"
		fingerprint := ""
		if lib.IndexingStrategy.VectorEnabled {
			state = "pending"
			fingerprint = embedder.Fingerprint
			texts = append(texts, chunk.EmbeddingContent())
			indices = append(indices, len(rows))
		}
		rows = append(rows, types.EmbeddingChunkIndex{ID: identity("child", chunk.Seq), ChunkID: identity("child", chunk.Seq), ChunkType: "child", ParentID: parent, Content: chunk.Content, ContextHeader: chunk.ContextHeader, Start: chunk.Start, End: chunk.End, Sequence: chunk.Seq, Locator: locate(chunk.Start, chunk.End), EmbeddingModelID: lib.EmbeddingModelID, Fingerprint: fingerprint, State: state})
	}
	if len(rows) == 0 {
		return &Error{Code: ErrCandidateInvalid, Operation: "index.chunk", Detail: "source produced no chunks"}
	}
	if lib.IndexingStrategy.VectorEnabled && (embedder.Embed == nil || embedder.Fingerprint == "") {
		return &Error{Code: ErrInvalid, Operation: "index.embed", Detail: "enabled vector index requires a frozen embedding service"}
	}
	if lib.IndexingStrategy.VectorEnabled {
		vectors, err := embedDocumentTexts(ctx, embedder, texts)
		if err != nil {
			return err
		}
		for i, index := range indices {
			rows[index].Vector = vectors[i]
			rows[index].Dimensions = len(vectors[i])
		}
	}
	// External embedding runs without the authorization lock and rechecks afterward; revocation can finish immediately and late results must not be persisted.
	if embedder.PublishGuard != nil {
		release, err := embedder.PublishGuard(ctx)
		if err != nil {
			return err
		}
		if release == nil {
			return &Error{Code: ErrInvalid, Operation: "index.document", Detail: "publication guard has no release function"}
		}
		defer release()
	}
	if err = e.store.SaveDocumentChunks(ctx, jobID, sourceID, rows); err != nil {
		return err
	}
	if !lib.IndexingStrategy.VectorEnabled {
		return nil
	}
	if err = e.store.CompleteDocumentChunks(ctx, jobID, sourceID, rows); err != nil {
		return err
	}
	return nil
}

// ListDocumentChunks returns actual parent and child chunks from the same source version.
func (e *Engine) ListDocumentChunks(ctx context.Context, libraryID, sourceID string) ([]IndexedDocumentChunk, error) {
	return e.store.ListDocumentChunks(ctx, libraryID, sourceID)
}

// SearchDocumentChunks 在指定知识库内执行 FTS5、向量或混合检索，错误不降级为空结果。
func (e *Engine) SearchDocumentChunks(ctx context.Context, libraryID, query string, vector []float32, fingerprint string, limit int) ([]DocumentChunkHit, error) {
	return e.store.SearchDocumentChunks(ctx, libraryID, query, vector, fingerprint, limit)
}

// PrepareIndexedDocument supplies actual persisted child chunks for Wiki citations, avoiding a separate evidence chunking scheme.
func (e *Engine) PrepareIndexedDocument(ctx context.Context, jobID, sourceID string, artifact ReadArtifact, embedder DocumentEmbedder) (ReadArtifact, error) {
	if err := e.IndexDocument(ctx, jobID, sourceID, artifact, embedder); err != nil {
		return artifact, err
	}
	job, err := e.GetJob(ctx, jobID)
	if err != nil {
		return artifact, err
	}
	rows, err := e.ListDocumentChunks(ctx, job.LibraryID, sourceID)
	if err != nil {
		return artifact, err
	}
	artifact.Blocks = nil
	for _, row := range rows {
		if row.ChunkType == "child" {
			artifact.Blocks = append(artifact.Blocks, &Chunk{ID: row.ChunkID, ChunkIndex: row.Sequence, StartAt: row.Start, EndAt: row.End, Content: row.Content, ChunkType: "text", Locator: row.Locator})
		}
	}
	return artifact, nil
}

// GetDocumentChunkParents reads matched parents within bounds, avoiding decoding all child vectors for a few hits.
func (e *Engine) GetDocumentChunkParents(ctx context.Context, libraryID string, ids []string) ([]IndexedDocumentChunk, error) {
	return e.store.GetDocumentChunkParents(ctx, libraryID, ids)
}
