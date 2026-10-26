package store

import (
	"context"
	"fmt"
	types "github.com/JieWaZi/neux-knowledge/internal/domain"
	"github.com/stretchr/testify/require"
	"sync"
	"testing"
)

func TestImportSourceReusesSameHashAndFileTypeFromUpstream(t *testing.T) {
	s, _, _, lib := auditStore(t)
	ctx := context.Background()
	in := types.ImportSourceInput{RequestID: "first", LibraryID: lib.ID, Filename: "first.md", Bytes: []byte("同一份内容")}
	first, err := s.ImportSource(ctx, in)
	require.NoError(t, err)
	in.RequestID = "second"
	in.Filename = "renamed.MD"
	second, err := s.ImportSource(ctx, in)
	require.NoError(t, err)
	require.Equal(t, first.ResourceID, second.ResourceID)
	require.Equal(t, "second", second.RequestID)
	replay, err := s.ImportSource(ctx, in)
	require.NoError(t, err)
	require.Equal(t, second.CreatedAt, replay.CreatedAt)
	var activities int64
	require.NoError(t, s.db.Model(&types.AuditLog{}).Where("scope_id = ? AND operation = ?", lib.ID, "source.import").Count(&activities).Error)
	require.EqualValues(t, 1, activities)
	var n int64
	require.NoError(t, s.db.Model(&types.SourceHead{}).Where("library_id = ?", lib.ID).Count(&n).Error)
	require.EqualValues(t, 1, n)
	in.RequestID = "different-type"
	in.Filename = "same.txt"
	third, err := s.ImportSource(ctx, in)
	require.NoError(t, err)
	require.NotEqual(t, first.ResourceID, third.ResourceID)
}

func TestConcurrentImportSourceDedupKeepsOneHead(t *testing.T) {
	s, _, _, lib := auditStore(t)
	const uploads = 6
	var wg sync.WaitGroup
	results := make(chan *types.Receipt, uploads)
	errors := make(chan error, uploads)
	for i := 0; i < uploads; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			receipt, err := s.ImportSource(context.Background(), types.ImportSourceInput{RequestID: fmt.Sprintf("concurrent-%d", i), LibraryID: lib.ID, Filename: "same.md", Bytes: []byte("并发上传相同内容")})
			results <- receipt
			errors <- err
		}(i)
	}
	wg.Wait()
	close(results)
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	identity := ""
	for result := range results {
		require.NotNil(t, result)
		if identity == "" {
			identity = result.ResourceID
		}
		require.Equal(t, identity, result.ResourceID)
	}
	var heads int64
	require.NoError(t, s.db.Model(&types.SourceHead{}).Where("library_id = ?", lib.ID).Count(&heads).Error)
	require.EqualValues(t, 1, heads)
}

func TestImportSourceDedupAllowsFailedRetryAndExplicitNewVersion(t *testing.T) {
	s, _, _, lib := auditStore(t)
	ctx := context.Background()
	in := types.ImportSourceInput{RequestID: "first", LibraryID: lib.ID, Filename: "first.md", Bytes: []byte("同一份内容")}
	first, err := s.ImportSource(ctx, in)
	require.NoError(t, err)
	require.NoError(t, s.db.Model(&types.SourceVersion{}).Where("id = ?", first.ResourceID).Update("state", "failed").Error)
	in.RequestID = "failed-retry"
	retry, err := s.ImportSource(ctx, in)
	require.NoError(t, err)
	require.NotEqual(t, first.ResourceID, retry.ResourceID)
	var current types.SourceVersion
	require.NoError(t, s.db.Where("id = ?", retry.ResourceID).Take(&current).Error)
	in.RequestID = "version"
	in.SourceID = current.SourceID
	in.ExpectedVersion = current.Version
	version, err := s.ImportSource(ctx, in)
	require.NoError(t, err)
	require.NotEqual(t, retry.ResourceID, version.ResourceID)
	require.EqualValues(t, 2, version.Version)
	in.SourceID = ""
	in.ExpectedVersion = 0
	in.RequestID = "current-head"
	again, err := s.ImportSource(ctx, in)
	require.NoError(t, err)
	require.Equal(t, version.ResourceID, again.ResourceID)
}

func TestImportSourceDedupProcessedDoesNotEnqueueOrganization(t *testing.T) {
	s, _, _, lib := auditStore(t)
	ctx := context.Background()
	in := types.ImportSourceInput{RequestID: "first", LibraryID: lib.ID, Filename: "first.md", Bytes: []byte("已整理的内容")}
	first, err := s.ImportSource(ctx, in)
	require.NoError(t, err)
	require.NoError(t, s.db.Model(&types.SourceVersion{}).Where("id = ?", first.ResourceID).Update("state", "processed").Error)
	in.RequestID = "duplicate"
	duplicate, err := s.ImportSource(ctx, in)
	require.NoError(t, err)
	job, err := s.AcceptJob(ctx, types.AcceptJobInput{RequestID: "organize-duplicate", LibraryID: lib.ID, ExpectedVersion: lib.Version, RulesVersion: "upstream", ConfigDigest: "config", StructureVersion: lib.StructureVersion, Permission: types.PermissionSnapshot{PolicyVersion: lib.PolicyVersion, Operations: []string{"organize"}}, SourceVersionIDs: []string{duplicate.ResourceID}})
	require.NoError(t, err)
	require.Equal(t, "no-op", job.State)
}
