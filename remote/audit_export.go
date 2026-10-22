package remote

import (
	"context"
	"net/url"

	"github.com/JieWaZi/neux-knowledge/core"
)

// LintLibrary forwards a read-only audit request to the bound custom service.
func (c *Custom) LintLibrary(ctx context.Context) (*core.LintReport, error) {
	return customGet[core.LintReport](ctx, c, "/audit", nil, "libraryId", "issues", "pageCount")
}

// ExportMarkdown reads a consistent Markdown export snapshot from the bound custom service.
func (c *Custom) ExportMarkdown(ctx context.Context, pageIDs []string) (*core.MarkdownExport, error) {
	q := url.Values{}
	for _, id := range pageIDs {
		q.Add("pageId", id)
	}
	return customGet[core.MarkdownExport](ctx, c, "/markdown-export", q, "libraryId", "formatVersion", "files")
}
