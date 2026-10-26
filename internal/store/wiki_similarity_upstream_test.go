// Copyright (C) 2026 Tencent. All rights reserved.
// See THIRD_PARTY_NOTICES.md for license information.

package store

import (
	"context"
	types "github.com/JieWaZi/neux-knowledge/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestFindSimilarPagesSQLiteRanksAndFiltersCandidates(t *testing.T) {
	db := setupWikiPagesTestDB(t)
	repo := NewWikiPageRepository(db)
	ctx := context.Background()

	exact := makeWikiPage("kb-a", "entity/acme-corporation", types.WikiPageTypeEntity, types.WikiPageStatusPublished)
	exact.Title = "Acme Corporation"
	partial := makeWikiPage("kb-a", "concept/acme-corp", types.WikiPageTypeConcept, types.WikiPageStatusPublished)
	partial.Title = "Acme Corp"
	unrelated := makeWikiPage("kb-a", "entity/other", types.WikiPageTypeEntity, types.WikiPageStatusPublished)
	unrelated.Title = "Different Company"
	archived := makeWikiPage("kb-a", "entity/acme-archived", types.WikiPageTypeEntity, types.WikiPageStatusArchived)
	archived.Title = "Acme Corporation"
	wrongType := makeWikiPage("kb-a", "summary/acme", "summary", types.WikiPageStatusPublished)
	wrongType.Title = "Acme Corporation"
	otherKB := makeWikiPage("kb-b", "entity/acme-other", types.WikiPageTypeEntity, types.WikiPageStatusPublished)
	otherKB.Title = "Acme Corporation"
	for _, page := range []*types.WikiPage{exact, partial, unrelated, archived, wrongType, otherKB} {
		require.NoError(t, repo.Create(ctx, page))
	}

	pages, err := repo.FindSimilarPages(ctx, "kb-a", "Acme Corporation", nil, 1)
	require.NoError(t, err)
	require.Len(t, pages, 1)
	assert.Equal(t, exact.Slug, pages[0].Slug)

	pages, err = repo.FindSimilarPages(ctx, "kb-a", "Acme Corporation", nil, 10)
	require.NoError(t, err)
	require.Len(t, pages, 2)
	assert.Equal(t, []string{exact.Slug, partial.Slug}, []string{pages[0].Slug, pages[1].Slug})

	pages, err = repo.FindSimilarPages(ctx, "kb-a", "zzzz", nil, 10)
	require.NoError(t, err)
	assert.Empty(t, pages)
}

func TestFindSimilarPagesSQLiteOrdersEqualScoresBySlug(t *testing.T) {
	db := setupWikiPagesTestDB(t)
	repo := NewWikiPageRepository(db)
	ctx := context.Background()

	zulu := makeWikiPage("kb-a", "entity/zulu", types.WikiPageTypeEntity, types.WikiPageStatusPublished)
	zulu.Title = "Acme"
	alpha := makeWikiPage("kb-a", "entity/alpha", types.WikiPageTypeEntity, types.WikiPageStatusPublished)
	alpha.Title = "Acme"
	require.NoError(t, repo.Create(ctx, zulu))
	require.NoError(t, repo.Create(ctx, alpha))

	pages, err := repo.FindSimilarPages(ctx, "kb-a", "Acme", nil, 10)
	require.NoError(t, err)
	require.Len(t, pages, 2)
	assert.Equal(t, []string{alpha.Slug, zulu.Slug}, []string{pages[0].Slug, pages[1].Slug})
}

func TestFindSimilarPagesSQLitePunctuationVariants(t *testing.T) {
	db := setupWikiPagesTestDB(t)
	repo := NewWikiPageRepository(db)
	page := makeWikiPage("kb-a", "entity/a-b", types.WikiPageTypeEntity, types.WikiPageStatusPublished)
	page.Title = "A-B"
	require.NoError(t, repo.Create(context.Background(), page))

	pages, err := repo.FindSimilarPages(context.Background(), "kb-a", "A B", nil, 10)
	require.NoError(t, err)
	require.Len(t, pages, 1)
	assert.Equal(t, page.Slug, pages[0].Slug)
}

func TestWikiTitleTrigramsAndJaccard(t *testing.T) {
	assert.Empty(t, wikiTitleTrigrams("  \t"))
	assert.Equal(t, 1.0, wikiTrigramJaccard(wikiTitleTrigrams("Acme"), wikiTitleTrigrams("Acme")))
	assert.Equal(t, 1.0, wikiTrigramJaccard(wikiTitleTrigrams("ACME"), wikiTitleTrigrams("acme")))
	assert.Zero(t, wikiTrigramJaccard(wikiTitleTrigrams("Acme"), wikiTitleTrigrams("Different")))
	assert.Greater(t, wikiTrigramJaccard(wikiTitleTrigrams("Acme Corporation"), wikiTitleTrigrams("Acme Corp")), 0.0)
}

func TestWikiTitleTrigramsWordBoundaries(t *testing.T) {
	for _, tt := range []struct {
		name, left, right string /* 测试名称与等价标题。 */
	}{
		{"punctuation", "a-b", "a b"},
		{"word order", "a b", "b a"},
		{"repeated whitespace", "a\t  b", "a b"},
		{"unicode words", "甲-乙", "甲 乙"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, wikiTitleTrigrams(tt.left), wikiTitleTrigrams(tt.right))
			assert.Equal(t, 1.0, wikiTrigramJaccard(wikiTitleTrigrams(tt.left), wikiTitleTrigrams(tt.right)))
		})
	}
	assert.Empty(t, wikiTitleTrigrams("--- !!!"))
	assert.Equal(t, map[string]struct{}{
		"  f": {}, " fo": {}, "foo": {}, "oo ": {},
		"  b": {}, " ba": {}, "bar": {}, "ar ": {},
	}, wikiTitleTrigrams("foo|bar"))
}
