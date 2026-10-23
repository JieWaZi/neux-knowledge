// Copyright (C) 2026 Tencent. MIT，许可见 THIRD_PARTY_NOTICES.md。
package wiki

import (
	"encoding/json"
	types "github.com/JieWaZi/neux-knowledge/internal/domain"
	"strings"
	"testing"
	"time"
)

func makeGraphFixture() []*types.WikiPage {
	return []*types.WikiPage{
		{
			Slug:     "hub",
			Title:    "Hub",
			PageType: types.WikiPageTypeSummary,
			OutLinks: types.StringArray{"a", "b", "c", "d"},
			InLinks:  types.StringArray{"a", "b"},
		},
		{
			Slug:     "a",
			Title:    "A",
			PageType: types.WikiPageTypeEntity,
			OutLinks: types.StringArray{"hub"},
			InLinks:  types.StringArray{"hub"},
		},
		{
			Slug:     "b",
			Title:    "B",
			PageType: types.WikiPageTypeEntity,
			OutLinks: types.StringArray{"hub"},
			InLinks:  types.StringArray{"hub"},
		},
		{
			Slug:     "c",
			Title:    "C",
			PageType: types.WikiPageTypeConcept,
			OutLinks: types.StringArray{"d"},
			InLinks:  types.StringArray{"hub"},
		},
		{
			Slug:     "d",
			Title:    "D",
			PageType: types.WikiPageTypeConcept,
			OutLinks: types.StringArray{},
			InLinks:  types.StringArray{"hub", "c"},
		},
		{
			Slug:     "x",
			Title:    "X",
			PageType: types.WikiPageTypeEntity,
			OutLinks: types.StringArray{"y"},
			InLinks:  types.StringArray{},
		},
		{
			Slug:     "y",
			Title:    "Y",
			PageType: types.WikiPageTypeEntity,
			OutLinks: types.StringArray{},
			InLinks:  types.StringArray{"x"},
		},
	}
}

func nodeSlugs(data *types.WikiGraphData) map[string]bool {
	out := make(map[string]bool, len(data.Nodes))
	for _, n := range data.Nodes {
		out[n.Slug] = true
	}
	return out
}

// TestComputeGraphSubset_OverviewTruncatesByLinkCount verifies that overview
// mode returns the most-connected nodes first and reports truncation
// honestly in Meta. At 4万 pages this is the path that must NOT return the
// full graph — the cap is what keeps the response size and frontend
// rendering tractable.

func TestComputeGraphSubset_OverviewTruncatesByLinkCount(t *testing.T) {
	pages := makeGraphFixture()
	got, err := ComputeGraphSubset(pages, &types.WikiGraphRequest{
		Mode:  types.WikiGraphModeOverview,
		Limit: 3,
	})
	if err != nil {
		t.Fatalf("computeGraphSubset: %v", err)
	}

	if len(got.Nodes) != 3 {
		t.Errorf("want 3 nodes, got %d (%v)", len(got.Nodes), nodeSlugs(got))
	}
	slugs := nodeSlugs(got)
	// hub links to four distinct pages, more than any other; it must survive the cap.
	if !slugs["hub"] {
		t.Errorf("expected hub to survive the top-3 cap, got %v", slugs)
	}
	if !got.Meta.Truncated {
		t.Errorf("expected Meta.Truncated=true when returned < total")
	}
	if got.Meta.Total != len(pages) {
		t.Errorf("Meta.Total = %d, want %d", got.Meta.Total, len(pages))
	}
	if got.Meta.Returned != len(got.Nodes) {
		t.Errorf("Meta.Returned mismatch: %d vs %d", got.Meta.Returned, len(got.Nodes))
	}

	// Every returned edge must connect two surviving nodes.
	for _, e := range got.Edges {
		if !slugs[e.Source] || !slugs[e.Target] {
			t.Errorf("edge %s->%s references a non-returned node", e.Source, e.Target)
		}
	}
}

func TestComputeGraphSubset_LinkCountCountsDistinctReachablePages(t *testing.T) {
	pages := []*types.WikiPage{
		{
			Slug: "p", PageType: types.WikiPageTypeEntity,
			OutLinks: types.StringArray{"q", "q", "gone", "p", "idx"}, InLinks: types.StringArray{"q"},
		},
		{
			Slug: "q", PageType: types.WikiPageTypeEntity,
			OutLinks: types.StringArray{"p"}, InLinks: types.StringArray{"p"},
		},
		{Slug: "idx", PageType: types.WikiPageTypeIndex, InLinks: types.StringArray{"p"}},
	}
	got, err := ComputeGraphSubset(pages, &types.WikiGraphRequest{
		Mode: types.WikiGraphModeOverview, Types: []string{types.WikiPageTypeEntity},
	})
	if err != nil {
		t.Fatalf("computeGraphSubset: %v", err)
	}
	for _, n := range got.Nodes {
		if n.LinkCount != 1 {
			t.Errorf("%s link_count = %d, want 1 (reciprocal, duplicate, self, dead and filtered links excluded)",
				n.Slug, n.LinkCount)
		}
	}
	if len(got.Edges) != 2 {
		t.Errorf("want one edge per direction between p and q, got %v", got.Edges)
	}
}

func TestComputeGraphSubset_LinkCountSurvivesStaleInLinks(t *testing.T) {
	pages := []*types.WikiPage{
		{Slug: "summary", PageType: types.WikiPageTypeSummary, OutLinks: types.StringArray{"topic"}},
		{Slug: "topic", PageType: types.WikiPageTypeConcept},
	}
	got, err := ComputeGraphSubset(pages, &types.WikiGraphRequest{Mode: types.WikiGraphModeOverview})
	if err != nil {
		t.Fatalf("computeGraphSubset: %v", err)
	}
	for _, n := range got.Nodes {
		if n.LinkCount != 1 {
			t.Errorf("%s link_count = %d, want 1 even though topic.in_links was never updated", n.Slug, n.LinkCount)
		}
	}

	ego, err := ComputeGraphSubset(pages, &types.WikiGraphRequest{
		Mode: types.WikiGraphModeEgo, Center: "topic", Depth: 1,
	})
	if err != nil {
		t.Fatalf("ego: %v", err)
	}
	if !nodeSlugs(ego)["summary"] || len(ego.Edges) != 1 {
		t.Errorf("ego of topic must reach the page linking to it, got nodes %v edges %v", nodeSlugs(ego), ego.Edges)
	}
}

func TestComputeGraphSubset_ConnectsPagesToTheirDocumentSummary(t *testing.T) {
	pages := []*types.WikiPage{
		{
			Slug: "summary/ollama-tags", PageType: types.WikiPageTypeSummary,
			SourceRefs: types.StringArray{"doc-1|Ollama标签.md"},
		},
		{Slug: "entity/qwen", PageType: types.WikiPageTypeEntity, SourceRefs: types.StringArray{"doc-1"}},
		{Slug: "concept/linked", PageType: types.WikiPageTypeConcept, SourceRefs: types.StringArray{"doc-1"}},
		{
			Slug: "concept/other-doc", PageType: types.WikiPageTypeConcept,
			SourceRefs: types.StringArray{"doc-2"},
		},
	}
	pages[0].OutLinks = types.StringArray{"concept/linked"}

	got, err := ComputeGraphSubset(pages, &types.WikiGraphRequest{Mode: types.WikiGraphModeOverview})
	if err != nil {
		t.Fatalf("computeGraphSubset: %v", err)
	}
	edges := map[string]bool{}
	for _, e := range got.Edges {
		edges[e.Source+"->"+e.Target] = true
	}
	want := []string{"summary/ollama-tags->concept/linked", "summary/ollama-tags->entity/qwen"}
	if len(edges) != len(want) || !edges[want[0]] || !edges[want[1]] {
		t.Errorf("edges = %v, want %v (one edge per pair, none for a document without summary)", edges, want)
	}
	counts := map[string]int{}
	for _, n := range got.Nodes {
		counts[n.Slug] = n.LinkCount
	}
	if counts["summary/ollama-tags"] != 2 || counts["entity/qwen"] != 1 || counts["concept/other-doc"] != 0 {
		t.Errorf("link counts = %v", counts)
	}
}

func TestComputeGraphSubset_LeavesOutIndexUnlessRequested(t *testing.T) {
	pages := []*types.WikiPage{
		{Slug: "index", PageType: types.WikiPageTypeIndex},
		{Slug: "entity/a", PageType: types.WikiPageTypeEntity},
	}
	got, err := ComputeGraphSubset(pages, &types.WikiGraphRequest{Mode: types.WikiGraphModeOverview})
	if err != nil {
		t.Fatalf("computeGraphSubset: %v", err)
	}
	if nodeSlugs(got)["index"] || got.Meta.Total != 1 {
		t.Errorf("overview must leave out the index page, got nodes %v total %d", nodeSlugs(got), got.Meta.Total)
	}
	got, err = ComputeGraphSubset(pages, &types.WikiGraphRequest{
		Mode: types.WikiGraphModeOverview, Types: []string{types.WikiPageTypeIndex},
	})
	if err != nil {
		t.Fatalf("computeGraphSubset: %v", err)
	}
	if !nodeSlugs(got)["index"] {
		t.Errorf("an explicit index type filter must still return it, got %v", nodeSlugs(got))
	}
}

func TestComputeGraphSubset_IsolatedEgoCenterHasEmptyEdgeList(t *testing.T) {
	pages := []*types.WikiPage{{Slug: "alone", PageType: types.WikiPageTypeEntity}}
	got, err := ComputeGraphSubset(pages, &types.WikiGraphRequest{
		Mode: types.WikiGraphModeEgo, Center: "alone", Depth: 1,
	})
	if err != nil {
		t.Fatalf("computeGraphSubset: %v", err)
	}
	body, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(body), `"edges":[]`) {
		t.Errorf("isolated center must serialize edges as [], got %s", body)
	}
}

func TestComputeGraphSubset_OverviewBreaksTiesBySignificance(t *testing.T) {
	now := time.Now()
	pages := []*types.WikiPage{
		{Slug: "a-index", PageType: types.WikiPageTypeIndex, UpdatedAt: now},
		{Slug: "b-summary", PageType: types.WikiPageTypeSummary, UpdatedAt: now},
		{Slug: "c-old", PageType: types.WikiPageTypeEntity, UpdatedAt: now.Add(-time.Hour)},
		{Slug: "d-new", PageType: types.WikiPageTypeConcept, UpdatedAt: now},
		{
			Slug: "e-familiar", PageType: types.WikiPageTypeSummary, UpdatedAt: now.Add(-2 * time.Hour),
			SourceRefs: types.StringArray{"doc-1"},
		},
	}
	got, err := ComputeGraphSubset(pages, &types.WikiGraphRequest{
		Mode: types.WikiGraphModeOverview, Limit: 3, FamiliarKnowledgeIDs: []string{"doc-1"},
	})
	if err != nil {
		t.Fatalf("computeGraphSubset: %v", err)
	}
	slugs := nodeSlugs(got)
	for _, want := range []string{"e-familiar", "d-new", "c-old"} {
		if !slugs[want] {
			t.Errorf("expected %q among tied pages, got %v", want, slugs)
		}
	}
}

func TestComputeGraphSubset_MarksFamiliarSourcePages(t *testing.T) {
	pages := makeGraphFixture()
	pages[0].SourceRefs = types.StringArray{"doc-1|排班手册"}
	pages[1].SourceRefs = types.StringArray{"doc-2"}

	got, err := ComputeGraphSubset(pages, &types.WikiGraphRequest{
		Mode:                 types.WikiGraphModeOverview,
		Limit:                0,
		FamiliarKnowledgeIDs: []string{"doc-1"},
	})
	if err != nil {
		t.Fatalf("computeGraphSubset: %v", err)
	}
	familiar := map[string]bool{}
	for _, n := range got.Nodes {
		if n.Familiar {
			familiar[n.Slug] = true
		}
	}
	if !familiar["hub"] {
		t.Errorf("hub sources doc-1, want familiar, got %v", familiar)
	}
	if familiar["a"] {
		t.Errorf("a sources a different document, must not light up")
	}
	if got.Meta.FamiliarCount != 1 {
		t.Errorf("FamiliarCount = %d, want 1", got.Meta.FamiliarCount)
	}
}

// TestComputeGraphSubset_OverviewUncapped ensures the Limit<=0 escape hatch
// still works for internal callers (wiki lint) that need every page.

func TestComputeGraphSubset_OverviewUncapped(t *testing.T) {
	pages := makeGraphFixture()
	got, err := ComputeGraphSubset(pages, &types.WikiGraphRequest{
		Mode:  types.WikiGraphModeOverview,
		Limit: 0,
	})
	if err != nil {
		t.Fatalf("computeGraphSubset: %v", err)
	}
	if len(got.Nodes) != len(pages) {
		t.Errorf("want %d nodes (uncapped), got %d", len(pages), len(got.Nodes))
	}
	if got.Meta.Truncated {
		t.Errorf("expected Meta.Truncated=false when nothing was dropped")
	}
}

// TestComputeGraphSubset_OverviewTypeFilter ensures the type filter applies
// to the candidate set (not just post-hoc), so "total" reflects the
// filter-aware denominator the frontend shows to the user.

func TestComputeGraphSubset_OverviewTypeFilter(t *testing.T) {
	pages := makeGraphFixture()
	got, err := ComputeGraphSubset(pages, &types.WikiGraphRequest{
		Mode:  types.WikiGraphModeOverview,
		Types: []string{types.WikiPageTypeEntity},
		Limit: 100,
	})
	if err != nil {
		t.Fatalf("computeGraphSubset: %v", err)
	}
	slugs := nodeSlugs(got)
	// Only entity-typed pages: a, b, x, y.
	for _, expected := range []string{"a", "b", "x", "y"} {
		if !slugs[expected] {
			t.Errorf("expected entity %q to be returned, got %v", expected, slugs)
		}
	}
	for _, forbidden := range []string{"hub", "c", "d"} {
		if slugs[forbidden] {
			t.Errorf("%q should have been filtered out by type, got %v", forbidden, slugs)
		}
	}
	if got.Meta.Total != 4 {
		t.Errorf("Meta.Total should equal entity-typed page count (4), got %d", got.Meta.Total)
	}
}

// TestComputeGraphSubset_EgoDepth1 checks that depth=1 returns center +
// immediate neighbors only, with no transitive hops.

func TestComputeGraphSubset_EgoDepth1(t *testing.T) {
	pages := makeGraphFixture()
	got, err := ComputeGraphSubset(pages, &types.WikiGraphRequest{
		Mode:   types.WikiGraphModeEgo,
		Center: "hub",
		Depth:  1,
		Limit:  100,
	})
	if err != nil {
		t.Fatalf("computeGraphSubset: %v", err)
	}
	slugs := nodeSlugs(got)
	// hub's direct neighbors: a, b, c, d (out) ∪ a, b (in) = {a, b, c, d} + hub.
	want := []string{"hub", "a", "b", "c", "d"}
	for _, s := range want {
		if !slugs[s] {
			t.Errorf("expected %q in ego depth=1 of hub, got %v", s, slugs)
		}
	}
	// x and y are in a disconnected cluster and must not leak in.
	if slugs["x"] || slugs["y"] {
		t.Errorf("disconnected nodes leaked into ego graph: %v", slugs)
	}
	if got.Meta.Mode != types.WikiGraphModeEgo || got.Meta.Center != "hub" || got.Meta.Depth != 1 {
		t.Errorf("Meta not populated correctly for ego: %+v", got.Meta)
	}
}

// TestComputeGraphSubset_EgoDepth2 checks that an extra hop reaches pages
// only accessible through a one-hop neighbor (c -> d becomes reachable from
// "a" at depth 2 because a -> hub -> c -> d ... wait, that's 3 hops. Use
// a clearer case: from "a", depth=2 should reach hub's neighbors.)

func TestComputeGraphSubset_EgoDepth2ExpandsFrontier(t *testing.T) {
	pages := makeGraphFixture()
	depth1, err := ComputeGraphSubset(pages, &types.WikiGraphRequest{
		Mode:   types.WikiGraphModeEgo,
		Center: "a",
		Depth:  1,
		Limit:  100,
	})
	if err != nil {
		t.Fatalf("depth=1: %v", err)
	}
	depth2, err := ComputeGraphSubset(pages, &types.WikiGraphRequest{
		Mode:   types.WikiGraphModeEgo,
		Center: "a",
		Depth:  2,
		Limit:  100,
	})
	if err != nil {
		t.Fatalf("depth=2: %v", err)
	}
	if len(depth2.Nodes) <= len(depth1.Nodes) {
		t.Errorf("depth=2 should expand the frontier beyond depth=1 (%d <= %d)",
			len(depth2.Nodes), len(depth1.Nodes))
	}
	// At depth 2 from "a": a -> hub -> {b, c, d}. So b, c, d must appear.
	slugs := nodeSlugs(depth2)
	for _, s := range []string{"a", "hub", "b", "c", "d"} {
		if !slugs[s] {
			t.Errorf("expected %q at depth=2 from a, got %v", s, slugs)
		}
	}
}

// TestComputeGraphSubset_EgoRejectsMissingCenter ensures we fail fast
// rather than returning an empty graph when the caller points at a
// non-existent slug — an empty result here would look identical to "your
// wiki has no links to that page" and hide the real bug.

func TestComputeGraphSubset_EgoRejectsMissingCenter(t *testing.T) {
	pages := makeGraphFixture()
	_, err := ComputeGraphSubset(pages, &types.WikiGraphRequest{
		Mode:   types.WikiGraphModeEgo,
		Center: "does-not-exist",
		Depth:  1,
		Limit:  100,
	})
	if err == nil {
		t.Fatalf("expected error for missing center slug")
	}
}
