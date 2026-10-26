package pipeline

import (
	"context"
	"strings"

	types "github.com/JieWaZi/neux-knowledge/internal/domain"
)

// publishSourceSummary 从当前单份不可变来源发布重新生成的摘要，不把旧摘要的归纳表排版当成事实身份。
func (s *upstreamReducer) publishSourceSummary(ctx context.Context, page *types.WikiPage, update *SlugUpdate, expected int) (*types.WikiPage, error) {
	source := s.sources[update.SourceVersionID]
	artifact, ok := s.artifacts[update.SourceVersionID]
	if source == nil || source.ID != update.SourceVersionID || source.LibraryID != s.job.LibraryID || !ok || artifact.SourceVersionID != source.ID || artifact.SHA256 != source.SHA256 || artifact.Status != "complete" || artifact.Scope != "all" || len(artifact.Blocks) == 0 {
		return nil, invalid("summary publication requires the current complete source artifact")
	}
	slug := "summary/" + slugify(source.SourceID)
	if update.KnowledgeID != source.SourceID || update.SourceRef != source.SourceID || update.Slug != slug || page.Slug != slug || page.KnowledgeBaseID != s.job.LibraryID {
		return nil, invalid("summary publication source identity or stable slug mismatch")
	}
	// 摘要分支只处理自己的单份摘要与同源撤回，不能替未参与合成的其他贡献保存证据或确认回执。
	summaryCount := 0
	for _, contribution := range s.updates {
		if contribution.Slug != slug || contribution.KnowledgeID != source.SourceID || contribution.SourceVersionID != source.ID {
			return nil, invalid("summary publication contains a foreign source or target contribution")
		}
		switch contribution.Type {
		case "summary":
			summaryCount++
			if contribution.SourceRef != source.SourceID {
				return nil, invalid("summary publication contribution source reference mismatch")
			}
		case "retract", "retractStale":
			if contribution.SourceRef != "" && contribution.SourceRef != source.SourceID {
				return nil, invalid("summary retraction source reference mismatch")
			}
		default:
			return nil, invalid("summary publication contains an unhandled contribution type")
		}
	}
	if summaryCount != 1 {
		return nil, invalid("summary publication requires exactly one current source summary")
	}
	if strings.TrimSpace(page.Content) == "" || strings.TrimSpace(page.Summary) == "" {
		return nil, invalid("summary publication requires nonempty complete model content")
	}
	// 摘要证据只指向本次真实来源；持久化仍核对人工保护、读取时间、页面版本及任务发布权限。
	page.Evidence.Sources = []types.SourceRef{documentRef(source)}
	return s.savePage(ctx, page, expected)
}
