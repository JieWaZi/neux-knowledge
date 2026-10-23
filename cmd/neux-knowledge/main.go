// Command neux-knowledge provides actual local SDK operations; the host injects model organization ports through the SDK.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/JieWaZi/neux-knowledge/core"
	"github.com/JieWaZi/neux-knowledge/rules"
	"io"
	"os"
)

type selector struct {
	LibraryID       string `json:"libraryId"`       // Target library.
	ID              string `json:"id"`              // Library, page, or job identity.
	PageID          string `json:"pageId"`          // Page identity.
	SourceVersionID string `json:"sourceVersionId"` // Immutable source version.
	PurgeID         string `json:"purgeId"`         // Accepted source purge identity.
	Slug            string `json:"slug"`            // Stable page address.
	RequestID       string `json:"requestId"`       // Idempotent receipt identity.
	Offset          int    `json:"offset"`          // Pagination start.
	Limit           int    `json:"limit"`           // Pagination limit.
	Version         int    `json:"version"`         // Historical revision version or stage attempt.
	Kind            string `json:"kind"`            // Rule type or schema stage.
	WikiLanguage    string `json:"wikiLanguage"`    // Language bound to the rules.
}
type listResult struct {
	Items any   `json:"items"` // Actual persisted entities.
	Total int64 `json:"total"` // Actual query total.
}
type operation struct {
	Name        string `json:"name"`        // Actual CLI command.
	Input       string `json:"input"`       // Corresponding public SDK input type.
	Description string `json:"description"` // Capability description.
}

var operations = []operation{
	{"library.create", "CreateLibraryInput", "创建本地知识库"}, {"library.update", "UpdateLibraryInput", "条件更新知识库"}, {"library.get", "{id}", "读取知识库"}, {"library.list", "{offset,limit}", "分页读取库"},
	{"source.import", "ImportSourceInput", "原字节以 JSON base64 bytes 保存，不启动模型"}, {"source.archive", "ArchiveSourceInput", "条件归档原件"}, {"source.get", "{libraryId,sourceVersionId}", "读取不可变来源版本"}, {"source.list", "{libraryId,offset,limit}", "分页读取原件"}, {"source.raw", "{libraryId,sourceVersionId}", "读取真实原字节"},
	{"page.write", "WritePageInput", "人工条件写入知识页"}, {"page.restore", "RestorePageInput", "恢复历史正文并保留当前位置"}, {"page.get", "{libraryId,pageId}", "读取知识页"}, {"page.slug", "{libraryId,slug}", "按稳定地址读取"}, {"page.list", "PageQuery", "分页和 SQLite 关键词搜索"}, {"revision.list", "{libraryId,pageId,offset,limit}", "真实修订列表"}, {"revision.get", "{libraryId,pageId,version}", "读取具体修订"},
	{"theme.write", "WriteThemeInput", "条件写入人工主题"}, {"theme.list", "{libraryId}", "读取真实目录"},
	{"job.accept", "AcceptJobInput", "冻结显式来源、权限和配置"}, {"job.get", "{id}", "读取持久任务"}, {"job.list", "{libraryId,offset,limit}", "任务分页"}, {"job.retry", "JobActionInput", "条件重试失败任务"}, {"stage.list", "{id}", "读取纯知识阶段诊断"}, {"stage.get", "{id}", "读取精确知识阶段诊断"}, {"receipt.get", "{requestId}", "对账已提交回执"},
	{"rules.get", "{kind,wikiLanguage}", "读取真实受信规则资源及摘要"},
	{"audit", "{libraryId}", "只读核查断链、孤儿、短正文与失效来源，不自动修复"}, {"export", "ExportInput", "从一致快照导出已发布Markdown；--out写入新目录"},
	{"source.purge.list", "{libraryId,offset,limit}", "分页读取持久清理进度，重启后可继续恢复"},
	{"source.purge.preview", "PurgePreviewInput", "只读预览一个来源全部版本及清理影响摘要"}, {"source.purge.confirm", "ConfirmSourcePurgeInput", "显式confirmed=true且摘要未变才接纳永久清理"}, {"source.purge.get", "{libraryId,purgeId}", "读取真实清理进度和保留边界"}, {"source.purge.recover", "RecoverSourcePurgeInput", "按同一持久清单与当前权限代次继续原件清理"},
}

func main() {
	if e := run(os.Args[1:], os.Stdin, os.Stdout); e != nil {
		_ = json.NewEncoder(os.Stderr).Encode(map[string]any{"error": e.Error()})
		os.Exit(1)
	}
}
func run(args []string, input io.Reader, output io.Writer) error {
	fs := flag.NewFlagSet("neux-knowledge", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	db := fs.String("db", "", "独立 SQLite 文件")
	raw := fs.String("raw", "", "受管原件目录")
	exportDir := fs.String("out", "", "Markdown 导出的新目录，父目录必须存在")
	if e := fs.Parse(args); e != nil {
		return e
	}
	if fs.NArg() != 1 {
		return errors.New("usage: neux-knowledge [--db file --raw directory] discover|operation < input.json")
	}
	command := fs.Arg(0)
	if *exportDir != "" && command != "export" {
		return errors.New("--out is only valid for export")
	}
	if command == "discover" {
		return json.NewEncoder(output).Encode(map[string]any{"protocolVersion": "1", "operations": operations, "organize": "SDK RunOrganize requires an explicit real ReaderPort/Generator; no production fixture or default model", "sqlite": "FTS5/BM25, sqlite-vec, normalized hybrid RRF"})
	}
	payload, e := io.ReadAll(io.LimitReader(input, 64<<20))
	if e != nil {
		return e
	}
	if len(payload) == 0 {
		payload = []byte(`{}`)
	}
	var sel selector
	if e = json.Unmarshal(payload, &sel); e != nil {
		return e
	}

	if command == "rules.get" {
		bundle, e := rules.Load(sel.Kind, sel.WikiLanguage)
		if e != nil {
			return e
		}
		return json.NewEncoder(output).Encode(bundle)
	}
	known := false
	for _, op := range operations {
		if op.Name == command {
			known = true
			break
		}
	}
	if !known {
		return fmt.Errorf("unknown operation %q; run discover", command)
	}
	ctx := context.Background()
	engine, e := core.Open(ctx, core.Options{DBPath: *db, RawDir: *raw})
	if e != nil {
		return e
	}
	defer engine.Close()
	var result any
	switch command {
	case "source.purge.list":
		limit := sel.Limit
		if limit == 0 {
			limit = 50
		}
		result, e = engine.ListSourcePurges(ctx, sel.LibraryID, sel.Offset, limit)
	case "source.purge.preview":
		var in core.PurgePreviewInput
		if e = decodeInput(payload, &in); e == nil {
			result, e = engine.PreviewSourcePurge(ctx, in)
		}
	case "source.purge.confirm":
		var in core.ConfirmSourcePurgeInput
		if e = decodeInput(payload, &in); e == nil {
			result, e = engine.ConfirmSourcePurge(ctx, in)
		}
	case "source.purge.get":
		result, e = engine.GetSourcePurge(ctx, sel.LibraryID, sel.PurgeID)
	case "source.purge.recover":
		var in core.RecoverSourcePurgeInput
		if e = decodeInput(payload, &in); e == nil {
			result, e = engine.RecoverSourcePurge(ctx, in)
		}
	case "audit":
		var in struct {
			LibraryID string `json:"libraryId"` // Sole read-only audit scope.
		}
		if e = decodeInput(payload, &in); e == nil {
			result, e = engine.LintLibrary(ctx, in.LibraryID)
		}
	case "export":
		var in core.ExportInput
		if e = decodeInput(payload, &in); e == nil {
			var bundle *core.MarkdownExport
			bundle, e = engine.ExportMarkdown(ctx, in)
			result = bundle
			if e == nil && *exportDir != "" {
				result, e = writeMarkdownExport(ctx, *exportDir, *db, *raw, bundle)
			}
		}
	case "library.create":
		var in core.CreateLibraryInput
		if e = decodeInput(payload, &in); e == nil {
			result, e = engine.CreateLibrary(ctx, in)
		}
	case "library.update":
		var in core.UpdateLibraryInput
		if e = decodeInput(payload, &in); e == nil {
			result, e = engine.UpdateLibrary(ctx, in)
		}
	case "library.get":
		result, e = engine.GetLibrary(ctx, sel.ID)
	case "library.list":
		var items []*core.Library
		var total int64
		items, total, e = engine.ListLibraries(ctx, sel.Offset, sel.Limit)
		result = listResult{items, total}
	case "source.import":
		var in core.ImportSourceInput
		if e = decodeInput(payload, &in); e == nil {
			result, e = engine.ImportSource(ctx, in)
		}
	case "source.archive":
		var in core.ArchiveSourceInput
		if e = decodeInput(payload, &in); e == nil {
			result, e = engine.ArchiveSource(ctx, in)
		}
	case "source.get":
		result, e = engine.GetSource(ctx, sel.LibraryID, sel.SourceVersionID)
	case "source.raw":
		var bytes []byte
		bytes, e = engine.ReadRaw(ctx, sel.LibraryID, sel.SourceVersionID)
		result = map[string]any{"bytes": bytes}
	case "source.list":
		var items []*core.SourceVersion
		var total int64
		items, total, e = engine.ListSources(ctx, sel.LibraryID, sel.Offset, sel.Limit)
		result = listResult{items, total}
	case "page.write":
		var in core.WritePageInput
		if e = decodeInput(payload, &in); e == nil {
			result, e = engine.WritePage(ctx, in)
		}
	case "page.restore":
		var in core.RestorePageInput
		if e = decodeInput(payload, &in); e == nil {
			result, e = engine.RestorePage(ctx, in)
		}
	case "page.get":
		result, e = engine.GetPage(ctx, sel.LibraryID, sel.PageID)
	case "page.slug":
		result, e = engine.GetPageBySlug(ctx, sel.LibraryID, sel.Slug)
	case "page.list":
		var in core.PageQuery
		if e = decodeInput(payload, &in); e == nil {
			var items []*core.Page
			var total int64
			items, total, e = engine.ListPages(ctx, in)
			result = listResult{items, total}
		}
	case "revision.list":
		var items []*core.Revision
		var total int64
		items, total, e = engine.ListRevisions(ctx, sel.LibraryID, sel.PageID, sel.Offset, sel.Limit)
		result = listResult{items, total}
	case "revision.get":
		result, e = engine.GetRevision(ctx, sel.LibraryID, sel.PageID, sel.Version)
	case "theme.write":
		var in core.WriteThemeInput
		if e = decodeInput(payload, &in); e == nil {
			result, e = engine.WriteTheme(ctx, in)
		}
	case "theme.list":
		result, e = engine.ListThemes(ctx, sel.LibraryID)
	case "job.accept":
		var in core.AcceptJobInput
		if e = decodeInput(payload, &in); e == nil {
			result, e = engine.AcceptJob(ctx, in)
		}
	case "job.get":
		result, e = engine.GetJob(ctx, sel.ID)
	case "job.list":
		var items []*core.Job
		var total int64
		items, total, e = engine.ListJobs(ctx, sel.LibraryID, sel.Offset, sel.Limit)
		result = listResult{items, total}

	case "job.retry":
		var in core.JobActionInput
		if e = decodeInput(payload, &in); e == nil {
			result, e = engine.RetryJob(ctx, in)
		}
	case "stage.list":
		result, e = engine.ListStages(ctx, sel.ID)
	case "stage.get":
		result, e = engine.GetStage(ctx, sel.ID)
	case "receipt.get":
		result, e = engine.GetReceipt(ctx, sel.RequestID)
	}
	if e != nil {
		return e
	}
	return json.NewEncoder(output).Encode(result)
}
func decodeInput(payload []byte, target any) error {
	d := json.NewDecoder(bytes.NewReader(payload))
	d.DisallowUnknownFields()
	return d.Decode(target)
}
