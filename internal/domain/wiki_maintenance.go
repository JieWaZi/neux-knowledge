package types

// ReplacePageTextInput exactly replaces all matching text in current content while preserving other manual protection.
type ReplacePageTextInput struct {
	RequestID             string        // Stable idempotent request identifier.
	LibraryID             string        // Authorized target library.
	PageID                string        // Stable page identifier.
	ExpectedVersion       int           // Current content version.
	StructureVersion      int64         // Current directory generation.
	ExpectedPolicyVersion int64         // Frozen permission generation; zero adds no extra freeze.
	OldText               string        // Exact original text that must actually match.
	NewText               string        // Replacement content; empty explicitly deletes the matches.
	Evidence              *PageEvidence // Optional complete evidence replacement; omission preserves existing evidence.
}

// RenamePageInput changes the readable page identifier while preserving page identity and atomically cascading links.
type RenamePageInput struct {
	RequestID             string // Stable idempotent request identifier.
	LibraryID             string // Authorized target library.
	PageID                string // Stable page identifier.
	ExpectedVersion       int    // Current content version.
	StructureVersion      int64  // Current directory generation.
	ExpectedPolicyVersion int64  // Frozen permission generation; zero adds no extra freeze.
	NewSlug               string // New readable identifier unique within the library.
}

// DeletePageInput archives a page and atomically removes current inbound links while retaining history and evidence.
type DeletePageInput struct {
	RequestID             string // Stable idempotent request identifier.
	LibraryID             string // Authorized target library.
	PageID                string // Stable page identifier.
	ExpectedVersion       int    // Current content version.
	StructureVersion      int64  // Current directory generation.
	ExpectedPolicyVersion int64  // Frozen permission generation; zero adds no extra freeze.
}
