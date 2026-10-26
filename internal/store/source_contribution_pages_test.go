package store

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	types "github.com/JieWaZi/neux-knowledge/internal/domain"
	"gorm.io/gorm"
)

// TestSourceContributionPagesUseBoundedBatches 验证完整贡献查询跨归档且按批读取，不随来源数量重复扫描页面。
func TestSourceContributionPagesUseBoundedBatches(t *testing.T) {
	ctx := context.Background()
	s, library, source, _ := sourceWorkFixture(t)
	pages := make([]types.WikiPage, 205)
	for i := range pages {
		status := "published"
		if i%2 == 0 {
			status = "archived"
		}
		pages[i] = types.WikiPage{ID: fmt.Sprintf("page-%03d", i), KnowledgeBaseID: library.ID, Slug: fmt.Sprintf("concept/%03d", i), Title: "旧贡献", PageType: "concept", Status: status, Evidence: types.PageEvidence{Sources: []types.SourceRef{{SourceID: source.SourceID, SourceVersionID: source.ID, SHA256: source.SHA256, ClaimKind: "document_context"}}}}
	}
	// 大量无关正文不进入返回批次；keyset使同批查询沿库内slug范围前进。
	for i := 0; i < 10000; i++ {
		pages = append(pages, types.WikiPage{ID: fmt.Sprintf("unrelated-%05d", i), KnowledgeBaseID: library.ID, Slug: fmt.Sprintf("z-unrelated/%05d", i), Title: "无关页面", PageType: "concept", Status: "published", Content: strings.Repeat("无关正文", 128), Evidence: types.PageEvidence{Sources: []types.SourceRef{{SourceID: "unrelated-source", SourceVersionID: "unrelated-version", ClaimKind: "document_context"}}}})
	}
	if err := s.db.CreateInBatches(pages, 50).Error; err != nil {
		t.Fatal(err)
	}
	queries := 0
	if err := s.db.Callback().Query().Before("gorm:query").Register("count_contribution_batches", func(tx *gorm.DB) {
		if tx.Statement.Table == "wiki_pages" {
			queries++
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer s.db.Callback().Query().Remove("count_contribution_batches")
	seen := map[string]bool{}
	archived := 0
	// 同批100个稳定来源只需按结果分页，不按来源发出100组查询。
	sourceIDs := []string{source.SourceID}
	for i := 0; i < 99; i++ {
		sourceIDs = append(sourceIDs, fmt.Sprintf("irrelevant-%d", i))
	}
	started := time.Now()
	for after := ""; ; {
		batch, err := s.ListSourceContributionPages(ctx, library.ID, sourceIDs, after, 100)
		if err != nil {
			t.Fatal(err)
		}
		if len(batch) > 100 {
			t.Fatalf("unbounded batch=%d", len(batch))
		}
		for _, page := range batch {
			if seen[page.ID] {
				t.Fatalf("duplicate page=%s", page.ID)
			}
			seen[page.ID] = true
			if page.Status == "archived" {
				archived++
			}
		}
		if len(batch) < 100 {
			break
		}
		after = batch[len(batch)-1].Slug
	}
	if len(seen) != 205 || archived != 103 || queries != 3 {
		t.Fatalf("pages=%d archived=%d queries=%d", len(seen), archived, queries)
	}
	var plan []struct {
		ID     int    // 查询步骤标识。
		Parent int    // 上级步骤标识。
		Unused int    // SQLite保留列。
		Detail string // 实际查询计划。
	}
	if err := s.db.Raw("EXPLAIN QUERY PLAN SELECT * FROM wiki_pages WHERE knowledge_base_id = ? AND slug > ? AND EXISTS (SELECT 1 FROM json_each(evidence, '$.sources') WHERE json_extract(value, '$.sourceId') IN ?) ORDER BY slug ASC LIMIT 100", library.ID, "", sourceIDs).Scan(&plan).Error; err != nil {
		t.Fatal(err)
	}
	details := []string{}
	for _, row := range plan {
		details = append(details, row.Detail)
	}
	t.Logf("matched=205 unrelated=10000 sourceIDs=100 queries=%d elapsed=%s; query plan: %s", queries, time.Since(started), strings.Join(details, " | "))
}

// TestLiteContributionAdoptionRequiresCommittedReceipt 验证旧贡献只在实际页面回执存在且当前尝试有权运行时映射身份。
func TestLiteContributionAdoptionRequiresCommittedReceipt(t *testing.T) {
	for _, mode := range []string{"committed", "missing", "stale-attempt", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			s, library, source, job := sourceWorkFixture(t)
			const oldHash = "old-content-hash"
			const identity = "semantic-contribution-hash"
			page := types.WikiPage{KnowledgeBaseID: library.ID, Slug: "concept/applied", Title: "已提交贡献", PageType: "concept", Status: "published", Content: "实际已提交的正文。", Evidence: types.PageEvidence{Sources: []types.SourceRef{{SourceID: source.SourceID, SourceVersionID: source.ID, SHA256: source.SHA256, ClaimKind: "document_context"}}}}
			if mode != "missing" {
				if _, err := s.PublishLite(ctx, "old-publication", job.ID, job.Attempt, &types.Publication{Pages: []types.PageMutation{{Page: page}}, SourceVersionIDs: []string{source.ID}}, "lite/"+job.ID+"/applied/"+oldHash); err != nil {
					t.Fatal(err)
				}
			}
			attempt := job.Attempt
			if mode == "stale-attempt" {
				attempt++
			}
			if mode == "cancelled" {
				if err := s.db.Model(&types.Job{}).Where("id = ?", job.ID).UpdateColumn("cancel_requested", true).Error; err != nil {
					t.Fatal(err)
				}
			}
			applied, err := s.AdoptLiteContribution(ctx, job.ID, attempt, identity, []string{oldHash})
			if mode == "stale-attempt" || mode == "cancelled" {
				if err == nil || applied {
					t.Fatalf("invalid attempt adopted contribution: applied=%v err=%v", applied, err)
				}
				return
			}
			if err != nil || applied != (mode == "committed") {
				t.Fatalf("wrong adoption: applied=%v err=%v", applied, err)
			}
			stored, err := s.HasLiteContribution(ctx, job.ID, identity)
			if err != nil || stored != applied {
				t.Fatalf("missing persisted alias: stored=%v err=%v", stored, err)
			}
		})
	}
}
