package core

import (
	"context"

	types "github.com/JieWaZi/neux-knowledge/internal/domain"
)

// Audit and export reuse internal read-only models without requiring callers to import internal packages.
type (
	AutoFixInput       = types.AutoFixInput
	AutoFixResult      = types.AutoFixResult
	LintIssue          = types.LintIssue
	LintReport         = types.LintReport
	ExportInput        = types.ExportInput
	ExportSourceRef    = types.ExportSourceRef
	ExportPageMetadata = types.ExportPageMetadata
	MarkdownFile       = types.MarkdownFile
	MarkdownExport     = types.MarkdownExport
)

// AutoFixLibrary 复用 WeKnora 的断链、短正文和失效来源修复，保留人工内容与版本回执。
func (e *Engine) AutoFixLibrary(ctx context.Context, in AutoFixInput) (*AutoFixResult, error) {
	return e.store.AutoFixLibrary(ctx, in)
}

// LintLibrary audits a consistent snapshot without automatically repairing or persisting findings.
func (e *Engine) LintLibrary(ctx context.Context, libraryID string) (*LintReport, error) {
	return e.store.LintLibrary(ctx, libraryID)
}

// ExportMarkdown exports a transferable copy of published knowledge; SQLite remains the sole authority.
func (e *Engine) ExportMarkdown(ctx context.Context, in ExportInput) (*MarkdownExport, error) {
	return e.store.ExportMarkdown(ctx, in)
}
