package store

import (
	"context"
	"path/filepath"
	"testing"

	types "github.com/JieWaZi/neux-knowledge/internal/domain"
)

func TestCurrentSchemaCreatesAndReopensWithoutChangingSources(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	databasePath := filepath.Join(dir, "knowledge.db")
	rawPath := filepath.Join(dir, "raw")
	s, err := Open(ctx, databasePath, rawPath)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := s.CreateLibrary(ctx, types.CreateLibraryInput{RequestID: "current-library", Name: "当前结构", Kind: "general", WikiLanguage: "zh-CN"})
	if err != nil {
		t.Fatal(err)
	}
	imported, err := s.ImportSource(ctx, types.ImportSourceInput{RequestID: "current-source", LibraryID: rec.ResourceID, Filename: "source.md", Bytes: []byte("当前原件")})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, databasePath, rawPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	raw, err := s.ReadRaw(ctx, rec.ResourceID, imported.ResourceID)
	if err != nil || string(raw) != "当前原件" {
		t.Fatalf("reopen raw=%q %v", raw, err)
	}
}

func TestCurrentSchemaRejectsMissingColumnWithoutRepair(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	databasePath := filepath.Join(dir, "knowledge.db")
	rawPath := filepath.Join(dir, "raw")
	s, err := Open(ctx, databasePath, rawPath)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.db.Migrator().DropColumn(&types.Library{}, "chunking_config"); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, databasePath, rawPath)
	if err == nil {
		reopened.Close()
		t.Fatal("incomplete schema was silently repaired")
	}
}

func TestCurrentSchemaRejectsDifferentVersionWithoutRewritingIt(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	databasePath := filepath.Join(dir, "knowledge.db")
	rawPath := filepath.Join(dir, "raw")
	s, err := Open(ctx, databasePath, rawPath)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.db.Model(&schemaState{}).Where("id = ?", 1).Update("version", 2).Error; err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, databasePath, rawPath)
	if err == nil {
		reopened.Close()
		t.Fatal("different schema version was accepted")
	}
}
