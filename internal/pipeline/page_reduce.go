package pipeline

import (
	"context"
	"fmt"
	types "github.com/JieWaZi/neux-knowledge/internal/domain"
	"github.com/JieWaZi/neux-knowledge/internal/wiki"
	"github.com/google/uuid"
	"sort"
	"strings"
)

func (s *liteService) planTaxonomy(ctx context.Context, updates map[string][]SlugUpdate) (map[string]string, []types.ThemeMutation) {
	themes, err := s.store.ListThemes(ctx, s.job.LibraryID)
	if err != nil {
		return nil, nil
	}
	existing := [][]string{}
	for _, t := range themes {
		existing = append(existing, types.WikiFolderPathSegments(t.Path))
		if len(existing) >= 400 {
			break
		}
	}

	items := collectTaxonomyItems(updates)
	existing = s.selectRelevantFolders(ctx, s.library, items, existing)
	assignments := map[string][]string{}
	for start := 0; start < len(items); start += 60 {
		end := min(start+60, len(items))
		var block strings.Builder
		allowed := map[string]bool{}
		for _, it := range items[start:end] {
			allowed[it.slug] = true
			fmt.Fprintf(&block, "- slug: %s | title: %s | type: %s | about: %s\n", it.slug, it.title, it.pageType, truncateString(it.about, 120))
		}
		tree := formatExistingTaxonomyForPrompt(existing)
		if tree == "" {
			tree = "(none yet — this knowledge base has no folders, design a fresh directory)"
		}
		selected := map[string][]SlugUpdate{}
		for slug := range allowed {
			selected[slug] = updates[slug]
		}
		ids := []string{}
		for _, list := range selected {
			for _, u := range list {
				ids = appendUniqueString(ids, u.SourceVersionID)
			}
		}
		sort.Strings(ids)
		raw, err := s.generate(ctx, "taxonomy", "", ids, wiki.WikiTaxonomyPlanPrompt, map[string]string{"ExistingTaxonomy": tree, "Items": block.String(), "Language": language(s.library.WikiLanguage)})
		if err != nil {
			continue
		}
		for slug, path := range parseTaxonomyAssignments(raw) {
			if !allowed[slug] {
				continue
			}
			clean := types.CleanWikiCategoryPath(path)
			assignments[slug] = clean
			if len(clean) > 0 {
				existing = append(existing, clean)
			}
		}
	}
	cache := map[string]string{}
	for _, t := range themes {
		cache[strings.ToLower(t.Path)] = t.ID
	}
	placements := map[string]string{}
	mutations := []types.ThemeMutation{}
	for _, slug := range sortedKeys(assignments) {
		path := assignments[slug]
		parent := ""
		for depth := range path {
			key := strings.Join(path[:depth+1], "/")
			fold := strings.ToLower(key)
			id := cache[fold]
			if id == "" {
				id = uuid.NewSHA1(uuid.NameSpaceURL, []byte(s.job.LibraryID+"/folder/"+fold)).String()
				cache[fold] = id
				mutations = append(mutations, types.ThemeMutation{Theme: types.WikiFolder{ID: id, KnowledgeBaseID: s.job.LibraryID, Name: path[depth], ParentID: parent, Path: key, Depth: depth + 1}})
			}
			parent = id
		}
		placements[slug] = parent
	}
	return placements, mutations
}

func appendEvidence(refs []types.SourceRef, ref types.SourceRef) []types.SourceRef {
	for _, prior := range refs {
		if prior.SourceVersionID == ref.SourceVersionID && prior.ExcerptHash == ref.ExcerptHash && prior.ClaimKind == ref.ClaimKind {
			return refs
		}
	}
	return append(refs, ref)
}
