package pipeline

import (
	"context"
	"errors"
	"fmt"
	types "github.com/JieWaZi/neux-knowledge/internal/domain"
	"github.com/JieWaZi/neux-knowledge/internal/wiki"
	"regexp"
	"sort"
	"strings"
)

var summaryWikiLinks = regexp.MustCompile(`\[\[[^\]]+\]\]`)

// plainSummaryLinks 仅移除显式链接包装，用于判断摘要是否仍是管线原文。
func plainSummaryLinks(body string) string {
	return summaryWikiLinks.ReplaceAllStringFunc(body, func(match string) string {
		parts := strings.SplitN(match[2:len(match)-2], "|", 2)
		if len(parts) > 1 {
			return parts[1]
		}
		return parts[0]
	})
}

func stripFailedSummaryLinks(body string, failed map[string]bool, existing []*types.WikiPage) string {
	present := map[string]bool{}
	for _, page := range existing {
		if page.Status != "archived" {
			present[page.Slug] = true
		}
	}
	return summaryWikiLinks.ReplaceAllStringFunc(body, func(match string) string {
		parts := strings.SplitN(match[2:len(match)-2], "|", 2)
		if !failed[parts[0]] || present[parts[0]] {
			return match
		}
		if len(parts) > 1 {
			return parts[1]
		}
		return parts[0]
	})
}
func (s *liteService) finalize(ctx context.Context, work []*types.OrganizeSourceWork) (*Result, error) {
	ops, err := s.store.ListWikiFinalizeOps(ctx, s.job.ID, s.job.Attempt)
	if err != nil {
		return nil, err
	}
	if s.library.IndexingStrategy.WikiEnabled && len(ops) > 0 {
		if err := s.rebuildIndex(ctx, ops); err != nil {
			return nil, err
		}
		if err := s.finalizeLinks(ctx, ops); err != nil {
			return nil, err
		}
	}
	failures := []types.SourceConclusion{}
	for _, w := range work {
		if w.State == "dead" {
			failures = append(failures, types.SourceConclusion{SourceVersionID: w.SourceVersionID, State: "failed", Diagnostic: w.Diagnostic})
		}
	}
	rec, err := s.publish(ctx, "finalize", &types.Publication{Sources: failures, Final: true})
	if err != nil {
		return nil, err
	}
	job, err := s.store.GetJob(ctx, s.job.ID)
	if err != nil {
		return nil, err
	}
	return &Result{JobID: job.ID, State: job.State, Receipt: rec}, nil
}
func (s *liteService) rebuildIndex(ctx context.Context, ops []*types.WikiFinalizeOp) error {
	pages, err := s.pages(ctx)
	if err != nil {
		return err
	}
	index, expected := newPage(s.job.LibraryID, "index", "index", pages)
	if expected == 0 {
		// 目录隐藏归档页，索引的稳定地址仍须沿原身份和读取时间更新。
		stored, readErr := s.store.GetPageBySlug(ctx, s.job.LibraryID, "index")
		if readErr == nil {
			index, expected = clonePage(*stored), stored.Version
		} else {
			var missing *types.Error
			if !errors.As(readErr, &missing) || missing.Code != types.ErrNotFound {
				return readErr
			}
		}
	}
	// 阶段身份只依赖同任务的语义增量集合，不随恢复时的尝试和数据库行身份变化。
	rawOps := ops
	byKey := map[string]*types.WikiFinalizeOp{}
	for _, op := range ops {
		if byKey[op.Key] == nil || op.Kind == "slug" {
			byKey[op.Key] = op
		}
	}
	ops = nil
	canonical := []types.WikiFinalizeOp{}
	for _, key := range sortedKeys(byKey) {
		op := byKey[key]
		ops = append(ops, op)
		copy := *op
		copy.ID = ""
		copy.JobAttempt = 0
		copy.CreatedAt = 0
		canonical = append(canonical, copy)
	}
	checkpointKey := "lite/" + s.job.ID + "/finalize-index/" + hash(marshal(canonical))
	applied, err := s.store.HasCurrentLitePageCheckpoint(ctx, s.job.ID, s.job.Attempt, checkpointKey, &index)
	if err != nil || applied {
		return err
	}
	index.Status = "published"
	index.Title = "Wiki Index"
	index.WikiLanguage = s.library.WikiLanguage
	existingIntro := strings.TrimSpace(index.Content)
	if existingIntro == "" {
		existingIntro = strings.TrimSpace(index.Summary)
	}
	if at := strings.Index(existingIntro, "\n## "); at >= 0 {
		existingIntro = strings.TrimSpace(existingIntro[:at])
	}
	var change strings.Builder
	indexKeys := []string{checkpointKey}
	legacyConsumed := map[string]bool{}
	legacyChecked := map[int]bool{}
	groups := map[int][]*types.WikiFinalizeOp{}
	for _, op := range rawOps {
		groups[op.JobAttempt] = append(groups[op.JobAttempt], op)
	}
	for _, op := range ops {
		if op.Kind != "added" && op.Kind != "removed" {
			continue
		}
		contributionHash := hash(marshal([]string{"finalize-index-op", op.Key}))
		consumed, e := s.store.HasLiteContribution(ctx, s.job.ID, contributionHash)
		if e != nil {
			return e
		}
		if consumed {
			continue
		}
		// 旧版阶段回执按尝试和原始增量集合命名；核验真实索引后迁入逐项回执，不再把旧增量交给模型。
		if !legacyChecked[op.JobAttempt] {
			legacyChecked[op.JobAttempt] = true
			legacyKey := fmt.Sprintf("lite/%s/%d/finalize-index/%s", s.job.ID, op.JobAttempt, hash(marshal(groups[op.JobAttempt])))
			applied, e := s.store.HasCurrentLitePageCheckpoint(ctx, s.job.ID, s.job.Attempt, legacyKey, &index)
			if e != nil {
				return e
			}
			if applied {
				for _, prior := range groups[op.JobAttempt] {
					legacyConsumed[prior.Key] = true
				}
			}
		}
		indexKeys = append(indexKeys, "lite/"+s.job.ID+"/applied/"+contributionHash)
		if legacyConsumed[op.Key] {
			continue
		}
		if op.Kind == "added" {
			fmt.Fprintf(&change, "<document_added>\n<title>%s</title>\n<summary>%s</summary>\n</document_added>\n\n", op.DocTitle, op.DocSummary)
		} else if op.Kind == "removed" {
			fmt.Fprintf(&change, "<document_removed>\n<title>%s</title>\n<summary>%s</summary>\n</document_removed>\n\n", op.DocTitle, op.DocSummary)
		}
	}
	var intro string
	switch {
	case existingIntro == "" || existingIntro == "Wiki index - table of contents":
		summaries := []*types.WikiPage{}
		for _, p := range pages {
			if p.PageType == "summary" && p.Status != "archived" {
				summaries = append(summaries, p)
			}
		}
		sort.SliceStable(summaries, func(i, j int) bool { return summaries[i].UpdatedAt.After(summaries[j].UpdatedAt) })
		total := len(summaries)
		if len(summaries) > 200 {
			summaries = summaries[:200]
		}
		var docs strings.Builder
		for _, p := range summaries {
			fmt.Fprintf(&docs, "<document>\n<title>%s</title>\n<summary>%s</summary>\n</document>\n\n", p.Title, p.Summary)
		}
		if docs.Len() == 0 {
			docs.WriteString("(no documents yet)")
		}
		framing := ""
		if total > len(summaries) {
			framing = fmt.Sprintf("(showing %d most recent of %d total documents)\n\n", len(summaries), total)
		}
		intro, err = s.generate(ctx, "index", "", s.job.SourceVersionIDs, wiki.WikiIndexIntroPrompt, map[string]string{"DocumentSummaries": framing + docs.String(), "Language": language(s.library.WikiLanguage), "CustomInstructions": s.contentInstructions(), "InstructionScope": "wiki_content"})
		if err != nil {
			if isCancellation(err) {
				return err
			}
			intro = "# Wiki Index\n\nThis wiki contains knowledge extracted from uploaded documents.\n"
		}
	case change.Len() > 0:
		intro, err = s.generate(ctx, "index", "", s.job.SourceVersionIDs, wiki.WikiIndexIntroUpdatePrompt, map[string]string{"ExistingIntro": existingIntro, "ChangeDescription": change.String(), "DocumentSummaries": "", "Language": language(s.library.WikiLanguage), "CustomInstructions": s.contentInstructions(), "InstructionScope": "wiki_content"})
		if err != nil {
			if isCancellation(err) {
				return err
			}
			intro = existingIntro
		}
	default:
		intro = existingIntro
	}
	if at := strings.Index(intro, "\n## "); at >= 0 {
		intro = strings.TrimSpace(intro[:at])
	}
	index.Content = strings.TrimSpace(intro)
	index.Summary = index.Content
	if index.Content == "" {
		index.Content = existingIntro
		index.Summary = existingIntro
	}
	index.Content = wiki.NormalizeIndexIntro(index.Content)
	index.Summary = index.Content
	mutation := types.PageMutation{Page: index, ExpectedVersion: expected}
	// 恢复可能读到同正文的新版本或读取时间；幂等身份必须覆盖完整提交内容。
	_, err = s.publish(ctx, "index/"+hash(marshal(mutation)), &types.Publication{Maintenance: true, Pages: []types.PageMutation{mutation}}, indexKeys...)
	return err
}
func (s *liteService) finalizeLinks(ctx context.Context, ops []*types.WikiFinalizeOp) error {
	pages, err := s.pages(ctx)
	if err != nil {
		return err
	}
	bySlug := map[string]*types.WikiPage{}
	live := map[string]struct{}{}

	for _, page := range pages {
		if page.Status == "archived" {
			continue
		}
		bySlug[page.Slug] = page
		live[page.Slug] = struct{}{}

	}
	affected := []string{}
	fresh := []wiki.LinkRef{}
	for _, op := range ops {
		if op.Kind == "slug" {
			affected = appendUniqueString(affected, op.Slug)
			if op.Title != "" {
				fresh = append(fresh, wiki.LinkRef{Slug: op.Slug, MatchText: op.Title})
			}
		}
	}
	for _, slug := range affected {
		old := bySlug[slug]
		if old == nil || old.PageType == "index" {
			continue
		}
		page := clonePage(*old)
		dead := map[string]struct{}{}
		for _, target := range page.OutLinks {
			if _, ok := live[target]; !ok {
				dead[target] = struct{}{}
			}
		}
		localLive := map[string]struct{}{}
		localTitles := map[string]string{}
		for _, target := range page.OutLinks {
			if linked := bySlug[target]; linked != nil {
				localLive[target] = struct{}{}
				localTitles[linked.Title] = target
			}
		}
		body, _ := stripDeadWikiLinks(page.Content, dead, localLive, localTitles)
		refs := []wiki.LinkRef{}
		for _, target := range page.OutLinks {
			if target != slug && bySlug[target] != nil {
				refs = append(refs, wiki.LinkRef{Slug: target, MatchText: bySlug[target].Title})
			}
		}
		for _, ref := range fresh {
			if ref.Slug != slug {
				refs = append(refs, ref)
			}
		}
		protected := map[string]string{}
		for i, text := range page.ProtectedContent {
			token := fmt.Sprintf("NEUXPROTECTED%dEND", i)
			protected[token] = text
			body = strings.ReplaceAll(body, text, token)
		}
		body, _ = wiki.Linkify(body, refs, page.Slug)
		for token, text := range protected {
			body = strings.ReplaceAll(body, token, text)
		}
		if body == page.Content {
			continue
		}
		page.Content = body
		release, err := s.lockPublication(ctx)
		if err != nil {
			return err
		}
		err = s.store.UpdateWikiMaintenance(ctx, s.job.ID, s.job.Attempt, &page, old.Version)
		release()
		if err != nil {
			return err
		}
	}
	return nil
}
func splitSummaryLine(raw string) (summary string, content string) {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "SUMMARY:") || strings.HasPrefix(raw, "SUMMARY：") {
		idx := strings.IndexByte(raw, '\n')
		if idx < 0 {
			// Only one line
			return strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(raw, "SUMMARY:"), "SUMMARY：")), ""
		}
		summaryLine := raw[:idx]
		summaryLine = strings.TrimPrefix(summaryLine, "SUMMARY:")
		summaryLine = strings.TrimPrefix(summaryLine, "SUMMARY：")
		return strings.TrimSpace(summaryLine), strings.TrimSpace(raw[idx+1:])
	}
	return "", raw
}

func slugify(text string) string {
	text = strings.ToLower(strings.TrimSpace(text))
	text = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '/' || r >= 0x4E00 && r <= 0x9FFF {
			return r
		}
		if r == ' ' || r == '_' {
			return '-'
		}
		return -1
	}, text)
	for strings.Contains(text, "--") {
		text = strings.ReplaceAll(text, "--", "-")
	}
	return strings.Trim(text, "-")
}
