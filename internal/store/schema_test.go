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
	if err = s.db.Model(&schemaState{}).Where("id = ?", 1).Update("version", currentSchemaVersion+1).Error; err != nil {
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

// TestCurrentSourceWorkSchemaRequiresReverseIndex 验证新库生成完整工作表及反查索引，缺失时严格拒开且不自动修补。
func TestCurrentSourceWorkSchemaRequiresReverseIndex(t *testing.T) {
	ctx := context.Background()
	s, databasePath, rawPath, _ := auditStore(t)
	var ddl []struct {
		Name string // 当前工作表或索引名称。
		SQL  string // SQLite 实际保存的建表或建索引定义。
	}
	if err := s.db.Raw("SELECT name,sql FROM sqlite_master WHERE tbl_name='knowledge_organize_source_work' AND sql IS NOT NULL ORDER BY type DESC,name").Scan(&ddl).Error; err != nil {
		t.Fatal(err)
	}
	for _, entry := range ddl {
		t.Logf("%s: %s", entry.Name, entry.SQL)
	}
	var columns []string
	if err := s.db.Raw("SELECT name FROM pragma_index_info('idx_source_work_version_job') ORDER BY seqno").Scan(&columns).Error; err != nil {
		t.Fatal(err)
	}
	if len(columns) != 4 || columns[0] != "source_version_id" || columns[1] != "library_id" || columns[2] != "job_id" || columns[3] != "job_attempt" {
		t.Fatalf("reverse lookup does not start with the requested source scope: %v", columns)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, databasePath, rawPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.db.Exec("DROP INDEX idx_source_work_version_job").Error; err != nil {
		t.Fatal(err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	if mismatched, err := Open(ctx, databasePath, rawPath); err == nil {
		mismatched.Close()
		t.Fatal("schema without the current reverse index was accepted or silently repaired")
	}
}
