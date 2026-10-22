package store

import (
	"context"
	"path/filepath"
	"testing"

	types "github.com/JieWaZi/neux-knowledge/internal/domain"
)

func TestUpdateLibraryPersistsWikiConfigAndIndexing(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(ctx, filepath.Join(dir, "knowledge.db"), filepath.Join(dir, "raw"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	rec, err := s.CreateLibrary(ctx, types.CreateLibraryInput{
		RequestID:    "lib-wiki-cfg",
		Name:         "通用库",
		Kind:         "general",
		WikiLanguage: "zh-CN",
	})
	if err != nil {
		t.Fatal(err)
	}
	lib, err := s.GetLibrary(ctx, rec.ResourceID)
	if err != nil {
		t.Fatal(err)
	}
	focused := types.WikiExtractionFocused
	_, err = s.UpdateLibrary(ctx, types.UpdateLibraryInput{
		RequestID:       "update-wiki-cfg",
		LibraryID:       lib.ID,
		ExpectedVersion: lib.Version,
		Name:            lib.Name,
		PolicyVersion:   lib.PolicyVersion,
		WikiConfig: &types.WikiConfig{
			ExtractionGranularity:  focused,
			ContentInstructions:    "法务审阅口吻",
			ExtractionInstructions: "产品、版本、组织",
		},
		IndexingStrategy: &types.IndexingStrategy{
			WikiEnabled:    true,
			VectorEnabled:  false,
			KeywordEnabled: true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	after, err := s.GetLibrary(ctx, lib.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.WikiConfig == nil || after.WikiConfig.ExtractionGranularity != focused {
		t.Fatalf("wiki config granularity = %v, want focused", after.WikiConfig)
	}
	if after.WikiConfig.ContentInstructions != "法务审阅口吻" {
		t.Fatalf("content instructions = %q", after.WikiConfig.ContentInstructions)
	}
	if !after.IndexingStrategy.WikiEnabled || after.IndexingStrategy.VectorEnabled {
		t.Fatalf("indexing = %+v", after.IndexingStrategy)
	}
}
