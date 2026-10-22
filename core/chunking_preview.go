// Only the HTTP wrapper is adapted into a standalone core function; algorithms, diagnostics, statistics, parent-child relationships, and boundaries retain their original rules.
package core

import (
	"context"
	"github.com/JieWaZi/neux-knowledge/internal/chunker"
	"math"
	"strings"
	"time"
	"unicode/utf8"
)

const previewMaxChunks = 500

// PreviewChunkingRequest is the body shape accepted by /chunker/preview.
// Text is checked manually below so we can return a friendlier error than
// gin's default "Field validation for 'Text' failed on the 'required' tag".
type PreviewChunkingRequest struct {
	// Sample text.
	Text string `json:"text"`
	// Chunking configuration.
	ChunkingConfig PreviewChunkingPayload `json:"chunking_config"`
}

// PreviewChunkingPayload mirrors the snake_case JSON the rest of the API
// uses for ChunkingConfig fields. We don't reuse types.ChunkingConfig
// directly because it carries unrelated parser rules and table-processing
// metadata that the preview path does not use.
type PreviewChunkingPayload struct {
	// Chunk size.
	ChunkSize int `json:"chunk_size"`
	// Chunk overlap.
	ChunkOverlap int `json:"chunk_overlap"`
	// Separators.
	Separators []string `json:"separators"`
	// Whether parent-child chunking is enabled.
	EnableParentChild bool `json:"enable_parent_child"`
	// Parent chunk size.
	ParentChunkSize int `json:"parent_chunk_size"`
	// Child chunk size.
	ChildChunkSize int `json:"child_chunk_size"`
	// Chunking strategy.
	Strategy string `json:"strategy"`
	// Token limit.
	TokenLimit int `json:"token_limit"`
	// Language hints.
	Languages []string `json:"languages"`
}

// PreviewChunkResult describes one chunk emitted during preview.
type PreviewChunkResult struct {
	// Chunk sequence number.
	Seq int `json:"seq"`
	// Starting character position.
	Start int `json:"start"`
	// Ending character position.
	End int `json:"end"`
	// Character count.
	SizeChars int `json:"size_chars"`
	// Estimated token count.
	SizeTokensApprox int `json:"size_tokens_approx"`
	// Context header.
	ContextHeader string `json:"context_header,omitempty"`
	// Chunk content.
	Content string `json:"content"`
}

// PreviewChunkingStats summarizes chunk-size distribution. Computed over
// the FULL chunk set, even when the response truncates to previewMaxChunks
// items, so avg/min/max/stddev reflect the real distribution, not just
// the first N.
type PreviewChunkingStats struct {
	// Total chunk count.
	Count int `json:"count"`
	// Mean character count.
	AvgChars int `json:"avg_chars"`
	// Minimum character count.
	MinChars int `json:"min_chars"`
	// Maximum character count.
	MaxChars int `json:"max_chars"`
	// Standard deviation of character counts.
	StddevChars int `json:"stddev_chars"`
	// TruncatedTo, when set, is the original chunk count before the
	// response was truncated to previewMaxChunks for transport.
	// Total chunk count before truncation.
	TruncatedTo int `json:"truncated_to,omitempty"`
}

// PreviewChunkingResponse is the body returned by /chunker/preview.
type PreviewChunkingResponse struct {
	// Selected strategy.
	SelectedTier chunker.StrategyTier `json:"selected_tier"`
	// Attempted strategy chain.
	TierChain []chunker.StrategyTier `json:"tier_chain"`
	// Rejected strategies and reasons.
	Rejected []chunker.TierRejection `json:"rejected"`
	// Document features.
	Profile *chunker.DocProfile `json:"profile"`
	// Preview chunks.
	Chunks []PreviewChunkResult `json:"chunks"`
	// Statistics for all chunks.
	Stats PreviewChunkingStats `json:"stats"`
}

// PreviewDocumentChunking provides the upstream read-only preview without database writes, embedding, or sample logging.
// The input is plain text from the local Reader; upstream OCR HTML table conversion is not applied.
func PreviewDocumentChunking(ctx context.Context, req PreviewChunkingRequest) (*PreviewChunkingResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.Text) == "" {
		return nil, &Error{Code: ErrInvalid, Operation: "chunking.preview", Detail: "text is empty"}
	}
	if utf8.RuneCountInString(req.Text) > 64*1024 {
		return nil, &Error{Code: ErrInvalid, Operation: "chunking.preview", Detail: "text exceeds preview limit"}
	}
	// Offsets must match the original text supplied by the current Reader, using the same input as actual ingestion.
	text := req.Text

	cfg := chunker.NormalizeSplitterConfig(chunker.SplitterConfig{
		ChunkSize:    req.ChunkingConfig.ChunkSize,
		ChunkOverlap: req.ChunkingConfig.ChunkOverlap,
		Separators:   req.ChunkingConfig.Separators,
		Strategy:     req.ChunkingConfig.Strategy,
		TokenLimit:   req.ChunkingConfig.TokenLimit,
		Languages:    req.ChunkingConfig.Languages,
	})

	// Run the splitter on a goroutine so we can honor the request timeout.
	// The splitter is CPU-bound and doesn't accept a context — wrapping
	// here is the cheapest cancellation we can offer.
	type splitResult struct {
		// chunks holds the actual chunks.
		chunks []chunker.Chunk
		// diag holds strategy selection diagnostics.
		diag *chunker.Diagnostics
	}
	resCh := make(chan splitResult, 1)
	go func() {
		var chunks []chunker.Chunk
		var diag *chunker.Diagnostics
		if req.ChunkingConfig.EnableParentChild {
			parentCfg, childCfg := chunker.DeriveParentChildConfigs(
				cfg,
				req.ChunkingConfig.ParentChunkSize,
				req.ChunkingConfig.ChildChunkSize,
			)
			result, parentDiag := chunker.SplitParentChildWithDiagnostics(text, parentCfg, childCfg)
			chunks = make([]chunker.Chunk, len(result.Children))
			for i, child := range result.Children {
				chunks[i] = child.Chunk
			}
			diag = parentDiag
		} else {
			chunks, diag = chunker.SplitWithDiagnostics(text, cfg)
		}
		resCh <- splitResult{chunks: chunks, diag: diag}
	}()

	var sr splitResult
	select {
	case sr = <-resCh:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	chunks, diag := sr.chunks, sr.diag

	// Diagnostics carries the profile when auto-strategy ran; for explicit
	// strategies the profile is nil and we materialize it here so the UI
	// can still show document stats. Avoids the previous double-pass.
	profile := diag.Profile
	if profile == nil {
		profile = chunker.ProfileDocument(text)
	}

	lang := chunker.LangMixed
	if len(profile.DetectedLangs) > 0 {
		lang = profile.DetectedLangs[0]
	}

	// Compute rune lengths once per chunk; reused for stats and result
	// payload below. Avoids the previous triple-pass over each chunk's
	// content (stats + result + ApproxTokenCount each rune-counted).
	runeLens := make([]int, len(chunks))
	for i, ch := range chunks {
		runeLens[i] = utf8.RuneCountInString(ch.Content)
	}

	// Compute stats over the FULL chunk set first so the metrics stay
	// representative even when we trim the response to previewMaxChunks.
	totalCount := len(chunks)
	stats := computeChunkSizeStats(runeLens)
	if totalCount > previewMaxChunks {
		stats.TruncatedTo = totalCount
		chunks = chunks[:previewMaxChunks]
		runeLens = runeLens[:previewMaxChunks]
	}

	results := make([]PreviewChunkResult, 0, len(chunks))
	for i, ch := range chunks {
		results = append(results, PreviewChunkResult{
			Seq:              ch.Seq,
			Start:            ch.Start,
			End:              ch.End,
			SizeChars:        runeLens[i],
			SizeTokensApprox: chunker.ApproxTokenCountFromRuneLen(runeLens[i], lang),
			ContextHeader:    ch.ContextHeader,
			Content:          ch.Content,
		})
	}

	resp := PreviewChunkingResponse{
		SelectedTier: diag.SelectedTier,
		TierChain:    diag.TierChain,
		Rejected:     diag.Rejected,
		Profile:      profile,
		Chunks:       results,
		Stats:        stats,
	}
	return &resp, nil
}

// computeChunkSizeStats summarizes count / avg / min / max / stddev from
// a pre-computed rune-length slice. Decoupling from chunker.Chunk lets
// the caller compute rune lengths once and reuse them for the response
// payload (avoids a second []rune allocation per chunk).
func computeChunkSizeStats(runeLens []int) PreviewChunkingStats {
	stats := PreviewChunkingStats{Count: len(runeLens)}
	if len(runeLens) == 0 {
		return stats
	}
	var sum, sumSq float64
	minLen, maxLen := math.MaxInt32, 0
	for _, l := range runeLens {
		sum += float64(l)
		sumSq += float64(l) * float64(l)
		if l < minLen {
			minLen = l
		}
		if l > maxLen {
			maxLen = l
		}
	}
	avg := sum / float64(len(runeLens))
	variance := sumSq/float64(len(runeLens)) - avg*avg
	if variance < 0 {
		// Float precision can push the variance slightly below zero on
		// near-uniform inputs; clamp so sqrt doesn't return NaN.
		variance = 0
	}
	stats.AvgChars = int(avg + 0.5)
	stats.MinChars = minLen
	stats.MaxChars = maxLen
	stats.StddevChars = int(math.Sqrt(variance) + 0.5)
	return stats
}
