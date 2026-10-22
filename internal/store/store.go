package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/JieWaZi/neux-knowledge/internal/domain"
	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	sqlitecodes "modernc.org/sqlite/lib"
)

// Store owns the knowledge database and managed source directory without starting background execution.
type Store struct {
	db               *gorm.DB       // Independent SQLite connection.
	raw              *os.Root       // Managed source root protected against path traversal.
	once             sync.Once      // Makes repeated closes safe.
	closeErr         error          // Actual error from the first close.
	processorDir     string         // Local stage-lock directory located by the actual database path.
	processorMu      sync.Mutex     // Serializes the close barrier and local stage entry.
	processorClosing bool           // Prevents acceptance of new local processing stages during close.
	processors       sync.WaitGroup // Local processing and cancellation recovery operations that have not actually exited.
	databaseIdentity string         // Digest of the canonical database path, identifying ownership of the managed source root.
	rawIdentity      string         // Digest of the canonical source-root path, checked bidirectionally against the authoritative database binding.
}

// Open creates the current knowledge schema and validates existing databases without changing SQLite defaults.
func Open(ctx context.Context, dbPath, rawDir string) (*Store, error) {
	if strings.TrimSpace(dbPath) == "" || strings.TrimSpace(rawDir) == "" || strings.ContainsAny(dbPath, "?\x00") {
		return nil, fail(types.ErrInvalid, "open", "database and raw paths are required")
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0700); err != nil {
		return nil, wrap(types.ErrStorage, "open", err)
	}
	if err := os.MkdirAll(rawDir, 0700); err != nil {
		return nil, wrap(types.ErrStorage, "open", err)
	}
	root, err := os.OpenRoot(rawDir)
	if err != nil {
		return nil, wrap(types.ErrStorage, "open", err)
	}
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{SkipDefaultTransaction: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		root.Close()
		return nil, wrap(types.ErrDatabase, "open", err)
	}
	s := &Store{db: db, raw: root}
	canonical, err := filepath.Abs(dbPath)
	if err == nil {
		canonical, err = filepath.EvalSymlinks(canonical)
	}
	if err != nil {
		s.Close()
		return nil, wrap(types.ErrStorage, "open", err)
	}
	s.processorDir = canonical + ".processors"
	s.databaseIdentity = hash([]byte(canonical))
	canonicalRaw, err := filepath.Abs(rawDir)
	if err == nil {
		canonicalRaw, err = filepath.EvalSymlinks(canonicalRaw)
	}
	if err != nil {
		s.Close()
		return nil, wrap(types.ErrStorage, "open", err)
	}
	s.rawIdentity = hash([]byte(canonicalRaw))
	if err = s.ensureSchema(ctx); err != nil {
		s.Close()
		return nil, err
	}

	release, err := s.lockBlobs(ctx)
	if err != nil {
		s.Close()
		return nil, err
	}
	err = s.blobBinding(ctx, true)
	if err == nil {
		err = s.resumeLibraryDeletions(ctx)
	}
	release()
	if err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

// Close releases resources after local processing actually exits; the host must first cancel and drain its own work.
func (s *Store) Close() error {
	s.once.Do(func() {
		s.processorMu.Lock()
		s.processorClosing = true
		s.processorMu.Unlock()
		s.processors.Wait()
		db, err := s.db.DB()
		if err == nil {
			err = db.Close()
		}
		rerr := s.raw.Close()
		s.closeErr = errors.Join(err, rerr)
	})
	return s.closeErr
}

// currentSchemaVersion is the sole version of the current development schema; the core does not run historical upgrades.
const currentSchemaVersion = 1

// schemaState identifies the current schema used by this database without recording a historical migration sequence.
type schemaState struct {
	ID      int `gorm:"primaryKey;autoIncrement:false"` // Fixed at 1; the database has only one schema record.
	Version int // Current schema version.
}

func (schemaState) TableName() string { return "knowledge_schema" }

func currentSchemaModels() []any {
	return []any{&schemaState{}, &types.Library{}, &types.SourceHead{}, &types.SourceVersion{}, &types.WikiPage{}, &types.WikiFolder{}, &types.WikiPageRevision{}, &types.WikiPageIssue{}, &types.Job{}, &types.Stage{}, &types.Receipt{}, &types.SourcePurgeRecord{}, &types.SourceTombstone{}, &types.ImportBlobIntent{}, &types.LibraryDeletionRecord{}, &types.EmbeddingChunkIndex{}, &types.AuditLog{}, &blobRootBinding{}, &types.OrganizeSourceWork{}, &types.WikiFinalizeOp{}}
}

var currentSchemaIndexes = map[string]string{
	"idx_source_library_created":  "CREATE INDEX idx_source_library_created ON knowledge_source_versions(library_id, created_at, id)",
	"idx_source_identity_version": "CREATE UNIQUE INDEX idx_source_identity_version ON knowledge_source_versions(source_id, version)",
	"idx_stage_job_created":       "CREATE INDEX idx_stage_job_created ON knowledge_stages(job_id, created_at, id)",
	"idx_page_library_updated":    "CREATE INDEX idx_page_library_updated ON wiki_pages(knowledge_base_id, updated_at, id)",
}

// ensureSchema creates the current schema only in an empty database; existing schemas must match completely, without adding columns or rewriting business rows.
func (s *Store) ensureSchema(ctx context.Context) error {
	return s.transaction(ctx, func(tx *gorm.DB) error {
		var tables []string
		if err := tx.Raw("SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'").Scan(&tables).Error; err != nil {
			return err
		}
		models := currentSchemaModels()
		if len(tables) == 0 {
			if err := tx.Migrator().CreateTable(models...); err != nil {
				return err
			}
			for _, ddl := range currentSchemaIndexes {
				if err := tx.Exec(ddl).Error; err != nil {
					return err
				}
			}
			return tx.Create(&schemaState{ID: 1, Version: currentSchemaVersion}).Error
		}
		if len(tables) != len(models) || !tx.Migrator().HasTable(&schemaState{}) {
			return fail(types.ErrDatabase, "schema.validate", "database does not use the current knowledge schema")
		}
		var state schemaState
		if err := tx.Where("id = ?", 1).Take(&state).Error; err != nil {
			return err
		}
		var states int64
		if err := tx.Model(&schemaState{}).Count(&states).Error; err != nil {
			return err
		}
		if states != 1 || state.Version != currentSchemaVersion {
			return fail(types.ErrDatabase, "schema.validate", "database schema version does not match this SDK")
		}
		for _, model := range models {
			stmt := &gorm.Statement{DB: tx}
			if err := stmt.Parse(model); err != nil {
				return err
			}
			if !tx.Migrator().HasTable(model) {
				return fail(types.ErrDatabase, "schema.validate", "current schema table is missing")
			}
			columns, err := tx.Migrator().ColumnTypes(model)
			if err != nil {
				return err
			}
			if len(columns) != len(stmt.Schema.DBNames) {
				return fail(types.ErrDatabase, "schema.validate", "current schema columns do not match")
			}
			for _, name := range stmt.Schema.DBNames {
				if !tx.Migrator().HasColumn(model, name) {
					return fail(types.ErrDatabase, "schema.validate", "current schema column is missing")
				}
			}
			for _, index := range stmt.Schema.ParseIndexes() {
				name := index.Name
				if !tx.Migrator().HasIndex(model, name) {
					return fail(types.ErrDatabase, "schema.validate", "current schema index is missing")
				}
			}
		}
		for name := range currentSchemaIndexes {
			var count int64
			if err := tx.Raw("SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?", name).Scan(&count).Error; err != nil {
				return err
			}
			if count != 1 {
				return fail(types.ErrDatabase, "schema.validate", "current schema index is missing")
			}
		}
		return nil
	})
}
func fail(code types.ErrorCode, op, detail string) error {
	return &types.Error{Code: code, Operation: op, Detail: detail}
}
func wrap(code types.ErrorCode, op string, err error) error {
	return &types.Error{Code: code, Operation: op, Cause: err}
}
func newID() string        { return strings.ReplaceAll(uuid.NewString(), "-", "") }
func digest(v any) string  { b, _ := json.Marshal(v); return hash(b) }
func hash(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// sqlitePrimaryCode handles extended error codes from the pure-Go modernc/glebarez drivers.
func sqlitePrimaryCode(err error) int {
	var coded interface{ Code() int }
	if errors.As(err, &coded) {
		return coded.Code() & 0xff
	}
	return 0
}
func isBusy(err error) bool {
	code := sqlitePrimaryCode(err)
	return code == sqlitecodes.SQLITE_BUSY || code == sqlitecodes.SQLITE_LOCKED
}

func translate(op string, err error) error {
	if err == nil {
		return nil
	}
	var typed *types.Error
	if errors.As(err, &typed) {
		return err
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, gorm.ErrRecordNotFound) || errors.Is(err, ErrWikiPageNotFound) {
		return wrap(types.ErrNotFound, op, err)
	}
	if errors.Is(err, ErrWikiPageConflict) {
		return wrap(types.ErrVersionConflict, op, err)
	}
	if sqlitePrimaryCode(err) == sqlitecodes.SQLITE_CONSTRAINT {
		return wrap(types.ErrVersionConflict, op, err)
	}
	if isBusy(err) {
		return wrap(types.ErrDatabaseBusy, op, err)
	}
	return wrap(types.ErrDatabase, op, err)
}

// transaction restarts the whole transaction on busy rather than replaying a single statement in an old snapshot.
func (s *Store) transaction(ctx context.Context, fn func(*gorm.DB) error) error {
	var err error
	for attempt := 0; attempt < 4; attempt++ {
		if err = ctx.Err(); err != nil {
			return err
		}
		err = s.db.WithContext(ctx).Transaction(fn)
		if !isBusy(err) || attempt == 3 {
			return translate("transaction", err)
		}
		timer := time.NewTimer(time.Second * time.Duration(1<<attempt))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return translate("transaction", err)
}
func (s *Store) mutate(ctx context.Context, requestID, op, libraryID string, input any, fn func(*gorm.DB) (*types.Receipt, error)) (*types.Receipt, error) {
	if strings.TrimSpace(requestID) == "" {
		return nil, fail(types.ErrInvalid, op, "requestId is required")
	}
	d := digest(struct {
		// Operation is the domain operation included in the idempotency digest.
		Operation string
		// Input is the complete request content included in the idempotency digest.
		Input any
	}{op, input})
	var result *types.Receipt
	err := s.transaction(ctx, func(tx *gorm.DB) error {
		var prior types.Receipt
		err := tx.Where("request_id = ?", requestID).Take(&prior).Error
		if err == nil {
			if prior.Digest != d {
				return fail(types.ErrIdempotencyConflict, op, "requestId has different input")
			}
			result = &prior
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		result, err = fn(tx)
		if err != nil {
			return err
		}
		result.RequestID = requestID
		result.Operation = op
		if libraryID != "" {
			result.LibraryID = libraryID
		}
		result.Digest = d
		result.CreatedAt = time.Now().UnixMilli()
		if err := appendMutationActivity(ctx, tx, result, input); err != nil {
			return err
		}
		return tx.Create(result).Error
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
func pagination(offset, limit int) (int, int) {
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	return offset, limit
}
func getLibrary(tx *gorm.DB, id string, active bool) (*types.Library, error) {
	var l types.Library
	if err := tx.Where("id = ?", id).Take(&l).Error; err != nil {
		return nil, err
	}
	if active && l.Kind != "general" {
		return nil, fail(types.ErrInvalid, "library", "retired library kind is read-only")
	}
	if active && l.Archived {
		return nil, fail(types.ErrScopeViolation, "library", "library is archived")
	}
	return &l, nil
}
func bumpStructure(tx *gorm.DB, l *types.Library) error {
	r := tx.Model(&types.Library{}).Where("id = ? AND structure_version = ?", l.ID, l.StructureVersion).Updates(map[string]any{"structure_version": l.StructureVersion + 1, "updated_at": time.Now().UnixMilli()})
	if r.Error != nil {
		return r.Error
	}
	if r.RowsAffected != 1 {
		return fail(types.ErrVersionConflict, "structure", "structure changed")
	}
	l.StructureVersion++
	return nil
}
