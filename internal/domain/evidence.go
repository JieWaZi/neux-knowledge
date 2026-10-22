package types

// SourceLocator expresses only locations actually confirmed by the Reader; unknown does not invent page numbers.
type SourceLocator struct {
	Kind      string `json:"kind"`                // page, line, section, range, or unknown.
	Page      int    `json:"page,omitempty"`      // Actual page number; 0 if unknown.
	StartLine int    `json:"startLine,omitempty"` // Starting line in the original text; 0 if unknown.
	EndLine   int    `json:"endLine,omitempty"`   // Ending line in the original text.
	Section   string `json:"section,omitempty"`   // Actual section location.
}

// SourceRef pins immutable source attribution and optional excerpts supporting a claim.
type SourceRef struct {
	SourceID        string        `json:"sourceId"`        // Stable source identifier.
	SourceVersionID string        `json:"sourceVersionId"` // Immutable source version.
	SHA256          string        `json:"sha256"`          // Byte digest of the complete original.
	Locator         SourceLocator `json:"locator"`         // Confirmed location or unknown; zero value for document-level attribution.
	Excerpt         string        `json:"excerpt"`         // Actual original text excerpt; document-level attribution does not store model-generated excerpts.
	ExcerptHash     string        `json:"excerptHash"`     // SHA-256 of the excerpt's UTF-8 bytes; empty for document-level attribution.
	ClaimKind       string        `json:"claimKind"`       // source_statement, inference, disagreement, or document_context, which identifies only the source.
}

// CodeRef pins the repository and commit for software implementation evidence.
type CodeRef struct {
	Repository string `json:"repository"`          // Authorized code repository identifier.
	Commit     string `json:"commit"`              // Commit actually read.
	Path       string `json:"path"`                // Repository-relative path.
	Symbol     string `json:"symbol,omitempty"`    // Confirmed symbol.
	StartLine  int    `json:"startLine,omitempty"` // Starting line.
	EndLine    int    `json:"endLine,omitempty"`   // Ending line.
	VerifiedAt int64  `json:"verifiedAt"`          // Actual verification time in Unix milliseconds.
}

// PageEvidence is the sole authoritative evidence; legacy SourceRefs is derived only from it.
type PageEvidence struct {
	Sources []SourceRef `json:"sources"` // Original-source evidence.
	Code    []CodeRef   `json:"code"`    // Implementation evidence.
}
