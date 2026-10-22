package types

// PurgePreviewInput explicitly selects one stable source in a library, including all its versions.
type PurgePreviewInput struct {
	LibraryID string `json:"libraryId"` // Library owning the source.
	SourceID  string `json:"sourceId"`  // Stable source identifier, rather than a single version.
}

// PurgeVersion returns unavailable identifiers and concurrency generations without filenames or original text.
type PurgeVersion struct {
	VersionID  string `json:"versionId"`  // Immutable source version to purge.
	Version    int64  `json:"version"`    // Generation of the source original.
	RowVersion int64  `json:"rowVersion"` // Generation of source processing and archive state.
}

// PurgePageImpact pins the referenced page version and content retention policy.
type PurgePageImpact struct {
	PageID       string `json:"pageId"`       // Currently affected page.
	Version      int    `json:"version"`      // Page version read by the preview.
	PreserveBody bool   `json:"preserveBody"` // Retain current content for manual or multi-source knowledge; otherwise archive and clear it.
}

// SourcePurgePreview previews impact without original text; Digest binds actual library records and the policy snapshot.
type SourcePurgePreview struct {
	LibraryID          string            `json:"libraryId"`          // Target library identifier.
	SourceID           string            `json:"sourceId"`           // Stable source identifier whose versions will all be deleted.
	LibraryVersion     int64             `json:"libraryVersion"`     // Library configuration version.
	StructureVersion   int64             `json:"structureVersion"`   // Directory structure version.
	PolicyVersion      int64             `json:"policyVersion"`      // Current authorization generation.
	Versions           []PurgeVersion    `json:"versions"`           // All source versions to purge.
	Pages              []PurgePageImpact `json:"pages"`              // Actual affected pages and their content retention policies.
	RevisionCount      int               `json:"revisionCount"`      // Number of historical revisions whose potential original text will be cleared.
	JobCount           int               `json:"jobCount"`           // Number of jobs in the same library whose internal diagnostics will be cleared.
	StageCount         int               `json:"stageCount"`         // Number of stages in the same library whose input and diagnostics will be cleared.
	PendingImportCount int               `json:"pendingImportCount"` // Number of registered source imports not yet completed.
	IssueCount         int               `json:"issueCount"`         // Number of issue records in the same library to clear.
	BlobCount          int               `json:"blobCount"`          // Number of distinct original objects.
	SharedBlobCount    int               `json:"sharedBlobCount"`    // Number of objects still used by other sources.
	RawOwnership       string            `json:"rawOwnership"`       // Always verified locally; remote implementations return actual ownership diagnostics.
	BlockingJobIDs     []string          `json:"blockingJobIds"`     // Active jobs in the same library, or jobs owning stages that are still executing.
	Digest             string            `json:"digest"`             // SHA-256 of the impact snapshot, without readable original text.
	Boundaries         []string          `json:"boundaries"`         // Known retention and physical erasure limits.
}

// ConfirmSourcePurgeInput accepts only explicit boolean confirmation and the original preview digest.
type ConfirmSourcePurgeInput struct {
	RequestID     string `json:"requestId"`     // Idempotent identifier for the confirmation operation.
	LibraryID     string `json:"libraryId"`     // Target library identifier.
	SourceID      string `json:"sourceId"`      // Target stable source.
	PreviewDigest string `json:"previewDigest"` // Impact digest verified by the user.
	Confirmed     bool   `json:"confirmed"`     // Must explicitly be true.
}

// RecoverSourcePurgeInput resumes the same persistent cleanup manifest after reacquiring current authorization.
type RecoverSourcePurgeInput struct {
	RequestID             string `json:"requestId"`             // Idempotent identifier for this recovery operation.
	LibraryID             string `json:"libraryId"`             // Library owning the original purge.
	PurgeID               string `json:"purgeId"`               // Accepted purge identifier.
	ExpectedPolicyVersion int64  `json:"expectedPolicyVersion"` // Authorization generation actually obtained for this recovery.
}

// PurgeBlobCleanup internally persists only content-addressed keys and cleanup progress, without original bytes.
type PurgeBlobCleanup struct {
	Key   string `json:"key"`   // SHA-256 object key of the managed original.
	State string `json:"state"` // pending, deleted, or retained_shared.
	Kind  string `json:"kind"`  // raw or temporary, for content objects or exact temporary import objects respectively.
}

// ImportBlobIntent pins import identity before writing files so unregistered objects remain attributable after interruption.
type ImportBlobIntent struct {
	RequestID string `gorm:"primaryKey"` // Original import request identifier.
	Digest    string // Complete original request digest, without original bytes.
	LibraryID string `gorm:"index"` // Target library identifier.
	SourceID  string `gorm:"index"` // Stable source identifier pinned in advance.
	RawKey    string // Expected original content digest key; cleared after purge confirmation.
	TempKey   string // Exact temporary import filename; cleared after purge confirmation.
	PurgeID   string // A nonempty value means the request is permanently revoked, retaining only a minimal idempotency tombstone.
	CreatedAt int64  // Intent registration time in Unix milliseconds.
}

func (ImportBlobIntent) TableName() string { return "knowledge_import_blob_intents" }

// SourcePurgeRecord owns the persistent manifest for logical and disk cleanup; it is not returned directly by the API.
type SourcePurgeRecord struct {
	ID            string             `gorm:"primaryKey"`  // Stable purge identifier.
	LibraryID     string             `gorm:"index"`       // Library owning the original source.
	SourceID      string             `gorm:"uniqueIndex"` // Revoked stable source identifier.
	Version       int64              // Optimistic lock for cleanup progress.
	State         string             // Locally pending or completed; the public protocol reserves blocked for remote use.
	PreviewDigest string             // Complete impact digest verified at confirmation.
	Versions      []PurgeVersion     `gorm:"serializer:json"` // Revoked version identifiers.
	Objects       []PurgeBlobCleanup `gorm:"serializer:json"` // Original objects and their individual cleanup states.
	PageIDs       []string           `gorm:"serializer:json"` // Identifiers of pages whose content or evidence was updated.
	CreatedAt     int64              // Confirmation commit time in Unix milliseconds.
	UpdatedAt     int64              // Most recent persisted recovery time in Unix milliseconds.
}

func (SourcePurgeRecord) TableName() string { return "knowledge_source_purges" }

// SourceTombstone retains only minimal unavailable source identifiers, without filenames, digests, excerpts, or original text.
type SourceTombstone struct {
	VersionID string `gorm:"primaryKey"` // Revoked immutable version identifier.
	SourceID  string `gorm:"index"`      // Revoked stable source identifier.
	LibraryID string `gorm:"index"`      // Original owning library.
	PurgeID   string // Persistent manifest responsible for cleanup.
}

func (SourceTombstone) TableName() string { return "knowledge_source_tombstones" }

// SourcePurgeStatus returns only verifiable cleanup facts and does not claim physical media erasure.
type SourcePurgeStatus struct {
	ID                      string         `json:"id"`                      // Purge identifier.
	LibraryID               string         `json:"libraryId"`               // Original owning library.
	SourceID                string         `json:"sourceId"`                // Revoked source identifier.
	Version                 int64          `json:"version"`                 // Cleanup progress version.
	State                   string         `json:"state"`                   // Locally pending or completed; the public protocol reserves blocked for remote use.
	Versions                []PurgeVersion `json:"versions"`                // Unavailable source versions.
	DeletedBlobCount        int            `json:"deletedBlobCount"`        // Number of nonshared originals deleted or already absent.
	RetainedSharedBlobCount int            `json:"retainedSharedBlobCount"` // Number of originals retained because other sources use them.
	BlockedBlobCount        int            `json:"blockedBlobCount"`        // Always 0 locally; remote implementations return actual blocking diagnostics.
	PendingBlobCount        int            `json:"pendingBlobCount"`        // Number of originals still requiring actual processing.
	PageIDs                 []string       `json:"pageIds"`                 // Updated knowledge page identifiers.
	Boundaries              []string       `json:"boundaries"`              // Retention limits for manual content, backups, physical media, and similar resources.
}
