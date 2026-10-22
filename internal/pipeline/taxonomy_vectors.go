package pipeline

import (
	"context"
	types "github.com/JieWaZi/neux-knowledge/internal/domain"
	"math"
	"sort"
	"strings"
)

const (
	wikiTaxonomyFeedAllMaxFolders = 60
	wikiTaxonomyPromptMaxPaths    = 150
	wikiTaxonomyRelevantTopK      = 3
)

func (s *liteService) selectRelevantFolders(ctx context.Context, kb *types.Library, items []wikiTaxonomyItem, pool [][]string) [][]string {
	if len(pool) <= wikiTaxonomyFeedAllMaxFolders {
		return pool
	}

	// Split into always-kept level-1 anchors and the deeper candidate folders
	// that similarity selects among.
	l1Seen := make(map[string]struct{})
	var l1Paths, deeper [][]string
	for _, p := range pool {
		if len(p) == 0 {
			continue
		}
		if _, ok := l1Seen[p[0]]; !ok {
			l1Seen[p[0]] = struct{}{}
			l1Paths = append(l1Paths, []string{p[0]})
		}
		if len(p) >= 2 {
			deeper = append(deeper, p)
		}
	}

	// Gate purely on whether an embedding model is configured — NOT on
	// NeedsEmbeddingModel(), which is false for wiki-only KBs that may still
	// opt into an embedding model purely for directory/taxonomy similarity.
	if strings.TrimSpace(kb.EmbeddingModelID) == "" || len(deeper) == 0 {
		return capFolders(pool, wikiTaxonomyPromptMaxPaths)
	}

	if s.input.Embed == nil {
		return capFolders(pool, wikiTaxonomyPromptMaxPaths)
	}
	folderTexts := make([]string, len(deeper))
	for i, p := range deeper {
		folderTexts[i] = strings.Join(p, " / ")
	}
	itemTexts := make([]string, len(items))
	for i, it := range items {
		itemTexts[i] = strings.TrimSpace(it.title + " " + previewText(it.about, 120))
	}

	// Through the pool, not straight at the provider: BatchEmbedWithPool is
	// what splits the inputs by BATCH_EMBED_SIZE. Calling BatchEmbed directly
	// put every folder (or item) into one request, which a provider with a
	// per-request input-count limit rejects however low that setting is
	// (#3390).
	folderVecs, err := s.embedTaxonomy(ctx, folderTexts)
	if err != nil {
		logWarnf(ctx, "wiki ingest: taxonomy plan folder embed failed, feeding all folders: %v", err)
		return capFolders(pool, wikiTaxonomyPromptMaxPaths)
	}
	itemVecs, err := s.embedTaxonomy(ctx, itemTexts)
	if err != nil {
		logWarnf(ctx, "wiki ingest: taxonomy plan item embed failed, feeding all folders: %v", err)
		return capFolders(pool, wikiTaxonomyPromptMaxPaths)
	}

	selected := append(l1Paths, selectFoldersByVectors(deeper, folderVecs, itemVecs, wikiTaxonomyRelevantTopK)...)
	return capFolders(selected, wikiTaxonomyPromptMaxPaths)
}

// selectFoldersByVectors returns the deeper folders that rank in any item's
// top-K by cosine similarity, preserving the input order for determinism.
func selectFoldersByVectors(deeper [][]string, folderVecs, itemVecs [][]float32, topK int) [][]string {
	if len(deeper) != len(folderVecs) || len(itemVecs) == 0 || topK <= 0 {
		return nil
	}
	chosen := make(map[int]struct{})
	for _, iv := range itemVecs {
		type scored struct {
			idx int
			sim float64
		}
		ranking := make([]scored, 0, len(folderVecs))
		for fi, fv := range folderVecs {
			ranking = append(ranking, scored{fi, cosineSimilarity(iv, fv)})
		}
		sort.SliceStable(ranking, func(a, b int) bool { return ranking[a].sim > ranking[b].sim })
		for k := 0; k < topK && k < len(ranking); k++ {
			chosen[ranking[k].idx] = struct{}{}
		}
	}
	out := make([][]string, 0, len(chosen))
	for i := range deeper {
		if _, ok := chosen[i]; ok {
			out = append(out, deeper[i])
		}
	}
	return out
}

// cosineSimilarity returns the cosine of two equal-length vectors, or 0 for empty
// / mismatched / zero-norm inputs.
func cosineSimilarity(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// capFolders truncates a folder list to at most max entries (max <= 0 = no cap).
func capFolders(paths [][]string, max int) [][]string {
	if max > 0 && len(paths) > max {
		return paths[:max]
	}
	return paths
}

// collectTaxonomyItems extracts the entity/concept pages from a batch's slug
// updates, in deterministic slug order so chunk boundaries are stable. Summary
// and retract-only slugs are skipped (they carry no directory category).

func (s *liteService) embedTaxonomy(ctx context.Context, texts []string) ([][]float32, error) {
	result := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += 5 {
		end := min(start+5, len(texts))
		vectors, err := s.input.Embed(ctx, texts[start:end])
		if err != nil {
			return nil, err
		}
		if len(vectors) != end-start {
			return nil, invalid("taxonomy embedding count mismatch")
		}
		result = append(result, vectors...)
	}
	return result, nil
}
