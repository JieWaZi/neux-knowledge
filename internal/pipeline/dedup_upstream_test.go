package pipeline

import (
	types "github.com/JieWaZi/neux-knowledge/internal/domain"
	"testing"
)

func TestDedupMergeRejectReason(t *testing.T) {
	cases := []struct {
		name        string
		src, dst    string
		candidates  map[string]bool
		wantAllowed bool
	}{
		{
			name:        "allowed: dst is a candidate for this item, same type prefix",
			src:         "entity/acme-corp",
			dst:         "entity/acme-corporation",
			candidates:  map[string]bool{"entity/acme-corporation": true},
			wantAllowed: true,
		},
		{
			name:        "rejected: dst similar to a DIFFERENT item (union hallucination)",
			src:         "entity/tencent-open",
			dst:         "entity/hiring-agent",
			candidates:  map[string]bool{"entity/tencent-ur": true}, // hiring-agent not here
			wantAllowed: false,
		},
		{
			name:        "rejected: dst is not a candidate at all (pure hallucination)",
			src:         "entity/hy3-preview",
			dst:         "entity/llm-cli-tool",
			candidates:  nil,
			wantAllowed: false,
		},
		{
			name:        "rejected: type mismatch even when dst is a candidate",
			src:         "entity/foo",
			dst:         "concept/foo",
			candidates:  map[string]bool{"concept/foo": true},
			wantAllowed: false,
		},
		{
			name:        "rejected: missing type prefix",
			src:         "foo",
			dst:         "entity/foo",
			candidates:  map[string]bool{"entity/foo": true},
			wantAllowed: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reason := dedupMergeRejectReason(tc.src, tc.dst, tc.candidates)
			gotAllowed := reason == ""
			if gotAllowed != tc.wantAllowed {
				t.Fatalf("dedupMergeRejectReason(%q, %q) allowed=%v (reason=%q), want allowed=%v",
					tc.src, tc.dst, gotAllowed, reason, tc.wantAllowed)
			}
		})
	}
}

// helper: build a minimal entity/concept WikiPage.
func TestNormalizeWikiIdentityTitlePreservesSemanticPunctuation(t *testing.T) {
	if got := normalizeWikiIdentityTitle("  Acme  Corp "); got != "acmecorp" {
		t.Fatalf("normalizeWikiIdentityTitle whitespace/case = %q, want acmecorp", got)
	}
	if normalizeWikiIdentityTitle("寓言") == normalizeWikiIdentityTitle("《寓言》") {
		t.Fatal("concept title and work/chapter title must keep distinct identities")
	}
}

// TestExactIdentityTargetPrefersSameType covers the same-type-wins rule of
// exactIdentityTarget now that a cross-type exact-title page is reused when
// no same-type page exists (issue #3526).
func TestExactIdentityTargetPrefersSameType(t *testing.T) {
	item := extractedItem{Name: "孔子", Slug: "entity/kong-zi"}
	pages := map[string]*types.WikiPageLite{
		"entity/confucius": {
			Slug:     "entity/confucius",
			Title:    "孔 子",
			PageType: types.WikiPageTypeEntity,
		},
		"concept/confucius": {
			Slug:     "concept/confucius",
			Title:    "孔子",
			PageType: types.WikiPageTypeConcept,
		},
	}
	candidates := map[string]bool{
		"entity/confucius":  true,
		"concept/confucius": true,
	}
	if got := exactIdentityTarget(item, types.WikiPageTypeEntity, candidates, pages); got != "entity/confucius" {
		t.Fatalf("exactIdentityTarget = %q, want entity/confucius", got)
	}
}

// TestExactIdentityTargetCrossTypeFallback covers issue #3526: a re-ingest
// may judge the same real-world thing as the other page type. Without a
// same-type page, the existing cross-type page must be reused instead of
// materializing a concept/X + entity/X twin.
func TestExactIdentityTargetCrossTypeFallback(t *testing.T) {
	item := extractedItem{Name: "孔子", Slug: "entity/kong-zi"}
	pages := map[string]*types.WikiPageLite{
		"concept/confucius": {
			Slug:     "concept/confucius",
			Title:    "孔子",
			PageType: types.WikiPageTypeConcept,
		},
	}
	candidates := map[string]bool{"concept/confucius": true}
	if got := exactIdentityTarget(item, types.WikiPageTypeEntity, candidates, pages); got != "concept/confucius" {
		t.Fatalf("exactIdentityTarget = %q, want concept/confucius (cross-type reuse)", got)
	}

	// Same-type match still wins over the cross-type page.
	pages["entity/confucius"] = &types.WikiPageLite{
		Slug: "entity/confucius", Title: "孔 子", PageType: types.WikiPageTypeEntity,
	}
	candidates["entity/confucius"] = true
	if got := exactIdentityTarget(item, types.WikiPageTypeEntity, candidates, pages); got != "entity/confucius" {
		t.Fatalf("exactIdentityTarget = %q, want same-type entity/confucius", got)
	}

	// Differently-titled cross-type pages must not be reused.
	other := extractedItem{Name: "孟子", Slug: "entity/mencius"}
	if got := exactIdentityTarget(other, types.WikiPageTypeEntity, candidates, pages); got != "" {
		t.Fatalf("exactIdentityTarget = %q, want empty for unrelated title", got)
	}
}
