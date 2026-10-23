// Copyright (C) 2026 Tencent. MIT，许可见 THIRD_PARTY_NOTICES.md。
package wiki

import (
	"fmt"
	types "github.com/JieWaZi/neux-knowledge/internal/domain"
	"sort"
	"strings"
)

// ComputeGraphSubset 进行概览排序、关系去重与邻域展开，不持有存储或模型。
func ComputeGraphSubset(pages []*types.WikiPage, req *types.WikiGraphRequest) (*types.WikiGraphData, error) {
	mode := req.Mode
	if mode == "" {
		mode = types.WikiGraphModeOverview
	}

	// Pre-compute link_count and the type allow-list used for candidate
	// filtering. We keep the full page list around so ego mode can still
	// traverse through neighbors whose type is in the allow-list.
	typeAllow := make(map[string]bool, len(req.Types))
	for _, t := range req.Types {
		if t != "" {
			typeAllow[t] = true
		}
	}
	hasTypeFilter := len(typeAllow) > 0
	// The index page is navigation over the whole wiki, not a subject. Its
	// directory is assembled on read, so it never carries links and would
	// only ever sit on the canvas as a lone node.
	if !typeAllow[types.WikiPageTypeIndex] {
		subjects := make([]*types.WikiPage, 0, len(pages))
		for _, p := range pages {
			if p.PageType != types.WikiPageTypeIndex {
				subjects = append(subjects, p)
			}
		}
		pages = subjects
	}

	familiarSet := make(map[string]struct{}, len(req.FamiliarKnowledgeIDs))
	for _, id := range req.FamiliarKnowledgeIDs {
		if id = strings.TrimSpace(id); id != "" {
			familiarSet[id] = struct{}{}
		}
	}

	pageBySlug := make(map[string]*types.WikiPage, len(pages))
	for _, p := range pages {
		pageBySlug[p.Slug] = p
	}
	// link_count is the number of distinct pages this page can be connected
	// to on the canvas: reciprocal links count once, and links to deleted or
	// type-filtered pages are excluded. The frontend compares it with the
	// visible degree to decide whether a page still has neighbors to load.
	// in_links can lag behind other pages' out_links, so links are collected
	// from both ends: edges are drawn from out_links, and a page someone links
	// to must not report zero links.
	neighbors := make(map[string]map[string]struct{}, len(pages))
	connect := func(a, b string) bool {
		pa, okA := pageBySlug[a]
		pb, okB := pageBySlug[b]
		if !okA || !okB || a == b {
			return false
		}
		if hasTypeFilter && (!typeAllow[pa.PageType] || !typeAllow[pb.PageType]) {
			return false
		}
		for _, pair := range [2][2]string{{a, b}, {b, a}} {
			set := neighbors[pair[0]]
			if set == nil {
				set = make(map[string]struct{})
				neighbors[pair[0]] = set
			}
			set[pair[1]] = struct{}{}
		}
		return true
	}
	for _, p := range pages {
		for _, target := range p.OutLinks {
			connect(p.Slug, target)
		}
		for _, source := range p.InLinks {
			connect(source, p.Slug)
		}
	}
	// A page extracted from a document belongs next to that document's
	// summary even when the summary text never links it, e.g. a name the
	// summary only writes as inline code, or a concept it never mentions by
	// title. Summary slugs are not always derived from the document ID, so
	// documents are matched through the summaries' own source_refs.
	summaryOf := make(map[string]string)
	for _, p := range pages {
		if p.PageType != types.WikiPageTypeSummary {
			continue
		}
		for _, id := range p.SourceKnowledgeIDs() {
			if prev, ok := summaryOf[id]; !ok || p.Slug < prev {
				summaryOf[id] = p.Slug
			}
		}
	}
	var sourceEdges [][2]string
	for _, p := range pages {
		if p.PageType == types.WikiPageTypeSummary {
			continue
		}
		for _, id := range p.SourceKnowledgeIDs() {
			if summary, ok := summaryOf[id]; ok && connect(summary, p.Slug) {
				sourceEdges = append(sourceEdges, [2]string{summary, p.Slug})
			}
		}
	}
	linkCount := make(map[string]int, len(pages))
	for slug, set := range neighbors {
		linkCount[slug] = len(set)
	}

	// Select the node slug set for the requested slice.
	var selected map[string]struct{}
	switch mode {
	case types.WikiGraphModeEgo:
		if req.Center == "" {
			return nil, &types.Error{Code: types.ErrInvalid, Operation: "graph", Detail: "ego graph requires a center slug"}
		}
		if _, ok := pageBySlug[req.Center]; !ok {
			return nil, &types.Error{Code: types.ErrInvalid, Operation: "graph", Detail: fmt.Sprintf("ego center slug %q not found", req.Center)}
		}
		depth := req.Depth
		if depth < 1 {
			depth = 1
		}
		selected = bfsEgoSlugs(pageBySlug, neighbors, req.Center, depth, typeAllow, req.Limit)
	default:
		// overview: keep only type-allowed candidates, sort by link_count desc, cap.
		candidates := make([]*types.WikiPage, 0, len(pages))
		for _, p := range pages {
			if hasTypeFilter && !typeAllow[p.PageType] {
				continue
			}
			candidates = append(candidates, p)
		}
		// Many low-degree pages tie at the truncation boundary. Prefer pages
		// this person relies on, then subject pages over navigation pages,
		// then recently updated ones; slug keeps the API deterministic.
		familiar := make(map[string]bool, len(candidates))
		for _, p := range candidates {
			familiar[p.Slug] = p.BuiltFrom(familiarSet)
		}
		sort.SliceStable(candidates, func(i, j int) bool {
			a, b := candidates[i], candidates[j]
			if la, lb := linkCount[a.Slug], linkCount[b.Slug]; la != lb {
				return la > lb
			}
			if familiar[a.Slug] != familiar[b.Slug] {
				return familiar[a.Slug]
			}
			if ra, rb := graphOverviewTypeRank(a.PageType), graphOverviewTypeRank(b.PageType); ra != rb {
				return ra < rb
			}
			if !a.UpdatedAt.Equal(b.UpdatedAt) {
				return a.UpdatedAt.After(b.UpdatedAt)
			}
			return a.Slug < b.Slug
		})
		if req.Limit > 0 && len(candidates) > req.Limit {
			candidates = candidates[:req.Limit]
		}
		selected = make(map[string]struct{}, len(candidates))
		for _, p := range candidates {
			selected[p.Slug] = struct{}{}
		}
	}

	// Build nodes from the selected set.
	nodes := make([]types.WikiGraphNode, 0, len(selected))
	for slug := range selected {
		p := pageBySlug[slug]
		nodes = append(nodes, types.WikiGraphNode{
			Slug:      p.Slug,
			Title:     p.Title,
			PageType:  p.PageType,
			LinkCount: linkCount[slug],
			Familiar:  p.BuiltFrom(familiarSet),
		})
	}
	// Deterministic node ordering — the map iteration above is random.
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].LinkCount != nodes[j].LinkCount {
			return nodes[i].LinkCount > nodes[j].LinkCount
		}
		return nodes[i].Slug < nodes[j].Slug
	})

	// Build edges, keeping only edges whose endpoints both survived selection.
	// An isolated ego center still needs "edges": [], not null.
	edges := []types.WikiGraphEdge{}
	drawn := make(map[[2]string]struct{})
	for _, p := range pages {
		if _, ok := selected[p.Slug]; !ok {
			continue
		}
		for _, target := range p.OutLinks {
			if _, ok := selected[target]; !ok || target == p.Slug {
				continue
			}
			if _, dup := drawn[[2]string{p.Slug, target}]; dup {
				continue
			}
			drawn[[2]string{p.Slug, target}] = struct{}{}
			edges = append(edges, types.WikiGraphEdge{
				Source: p.Slug,
				Target: target,
			})
		}
	}
	for _, e := range sourceEdges {
		_, okS := selected[e[0]]
		_, okT := selected[e[1]]
		if !okS || !okT {
			continue
		}
		_, forward := drawn[e]
		_, backward := drawn[[2]string{e[1], e[0]}]
		if forward || backward {
			continue
		}
		drawn[e] = struct{}{}
		edges = append(edges, types.WikiGraphEdge{Source: e[0], Target: e[1]})
	}

	// total is the count of candidate nodes before truncation — i.e. the
	// population the frontend would need to fetch if it asked for the
	// whole graph. For overview this respects the type filter; for ego
	// it is the total KB page count (the user still sees "X of Y" based
	// on the full wiki, not a filtered denominator).
	total := len(pages)
	if mode == types.WikiGraphModeOverview && hasTypeFilter {
		total = 0
		for _, p := range pages {
			if typeAllow[p.PageType] {
				total++
			}
		}
	}

	meta := types.WikiGraphMeta{
		Mode:      mode,
		Total:     total,
		Returned:  len(nodes),
		Truncated: len(nodes) < total,
	}
	for _, n := range nodes {
		if n.Familiar {
			meta.FamiliarCount++
		}
	}
	if mode == types.WikiGraphModeEgo {
		meta.Center = req.Center
		meta.Depth = req.Depth
		if meta.Depth < 1 {
			meta.Depth = 1
		}
	}

	return &types.WikiGraphData{
		Nodes: nodes,
		Edges: edges,
		Meta:  meta,
	}, nil
}

// graphOverviewTypeRank orders page types for overview tie-breaks: subject
// pages first, generated summaries next, navigation pages last.
func graphOverviewTypeRank(pageType string) int {
	switch pageType {
	case types.WikiPageTypeEntity, types.WikiPageTypeConcept:
		return 0
	case types.WikiPageTypeSynthesis, types.WikiPageTypeComparison:
		return 1
	case types.WikiPageTypeSummary:
		return 2
	default:
		return 3
	}
}

// bfsEgoSlugs computes the undirected BFS neighborhood of `center` up to
// `depth` hops using both inbound and outbound links. Type-filtered pages
// are excluded from the result but are also NOT traversed through — so a
// filter that hides "index" pages will not leak the whole wiki via the
// index. The caller guarantees center exists in pageBySlug.
func bfsEgoSlugs(
	pageBySlug map[string]*types.WikiPage,
	adjacency map[string]map[string]struct{},
	center string,
	depth int,
	typeAllow map[string]bool,
	limit int,
) map[string]struct{} {
	hasTypeFilter := len(typeAllow) > 0
	centerPage, ok := pageBySlug[center]
	if !ok {
		return map[string]struct{}{}
	}
	// If the center itself fails the type filter we honor the filter and
	// return an empty set — the handler will surface Returned=0.
	if hasTypeFilter && !typeAllow[centerPage.PageType] {
		return map[string]struct{}{}
	}

	visited := map[string]struct{}{center: {}}
	frontier := []string{center}

	for hop := 0; hop < depth; hop++ {
		if limit > 0 && len(visited) >= limit {
			break
		}
		next := make([]string, 0, len(frontier))
		for _, slug := range frontier {
			// Undirected adjacency built from every page's links, so a page
			// with stale in_links still reaches the pages that link to it.
			neighbors := make([]string, 0, len(adjacency[slug]))
			for nb := range adjacency[slug] {
				neighbors = append(neighbors, nb)
			}
			sort.Strings(neighbors)
			for _, nb := range neighbors {
				if _, seen := visited[nb]; seen {
					continue
				}
				np, exists := pageBySlug[nb]
				if !exists {
					continue
				}
				if hasTypeFilter && !typeAllow[np.PageType] {
					continue
				}
				visited[nb] = struct{}{}
				next = append(next, nb)
				if limit > 0 && len(visited) >= limit {
					break
				}
			}
			if limit > 0 && len(visited) >= limit {
				break
			}
		}
		frontier = next
		if len(frontier) == 0 {
			break
		}
	}

	return visited
}

// GetStats returns aggregate statistics about the wiki
