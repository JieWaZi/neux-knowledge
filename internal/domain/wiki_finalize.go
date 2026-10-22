package types

// WikiFinalizeOp stores published library-wide finalization deltas from Lite batches; it is written in the same transaction and merged after a crash.
type WikiFinalizeOp struct {
	ID         string `json:"id" gorm:"primaryKey"`                            // Stable finalization row identifier.
	LibraryID  string `json:"libraryId" gorm:"index"`                          // Owning knowledge library.
	JobID      string `json:"jobId" gorm:"uniqueIndex:idx_lite_finalize"`      // Owning explicit organization job.
	JobAttempt int    `json:"jobAttempt" gorm:"uniqueIndex:idx_lite_finalize"` // Actual job attempt.
	Key        string `json:"key" gorm:"uniqueIndex:idx_lite_finalize"`        // Stable deduplication key for a slug or change.
	Kind       string `json:"kind"`                                            // slug, added, or removed.
	Slug       string `json:"slug"`                                            // Actual published page address.
	Title      string `json:"title"`                                           // Actual published page title.
	DocTitle   string `json:"docTitle"`                                        // Title of the document that actually changed.
	DocSummary string `json:"docSummary"`                                      // One-sentence summary of the changed document.
	CreatedAt  int64  `json:"createdAt"`                                       // First write time in Unix milliseconds.
}

func (WikiFinalizeOp) TableName() string { return "knowledge_wiki_finalize_ops" }
