package core

import (
	"context"
	"github.com/JieWaZi/neux-knowledge/internal/store"
)

// Options specifies the standalone database and managed source root.
type Options struct {
	DBPath string // Standalone SQLite file path.
	RawDir string // Managed root directory for source objects.
}

// Engine owns the standalone knowledge database lifecycle and does not start background models.
type Engine struct {
	store *store.Store // Internal persistence and domain transaction boundary.
}

// Open opens the standalone knowledge core, creating the current schema for an empty database or validating an existing schema.
func Open(ctx context.Context, options Options) (*Engine, error) {
	s, err := store.Open(ctx, options.DBPath, options.RawDir)
	if err != nil {
		return nil, err
	}
	return &Engine{store: s}, nil
}

// Close releases core resources and is safe to call repeatedly.
func (e *Engine) Close() error { return e.store.Close() }
