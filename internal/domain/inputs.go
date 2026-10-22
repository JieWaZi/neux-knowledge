package types

// CreateLibraryInput explicitly stores the new-library request identifier.
type CreateLibraryInput struct {
	ChunkingConfig   *ChunkingConfig   // Complete chunking configuration; omission uses upstream defaults on creation and preserves existing values on updates.
	RequestID        string            // Idempotent request identifier.
	Name             string            // Name.
	Kind             string            // Library type.
	WikiLanguage     string            // Wiki language.
	Purpose          string            // Purpose description.
	WikiConfig       *WikiConfig       // Optional Wiki organization configuration.
	IndexingStrategy *IndexingStrategy // Optional indexing strategy; omission uses the type default.
	EmbeddingModelID string            // Embedding model identifier required for vector indexing.
}

// UpdateLibraryInput protects configuration and permission updates with the library version.
type UpdateLibraryInput struct {
	ChunkingConfig   *ChunkingConfig   // Complete chunking configuration; omission uses upstream defaults on creation and preserves existing values on updates.
	RequestID        string            // Idempotent request identifier.
	LibraryID        string            // Knowledge library identifier.
	ExpectedVersion  int64             // Current library version.
	Name             string            // New name.
	Purpose          string            // New purpose.
	Archived         bool              // New archive state.
	PolicyVersion    int64             // New permission generation; it must not decrease.
	WikiConfig       *WikiConfig       // New Wiki configuration; nil leaves it unchanged.
	IndexingStrategy *IndexingStrategy // New indexing strategy; nil leaves it unchanged.
	EmbeddingModelID *string           // New embedding model; nil leaves it unchanged.
}

// ImportSourceInput stores complete original bytes without triggering organization.
type ImportSourceInput struct {
	RequestID       string // Idempotent request identifier.
	LibraryID       string // Owning library.
	SourceID        string // Empty for a new source; specify the stable source identifier for replacement.
	ExpectedVersion int64  // 0 for a new source; the current original generation for replacement.
	Filename        string // Original filename; it must not be a path.
	MIME            string // Media type.
	Bytes           []byte // Complete original bytes.
}

// ArchiveSourceInput archives or restores originals using the state row version.
type ArchiveSourceInput struct {
	RequestID       string // Idempotent request identifier.
	LibraryID       string // Owning library.
	SourceVersionID string // Source version identifier.
	ExpectedVersion int64  // Source state row version.
	Archived        bool   // Target archive state.
}

// WritePageInput explicitly carries base versions for content and directory structure.
type WritePageInput struct {
	ExpectedPolicyVersion int64    // Explicitly frozen permission generation; zero disables freezing, and positive values must match within the content write transaction.
	RequestID             string   // Idempotent request identifier.
	LibraryID             string   // Target library.
	ExpectedVersion       int      // Current page version; 0 on creation.
	StructureVersion      int64    // Current structure version.
	Page                  WikiPage // Page content; TenantID is ignored and the library identifier must match.
}

// RestorePageInput restores historical content while preserving the current theme and order.
type RestorePageInput struct {
	RequestID        string // Idempotent request identifier.
	LibraryID        string // Owning library.
	PageID           string // Stable page identifier.
	ExpectedVersion  int    // Current page version.
	RevisionVersion  int    // Historical content version to restore.
	StructureVersion int64  // Current structure version.
}

// WriteThemeInput protects theme writes and deletions with the structure version.
type WriteThemeInput struct {
	RequestID        string     // Idempotent request identifier.
	LibraryID        string     // Target library.
	StructureVersion int64      // Current directory generation.
	Theme            WikiFolder // Theme content.
	Delete           bool       // Whether to delete the empty theme.
}

// AcceptJobInput pins sources and authorization for organization without starting a model.
type AcceptJobInput struct {
	// Rebuild explicitly rebuilds parsing artifacts for selected sources; omission preserves ordinary organization and prior request digests.
	Rebuild              bool               `json:"rebuild,omitempty"`
	EmbeddingFingerprint string             // Endpoint and model fingerprint frozen when vectors are enabled.
	RequestID            string             // Idempotent request identifier.
	LibraryID            string             // Target library.
	ExpectedVersion      int64              // Library version at acceptance.
	StructureVersion     int64              // Directory generation at acceptance.
	SourceVersionIDs     []string           // Explicitly frozen source version set.
	Permission           PermissionSnapshot // Scope and permissions validated by the host.
	RulesVersion         string             // Pinned rule version.
	ConfigDigest         string             // Pinned pipeline configuration digest.
}

// JobActionInput protects job state during cancellation and retry.
type JobActionInput struct {
	RequestID       string // Idempotent request identifier.
	JobID           string // Job identifier.
	ExpectedVersion int64  // Current job version.
}

// PageQuery uses SQLite keyword search with bounded pagination.
type PageQuery struct {
	LibraryID string  // Owning library.
	Query     string  // Escaped plain keywords, rather than BM25.
	PageType  string  // Optional page type.
	ThemeID   *string // Optional exact theme; an empty string denotes the root.
	Status    string  // Optional state.
	Offset    int     // Starting offset.
	Limit     int     // Defaults to 50, with a maximum of 100.
}

// FailJobInput faithfully records pipeline failure without requiring a fabricated successful publication stage.
type FailJobInput struct {
	JobActionInput                    // Job request identifier and current version.
	Diagnostic     string             // Actual reason for failure or partial failure.
	Sources        []SourceConclusion // May contain only failed/cancelled conclusions for currently frozen sources.
}
