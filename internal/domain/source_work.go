package types

// OrganizeSourceWork stores per-document rescheduling and failure budgets within one job attempt.
type OrganizeSourceWork struct {
	StartedAt       int64  `json:"startedAt"`                                                   // First actual reading start time for the current job attempt, in Unix milliseconds.
	ID              string `json:"id" gorm:"primaryKey"`                                        // Stable work item identifier.
	LibraryID       string `json:"libraryId" gorm:"index"`                                      // Library owning the material, for purge and isolation validation.
	JobID           string `json:"jobId" gorm:"uniqueIndex:idx_organize_source_work"`           // Owning persistent job.
	JobAttempt      int    `json:"jobAttempt" gorm:"uniqueIndex:idx_organize_source_work"`      // Job attempt owning the frozen source.
	SourceVersionID string `json:"sourceVersionId" gorm:"uniqueIndex:idx_organize_source_work"` // Immutable source version for this rescheduling.
	FailureCount    int    `json:"failureCount"`                                                // Persisted failure count; the sixth failure moves the item to the dead-letter state.
	NotBefore       int64  `json:"notBefore"`                                                   // Earliest retry time in Unix milliseconds; zero allows immediate execution.
	State           string `json:"state"`                                                       // pending, done, or dead.
	Diagnostic      string `json:"diagnostic"`                                                  // Most recent actual failure diagnostics.
	Version         int64  `json:"version"`                                                     // Optimistic lock version for the work item.
	CreatedAt       int64  `json:"createdAt"`                                                   // First registration time in Unix milliseconds.
	UpdatedAt       int64  `json:"updatedAt"`                                                   // Most recent commit time in Unix milliseconds.
}

func (OrganizeSourceWork) TableName() string { return "knowledge_organize_source_work" }

// SourceWorkActionInput protects deferred per-document retries with request identity and version.
type SourceWorkActionInput struct {
	RequestID       string // Idempotent request identifier.
	JobID           string // Owning job; only its current attempt may be modified.
	JobAttempt      int    // Required job attempt number; late requests from an older attempt must not write into the new queue.
	SourceVersionID string // Immutable source within the job's frozen scope.
	ExpectedVersion int64  // Current work item version.
	Diagnostic      string // Actual diagnostics for this failure.
	NotBefore       int64  // Host-selected earliest retry time in Unix milliseconds.
}
