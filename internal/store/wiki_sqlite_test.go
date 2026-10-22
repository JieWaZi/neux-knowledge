package store

import (
	"context"
	"github.com/JieWaZi/neux-knowledge/internal/domain"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestSQLiteQueriesIncludeAliasesAndAvoidPostgresFunctions(t *testing.T) {
	db := setupWikiPagesTestDB(t)
	r := NewWikiPageRepository(db)
	ctx := context.Background()
	p := makeWikiPage("kb", "concept/real", "concept", "published")
	p.Aliases = types.StringArray{"罕见别名", "100% alias"}
	require.NoError(t, r.Create(ctx, p))
	pages, n, err := r.List(ctx, &types.WikiPageListRequest{KnowledgeBaseID: "kb", Query: "罕见别名"})
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	require.Len(t, pages, 1)
	hits, err := r.SearchAcross(ctx, []string{"kb"}, "罕见别名", 10)
	require.NoError(t, err)
	require.Len(t, hits, 1)
	near, err := r.FindSimilarPages(ctx, "kb", "罕见别名", nil, 10)
	require.NoError(t, err)
	require.Len(t, near, 1)
	_, n, err = r.List(ctx, &types.WikiPageListRequest{KnowledgeBaseID: "kb", Query: "100%"})
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
}

func TestMetadataAndLinkMaintenanceUseCASAndHistory(t *testing.T) {
	db := setupWikiPagesTestDB(t)
	r := NewWikiPageRepository(db)
	ctx := context.Background()
	p := makeWikiPage("kb", "concept/cas-meta", "concept", "published")
	require.NoError(t, r.Create(ctx, p))
	stale := *p
	p.OutLinks = types.StringArray{"concept/other"}
	require.NoError(t, r.UpdateMeta(ctx, p))
	require.Equal(t, 2, p.Version)
	stale.Content = "stale autolink rewrite"
	err := r.UpdateAutoLinkedContent(ctx, &stale)
	require.ErrorIs(t, err, ErrWikiPageConflict)
	stored, err := r.GetByID(ctx, p.ID)
	require.NoError(t, err)
	require.NotEqual(t, stale.Content, stored.Content)
	revisions, n, err := r.ListRevisions(ctx, "kb", p.ID, 20, 0)
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	require.Len(t, revisions, 1)
}
