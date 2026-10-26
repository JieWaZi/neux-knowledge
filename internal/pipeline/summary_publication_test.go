package pipeline

import (
	"context"
	"testing"

	types "github.com/JieWaZi/neux-knowledge/internal/domain"
)

// TestSummaryPublicationRejectsUnhandledContributions 防止旧计划或保留地址碰撞把未合成的概念贡献写进摘要证据和成功回执。
func TestSummaryPublicationRejectsUnhandledContributions(t *testing.T) {
	for _, mode := range []string{"foreign-concept", "same-source-concept", "duplicate-summary", "foreign-retract", "same-source-retract"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			s, j, sources := contractFixture(t, 2)
			artifacts := map[string]ReadArtifact{}
			sourceMap := map[string]*types.SourceVersion{}
			for _, source := range sources {
				raw, err := s.ReadRaw(ctx, j.LibraryID, source.ID)
				if err != nil {
					t.Fatal(err)
				}
				artifact, err := (MarkdownReader{}).Read(ctx, ReadRequest{SourceID: source.SourceID, SourceVersionID: source.ID, SHA256: source.SHA256, Filename: source.Filename, MIME: source.MIME, Raw: raw, Scope: "all"})
				if err != nil {
					t.Fatal(err)
				}
				artifacts[source.ID] = artifact
				sourceMap[source.ID] = source
			}
			source := sources[0]
			slug := "summary/" + source.SourceID
			summary := SlugUpdate{Slug: slug, Type: "summary", SourceVersionID: source.ID, KnowledgeID: source.SourceID, SourceRef: source.SourceID, DocTitle: source.Filename, SummaryBody: "只归纳来源甲的真实摘要。", SummaryLine: "来源甲摘要"}
			extra := SlugUpdate{Slug: slug, Type: "concept", SourceVersionID: sources[1].ID, KnowledgeID: sources[1].SourceID, SourceRef: sources[1].SourceID, DocTitle: sources[1].Filename, Item: extractedItem{Name: "未合成概念", Slug: slug, Details: "不应写入摘要的概念事实。"}}
			switch mode {
			case "same-source-concept":
				extra.SourceVersionID = source.ID
				extra.KnowledgeID = source.SourceID
				extra.SourceRef = source.SourceID
			case "duplicate-summary":
				extra = summary
				extra.SummaryBody = "第二份未独立处理的摘要。"
			case "foreign-retract":
				extra.Type = "retract"
				extra.SourceRef = ""
			case "same-source-retract":
				extra.Type = "retract"
				extra.KnowledgeID = source.SourceID
				extra.SourceVersionID = source.ID
				extra.SourceRef = ""
			}
			updates := []SlugUpdate{summary, extra}
			svc := &liteService{store: s, job: j, library: j.LibrarySnapshot}
			reducer := &upstreamReducer{liteService: svc, sources: sourceMap, artifacts: artifacts}
			_, _, _, _, err := reducer.reduceSlugUpdates(ctx, j.LibraryID, slug, updates, svc.newBatchContext(nil))
			if mode == "same-source-retract" {
				if err != nil {
					t.Fatal(err)
				}
				page, e := s.GetPageBySlug(ctx, j.LibraryID, slug)
				if e != nil {
					t.Fatal(e)
				}
				if len(page.Evidence.Sources) != 1 || page.Evidence.Sources[0].SourceVersionID != source.ID || page.Content != summary.SummaryBody {
					t.Fatalf("valid summary group mixed evidence: %+v", page)
				}
			} else {
				if err == nil {
					t.Errorf("unhandled summary group accepted: %s", mode)
				}
				_, total, e := s.ListPages(ctx, types.PageQuery{LibraryID: j.LibraryID, Limit: 10})
				if e != nil {
					t.Fatal(e)
				}
				if total != 0 {
					t.Errorf("unhandled group published %d pages", total)
				}
				for _, update := range updates {
					applied, e := s.HasLiteContribution(ctx, j.ID, hash(marshal(update)))
					if e != nil {
						t.Fatal(e)
					}
					if applied {
						t.Errorf("unhandled %s contribution marked applied", update.Type)
					}
				}
			}
		})
	}
}
