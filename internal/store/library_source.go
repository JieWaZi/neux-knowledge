package store

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/JieWaZi/neux-knowledge/internal/domain"
	"gorm.io/gorm"
)

// CreateLibrary creates an empty library without preset topics or sources.
func (s *Store) CreateLibrary(ctx context.Context, in types.CreateLibraryInput) (*types.Receipt, error) {
	if in.Kind == "" {
		in.Kind = "general"
	}
	return s.mutate(ctx, in.RequestID, "library.create", "", in, func(tx *gorm.DB) (*types.Receipt, error) {
		if strings.TrimSpace(in.Name) == "" || in.Kind != "general" || strings.TrimSpace(in.WikiLanguage) == "" {
			return nil, fail(types.ErrInvalid, "library.create", "name, kind and wikiLanguage are required")
		}
		chunking := in.ChunkingConfig
		if chunking == nil {
			defaults := types.DefaultChunkingConfig()
			chunking = &defaults
		}
		if err := types.ValidateChunkingConfig(chunking); err != nil {
			return nil, err
		}
		now := time.Now().UnixMilli()
		indexing := types.DefaultIndexingStrategy()
		if in.Kind == "general" {
			indexing = types.DefaultGeneralLibraryIndexingStrategy()
		}
		if in.IndexingStrategy != nil {
			indexing = *in.IndexingStrategy
			if !indexing.HasAnyIndexing() {
				return nil, fail(types.ErrInvalid, "library.indexing", "at least one supported indexing strategy must be enabled")
			}
		}
		if indexing.VectorEnabled && strings.TrimSpace(in.EmbeddingModelID) == "" {
			return nil, fail(types.ErrInvalid, "library.create", "vector indexing requires embeddingModelId")
		}
		l := types.Library{ChunkingConfig: chunking, ID: newID(), Name: strings.TrimSpace(in.Name), Kind: in.Kind, WikiLanguage: in.WikiLanguage, Purpose: in.Purpose, WikiConfig: in.WikiConfig, IndexingStrategy: indexing, EmbeddingModelID: strings.TrimSpace(in.EmbeddingModelID), Version: 1, StructureVersion: 1, PolicyVersion: 1, CreatedAt: now, UpdatedAt: now}
		if err := tx.Create(&l).Error; err != nil {
			return nil, err
		}
		name := "Unclassified"
		if strings.HasPrefix(strings.ToLower(l.WikiLanguage), "zh") {
			name = "未分类"
		}
		theme := types.WikiFolder{ID: newID(), KnowledgeBaseID: l.ID, SystemKey: "unclassified", Name: name, Path: name, Depth: 1, CreatedAt: time.Now(), UpdatedAt: time.Now()}
		if err := tx.Create(&theme).Error; err != nil {
			return nil, err
		}
		return &types.Receipt{LibraryID: l.ID, ResourceID: l.ID, Version: 1, State: "saved"}, nil
	})
}

// UpdateLibrary saves configuration and authorization generations with CAS; archiving preserves history.
func (s *Store) UpdateLibrary(ctx context.Context, in types.UpdateLibraryInput) (*types.Receipt, error) {
	return s.mutate(ctx, in.RequestID, "library.update", in.LibraryID, in, func(tx *gorm.DB) (*types.Receipt, error) {
		l, err := getLibrary(tx, in.LibraryID, false)
		if err != nil {
			return nil, err
		}
		if l.Version != in.ExpectedVersion {
			return nil, fail(types.ErrVersionConflict, "library.update", "library changed")
		}
		if strings.TrimSpace(in.Name) == "" || in.PolicyVersion < l.PolicyVersion {
			return nil, fail(types.ErrInvalid, "library.update", "invalid name or policy version")
		}
		indexing := l.IndexingStrategy
		if in.IndexingStrategy != nil {
			indexing = *in.IndexingStrategy
			if !indexing.HasAnyIndexing() {
				return nil, fail(types.ErrInvalid, "library.indexing", "at least one supported indexing strategy must be enabled")
			}
		}
		chunking := l.ChunkingConfig
		if in.ChunkingConfig != nil {
			chunking = in.ChunkingConfig
		}
		if err := types.ValidateChunkingConfig(chunking); err != nil {
			return nil, err
		}
		wikiConfig := l.WikiConfig
		if in.WikiConfig != nil {
			wikiConfig = in.WikiConfig
		}
		embeddingModelID := l.EmbeddingModelID
		if in.EmbeddingModelID != nil {
			embeddingModelID = strings.TrimSpace(*in.EmbeddingModelID)
			if embeddingModelID != l.EmbeddingModelID {
				if err := rejectEmbeddingModelChange(tx, l, embeddingModelID); err != nil {
					return nil, err
				}
			}
		}
		if indexing.VectorEnabled && embeddingModelID == "" {
			return nil, fail(types.ErrInvalid, "library.update", "vector indexing requires embeddingModelId")
		}
		updated := &types.Library{Name: strings.TrimSpace(in.Name), Purpose: in.Purpose, Archived: in.Archived, PolicyVersion: in.PolicyVersion, WikiConfig: wikiConfig, ChunkingConfig: chunking, IndexingStrategy: indexing, EmbeddingModelID: embeddingModelID, Version: l.Version + 1, UpdatedAt: time.Now().UnixMilli()}
		// Select update columns explicitly so zero values are saved and GORM uses the existing JSON serializer for configuration fields.
		r := tx.Model(&types.Library{}).Where("id = ? AND version = ?", l.ID, in.ExpectedVersion).
			Select("name", "purpose", "archived", "policy_version", "wiki_config", "chunking_config", "indexing_strategy", "embedding_model_id", "version", "updated_at").
			Updates(updated)
		if r.Error != nil {
			return nil, r.Error
		}
		if r.RowsAffected != 1 {
			return nil, fail(types.ErrVersionConflict, "library.update", "library changed")
		}
		// Reuse old values from the same transaction and record only fields that actually changed; the central receipt hook does not duplicate this event.
		changedFields := libraryChangedFields(l, updated)
		if err := recordKBActivity(tx, in.RequestID, "library.update", l.ID, types.ActivityActorFromContext(ctx), types.AuditActionKBUpdated, "knowledge_base", l.ID, types.AuditOutcomeSuccess, map[string]any{"changed_fields": changedFields}); err != nil {
			return nil, err
		}
		return &types.Receipt{ResourceID: l.ID, Version: l.Version + 1, State: "saved"}, nil
	})
}

// GetLibrary reads actual library configuration.
func (s *Store) GetLibrary(ctx context.Context, id string) (result *types.Library, err error) {
	err = s.transaction(ctx, func(tx *gorm.DB) error { var e error; result, e = getLibrary(tx, id, false); return e })
	return
}

// ListLibraries returns stable pagination and the complete total.
func (s *Store) ListLibraries(ctx context.Context, offset, limit int) (items []*types.Library, total int64, err error) {
	offset, limit = pagination(offset, limit)
	err = s.transaction(ctx, func(tx *gorm.DB) error {
		q := tx.Model(&types.Library{})
		if e := q.Count(&total).Error; e != nil {
			return e
		}
		return q.Order("created_at ASC, id ASC").Offset(offset).Limit(limit).Find(&items).Error
	})
	return
}

func validFilename(name string) bool {
	return name != "" && name != "." && name != ".." && filepath.IsLocal(name) && !strings.ContainsAny(name, "/\\\x00")
}

// saveRaw atomically saves the content-addressed object before registering it; interruption leaves at most an unreferenced object that can be reconciled.
func (s *Store) saveRaw(bytes []byte, temp string) (string, error) {
	if !validImportTemp(temp) {
		return "", fail(types.ErrStorage, "raw.save", "import temporary identity is invalid")
	}
	key := hash(bytes)
	existing, err := s.raw.Open(key)
	if err == nil {
		defer existing.Close()
		b, e := io.ReadAll(existing)
		if e != nil {
			return "", wrap(types.ErrStorage, "raw.read", e)
		}
		if hash(b) != key {
			return "", fail(types.ErrStorage, "raw.save", "existing object hash mismatch")
		}
		if e = s.raw.Remove(temp); e != nil && !errors.Is(e, os.ErrNotExist) {
			return "", wrap(types.ErrStorage, "raw.save", e)
		}
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", wrap(types.ErrStorage, "raw.open", err)
	}
	if err = s.raw.Remove(temp); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", wrap(types.ErrStorage, "raw.create", err)
	}
	f, err := s.raw.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", wrap(types.ErrStorage, "raw.create", err)
	}
	defer s.raw.Remove(temp)
	_, err = f.Write(bytes)
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return "", wrap(types.ErrStorage, "raw.write", err)
	}
	if err = s.raw.Rename(temp, key); err != nil {
		return "", wrap(types.ErrStorage, "raw.rename", err)
	}
	return key, nil
}

// ImportSource saves immutable original bytes; importing source files does not create an organization task.
func (s *Store) ImportSource(ctx context.Context, in types.ImportSourceInput) (*types.Receipt, error) {
	if !validFilename(in.Filename) {
		return nil, fail(types.ErrInvalid, "source.import", "filename must not contain a path")
	}
	if strings.TrimSpace(in.RequestID) == "" {
		return nil, fail(types.ErrInvalid, "source.import", "requestId is required")
	}
	release, err := s.lockBlobs(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	if err = s.blobBinding(ctx, false); err != nil {
		return nil, err
	}
	prior, intent, err := s.prepareImport(ctx, in)
	if err != nil || prior != nil {
		return prior, err
	}
	// File IO occurs outside the database transaction; object keys do not accept external input.
	key, err := s.saveRaw(in.Bytes, intent.TempKey)
	if err != nil {
		return nil, err
	}
	return s.mutate(ctx, in.RequestID, "source.import", in.LibraryID, in, func(tx *gorm.DB) (*types.Receipt, error) {
		library, err := getLibrary(tx, in.LibraryID, true)
		if err != nil {
			return nil, err
		}
		if in.ExpectedPolicyVersion > 0 && library.PolicyVersion != in.ExpectedPolicyVersion {
			return nil, fail(types.ErrScopeViolation, "source.import", "policy changed")
		}

		sourceID := in.SourceID
		version := int64(1)
		if sourceID == "" {
			if in.ExpectedVersion != 0 {
				return nil, fail(types.ErrVersionConflict, "source.import", "new source requires version zero")
			}
			sourceID = intent.SourceID
		} else {
			var head types.SourceHead
			if err := tx.Where("id = ?", sourceID).Take(&head).Error; err != nil {
				return nil, err
			}
			if head.LibraryID != in.LibraryID {
				return nil, fail(types.ErrScopeViolation, "source.import", "source belongs to another library")
			}
			if head.Version != in.ExpectedVersion {
				return nil, fail(types.ErrVersionConflict, "source.import", "source changed")
			}
			version = head.Version + 1
		}
		v := types.SourceVersion{ID: newID(), LibraryID: in.LibraryID, SourceID: sourceID, Version: version, RowVersion: 1, Filename: in.Filename, MIME: in.MIME, SHA256: key, Size: int64(len(in.Bytes)), RawKey: key, State: "pending", CreatedAt: time.Now().UnixMilli()}
		if err := tx.Create(&v).Error; err != nil {
			return nil, err
		}
		if version == 1 {
			if err := tx.Create(&types.SourceHead{ID: sourceID, LibraryID: in.LibraryID, VersionID: v.ID, Version: version}).Error; err != nil {
				return nil, err
			}
		} else {
			r := tx.Model(&types.SourceHead{}).Where("id = ? AND library_id = ? AND version = ?", sourceID, in.LibraryID, in.ExpectedVersion).Updates(map[string]any{"version_id": v.ID, "version": version})
			if r.Error != nil {
				return nil, r.Error
			}
			if r.RowsAffected != 1 {
				return nil, fail(types.ErrVersionConflict, "source.import", "source changed")
			}
		}
		if err := tx.Where("request_id = ?", in.RequestID).Delete(&types.ImportBlobIntent{}).Error; err != nil {
			return nil, err
		}
		return &types.Receipt{ResourceID: v.ID, Version: version, State: "saved"}, nil
	})
}
func getSource(tx *gorm.DB, libraryID, versionID string) (*types.SourceVersion, error) {
	var v types.SourceVersion
	if err := tx.Where("id = ? AND library_id = ?", versionID, libraryID).Take(&v).Error; err != nil {
		return nil, err
	}
	return &v, nil
}

// ArchiveSource preserves source files and Wiki content, changing only archive state.
func (s *Store) ArchiveSource(ctx context.Context, in types.ArchiveSourceInput) (*types.Receipt, error) {
	return s.mutate(ctx, in.RequestID, "source.archive", in.LibraryID, in, func(tx *gorm.DB) (*types.Receipt, error) {
		v, err := getSource(tx, in.LibraryID, in.SourceVersionID)
		if err != nil {
			return nil, err
		}
		if v.RowVersion != in.ExpectedVersion {
			return nil, fail(types.ErrVersionConflict, "source.archive", "source state changed")
		}
		r := tx.Model(&types.SourceVersion{}).Where("id = ? AND row_version = ?", v.ID, in.ExpectedVersion).Updates(map[string]any{"archived": in.Archived, "row_version": v.RowVersion + 1})
		if r.Error != nil {
			return nil, r.Error
		}
		if r.RowsAffected != 1 {
			return nil, fail(types.ErrVersionConflict, "source.archive", "source changed")
		}
		return &types.Receipt{ResourceID: v.ID, Version: v.RowVersion + 1, State: "saved"}, nil
	})
}

// GetSource reads only the immutable source version in the specified library.
func (s *Store) GetSource(ctx context.Context, libraryID, versionID string) (result *types.SourceVersion, err error) {
	err = s.transaction(ctx, func(tx *gorm.DB) error { var e error; result, e = getSource(tx, libraryID, versionID); return e })
	return
}

// ListSources returns a page of source versions and the complete total.
func (s *Store) ListSources(ctx context.Context, libraryID string, offset, limit int) (items []*types.SourceVersion, total int64, err error) {
	offset, limit = pagination(offset, limit)
	err = s.transaction(ctx, func(tx *gorm.DB) error {
		if _, e := getLibrary(tx, libraryID, false); e != nil {
			return e
		}
		q := tx.Model(&types.SourceVersion{}).Where("library_id = ?", libraryID)
		if e := q.Count(&total).Error; e != nil {
			return e
		}
		return q.Order("created_at ASC, id ASC").Offset(offset).Limit(limit).Find(&items).Error
	})
	return
}

// ReadRaw returns the complete original bytes after verifying their digest and length.
func (s *Store) ReadRaw(ctx context.Context, libraryID, versionID string) ([]byte, error) {
	release, err := s.lockBlobs(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	if err = s.blobBinding(ctx, false); err != nil {
		return nil, err
	}
	v, err := s.GetSource(ctx, libraryID, versionID)
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if len(v.RawKey) != 64 || v.RawKey != v.SHA256 {
		return nil, fail(types.ErrStorage, "raw.read", "invalid managed object key")
	}
	f, err := s.raw.Open(v.RawKey)
	if err != nil {
		return nil, wrap(types.ErrStorage, "raw.read", err)
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil {
		return nil, wrap(types.ErrStorage, "raw.read", err)
	}
	if int64(len(b)) != v.Size || hash(b) != v.SHA256 {
		return nil, fail(types.ErrStorage, "raw.read", "raw bytes failed integrity validation")
	}
	return b, nil
}

// GetReceipt reconciles a lost response using the original requestId.
func (s *Store) GetReceipt(ctx context.Context, id string) (result *types.Receipt, err error) {
	err = s.transaction(ctx, func(tx *gorm.DB) error {
		result = &types.Receipt{}
		return tx.Where("request_id = ?", id).Take(result).Error
	})
	return
}

// rejectEmbeddingModelChange guards against changing the embedding model of a nonempty library.
// Local source heads represent original files and existing Wiki pages represent persisted content; use only bounded single-table existence checks.
// Initial model configuration is allowed; the host's existing embedding configuration entry point checks the model type.
func rejectEmbeddingModelChange(tx *gorm.DB, library *types.Library, next string) error {
	if next == "" {
		return fail(types.ErrInvalid, "library.update", "embedding_model_id cannot be empty")
	}
	if library.EmbeddingModelID == "" {
		return nil
	}
	var source []types.SourceHead
	if err := tx.Select("id").Where("library_id = ?", library.ID).Limit(1).Find(&source).Error; err != nil {
		return err
	}
	if len(source) > 0 {
		return fail(types.ErrInvalid, "library.update", "embedding model cannot change while library contains files")
	}
	var pages []types.WikiPage
	if err := tx.Select("id").Where("knowledge_base_id = ?", library.ID).Limit(1).Find(&pages).Error; err != nil {
		return err
	}
	if len(pages) > 0 {
		return fail(types.ErrInvalid, "library.update", "embedding model cannot change while library contains wiki content")
	}
	return nil
}

// libraryChangedFields creates a field-change list from values actually saved without recording field contents.
func libraryChangedFields(before, after *types.Library) []string {
	fields := []string{}
	if before.Name != after.Name {
		fields = append(fields, "name")
	}
	if before.Purpose != after.Purpose {
		fields = append(fields, "purpose")
	}
	if before.Archived != after.Archived {
		fields = append(fields, "archived")
	}
	if before.PolicyVersion != after.PolicyVersion {
		fields = append(fields, "policy_version")
	}
	if digest(before.WikiConfig) != digest(after.WikiConfig) {
		fields = append(fields, "wiki_config")
	}
	if digest(before.ChunkingConfig) != digest(after.ChunkingConfig) {
		fields = append(fields, "chunking_config")
	}
	if before.IndexingStrategy != after.IndexingStrategy {
		fields = append(fields, "indexing_strategy")
	}
	if before.EmbeddingModelID != after.EmbeddingModelID {
		fields = append(fields, "embedding_model_id")
	}
	return fields
}
