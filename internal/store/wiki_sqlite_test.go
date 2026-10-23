package store

import (
	"context"
	"fmt"
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

func TestMetadataAndLinkMaintenanceKeepVersionAndRejectStaleBody(t *testing.T) {
	db := setupWikiPagesTestDB(t)
	r := NewWikiPageRepository(db)
	ctx := context.Background()
	p := makeWikiPage("kb", "concept/cas-meta", "concept", "published")
	require.NoError(t, r.Create(ctx, p))
	stale := *p
	p.OutLinks = types.StringArray{"concept/other"}
	require.NoError(t, r.UpdateMeta(ctx, p))
	require.Equal(t, 1, p.Version)
	stale.Content = "stale autolink rewrite"
	err := r.UpdateAutoLinkedContent(ctx, &stale)
	require.ErrorIs(t, err, ErrWikiPageConflict)
	stored, err := r.GetByID(ctx, p.ID)
	require.NoError(t, err)
	require.NotEqual(t, stale.Content, stored.Content)
	revisions, n, err := r.ListRevisions(ctx, "kb", p.ID, 20, 0)
	require.NoError(t, err)
	require.Zero(t, n)
	require.Empty(t, revisions)
}

// TestProductionWikiWritesBoundRevisionHistory 验证实际写入入口自动裁剪，而不是仅测试裁剪函数本身。
func TestProductionWikiWritesBoundRevisionHistory(t *testing.T) {
	for _, tc := range []struct {
		name        string // 写入路径与保留策略。
		source      string // 历史编辑来源。
		maintenance bool   // 是否经过链接维护路径。
		updates     int    // 连续写入次数。
		want        int64  // 应保留的历史快照数。
	}{
		{"pipeline", types.WikiEditSourcePipeline, false, 65, 49},
		{"manual", types.WikiEditSourceUser, false, 65, 49},
		{"maintenance", types.WikiEditSourcePipeline, true, 65, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := setupWikiPagesTestDB(t)
			repo := NewWikiPageRepository(db)
			ctx := context.Background()
			page := makeWikiPage("kb-retention", "concept/bounded", "concept", "published")
			page.LastEditSource = tc.source
			require.NoError(t, repo.Create(ctx, page))
			for i := 0; i < tc.updates; i++ {
				rev := revisionOf(page)
				page.Content = fmt.Sprintf("第 %d 次更新", i)
				if tc.maintenance {
					require.NoError(t, repo.UpdateAutoLinkedContent(ctx, page))
				} else {
					require.NoError(t, repo.UpdateWithRevision(ctx, page, rev))
				}
			}
			_, total, err := repo.ListRevisions(ctx, page.KnowledgeBaseID, page.ID, 1000, 0)
			require.NoError(t, err)
			require.Equal(t, tc.want, total)
			if tc.maintenance {
				require.Equal(t, 1, page.Version)
			} else {
				require.Equal(t, tc.updates+1, page.Version)
			}
			_, err = repo.GetRevision(ctx, page.KnowledgeBaseID, page.ID, 1)
			require.ErrorIs(t, err, ErrWikiPageNotFound)
		})
	}
}

// TestMaintenancePrunesLegacyExcessWithoutNewVersion 验证旧超限历史可在维护事务中裁剪，同时不新增内容版本。
func TestMaintenancePrunesLegacyExcessWithoutNewVersion(t *testing.T) {
	db := setupWikiPagesTestDB(t)
	repo := NewWikiPageRepository(db)
	ctx := context.Background()
	page := makeWikiPage("kb-old-history", "concept/old", "concept", "published")
	page.Version = 71
	require.NoError(t, repo.Create(ctx, page))
	for version := 1; version <= 70; version++ {
		old := *page
		old.Version = version
		old.Content = fmt.Sprint("历史", version)
		require.NoError(t, db.Create(revisionOf(&old)).Error)
	}
	page.OutLinks = types.StringArray{"concept/linked"}
	require.NoError(t, repo.UpdateMeta(ctx, page))
	require.Equal(t, 71, page.Version)
	rows, n, err := repo.ListRevisions(ctx, page.KnowledgeBaseID, page.ID, 100, 0)
	require.NoError(t, err)
	require.EqualValues(t, 49, n)
	require.Equal(t, 22, rows[len(rows)-1].Version)
}
