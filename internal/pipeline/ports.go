// Package pipeline provides Lite batching, extraction, and reduction logic.
package pipeline

import (
	"context"
	"encoding/json"
	types "github.com/JieWaZi/neux-knowledge/internal/domain"
	"github.com/JieWaZi/neux-knowledge/internal/modelcontext"
)

// ReadRequest fixes the immutable source allowed for this round without granting arbitrary filesystem access.
type ReadRequest struct {
	SourceID        string `json:"sourceId"`        // Stable source identity.
	SourceVersionID string `json:"sourceVersionId"` // Immutable source version.
	SHA256          string `json:"sha256"`          // Digest of the original bytes.
	MIME            string `json:"mime"`            // Actual media type.
	Filename        string `json:"filename"`        // Original filename, used only to select reader capabilities.
	Raw             []byte `json:"raw,omitempty"`   // Complete original bytes; a filename cannot stand in for source content.
	Scope           string `json:"scope"`           // Explicitly requested scope; initially all.
	MaxRunes        int    `json:"maxRunes"`        // Actual reading budget; exceeding it must return partial.
}

// Chunk preserves stable chunk ordering; each chunk binds to the position actually read by the reader.
type Chunk struct {
	EndAt      int                 `json:"endAt"`   // Exclusive end rune offset of this chunk in the source.
	ID         string              `json:"id"`      // Stable chunk identity within this source.
	ChunkIndex int                 `json:"index"`   // Source order.
	StartAt    int                 `json:"startAt"` // Rune offset in the source.
	Content    string              `json:"content"` // Source text actually read.
	ChunkType  string              `json:"type"`    // Currently text only.
	Locator    types.SourceLocator `json:"locator"` // Verified position.
}

// ReadArtifact records actual reader coverage without upgrading partial to complete.
type ReadArtifact struct {
	SourceVersionID string          `json:"sourceVersionId"` // Immutable version actually read.
	SHA256          string          `json:"sha256"`          // Digest of the bytes actually read.
	Status          string          `json:"status"`          // complete, partial, unsupported, or failed.
	Scope           string          `json:"scope"`           // Requested scope actually executed.
	Blocks          []*Chunk        `json:"blocks"`          // Source evidence chunks; fabricated page numbers are forbidden.
	Coverage        json.RawMessage `json:"coverage"`        // Verifiable coverage and reader capabilities.
	Diagnostic      string          `json:"diagnostic"`      // Actual limitation or error.
}

// ReaderPort supplies real host capabilities for PDF/DOCX and other formats; Markdown can use MarkdownReader.
type ReaderPort interface {
	Read(context.Context, ReadRequest) (ReadArtifact, error)
}

// ModelMessage contains an explicit role and text for one independent model request.
type ModelMessage struct {
	Role    string `json:"role"`    // system, user, or assistant for this continuation.
	Content string `json:"content"` // Complete text for this request.
}

// GenerateRequest is the knowledge core's sole model port; it grants no tool or file permissions.
type GenerateRequest struct {
	JobID           string         `json:"jobId"`                     // Owning knowledge task.
	SourceVersionID string         `json:"sourceVersionId,omitempty"` // Immutable version for a single-source stage.
	StageID         string         `json:"stageId"`                   // Diagnostic identity for this knowledge-only call.
	Stage           string         `json:"stage"`                     // map, cite, dedup, summary, taxonomy, reduce, or index.
	Messages        []ModelMessage `json:"messages"`                  // Independent context for this call; history must not be appended across calls.
	MaxTokens       int            `json:"maxTokens"`                 // Upstream completion budget.
	Temperature     float64        `json:"temperature"`               // Upstream temperature.
	Thinking        bool           `json:"thinking"`                  // Thinking is disabled by default.
}

// GenerateResult retains raw model output, stop reason, and available actual usage.
type GenerateResult struct {
	Diagnostics  *types.ModelDiagnostics `json:"diagnostics,omitempty"` // Actual CLI capabilities and configuration diagnostics for this call.
	Text         string                  `json:"text"`                  // Complete text returned directly by the model, parsed by the core.
	FinishReason string                  `json:"finishReason"`          // Native model stop reason.
	Usage        *types.ModelUsage       `json:"usage,omitempty"`       // Native usage; nil when unknown.
}

// Generator calls are independent; cancellation propagates through the supplied context.
type Generator interface {
	Generate(context.Context, GenerateRequest) (GenerateResult, error)
}

// Input specifies the persistently accepted task and external capabilities.
type Input struct {
	OnProgress      func(context.Context)                                             // Existing resource-change notification after source start or requeue facts are committed.
	Instruction     string                                                            // Explicit additional user requirements for this run, scoped only to the current task's business instructions.
	Embed           func(context.Context, []string) ([][]float32, error)              // Frozen embedding capability, used only for folder similarity ranking.
	JobID           string                                                            // Accepted knowledge task.
	Reader          ReaderPort                                                        // Actual capability for reading source files.
	Generator       Generator                                                         // Direct model text port.
	PrepareDocument func(context.Context, string, ReadArtifact) (ReadArtifact, error) // Actual chunking and vector indexing.
	PublishGuard    func(context.Context) (func(), error)                             // Host authorization gate for publication.
	OnPublished     func(context.Context, *types.Receipt)                             // Notification after an actual commit.
	MaxReadRunes    int                                                               // Source reading limit; zero means unlimited.
}

// Result returns the persistent task and actual receipt.
type Result struct {
	Observer bool           `json:"observer,omitempty"` // An existing organization run holds this library's execution slot.
	JobID    string         `json:"jobId"`              // Knowledge task identifier.
	State    string         `json:"state"`              // Persistent task state.
	Receipt  *types.Receipt `json:"receipt,omitempty"`  // Last committed receipt.
}

// extractedItem preserves upstream candidate semantics; its type is fixed in updates.
type extractedItem struct {
	Name         string   `json:"name"`                    // Knowledge name.
	Slug         string   `json:"slug"`                    // Stable semantic slug.
	Aliases      []string `json:"aliases,omitempty"`       // Verified aliases.
	Description  string   `json:"description"`             // Page scope description.
	Details      string   `json:"details"`                 // Source-supported details, used as synthesis input following the upstream fallback when chunk citations are absent.
	SourceChunks []string `json:"source_chunks,omitempty"` // Actual evidence chunks.
}

// newSlugFromCitation preserves the upstream contract for knowledge discovered during citation.
type newSlugFromCitation struct {
	Type         string   `json:"type,omitempty"`          // Page type.
	Name         string   `json:"name"`                    // Knowledge name.
	Slug         string   `json:"slug"`                    // Stable semantic slug.
	Aliases      []string `json:"aliases,omitempty"`       // Aliases.
	Description  string   `json:"description"`             // Scope description.
	Details      string   `json:"details"`                 // Candidate description.
	SourceChunks []string `json:"source_chunks,omitempty"` // Actual evidence chunk handles.
}

// SlugUpdate is a knowledge update from one actual source.
type SlugUpdate struct {
	Slug              string `json:"slug"`              // Target page slug.
	DocTitle          string `json:"docTitle"`          // Document title.
	KnowledgeID       string `json:"knowledgeId"`       // Stable document identity, corresponding to the local SourceID.
	SourceRef         string `json:"sourceRef"`         // Original upstream document-level references.
	Language          string `json:"language"`          // Business language for this stage.
	SummaryBody       string `json:"summaryBody"`       // Document summary body.
	SummaryLine       string `json:"summaryLine"`       // One-sentence document summary.
	RetractDocContent string `json:"retractDocContent"` // Contributions from the old version to retract.
	DocSummary        string `json:"docSummary"`        // Document context provided to the editor.

	Type            string        `json:"type"`            // Specific page type.
	Item            extractedItem `json:"item"`            // Candidate knowledge.
	SourceVersionID string        `json:"sourceVersionId"` // Immutable source identity.
	SourceChunks    []string      `json:"sourceChunks"`    // Evidence chunks already parsed by the core.
}
type chunkBatch struct {
	chunks       []*Chunk                  // Source text for this batch.
	handles      *modelcontext.HandleTable // Mapping between short handles shown to generation and source chunks.
	totalRuneLen int                       // Actual character budget.
}
type wikiTaxonomyItem struct {
	slug     string // Semantic slug.
	title    string // Display name.
	pageType string // Page type.
	about    string // Scope description.
}
type wikiTaxonomyNode struct {
	children map[string]*wikiTaxonomyNode // Child folders.
}

const (
	maxRunesPerCitationBatch    = 12000
	maxCitationBatchConcurrency = 4
	dedupCandidateTopK          = 5
	dedupCandidateScoreFloor    = 0.08
	dedupSmallCorpusBypass      = 25
)
