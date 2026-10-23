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
	// ExpectedPolicyVersion 非零时在原件接纳和提交事务内检查原授权代次。
	ExpectedPolicyVersion int64  `json:",omitempty"`
	RequestID             string // 稳定请求标识。
	LibraryID             string // 原件所属知识库。
	SourceID              string // 新来源为空；替换时指定原来源身份。
	ExpectedVersion       int64  // 新来源为零；替换时为已读原件版本。
	Filename              string // 原文件名，不能包含路径。
	MIME                  string // 原件媒体类型。
	Bytes                 []byte // 完整原件字节。
}

// ArchiveSourceInput archives or restores originals using the state row version.
type ArchiveSourceInput struct {
	RequestID       string // Idempotent request identifier.
	LibraryID       string // Owning library.
	SourceVersionID string // Source version identifier.
	ExpectedVersion int64  // Source state row version.
	Archived        bool   // Target archive state.
}

// WritePageInput 携带内容版本、目录版本和整页生成时的读取时间。
type WritePageInput struct {
	ExpectedPolicyVersion int64    // 冻结的权限代次；零表示不冻结，正值须在内容事务内匹配。
	RequestID             string   // 稳定幂等请求身份。
	LibraryID             string   // 目标知识库。
	ExpectedVersion       int      // 已读取的内容版本，新建为零。
	StructureVersion      int64    // 已读取的目录版本。
	Page                  WikiPage // 页面内容；更新必须原样携带读取时的 UpdatedAt，忽略租户字段且库身份须匹配。
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
	// ExpectedPolicyVersion 非零时在目录写事务内核对原授权策略代次。
	ExpectedPolicyVersion int64 `json:",omitempty"`
	// RequestID 是稳定请求身份。
	RequestID string
	// LibraryID 是目标知识库。
	LibraryID string
	// StructureVersion 是调用者已读的目录代次。
	StructureVersion int64
	// Theme 是人工目录内容。
	Theme WikiFolder
	// Delete 只删除已清空的人工目录。
	Delete bool
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
