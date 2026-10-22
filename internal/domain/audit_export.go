package types

// LintIssue is a read-only review finding and grants no implicit repair authority.
type LintIssue struct {
	Type        string `json:"type"`               // orphan_page, broken_link, empty_content, or stale_ref.
	Severity    string `json:"severity"`           // warning or error, for manual review only.
	PageID      string `json:"pageId"`             // Stable identifier of the reviewed page.
	PageSlug    string `json:"pageSlug"`           // Readable address of the reviewed page.
	PageVersion int    `json:"pageVersion"`        // Page version in this snapshot.
	TargetID    string `json:"targetId,omitempty"` // Page address or source version identifier targeted by the issue.
	Description string `json:"description"`        // Chinese issue description based on the actual snapshot.
}

// LintReport pins the library version and all findings from one consistent read without persisting a second source of issue facts.
type LintReport struct {
	LibraryID        string      `json:"libraryId"`        // Knowledge library under review.
	LibraryVersion   int64       `json:"libraryVersion"`   // Library configuration version.
	StructureVersion int64       `json:"structureVersion"` // Navigation structure version.
	PolicyVersion    int64       `json:"policyVersion"`    // Permission policy version for this read.
	PageCount        int         `json:"pageCount"`        // Number of nonarchived pages actually reviewed.
	Issues           []LintIssue `json:"issues"`           // Issues sorted deterministically by page and rule.
}

// ExportInput selects published pages in one library; an empty set selects all published pages.
type ExportInput struct {
	LibraryID string   `json:"libraryId"`         // Knowledge library to export.
	PageIDs   []string `json:"pageIds,omitempty"` // Explicit page identifiers; at most 1000 entries with no duplicates.
}

// ExportSourceRef exports only source identifiers and locations, without originals or text excerpts.
type ExportSourceRef struct {
	SourceID        string        `json:"sourceId"`           // Stable source identifier bound by the page's original evidence.
	SourceVersionID string        `json:"sourceVersionId"`    // Immutable version bound by the page's original evidence.
	Version         int64         `json:"version,omitempty"`  // Source generation still present in the snapshot.
	SHA256          string        `json:"sha256"`             // Source byte digest retained in the page's original evidence.
	Filename        string        `json:"filename,omitempty"` // Source file display name in the snapshot; it cannot serve as an output path.
	Locator         SourceLocator `json:"locator"`            // Actual location saved on the page, or unknown.
	ClaimKind       string        `json:"claimKind"`          // Claim kind saved on the page.
}

// ExportPageMetadata is derived from pages, the library, and themes in one transaction for both frontmatter and the manifest.
type ExportPageMetadata struct {
	LibraryID         string             `json:"libraryId"`                   // Owning library identifier.
	LibraryVersion    int64              `json:"libraryVersion"`              // Library configuration version.
	StructureVersion  int64              `json:"structureVersion"`            // Navigation structure version.
	PolicyVersion     int64              `json:"policyVersion"`               // Permission policy version.
	PageID            string             `json:"pageId"`                      // Stable page identifier.
	PageVersion       int                `json:"pageVersion"`                 // Published content version.
	Slug              string             `json:"slug"`                        // Page address, for display and relationship mapping only.
	Title             string             `json:"title"`                       // Published title.
	PageType          string             `json:"pageType"`                    // Page knowledge type.
	WikiLanguage      string             `json:"wikiLanguage"`                // Published content language.
	ThemeID           string             `json:"themeId"`                     // Current primary theme identifier.
	ThemePath         []string           `json:"themePath"`                   // Name chain derived from actual theme ancestry.
	SortOrder         int                `json:"sortOrder"`                   // Page order within its current theme.
	Sources           []ExportSourceRef  `json:"sources"`                     // Source identifiers and locations, without original text.
	Code              []CodeRef          `json:"code"`                        // Saved software implementation evidence.
	SourcePurgeReview *SourcePurgeReview `json:"sourcePurgeReview,omitempty"` // Purge marker containing only review facts and unavailable identifiers.
}

// MarkdownFile is a transferable export copy; editing it does not write back to SQLite.
type MarkdownFile struct {
	Path     string             `json:"path"`               // Safe filename based on a digest of the page identifier.
	SHA256   string             `json:"sha256"`             // Byte digest of the complete Markdown file.
	Metadata ExportPageMetadata `json:"metadata"`           // Snapshot identity used to generate this file.
	Markdown string             `json:"markdown,omitempty"` // YAML frontmatter plus original knowledge content, represented as JSON.
}

// MarkdownExport contains an export manifest and Markdown files from one consistent SQLite snapshot.
type MarkdownExport struct {
	FormatVersion    int            `json:"formatVersion"`    // Current export format version, fixed at 1.
	LibraryID        string         `json:"libraryId"`        // The single source knowledge library for this export.
	LibraryVersion   int64          `json:"libraryVersion"`   // Library configuration version in the consistent snapshot.
	StructureVersion int64          `json:"structureVersion"` // Navigation version in the consistent snapshot.
	PolicyVersion    int64          `json:"policyVersion"`    // Permission policy version in the consistent snapshot.
	Files            []MarkdownFile `json:"files"`            // Published files sorted by stable page identifier.
}
