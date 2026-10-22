package types

// ParserEngineRule maps a set of file types to a specific parser engine.
type ParserEngineRule struct {
	// FileTypes contains matching file extensions.
	FileTypes []string `yaml:"file_types" json:"file_types" extensions:"x-agent"`
	// Engine identifies the selected parser engine.
	Engine string `yaml:"engine"     json:"engine" extensions:"x-agent"`
	// XLSXFirstRowAsHeader restores row-1 column context for flat XLSX tables.
	// nil preserves the parser default; an explicit false disables the mode.
	// XLSXFirstRowAsHeader uses the first spreadsheet row as column names; omission preserves the parser default.
	XLSXFirstRowAsHeader *bool `yaml:"xlsx_first_row_as_header,omitempty" json:"xlsx_first_row_as_header,omitempty" extensions:"x-agent"`
}

// ChunkingConfig represents the document splitting configuration
type ChunkingConfig struct {
	// Chunk size in characters. Empty on create uses the server default.
	// ChunkSize is the target character count for ordinary chunks; zero uses the upstream default.
	ChunkSize int `yaml:"chunk_size"    json:"chunk_size" minimum:"100" maximum:"10000" extensions:"x-agent"`
	// Chunk overlap in characters.
	// ChunkOverlap is the character overlap between adjacent chunks; upstream normalization treats zero as the default.
	ChunkOverlap int `yaml:"chunk_overlap" json:"chunk_overlap" minimum:"0" maximum:"5000" extensions:"x-agent"`
	// Separators, in the order they are tried.
	// Separators contains text delimiters tried in priority order.
	Separators []string `yaml:"separators"    json:"separators" extensions:"x-agent,x-agent-maximum=20"`
	// ParserEngineRules configures which parser engine to use for each file type.
	// When empty, DefaultParserEngine is used (builtin/simple routing, except
	// types that only a specific engine can parse: ppt/pptx fall back to
	// markitdown). A linked anydoc binding is preferred for every type it
	// converts.
	// ParserEngineRules contains complete file-type parser selection rules; this core does not yet support nonempty rules.
	ParserEngineRules []ParserEngineRule `yaml:"parser_engine_rules,omitempty" json:"parser_engine_rules,omitempty" extensions:"x-agent,x-agent-response"`
	// EnableParentChild splits into large parent chunks and small child chunks.
	// Search matches a child and returns its parent.
	// EnableParentChild splits context-providing parent chunks from retrieval child chunks.
	EnableParentChild bool `yaml:"enable_parent_child,omitempty" json:"enable_parent_child,omitempty" extensions:"x-agent"`
	// ParentChunkSize is the size of parent chunks (default: 4096).
	// Only used when EnableParentChild is true.
	// ParentChunkSize is the target parent chunk character count, defaulting to 4096.
	ParentChunkSize int `yaml:"parent_chunk_size,omitempty" json:"parent_chunk_size,omitempty" minimum:"1" maximum:"20000" extensions:"x-agent"`
	// ChildChunkSize is the size of child chunks used for embedding (default: 384).
	// Only used when EnableParentChild is true.
	// ChildChunkSize is the target child chunk character count, defaulting to 384.
	ChildChunkSize int `yaml:"child_chunk_size,omitempty" json:"child_chunk_size,omitempty" minimum:"1" maximum:"10000" extensions:"x-agent"`
	// Strategy selects how documents are split: auto, heading, heuristic,
	// recursive or legacy. Empty keeps the server default.
	// Strategy selects adaptive, heading, heuristic, or recursive chunking.
	Strategy string `yaml:"strategy,omitempty" json:"strategy,omitempty" enums:"auto,heading,heuristic,recursive,legacy" extensions:"x-agent"`
	// TokenLimit caps chunk size in approximate tokens. 0 uses ChunkSize as a character count.
	// TokenLimit is an approximate token limit; zero uses the character budget.
	TokenLimit int `yaml:"token_limit,omitempty" json:"token_limit,omitempty" minimum:"0" maximum:"100000" extensions:"x-agent"`
	// Languages hints the splitter, for example de or zh. Empty detects the language from the document.
	// Languages supplies language hints for multilingual boundary detection.
	Languages []string `yaml:"languages,omitempty" json:"languages,omitempty" extensions:"x-agent,x-agent-maximum=20"`
	// TableMetadataInstructions is extra guidance for CSV and Excel summaries. The output format stays fixed.
	// TableMetadataInstructions supplies extra table-summary guidance; this core does not yet support nonempty guidance.
	TableMetadataInstructions string `yaml:"table_metadata_instructions,omitempty" json:"table_metadata_instructions,omitempty" extensions:"x-agent,x-agent-detail,x-agent-max-bytes=4000"`
}

// DefaultChunkingConfig returns upstream defaults for the new-library creation form.
// Explicit zero-value configuration is still normalized by the imported chunker without modification.
func DefaultChunkingConfig() ChunkingConfig {
	return ChunkingConfig{
		ChunkSize:         512,
		ChunkOverlap:      80,
		Separators:        []string{"\n\n", "\n", "。", "！", "？", ";", "；"},
		EnableParentChild: true,
		ParentChunkSize:   4096,
		ChildChunkSize:    384,
		Strategy:          "auto",
		Languages:         []string{},
	}
}

// ValidateChunkingConfig explicitly rejects parser and table-summary capabilities not yet integrated into the plain-text core.
// The upstream algorithm owns chunk size, overlap, and strategy defaults and guards; they are not duplicated here.
func ValidateChunkingConfig(cfg *ChunkingConfig) error {
	if cfg == nil {
		return nil
	}
	if len(cfg.ParserEngineRules) > 0 {
		return &Error{Code: ErrInvalid, Operation: "chunking.config", Detail: "parser_engine_rules require an unavailable parser engine"}
	}
	if cfg.TableMetadataInstructions != "" {
		return &Error{Code: ErrInvalid, Operation: "chunking.config", Detail: "table_metadata_instructions require an unavailable table metadata pipeline"}
	}
	return nil
}
