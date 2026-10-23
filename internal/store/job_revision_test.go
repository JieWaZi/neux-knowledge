package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	types "github.com/JieWaZi/neux-knowledge/internal/domain"
	"github.com/stretchr/testify/require"
)

// publishJobPage 经真实发布入口更新同一页面，保留生成时读到的版本和时间。
func publishJobPage(t *testing.T, s *Store, j *types.Job, source *types.SourceVersion, request string, page *types.WikiPage) error {
	t.Helper()
	_, err := s.PublishLite(context.Background(), request, j.ID, j.Attempt, &types.Publication{
		SourceVersionIDs: []string{source.ID},
		Pages:            []types.PageMutation{{Page: *page, ExpectedVersion: page.Version}},
	})
	return err
}

// seedJobPage 创建整理前已有的正文，避免把建立测试前置数据误当作本轮更新。
func seedJobPage(t *testing.T, s *Store, l *types.Library) *types.WikiPage {
	t.Helper()
	p := makeWikiPage(l.ID, "concept/shared", "concept", "published")
	p.Content = "整理之前的完整内容"
	p.WikiLanguage = l.WikiLanguage
	p.LastEditSource = types.WikiEditSourcePipeline
	require.NoError(t, s.db.Create(p).Error)
	return p
}

// TestJobPageKeepsOneRevisionAcrossBatches 捕获每个批次都增加版本并污染历史的问题。
func TestJobPageKeepsOneRevisionAcrossBatches(t *testing.T) {
	ctx := context.Background()
	s, l, source, j := sourceWorkFixture(t)
	seed := seedJobPage(t, s, l)
	for i := 0; i < 10; i++ {
		p, err := s.GetPage(ctx, l.ID, seed.ID)
		require.NoError(t, err)
		p.Content = fmt.Sprintf("整理批次 %d 的合并内容", i)
		require.NoError(t, publishJobPage(t, s, j, source, fmt.Sprintf("batch-%d", i), p))
	}
	p, err := s.GetPage(ctx, l.ID, seed.ID)
	require.NoError(t, err)
	require.Equal(t, 2, p.Version)
	require.Equal(t, "整理批次 9 的合并内容", p.Content)
	repo := NewWikiPageRepository(s.db)
	old, err := repo.GetRevision(ctx, l.ID, p.ID, 1)
	require.NoError(t, err)
	require.Equal(t, "整理之前的完整内容", old.Content)
	_, n, err := repo.ListRevisions(ctx, l.ID, p.ID, 100, 0)
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
}

// TestJobNewPageStaysAtFirstVersion 防止同一任务的新页面被后续来源推到 v2。
func TestJobNewPageStaysAtFirstVersion(t *testing.T) {
	ctx := context.Background()
	s, l, source, j := sourceWorkFixture(t)
	p := types.WikiPage{Slug: "concept/new", Title: "新页面", PageType: "concept", Content: "首次生成", WikiLanguage: l.WikiLanguage}
	require.NoError(t, publishJobPage(t, s, j, source, "new-page", &p))
	current, err := s.GetPageBySlug(ctx, l.ID, p.Slug)
	require.NoError(t, err)
	current.Content = "补充其他来源后的最终内容"
	require.NoError(t, publishJobPage(t, s, j, source, "new-page-more", current))
	current, err = s.GetPage(ctx, l.ID, current.ID)
	require.NoError(t, err)
	require.Equal(t, 1, current.Version)
	_, n, err := NewWikiPageRepository(s.db).ListRevisions(ctx, l.ID, current.ID, 100, 0)
	require.NoError(t, err)
	require.Zero(t, n)
}

// TestJobUnchangedContentCreatesNoRevision 防止来源归属或相同正文写入产生伪历史。
func TestJobUnchangedContentCreatesNoRevision(t *testing.T) {
	ctx := context.Background()
	s, l, source, j := sourceWorkFixture(t)
	p := seedJobPage(t, s, l)
	p.Evidence.Sources = []types.SourceRef{{SourceID: source.SourceID, SourceVersionID: source.ID, SHA256: source.SHA256, ClaimKind: "document_context"}}
	require.NoError(t, publishJobPage(t, s, j, source, "only-evidence", p))
	p, err := s.GetPage(ctx, l.ID, p.ID)
	require.NoError(t, err)
	require.Equal(t, 1, p.Version)
	require.Len(t, p.Evidence.Sources, 1)
	_, n, err := NewWikiPageRepository(s.db).ListRevisions(ctx, l.ID, p.ID, 100, 0)
	require.NoError(t, err)
	require.Zero(t, n)
}

// TestJobVersionRefusesStaleBodyAndManualInterleaving 防止合并版本后失去对旧模型响应和人工编辑的保护。
func TestJobVersionRefusesStaleBodyAndManualInterleaving(t *testing.T) {
	ctx := context.Background()
	s, l, source, j := sourceWorkFixture(t)
	p := seedJobPage(t, s, l)
	p.Content = "本轮第一批"
	require.NoError(t, publishJobPage(t, s, j, source, "first-batch", p))
	stale, err := s.GetPage(ctx, l.ID, p.ID)
	require.NoError(t, err)
	fresh := *stale
	fresh.Content = "本轮第二批"
	require.NoError(t, publishJobPage(t, s, j, source, "second-batch", &fresh))
	stale.Content = "迟到的模型正文"
	err = publishJobPage(t, s, j, source, "stale-batch", stale)
	require.Error(t, err)
	require.Equal(t, types.ErrVersionConflict, err.(*types.Error).Code)
	p, err = s.GetPage(ctx, l.ID, p.ID)
	require.NoError(t, err)
	p.Content = "人工改写并保留的内容"
	_, err = s.WritePage(ctx, types.WritePageInput{RequestID: "human-interleave", LibraryID: l.ID, ExpectedVersion: p.Version, StructureVersion: l.StructureVersion, Page: *p})
	require.NoError(t, err)
	latest, err := s.GetPage(ctx, l.ID, p.ID)
	require.NoError(t, err)
	latest.Summary = "模型基于人工新正文继续写入也须停止原任务"
	err = publishJobPage(t, s, j, source, "after-human", latest)
	require.Error(t, err)
	after, err := s.GetPage(ctx, l.ID, p.ID)
	require.NoError(t, err)
	require.Equal(t, 3, after.Version)
	require.Equal(t, "人工改写并保留的内容", after.Content)
}

// TestJobRetryAttemptReusesContentVersion 防止显式重试沿同一个任务身份再次增加内容版本。
func TestJobRetryAttemptReusesContentVersion(t *testing.T) {
	ctx := context.Background()
	s, l, source, j := sourceWorkFixture(t)
	p := seedJobPage(t, s, l)
	p.Content = "已成功的部分"
	require.NoError(t, publishJobPage(t, s, j, source, "attempt-one", p))
	_, err := s.PublishLite(ctx, "fail-attempt", j.ID, j.Attempt, &types.Publication{Sources: []types.SourceConclusion{{SourceVersionID: source.ID, State: "failed", Diagnostic: "其他页面失败"}}, Final: true})
	require.NoError(t, err)
	j, err = s.GetJob(ctx, j.ID)
	require.NoError(t, err)
	_, err = s.RetryJob(ctx, types.JobActionInput{RequestID: "retry-attempt", JobID: j.ID, ExpectedVersion: j.Version})
	require.NoError(t, err)
	j, err = s.GetJob(ctx, j.ID)
	require.NoError(t, err)
	require.Equal(t, 2, j.Attempt)
	p, err = s.GetPage(ctx, l.ID, p.ID)
	require.NoError(t, err)
	p.Content = "重试补齐后的最终内容"
	require.NoError(t, publishJobPage(t, s, j, source, "attempt-two", p))
	p, err = s.GetPage(ctx, l.ID, p.ID)
	require.NoError(t, err)
	require.Equal(t, 2, p.Version)
}

// TestJobVersionCheckpointRollsBackWithPage 防止记账失败后页面已更新而重试又新增版本。
func TestJobVersionCheckpointRollsBackWithPage(t *testing.T) {
	ctx := context.Background()
	s, l, source, j := sourceWorkFixture(t)
	p := seedJobPage(t, s, l)
	p.Content = "待写入的新内容"
	require.NoError(t, s.db.Exec(`CREATE TRIGGER reject_job_version BEFORE INSERT ON knowledge_receipts WHEN NEW.operation = 'wiki.job-version' BEGIN SELECT RAISE(ABORT, 'controlled checkpoint failure'); END`).Error)
	err := publishJobPage(t, s, j, source, "checkpoint-failure", p)
	require.Error(t, err)
	stored, err := s.GetPage(ctx, l.ID, p.ID)
	require.NoError(t, err)
	require.Equal(t, 1, stored.Version)
	require.Equal(t, "整理之前的完整内容", stored.Content)
	_, n, err := NewWikiPageRepository(s.db).ListRevisions(ctx, l.ID, p.ID, 100, 0)
	require.NoError(t, err)
	require.Zero(t, n)
	require.NoError(t, s.db.Exec(`DROP TRIGGER reject_job_version`).Error)
	require.NoError(t, publishJobPage(t, s, j, source, "checkpoint-failure", p))
}

// TestIndependentJobsKeepFinalSnapshotsAndBoundHistory 捕获把不同整理任务混成同版或把中间正文存入历史的问题。
func TestIndependentJobsKeepFinalSnapshotsAndBoundHistory(t *testing.T) {
	ctx := context.Background()
	s, l, source, j := sourceWorkFixture(t)
	page := seedJobPage(t, s, l)
	for i := 0; i < 55; i++ {
		if i > 0 {
			lib, err := s.GetLibrary(ctx, l.ID)
			require.NoError(t, err)
			rec, err := s.AcceptJob(ctx, types.AcceptJobInput{RequestID: fmt.Sprintf("independent-job-%d", i), LibraryID: l.ID, ExpectedVersion: lib.Version, StructureVersion: lib.StructureVersion, SourceVersionIDs: []string{source.ID}, Permission: types.PermissionSnapshot{PolicyVersion: lib.PolicyVersion, Operations: []string{"organize"}}, RulesVersion: "rules", ConfigDigest: "config"})
			require.NoError(t, err)
			j, err = s.GetJob(ctx, rec.JobID)
			require.NoError(t, err)
		}
		for _, phase := range []string{"中间", "最终"} {
			p, err := s.GetPage(ctx, l.ID, page.ID)
			require.NoError(t, err)
			p.Content = fmt.Sprintf("第 %d 次任务的%s内容", i, phase)
			require.NoError(t, publishJobPage(t, s, j, source, fmt.Sprintf("job-%d-%s", i, phase), p))
		}
		_, err := s.PublishLite(ctx, fmt.Sprintf("job-%d-finish", i), j.ID, j.Attempt, &types.Publication{Sources: []types.SourceConclusion{{SourceVersionID: source.ID, State: "failed", Diagnostic: "模拟本轮其他页面失败"}}, Final: true})
		require.NoError(t, err)
	}
	p, err := s.GetPage(ctx, l.ID, page.ID)
	require.NoError(t, err)
	require.Equal(t, 56, p.Version)
	repo := NewWikiPageRepository(s.db)
	revisions, n, err := repo.ListRevisions(ctx, l.ID, page.ID, 100, 0)
	require.NoError(t, err)
	require.EqualValues(t, 49, n)
	require.Equal(t, 7, revisions[len(revisions)-1].Version)
	old, err := repo.GetRevision(ctx, l.ID, page.ID, 55)
	require.NoError(t, err)
	require.Equal(t, "第 53 次任务的最终内容", old.Content)
}

// TestStaleWholePageWriterCannotOverwriteSameJobBatch 验证人工和智能体整页旧请求不能覆盖同版内后到的来源内容。
func TestStaleWholePageWriterCannotOverwriteSameJobBatch(t *testing.T) {
	for _, agent := range []bool{false, true} {
		t.Run(fmt.Sprint(agent), func(t *testing.T) {
			ctx := context.Background()
			s, l, source, j := sourceWorkFixture(t)
			p := seedJobPage(t, s, l)
			p.Content = "第一批正文"
			require.NoError(t, publishJobPage(t, s, j, source, "whole-first", p))
			stale, err := s.GetPage(ctx, l.ID, p.ID)
			require.NoError(t, err)
			fresh := *stale
			fresh.Content = "第二批补充后的完整正文"
			require.NoError(t, publishJobPage(t, s, j, source, "whole-second", &fresh))
			stale.Content = "用户基于第一批进行的整页编辑"
			in := types.WritePageInput{RequestID: "stale-whole-page", LibraryID: l.ID, ExpectedVersion: stale.Version, StructureVersion: l.StructureVersion, Page: *stale}
			if agent {
				_, err = s.WriteAgentPage(ctx, in)
			} else {
				_, err = s.WritePage(ctx, in)
			}
			require.Error(t, err)
			current, err := s.GetPage(ctx, l.ID, p.ID)
			require.NoError(t, err)
			require.Equal(t, "第二批补充后的完整正文", current.Content)
			require.Equal(t, 2, current.Version)
		})
	}
}

// TestJobReadTimeRoundTripsAtMilliseconds 验证毫秒读取标识可保存，缺失标识不能绕过同版并发校验。
func TestJobReadTimeRoundTripsAtMilliseconds(t *testing.T) {
	ctx := context.Background()
	s, l, source, j := sourceWorkFixture(t)
	p := seedJobPage(t, s, l)
	p.UpdatedAt = time.UnixMilli(time.Now().UnixMilli() + 10000)
	require.NoError(t, s.db.Model(p).UpdateColumn("updated_at", p.UpdatedAt).Error)
	for i := 0; i < 3; i++ {
		previous := p.UpdatedAt.UnixMilli()
		p.Content = fmt.Sprint("正文批次", i)
		require.NoError(t, publishJobPage(t, s, j, source, fmt.Sprint("clock-batch", i), p))
		var err error
		p, err = s.GetPage(ctx, l.ID, p.ID)
		require.NoError(t, err)
		require.Greater(t, p.UpdatedAt.UnixMilli(), previous)
	}
	p.Content = "基于最新内容的人工修改"
	missing := *p
	missing.UpdatedAt = time.Time{}
	_, err := s.WritePage(ctx, types.WritePageInput{RequestID: "missing-read-time", LibraryID: l.ID, ExpectedVersion: p.Version, StructureVersion: l.StructureVersion, Page: missing})
	require.Error(t, err)
	p.UpdatedAt = time.UnixMilli(p.UpdatedAt.UnixMilli())
	_, err = s.WritePage(ctx, types.WritePageInput{RequestID: "millisecond-read-time", LibraryID: l.ID, ExpectedVersion: p.Version, StructureVersion: l.StructureVersion, Page: *p})
	require.NoError(t, err)
}

// TestWikiMaintenanceFailureKeepsReadTime 防止维护事务失败后修改调用者读取时间，导致原请求无法重试。
func TestWikiMaintenanceFailureKeepsReadTime(t *testing.T) {
	ctx := context.Background()
	s, l, _, j := sourceWorkFixture(t)
	p := seedJobPage(t, s, l)
	readTime := p.UpdatedAt
	p.Content = "补充链接后的正文"
	require.NoError(t, s.db.Exec(`CREATE TRIGGER reject_maintenance BEFORE UPDATE OF content ON wiki_pages BEGIN SELECT RAISE(ABORT, 'controlled maintenance failure'); END`).Error)
	require.Error(t, s.UpdateWikiMaintenance(ctx, j.ID, j.Attempt, p, p.Version))
	require.Equal(t, readTime, p.UpdatedAt)
	require.NoError(t, s.db.Exec(`DROP TRIGGER reject_maintenance`).Error)
	require.NoError(t, s.UpdateWikiMaintenance(ctx, j.ID, j.Attempt, p, p.Version))
	current, err := s.GetPage(ctx, l.ID, p.ID)
	require.NoError(t, err)
	require.Equal(t, 1, current.Version)
	require.Equal(t, p.Content, current.Content)
}
