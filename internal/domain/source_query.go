package types

// SourceQuery specifies display-state filters and global pagination for nonarchived sources.
type SourceQuery struct {
	Offset int    // Starting offset of matching results; negative values are treated as zero.
	Limit  int    // Page size; zero uses the default, with a maximum of 100.
	Query  string // Case-insensitive literal substring condition for filenames.
	State  string // Display state: all, pending, queued, processing, processed, unchanged, failed, or cancelled.
	Sort   string // Creation time order: newest or oldest, with identifier-based stable ordering for ties.
}
