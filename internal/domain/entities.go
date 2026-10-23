package types

import "encoding/json"

// Library stores knowledge library configuration and independent structure and permission generations.
type Library struct {
	ChunkingConfig   *ChunkingConfig  `json:"chunkingConfig,omitempty" gorm:"serializer:json"`       // Upstream chunking settings; omission uses creation defaults.
	ID               string           `json:"id" gorm:"primaryKey"`                                  // Stable library identifier.
	Name             string           `json:"name"`                                                  // User-visible name.
	Kind             string           `json:"kind"`                                                  // Current library type, fixed at general.
	WikiLanguage     string           `json:"wikiLanguage"`                                          // Wiki content language.
	Purpose          string           `json:"purpose"`                                               // Description of the library's purpose.
	WikiConfig       *WikiConfig      `json:"wikiConfig,omitempty" gorm:"type:json;serializer:json"` // Wiki organization granularity and additional instructions.
	IndexingStrategy IndexingStrategy `json:"indexingStrategy" gorm:"type:json;serializer:json"`     // Enabled indexing and Wiki pipelines.
	EmbeddingModelID string           `json:"embeddingModelId,omitempty"`                            // Embedding model identifier used by vector or keyword indexing.
	Version          int64            `json:"version"`                                               // Optimistic lock for all library configuration writes.
	StructureVersion int64            `json:"structureVersion"`                                      // Structure generation for themes and page placement.
	PolicyVersion    int64            `json:"policyVersion"`                                         // Host authorization policy generation; changes prevent older jobs from publishing.
	Archived         bool             `json:"archived"`                                              // Whether it is archived.
	CreatedAt        int64            `json:"createdAt"`                                             // Creation time in Unix milliseconds.
	UpdatedAt        int64            `json:"updatedAt"`                                             // Update time in Unix milliseconds.
}

func (Library) TableName() string { return "knowledge_libraries" }

// SourceVersion stores immutable original identity and mutable processing state; original bytes are stored separately in the managed directory.
type SourceVersion struct {
	// OrganizeBlocked 由活动任务按原件身份投影，只限制再次接纳，不改变真实处理结果。
	OrganizeBlocked bool `json:"organizeBlocked,omitempty" gorm:"-"`
	// Description 保存普通文档分析生成的简短摘要。
	Description string `json:"description,omitempty"`
	// Profile 保存规范化的主题、类型及问题，不包含完整原文。
	Profile *KnowledgeProfile `json:"profile,omitempty" gorm:"serializer:json"`
	// SummaryStatus 保存资料摘要的实际处理状态。
	SummaryStatus string `json:"summaryStatus,omitempty"`

	ID         string          `json:"id" gorm:"primaryKey"`                      // 不可变的来源版本标识。
	LibraryID  string          `json:"libraryId" gorm:"index"`                    // 所属知识库标识。
	SourceID   string          `json:"sourceId" gorm:"index"`                     // 跨版本稳定的来源标识。
	Version    int64           `json:"version"`                                   // 同一来源的原件版本。
	RowVersion int64           `json:"rowVersion"`                                // 归档及处理状态的乐观锁版本。
	Filename   string          `json:"filename"`                                  // 原始文件名，不作为磁盘路径。
	MIME       string          `json:"mime"`                                      // 来源媒体类型。
	SHA256     string          `json:"sha256"`                                    // 原件字节摘要。
	Size       int64           `json:"size"`                                      // 原件字节长度。
	RawKey     string          `json:"rawKey"`                                    // 由核心生成的受管原件键。
	State      string          `json:"state"`                                     // 待处理、已处理、无变化、失败或已取消。
	Archived   bool            `json:"archived"`                                  // 来源是否已归档。
	Coverage   json.RawMessage `json:"coverage" gorm:"type:json;serializer:json"` // 阅读器提供的实际读取覆盖信息。
	Diagnostic string          `json:"diagnostic"`                                // 最近一次处理诊断。
	CreatedAt  int64           `json:"createdAt"`                                 // 创建时间，单位为 Unix 毫秒。
}

func (SourceVersion) TableName() string { return "knowledge_source_versions" }

// SourceHead determines a source's current version; filenames do not determine source identity.
type SourceHead struct {
	ID        string `gorm:"primaryKey"` // Stable source identifier.
	LibraryID string `gorm:"index"`      // Owning library.
	VersionID string // Current source version identifier.
	Version   int64  // Current original generation.
}

func (SourceHead) TableName() string { return "knowledge_sources" }

// PermissionSnapshot pins permissions and actual project scope at acceptance.
type PermissionSnapshot struct {
	PolicyVersion int64    `json:"policyVersion"` // Permission policy generation at acceptance.
	Scope         []string `json:"scope"`         // Actual project scope authorized by the host.
	Operations    []string `json:"operations"`    // Explicitly permitted domain operations.
}

// Job is a persistent knowledge job and does not own the model host.
type Job struct {
	// ActivityActor pins the actual initiator identity at acceptance for background terminal-state activities.
	ActivityActor        ActivityActor `json:"activityActor,omitempty" gorm:"serializer:json"`
	EmbeddingFingerprint string        `json:"-"`                            // Embedding model space fingerprint frozen at acceptance, without secrets.
	LibrarySnapshot      *Library      `json:"-" gorm:"serializer:json"`     // Complete library configuration pinned by the acceptance transaction; later settings changes do not affect in-flight jobs.
	ID                   string        `json:"id" gorm:"primaryKey"`         // Stable job identifier.
	LibraryID            string        `json:"libraryId" gorm:"index"`       // Owning knowledge library.
	RequestID            string        `json:"requestId" gorm:"uniqueIndex"` // Acceptance idempotency key.
	// State is the pending record's current lifecycle state.
	State            string             `json:"state"`                                   // queued, processing, completed, partial_failed, failed, cancel_requested, or cancelled.
	Attempt          int                `json:"attempt"`                                 // Current job attempt number.
	Version          int64              `json:"version"`                                 // Optimistic lock for job state.
	StructureVersion int64              `json:"structureVersion"`                        // Directory base version at acceptance.
	SourceVersionIDs []string           `json:"sourceVersionIds" gorm:"serializer:json"` // Frozen source version set.
	Permission       PermissionSnapshot `json:"permission" gorm:"serializer:json"`       // Frozen permission snapshot.
	RulesVersion     string             `json:"rulesVersion"`                            // Pinned rule version.
	ConfigDigest     string             `json:"configDigest"`                            // Pinned pipeline and host configuration digest.
	Diagnostic       string             `json:"diagnostic"`                              // Most recent failure or cancellation diagnostics.
	AttemptStartedAt int64              `json:"attemptStartedAt,omitempty"`              // First acceptance time of the current attempt in Unix milliseconds; recovery must not overwrite it.
	CancelRequested  bool               `json:"cancelRequested"`                         // Persistent cancellation barrier.
	CreatedAt        int64              `json:"createdAt"`                               // Acceptance time in Unix milliseconds.
	UpdatedAt        int64              `json:"updatedAt"`                               // State change time in Unix milliseconds.
}

func (Job) TableName() string { return "knowledge_jobs" }

// ModelUsage stores usage reported by the model provider for this request.
type ModelUsage struct {
	InputTokens  int64 `json:"inputTokens"`  // Actual input tokens.
	OutputTokens int64 `json:"outputTokens"` // Actual output tokens.
	TotalTokens  int64 `json:"totalTokens"`  // Actual total tokens.
}

// ModelDiagnostics stores verifiable capabilities and reasoning settings for direct CLI calls.
type ModelDiagnostics struct {
	ActualReasoning   string   `json:"actualReasoning,omitempty"`   // Reasoning level actually used by the CLI.
	ThinkingDisabled  bool     `json:"thinkingDisabled"`            // Thinking is verifiably disabled.
	ReasoningFallback string   `json:"reasoningFallback,omitempty"` // Actual fallback when the CLI lacks a disable option.
	ToolsDisabled     bool     `json:"toolsDisabled"`               // Tools are verifiably disabled.
	Limitations       []string `json:"limitations,omitempty"`       // Restrictions that native capabilities cannot guarantee.
}

type Stage struct {
	Diagnostics *ModelDiagnostics `json:"diagnostics,omitempty" gorm:"serializer:json"` // 本次原生调用的可核验诊断。

	SourceVersionID  string      `json:"sourceVersionId,omitempty"`                         // 所属来源版本标识。
	SourceVersionIDs []string    `json:"sourceVersionIds,omitempty" gorm:"serializer:json"` // 本阶段实际读取的来源集合。
	ResultText       string      `json:"resultText"`                                        // 模型实际返回的完整正文。
	Usage            *ModelUsage `json:"usage,omitempty" gorm:"serializer:json"`            // 实际模型用量，未知时为空。
	FinishedAt       int64       `json:"finishedAt"`                                        // 阶段实际结束时间，单位为 Unix 毫秒。

	ID           string          `json:"id" gorm:"primaryKey"`                   // 阶段标识。
	JobID        string          `json:"jobId" gorm:"index"`                     // 所属知识任务标识。
	Name         string          `json:"name"`                                   // 管线阶段名称。
	Kind         string          `json:"kind"`                                   // 生成阶段种类。
	JobAttempt   int             `json:"jobAttempt"`                             // 创建时所属任务尝试次数。
	Attempt      int             `json:"attempt"`                                // 当前阶段尝试次数。
	Version      int64           `json:"version"`                                // 阶段状态乐观锁版本。
	State        string          `json:"state"`                                  // 阶段持久状态。
	Outcome      string          `json:"outcome"`                                // 真实执行结果。
	Completeness string          `json:"completeness"`                           // 正文完整性结论。
	StopReason   string          `json:"stopReason"`                             // 宿主原生停止原因。
	Diagnostic   string          `json:"diagnostic"`                             // 可信执行诊断。
	Input        json.RawMessage `json:"input" gorm:"type:json;serializer:json"` // 可恢复的阶段输入。
	CreatedAt    int64           `json:"createdAt"`                              // 创建时间，单位为 Unix 毫秒。
}

func (Stage) TableName() string { return "knowledge_stages" }

// PageMutation explicitly declares the page version used to generate a candidate.
type PageMutation struct {
	Page            WikiPage `json:"page"`            // Complete new page content; the page identifier is stable.
	ExpectedVersion int      `json:"expectedVersion"` // 0 on creation; the generation base version on updates.
}

// ThemeMutation protects theme creation and updates with the library structure generation.
type ThemeMutation struct {
	Theme  WikiFolder `json:"theme"`  // Theme content; at most two levels.
	Delete bool       `json:"delete"` // Whether to delete the empty theme.
}

// SourceConclusion marks processing conclusions only for sources actually read.
type SourceConclusion struct {
	// Description is the source summary produced by the same actual summary stage, awaiting atomic publication.
	Description string `json:"description,omitempty"`
	// Profile is the normalized source profile published with the summary.
	Profile *KnowledgeProfile `json:"profile,omitempty"`

	SourceVersionID string          `json:"sourceVersionId"` // Immutable version corresponding to this conclusion.
	State           string          `json:"state"`           // processed, no-change, failed, or cancelled.
	Complete        bool            `json:"complete"`        // Whether complete reading of the required scope is confirmed.
	Coverage        json.RawMessage `json:"coverage"`        // Verifiable actual coverage.
	Diagnostic      string          `json:"diagnostic"`      // Failure reason or limitation.
}

// Publication is one batch of atomic changes pinned after trusted pipeline validation.
type Publication struct {
	Maintenance      bool               `json:"maintenance,omitempty"`      // Global finalization writes do not append to the same finalization queue again.
	SourceVersionIDs []string           `json:"sourceVersionIds,omitempty"` // Frozen source read set that actually contributed, including source inputs for unchanged pages but excluding retained historical attribution alone.
	PageVersions     map[string]int     `json:"pageVersions,omitempty"`     // Page versions actually read during generation, including the read set for unchanged conclusions.
	Pages            []PageMutation     `json:"pages"`                      // Pages requiring atomic writes.
	Themes           []ThemeMutation    `json:"themes"`                     // Themes requiring atomic writes.
	Sources          []SourceConclusion `json:"sources"`                    // Confirmed source conclusions for this batch.
	Final            bool               `json:"final"`                      // Whether to finish this job.
}

// Receipt stores the result actually committed by an idempotent operation.
type Receipt struct {
	JobAttempt int      `json:"jobAttempt,omitempty"`                     // Job attempt owning the actual change, for internal source rescheduling diagnostics.
	RequestID  string   `json:"requestId" gorm:"primaryKey"`              // Global idempotent request identifier.
	Digest     string   `json:"-"`                                        // Original request digest; it is not serialized to applications.
	Operation  string   `json:"operation"`                                // Actual operation type.
	LibraryID  string   `json:"libraryId" gorm:"index"`                   // Knowledge library targeted by the operation.
	ResourceID string   `json:"resourceId"`                               // Main resource identifier created or modified.
	JobID      string   `json:"jobId,omitempty"`                          // Associated knowledge job.
	Version    int64    `json:"version"`                                  // Main resource version after commit.
	State      string   `json:"state"`                                    // Actual fact of being saved, accepted, or published.
	PageIDs    []string `json:"pageIds,omitempty" gorm:"serializer:json"` // Pages actually changed in this batch.
	CreatedAt  int64    `json:"createdAt"`                                // Atomic commit time in Unix milliseconds.
}

func (Receipt) TableName() string { return "knowledge_receipts" }
