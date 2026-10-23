package store

import (
	"context"
	"time"

	types "github.com/JieWaZi/neux-knowledge/internal/domain"
	"gorm.io/gorm"
)

// EnqueueEmbeddingChunks persists pending rows when vector indexing is enabled for later asynchronous embedding.
func (s *Store) EnqueueEmbeddingChunks(ctx context.Context, in types.EnqueueEmbeddingChunksInput) error {
	if in.LibraryID == "" || in.SourceVersionID == "" || in.EmbeddingModelID == "" || len(in.ChunkIDs) == 0 {
		return fail(types.ErrInvalid, "embedding.enqueue", "library, source, model and chunks are required")
	}
	return s.transaction(ctx, func(tx *gorm.DB) error {
		now := time.Now().UnixMilli()
		for _, chunkID := range in.ChunkIDs {
			row := types.EmbeddingChunkIndex{ID: newID(), LibraryID: in.LibraryID, SourceVersionID: in.SourceVersionID, ChunkID: chunkID, EmbeddingModelID: in.EmbeddingModelID, State: "pending", CreatedAt: now, UpdatedAt: now}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// SaveDocumentChunks accepts actual chunks under the frozen task and forbids mixing model spaces within a library.
func (s *Store) SaveDocumentChunks(ctx context.Context, jobID, sourceID string, rows []types.EmbeddingChunkIndex) error {
	return s.transaction(ctx, func(tx *gorm.DB) error {
		job, lib, err := activeJob(tx, jobID)
		if err != nil {
			return err
		}
		if err = validateOrganizeSource(tx, job, sourceID); err != nil {
			return err
		}
		for _, row := range rows {
			if row.ChunkType == "child" && row.Fingerprint != "" {
				var mismatched int64
				if err = tx.Model(&types.EmbeddingChunkIndex{}).Where("library_id = ? AND state = ? AND (fingerprint <> ? OR embedding_model_id <> ? OR dimensions <> ?)", lib.ID, "indexed", row.Fingerprint, row.EmbeddingModelID, row.Dimensions).Limit(1).Count(&mismatched).Error; err != nil {
					return err
				}
				if mismatched > 0 {
					return fail(types.ErrVersionConflict, "index.write", "embedding endpoint or model changed; rebuild is required")
				}
				break
			}
		}
		if err = deleteDocumentChunks(tx, lib.ID, sourceID); err != nil {
			return err
		}
		for i := range rows {
			rows[i].LibraryID = lib.ID
			rows[i].SourceVersionID = sourceID
			rows[i].CreatedAt = time.Now().UnixMilli()
			rows[i].UpdatedAt = rows[i].CreatedAt
			if err = tx.Create(&rows[i]).Error; err != nil {
				return err
			}
			if rows[i].State == "parsed" && rows[i].ChunkType == "child" {
				if err = syncFTS5Insert(tx, rows[i]); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// CompleteDocumentChunks atomically writes actual vectors only after a complete model response while the task fence remains valid.
func (s *Store) CompleteDocumentChunks(ctx context.Context, jobID, sourceID string, rows []types.EmbeddingChunkIndex) error {
	return s.transaction(ctx, func(tx *gorm.DB) error {
		job, lib, err := activeJob(tx, jobID)
		if err != nil {
			return err
		}
		if err = validateOrganizeSource(tx, job, sourceID); err != nil {
			return err
		}
		for _, row := range rows {
			if row.ChunkType != "child" {
				continue
			}
			result := tx.Model(&types.EmbeddingChunkIndex{}).Where("id = ? AND library_id = ? AND source_version_id = ? AND fingerprint = ? AND state = ?", row.ID, lib.ID, sourceID, row.Fingerprint, "pending").Updates(map[string]any{"dimensions": row.Dimensions, "state": "indexed", "diagnostic": "", "updated_at": time.Now().UnixMilli()})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return fail(types.ErrVersionConflict, "index.complete", "chunk changed before embedding completed")
			}
			if err = writeDocumentVector(tx, row); err != nil {
				return err
			}
			if err = syncFTS5Insert(tx, row); err != nil {
				return err
			}
		}
		return nil
	})
}

// FailDocumentChunks persists failures; pending or failed entries never count as searchable vectors.
func (s *Store) FailDocumentChunks(ctx context.Context, libraryID, sourceID string) error {
	return s.transaction(ctx, func(tx *gorm.DB) error {
		return tx.Model(&types.EmbeddingChunkIndex{}).Where("library_id = ? AND source_version_id = ? AND state = ?", libraryID, sourceID, "pending").Updates(map[string]any{"state": "failed", "diagnostic": "embedding failed; retry the original job", "updated_at": time.Now().UnixMilli()}).Error
	})
}

// ListDocumentChunks returns actual source chunks, excluding permanently deleted sources.
func (s *Store) ListDocumentChunks(ctx context.Context, libraryID, sourceID string) ([]types.EmbeddingChunkIndex, error) {
	rows := []types.EmbeddingChunkIndex{}
	err := s.transaction(ctx, func(tx *gorm.DB) error {
		if _, e := getLibrary(tx, libraryID, true); e != nil {
			return e
		}
		if _, e := getSource(tx, libraryID, sourceID); e != nil {
			return e
		}
		return tx.Where("library_id = ? AND source_version_id = ?", libraryID, sourceID).Order("chunk_type ASC, sequence ASC").Find(&rows).Error
	})
	return rows, err
}

// SearchDocumentChunks 在库范围内分别召回关键词和向量，并按 Lite 的归一化 RRF 融合。
func (s *Store) SearchDocumentChunks(ctx context.Context, libraryID, query string, vector []float32, fingerprint string, limit int) ([]types.DocumentChunkHit, error) {
	return s.searchDocumentIndex(ctx, libraryID, query, vector, fingerprint, limit)
}

// GetDocumentChunkParents reads parent chunks only within the specified library for at most five actual hit identities.
func (s *Store) GetDocumentChunkParents(ctx context.Context, libraryID string, ids []string) ([]types.EmbeddingChunkIndex, error) {
	if len(ids) > 5 {
		return nil, &types.Error{Code: types.ErrInvalid, Operation: "index.parents", Detail: "parent lookup exceeds retrieval limit"}
	}
	rows := []types.EmbeddingChunkIndex{}
	err := s.transaction(ctx, func(tx *gorm.DB) error {
		if _, err := getLibrary(tx, libraryID, true); err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		return tx.Where("library_id = ? AND chunk_type = ? AND chunk_id IN ?", libraryID, "parent", ids).Find(&rows).Error
	})
	return rows, err
}
