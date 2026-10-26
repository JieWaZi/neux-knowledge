package pipeline

import (
	"context"
	"errors"
	"fmt"
	types "github.com/JieWaZi/neux-knowledge/internal/domain"
	"strings"
)

// reconcileArchivedSourceVersions 对完整来源贡献集合换版，旧计划与消失主题也须撤回归档页上的全部旧版本证据。
func (s *liteService) reconcileArchivedSourceVersions(ctx context.Context, updates map[string][]SlugUpdate, pages []*types.WikiPage, sources map[string]*types.SourceVersion) ([]*types.WikiPage, map[string]error, error) {
	pages = append([]*types.WikiPage(nil), pages...)
	bySlug := map[string]*types.WikiPage{}
	for _, page := range pages {
		bySlug[page.Slug] = page
	}
	current := map[string]SlugUpdate{}
	for _, slug := range sortedKeys(updates) {
		for _, u := range updates[slug] {
			if u.Type == "summary" || u.Type == types.WikiPageTypeConcept || u.Type == types.WikiPageTypeEntity {
				// 初版不存在旧版本贡献；同批换版来源一次查询，避免每份首次导入都扫描库内证据。
				if source := sources[u.SourceVersionID]; source != nil && source.Version > 1 {
					current[u.KnowledgeID] = u
				}
			}
		}
	}
	sourceFailures := map[string]error{}
	sourceIDs := sortedKeys(current)
	for after := ""; ; {
		contributions, e := s.store.ListSourceContributionPages(ctx, s.job.LibraryID, sourceIDs, after, 100)
		if e != nil {
			return nil, nil, e
		}
		for _, page := range contributions {
			if bySlug[page.Slug] == nil {
				pages = append(pages, page)
				bySlug[page.Slug] = page
			}
		}
		if len(contributions) < 100 {
			break
		}
		after = contributions[len(contributions)-1].Slug
	}
	priorContents := map[string]string{}
	for _, page := range pages {
		if page.PageType == "summary" || page.PageType == "index" {
			continue
		}
		// 历史追加实现可能保留同一来源的多版证据；清除范围与模型收到的撤回材料必须一致。
		priorVersions := map[string]map[string]bool{}
		for _, ref := range page.Evidence.Sources {
			if u, ok := current[ref.SourceID]; ok && ref.SourceVersionID != u.SourceVersionID {
				if priorVersions[ref.SourceID] == nil {
					priorVersions[ref.SourceID] = map[string]bool{}
				}
				priorVersions[ref.SourceID][ref.SourceVersionID] = true
			}
		}
		for _, sourceID := range sortedKeys(priorVersions) {
			u := current[sourceID]
			if sourceFailures[u.SourceVersionID] != nil {
				continue
			}
			contents := []string{}
			versionsByContent := map[string][]string{}
			for _, versionID := range sortedKeys(priorVersions[sourceID]) {
				key := sourceID + "\x00" + versionID
				prior, loaded := priorContents[key]
				if !loaded {
					summaryPages := []*types.WikiPage{}
					if summary := bySlug["summary/"+slugify(sourceID)]; summary != nil {
						summaryPages = append(summaryPages, summary)
					}
					var e error
					prior, e = s.priorSourceSummary(ctx, summaryPages, sourceID, versionID)
					if e == nil && strings.TrimSpace(prior) == "" {
						prior, e = s.readPriorSourceContent(ctx, sourceID, versionID)
					}
					if e != nil {
						if isCancellation(e) {
							return nil, nil, e
						}
						sourceFailures[u.SourceVersionID] = fmt.Errorf("source %s cannot restore prior source version %s for page %s: %w", u.SourceVersionID, versionID, page.Slug, e)
						break
					}
					priorContents[key] = prior
				}
				if _, ok := versionsByContent[prior]; !ok {
					contents = append(contents, prior)
				}
				versionsByContent[prior] = append(versionsByContent[prior], versionID)
			}
			if sourceFailures[u.SourceVersionID] != nil {
				continue
			}
			var prior strings.Builder
			// 相同材料只发送一次，同时逐一标明已覆盖的确切版本；不采样或截断后删除未覆盖证据。
			for _, content := range contents {
				fmt.Fprintf(&prior, "<prior_source_version>\n<source_id>%s</source_id>\n<source_version_ids>%s</source_version_ids>\n<content>\n%s\n</content>\n</prior_source_version>\n\n", xmlEscape(sourceID), xmlEscape(strings.Join(versionsByContent[content], ",")), content)
			}
			list := updates[page.Slug]
			found, addition := false, false
			for i, existing := range list {
				if existing.KnowledgeID != sourceID {
					continue
				}
				if existing.Type == "retract" || existing.Type == "retractStale" {
					list[i].RetractDocContent = prior.String()
					found = true
				}
				if existing.Type == types.WikiPageTypeConcept || existing.Type == types.WikiPageTypeEntity {
					addition = true
				}
			}
			if !found {
				kind := "retractStale"
				if addition {
					kind = "retract"
				}
				list = append(list, SlugUpdate{Slug: page.Slug, Type: kind, KnowledgeID: sourceID, SourceVersionID: u.SourceVersionID, DocTitle: u.DocTitle, Language: u.Language, RetractDocContent: prior.String()})
			}
			updates[page.Slug] = list
		}
	}
	return pages, sourceFailures, nil
}

// priorSourceSummary 只返回目标旧版本的摘要；摘要先被换版时分页读取保留修订，不以新版正文充当旧贡献。
func (s *liteService) priorSourceSummary(ctx context.Context, pages []*types.WikiPage, sourceID, versionID string) (string, error) {
	slug := "summary/" + slugify(sourceID)
	var summary *types.WikiPage
	for _, page := range pages {
		if page.Slug == slug && page.PageType == "summary" {
			summary = page
			break
		}
	}
	if summary == nil {
		var err error
		summary, err = s.store.GetPageBySlug(ctx, s.job.LibraryID, slug)
		if err != nil {
			var missing *types.Error
			if errors.As(err, &missing) && missing.Code == types.ErrNotFound {
				return "", nil
			}
			return "", err
		}
	}
	if summary.PageType != "summary" {
		return "", nil
	}
	matches := func(refs []types.SourceRef) bool {
		// 旧摘要曾追加而未替换证据；混合版本不能证明正文属于其中任意一版。
		if len(refs) == 0 {
			return false
		}
		for _, ref := range refs {
			if ref.SourceID != sourceID || ref.SourceVersionID != versionID {
				return false
			}
		}
		return true
	}
	if matches(summary.Evidence.Sources) {
		return summary.Content, nil
	}
	for offset := 0; ; {
		revisions, total, err := s.store.ListRevisions(ctx, s.job.LibraryID, summary.ID, offset, 50)
		if err != nil {
			return "", err
		}
		for _, revision := range revisions {
			if matches(revision.Evidence.Sources) {
				return revision.Content, nil
			}
		}
		offset += len(revisions)
		if len(revisions) == 0 || int64(offset) >= total {
			return "", nil
		}
	}
}

// readPriorSourceContent 在摘要已被裁剪时回读确切旧原件；只有完整阅读才可替代旧摘要撤回其全部证据。
func (s *liteService) readPriorSourceContent(ctx context.Context, sourceID, versionID string) (string, error) {
	source, err := s.store.GetSource(ctx, s.job.LibraryID, versionID)
	if err != nil {
		return "", err
	}
	if source.ID != versionID || source.SourceID != sourceID {
		return "", invalid("prior source identity changed")
	}
	raw, err := s.store.ReadRaw(ctx, s.job.LibraryID, versionID)
	if err != nil {
		return "", err
	}
	request := ReadRequest{SourceID: sourceID, SourceVersionID: versionID, SHA256: source.SHA256, MIME: source.MIME, Filename: source.Filename, Raw: raw, Scope: "all", MaxRunes: s.input.MaxReadRunes}
	reader := s.input.Reader
	if reader == nil {
		reader = MarkdownReader{}
	}
	artifact, err := reader.Read(ctx, request)
	if err != nil {
		return "", err
	}
	if err = validateRead(request, artifact); err != nil {
		return "", err
	}
	if artifact.Status != "complete" {
		return "", invalid("prior source reading " + artifact.Status + ": " + artifact.Diagnostic)
	}
	content := reconstructContent(artifact.Blocks)
	if strings.TrimSpace(content) == "" {
		return "", invalid("prior source reading returned empty content")
	}
	return content, nil
}
