package types

import (
	"database/sql/driver"
	"encoding/json"
	"strings"
	"time"

	"gorm.io/gorm"
)

// WikiCategoryMaxDepth is the hard cap on how many folder levels a wiki page's
// category_path may keep. The ingest prompts intentionally ask the model for at
// most 2 levels to keep the directory tree shallow; this storage cap is one
// level deeper as a defensive bound so a slightly over-eager model (or a
// reconcile remap) cannot create an unbounded breadcrumb. It is the single
// source of truth shared by the service, repository, and taxonomy layers so
// stored paths and queried paths are always cleaned identically.
const WikiCategoryMaxDepth = 3

var wikiCategorySeparatorReplacer = strings.NewReplacer("／", "/", "｜", "/", "|", "/")

// CleanWikiCategoryPart normalizes a single raw category label that may itself
// carry embedded separators, wrapping quotes/brackets, or page-type noise, and
// returns the cleaned sub-labels (type labels such as "entity" or its Chinese equivalent are dropped).
func CleanWikiCategoryPart(part string) []string {
	part = strings.TrimSpace(part)
	if part == "" {
		return nil
	}
	part = wikiCategorySeparatorReplacer.Replace(part)
	rawParts := strings.Split(part, "/")
	cleaned := make([]string, 0, len(rawParts))
	for _, raw := range rawParts {
		label := strings.TrimSpace(raw)
		label = strings.Trim(label, `"'“”‘’[]（）()`)
		label = strings.TrimSpace(label)
		if label == "" || isWikiTypeCategoryLabel(label) {
			continue
		}
		cleaned = append(cleaned, label)
	}
	return cleaned
}

// CleanWikiCategoryPath cleans, deduplicates, and caps a full category path at
// WikiCategoryMaxDepth. Centralizing this guarantees that the path a page is
// stored with and the path a list/filter query is matched against go through
// the exact same normalization, so directory filters cannot silently drift.
func CleanWikiCategoryPath(parts []string) []string {
	cleaned := make([]string, 0, len(parts))
	for _, part := range parts {
		for _, label := range CleanWikiCategoryPart(part) {
			if containsWikiString(cleaned, label) {
				continue
			}
			cleaned = append(cleaned, label)
			if len(cleaned) >= WikiCategoryMaxDepth {
				return cleaned
			}
		}
	}
	return cleaned
}

// TrimWikiFolderSegments keeps folder path segments verbatim, dropping only
// blank entries. Folder names are validated on creation (no separators), and
// the folder tree is the source of truth for a page's placement, so unlike
// CleanWikiCategoryPath no page-type filtering, deduplication, or depth cap
// applies: a user may legitimately name a folder "Concepts" in English or Chinese.
func TrimWikiFolderSegments(parts []string) []string {
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// WikiFolderPathSegments splits a materialized folder path ("AI/RAG") into its
// literal segments. An empty/blank path yields nil (the wiki root).
func WikiFolderPathSegments(path string) []string {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	return TrimWikiFolderSegments(strings.Split(path, "/"))
}

// SplitWikiPageTypes parses a page_type value that may carry several
// comma-separated types (e.g. "entity,concept") into a deduplicated slice,
// dropping blanks. An empty/whitespace-only input yields nil ("no filter").
// Shared by the handler (query parsing) and repository (List filter) so the
// two layers split identically.
func SplitWikiPageTypes(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	seen := make(map[string]struct{})
	out := make([]string, 0, 4)
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if _, ok := seen[part]; ok {
			continue
		}
		seen[part] = struct{}{}
		out = append(out, part)
	}
	return out
}

func isWikiTypeCategoryLabel(label string) bool {
	normalized := strings.ToLower(strings.TrimSpace(label))
	normalized = strings.TrimSuffix(normalized, "s")
	switch normalized {
	case "entity", "实体", "實體", "concept", "概念", "summary", "摘要", "wiki", "页面", "頁面":
		return true
	default:
		return false
	}
}

func containsWikiString(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}

// WikiPageType constants define the types of wiki pages
const (
	// WikiPageTypeSummary represents a document summary page
	WikiPageTypeSummary = "summary"
	// WikiPageTypeEntity represents an entity page (person, organization, place, etc.)
	WikiPageTypeEntity = "entity"
	// WikiPageTypeConcept represents a concept/topic page
	WikiPageTypeConcept = "concept"
	// WikiPageTypeIndex represents the wiki index page (index.md)
	WikiPageTypeIndex = "index"
	// WikiPageTypeSynthesis represents a synthesis/analysis page.
	// NOT auto-created by ingest — Agent creates these via wiki_write_page tool
	// when it generates cross-document analysis, trends, or insights during conversations.
	WikiPageTypeSynthesis = "synthesis"
	// WikiPageTypeComparison represents a comparison page.
	// NOT auto-created by ingest — Agent creates these via wiki_write_page tool
	// when the user asks to compare entities, concepts, or approaches.
	WikiPageTypeComparison = "comparison"
)

// WikiPageStatus constants
const (
	// WikiPageStatusDraft indicates the page is a draft
	WikiPageStatusDraft = "draft"
	// WikiPageStatusPublished indicates the page is published and visible
	WikiPageStatusPublished = "published"
	// WikiPageStatusArchived indicates the page is archived
	WikiPageStatusArchived = "archived"
)

// IsValidWikiPageType reports whether pageType is one of the known page
// types. Unknown values would silently disappear from the type-filtered
// listings the browser is built on, so write paths reject them.
func IsValidWikiPageType(pageType string) bool {
	switch pageType {
	case WikiPageTypeSummary, WikiPageTypeEntity, WikiPageTypeConcept,
		WikiPageTypeIndex, WikiPageTypeSynthesis, WikiPageTypeComparison:
		return true
	default:
		return false
	}
}

// IsValidWikiPageStatus reports whether status is one of the known statuses.
func IsValidWikiPageStatus(status string) bool {
	switch status {
	case WikiPageStatusDraft, WikiPageStatusPublished, WikiPageStatusArchived:
		return true
	default:
		return false
	}
}

// WikiPage represents a single wiki page in a wiki knowledge base.
// Wiki pages are LLM-generated, interlinked markdown documents that form
// a persistent, compounding knowledge artifact.
type WikiPage struct {
	// WikiLanguage 正文语言，与界面语言独立。
	WikiLanguage string `json:"wikiLanguage"`
	// Evidence 唯一可写的结构化证据，来源引用由核心推导。
	Evidence PageEvidence `json:"evidence" gorm:"serializer:json"`
	// ManualPlacement 是否保护人工确定的目录和顺序。
	ManualPlacement bool `json:"manualPlacement"`
	// ProtectedContent 必须逐字保留的人工正文片段。
	ProtectedContent StringArray `json:"protectedContent" gorm:"type:json"`
	// ID 页面稳定身份。
	ID string `json:"id" gorm:"type:varchar(36);primaryKey"`
	// TenantID 兼容上游算法的租户字段，独立核心固定为零。
	TenantID uint64 `json:"tenant_id" gorm:"index"`
	// KnowledgeBaseID 所属知识库的稳定身份。
	KnowledgeBaseID string `json:"knowledge_base_id" gorm:"type:varchar(36);index;uniqueIndex:idx_kb_slug"`
	// Slug 库内唯一的可读页面地址。
	Slug string `json:"slug" gorm:"type:varchar(255);uniqueIndex:idx_kb_slug" extensions:"x-agent"`
	// Title 用户可见的页面标题。
	Title string `json:"title" gorm:"type:varchar(512)" extensions:"x-agent"`
	// PageType 知识页面类型。
	PageType string `json:"page_type" gorm:"type:varchar(32);index" extensions:"x-agent"`
	// Status 草稿、已发布或已归档状态。
	Status string `json:"status" gorm:"type:varchar(32);default:'published'" extensions:"x-agent"`
	// Content Markdown 正文。
	Content string `json:"content" gorm:"type:text"`
	// Summary 目录中显示的页面简介。
	Summary string `json:"summary" gorm:"type:text" extensions:"x-agent"`
	// Aliases 精确身份匹配和搜索使用的别名。
	Aliases StringArray `json:"aliases" gorm:"type:json"`
	// ParentSlug 可选的语义父页面地址。
	ParentSlug string `json:"parent_slug,omitempty" gorm:"type:varchar(255);index"`
	// FolderID 页面所属目录的唯一权威身份，空值表示根目录。
	FolderID string `json:"folder_id,omitempty" gorm:"column:folder_id;type:varchar(36);index;default:''" extensions:"x-agent"`
	// CategoryPath 从所属目录链推导的面包屑缓存。
	CategoryPath StringArray `json:"category_path,omitempty" gorm:"type:json"`
	// WikiPath 根据目录与标题推导的显示及排序路径。
	WikiPath string `json:"wiki_path,omitempty" gorm:"type:varchar(1024);index"`
	// Depth 目录链层数的缓存。
	Depth int `json:"depth,omitempty" gorm:"default:0;index"`
	// SortOrder 同级显示顺序，值小的在前。
	SortOrder int `json:"sort_order,omitempty" gorm:"default:0;index"`
	// SourceRefs 由结构化证据推导的来源引用，不能独立写入。
	SourceRefs StringArray `json:"source_refs" gorm:"type:json"`
	// ChunkRefs 贡献正文的可验证原文分块引用。
	ChunkRefs StringArray `json:"chunk_refs" gorm:"type:json"`
	// InLinks 引用当前页面的入链地址缓存。
	InLinks StringArray `json:"in_links" gorm:"type:json"`
	// OutLinks 从当前正文解析的出链地址缓存。
	OutLinks StringArray `json:"out_links" gorm:"type:json"`
	// PageMetadata 页面附加结构化信息。
	PageMetadata JSON `json:"page_metadata" gorm:"column:page_metadata;type:json"`
	// Version 用户可见内容版本；同次整理最多增加一版，内部中间写入另以 UpdatedAt 校验。
	Version int `json:"version" gorm:"default:1" extensions:"x-agent"`
	// LastEditSource 当前内容版本的实际作者类型，历史快照沿用该归属。
	LastEditSource string `json:"last_edit_source,omitempty" gorm:"type:varchar(16);default:''"`
	// LastEditorID 当前内容版本的实际编辑者身份。
	LastEditorID string `json:"last_editor_id,omitempty" gorm:"type:varchar(64);default:''"`
	// CreatedAt 记录创建时间。
	CreatedAt time.Time `json:"created_at"`
	// UpdatedAt 最近一次写入时间，也是同一内容版本内的读取屏障。
	UpdatedAt time.Time `json:"updated_at" extensions:"x-agent"`
	// DeletedAt 软删除时间，不表示原件物理清理完成。
	DeletedAt gorm.DeletedAt `json:"deleted_at" gorm:"index"`
}

// PageDirectoryMaxLimit 限制轻量目录单批的最大条数。
const PageDirectoryMaxLimit = 1000

// WikiPageDirectoryEntry 只承载导航、搜索与索引展示需要的页面字段。
type WikiPageDirectoryEntry struct {
	// ID 页面稳定身份。
	ID string `json:"id"`
	// KnowledgeBaseID 所属知识库，用于远端范围校验。
	KnowledgeBaseID string `json:"knowledge_base_id"`
	// Slug 库内可读地址。
	Slug string `json:"slug"`
	// Title 页面标题。
	Title string `json:"title"`
	// PageType 页面类型。
	PageType string `json:"page_type"`
	// Summary 目录与索引展示的简介。
	Summary string `json:"summary"`
	// FolderID 页面所属目录。
	FolderID string `json:"folder_id"`
	// CategoryPath 索引展示的目录路径。
	CategoryPath StringArray `json:"category_path" gorm:"type:json"`
	// SortOrder 同目录展示顺序。
	SortOrder int `json:"sort_order"`
	// Version 当前内容版本。
	Version int `json:"version"`
}

// WikiPageDetail adds display titles to the page detail response without
// persisting them or loading backlink content.
type WikiPageDetail struct {
	// embedded contains the page body or common request conditions embedded in this entry.
	*WikiPage
	// InLinkTitles maps backlink slugs to display titles.
	InLinkTitles map[string]string `json:"in_link_titles"`
}

// TableName specifies the database table name
func (WikiPage) TableName() string {
	return "wiki_pages"
}

// Wiki edit source constants describe who authored a page version. Stored in
// WikiPage.LastEditSource (current version) and WikiPageRevision.EditSource
// (historical versions).
const (
	// WikiEditSourcePipeline marks versions written by the wiki ingest
	// pipeline (also the default for an omitted source).
	WikiEditSourcePipeline = "pipeline"
	// WikiEditSourceAgent marks versions written through the agent wiki
	// tools (wiki_write_page / wiki_replace_text / ...).
	WikiEditSourceAgent = "agent"
	// WikiEditSourceUser marks versions written by a human through the
	// wiki editor UI / REST API.
	WikiEditSourceUser = "user"
	// WikiEditSourceRevert marks versions produced by rolling the page
	// back to an earlier revision.
	WikiEditSourceRevert = "revert"
)

// NormalizeWikiEditSource maps unknown / empty values to
// WikiEditSourcePipeline so an omitted or unknown source uses
// the historical behavior ("the machine wrote this").
func NormalizeWikiEditSource(source string) string {
	switch source {
	case WikiEditSourceAgent, WikiEditSourceUser, WikiEditSourceRevert, WikiEditSourcePipeline:
		return source
	default:
		return WikiEditSourcePipeline
	}
}

// WikiPageRevision is one immutable snapshot of a superseded page version.
// The current version lives only in wiki_pages; when an edit replaces
// version V the pre-edit state is inserted here as (page_id, V) inside the
// same transaction, so every historical version stays diffable and
// revertable. Rows are pruned per WikiRevisionPruneRequest to bound storage
// on hot pipeline pages.
type WikiPageRevision struct {
	// WikiLanguage stores the content language at that time.
	WikiLanguage string `json:"wikiLanguage"`
	// Evidence stores the sole authoritative evidence at that time.
	Evidence PageEvidence `json:"evidence" gorm:"serializer:json"`
	// SourceRefs stores upstream source references derived from evidence.
	SourceRefs StringArray `json:"source_refs" gorm:"type:json"`
	// ChunkRefs stores reader chunk references at that time.
	ChunkRefs StringArray `json:"chunk_refs" gorm:"type:json"`
	// PageMetadata stores additional page information at that time.
	PageMetadata JSON `json:"page_metadata" gorm:"type:json"`
	// ProtectedContent stores manually protected fragments at that time.
	ProtectedContent StringArray `json:"protectedContent" gorm:"type:json"`
	// ID is the stable record identifier.
	ID string `json:"id" gorm:"type:varchar(36);primaryKey"`
	// TenantID is retained for the upstream algorithm and is fixed at zero in the standalone core.
	TenantID uint64 `json:"tenant_id" gorm:"index"`
	// KnowledgeBaseID is the stable identifier of the owning knowledge library.
	KnowledgeBaseID string `json:"knowledge_base_id" gorm:"type:varchar(36);index:idx_wiki_page_revisions_kb_slug"`
	// PageID is the stable identifier of the corresponding page.
	PageID string `json:"page_id" gorm:"type:varchar(36);uniqueIndex:idx_wiki_page_revisions_page_version"`
	// Slug is the page's stable readable identifier.
	Slug string `json:"slug" gorm:"type:varchar(255);index:idx_wiki_page_revisions_kb_slug"`
	// Version records the corresponding version number for explicit history and concurrency boundaries.
	Version int `json:"version" gorm:"uniqueIndex:idx_wiki_page_revisions_page_version" extensions:"x-agent"`
	// Title is the visible page title.
	Title string `json:"title" gorm:"type:varchar(512)" extensions:"x-agent"`
	// PageType is the page's knowledge type.
	PageType string `json:"page_type" gorm:"type:varchar(32)"`
	// Status is the current state of the page or issue.
	Status string `json:"status" gorm:"type:varchar(32)"`
	// Content is the page body in Markdown.
	Content string `json:"content,omitempty" gorm:"type:text" extensions:"x-agent,x-agent-max-runes=8000"`
	// Summary stores the page introduction for directory entries without the full content.
	Summary string `json:"summary" gorm:"type:text" extensions:"x-agent"`
	// Aliases supports exact identity matching and keyword queries.
	Aliases StringArray `json:"aliases" gorm:"type:json"`
	// Author of THIS version (same semantics as WikiPage.LastEditSource).
	// EditSource is the actual write source of this historical version.
	EditSource string `json:"edit_source" gorm:"type:varchar(16);default:''" extensions:"x-agent"`
	// EditorID is the editor identifier of this historical version.
	EditorID string `json:"editor_id" gorm:"type:varchar(64);default:''"`
	// When this version was authored (the page's updated_at while current).
	// EditedAt is the last edit time of this historical version.
	EditedAt time.Time `json:"edited_at" extensions:"x-agent"`
	// CreatedAt is the record creation time.
	CreatedAt time.Time `json:"created_at"`
}

// TableName specifies the database table name
func (WikiPageRevision) TableName() string {
	return "wiki_page_revisions"
}

// Wiki 修订统一保留最近 50 个版本（含当前页），不按编辑来源延长保留。
const (
	// WikiMaxRevisionsPerPage 是每页当前版本与历史快照合计的统一上限。
	WikiMaxRevisionsPerPage = 50
	// WikiMaxRevisionsHardCap 与统一上限一致，供底层裁剪请求使用。
	WikiMaxRevisionsHardCap = WikiMaxRevisionsPerPage
)

// WikiPrunableEditSources lists the edit sources whose snapshots may be
// dropped by the soft cap. Everything else (user / agent / revert) survives
// until the hard cap. Legacy rows carry an empty source, hence "".
var WikiPrunableEditSources = []string{"", WikiEditSourcePipeline}

// WikiRevisionPruneRequest describes the two-tier retention applied to one
// page's snapshot history. A version threshold <= 0 disables that tier.
type WikiRevisionPruneRequest struct {
	// PageID is the stable identifier of the corresponding page.
	PageID string
	// KeepFromVersion: snapshots below this version are dropped, but only
	// when their edit source is listed in PrunableSources.
	// KeepFromVersion is the lowest history version retained under the source protection policy.
	KeepFromVersion int
	// PrunableSources contains write sources eligible for pruning under soft retention rules.
	PrunableSources []string
	// HardKeepFromVersion: snapshots below this version are always dropped,
	// whatever their author.
	// HardKeepFromVersion is the hard lower bound for history retention regardless of source.
	HardKeepFromVersion int
}

// WikiPageUpdateRequest is the partial-update payload for
// PUT /wiki/pages/*slug. All content fields are optional pointers — absent
// fields keep their stored value, so a client can change just the body
// without re-sending (and risking clobbering) title/status/aliases.
type WikiPageUpdateRequest struct {
	// Title is the visible page title.
	Title *string `json:"title,omitempty"`
	// Content is the page body in Markdown.
	Content *string `json:"content,omitempty"`
	// Summary stores the page introduction for directory entries without the full content.
	Summary *string `json:"summary,omitempty"`
	// PageType is the page's knowledge type.
	PageType *string `json:"page_type,omitempty"`
	// Status is the current state of the page or issue.
	Status *string `json:"status,omitempty"`
	// Aliases supports exact identity matching and keyword queries.
	Aliases *StringArray `json:"aliases,omitempty"`
	// Version is the optimistic-lock guard: when > 0 the update is rejected
	// with a conflict if the stored version differs (someone else edited the
	// page since the client loaded it). 0 skips the check in the upstream low-level API.
	// Version records the corresponding version number for explicit history and concurrency boundaries.
	Version int `json:"version,omitempty"`
}

// WikiPageRevisionListResponse is the payload for GET /wiki/revisions/*slug.
// Revisions hold the superseded versions (content omitted in list mode);
// the current version is described by CurrentVersion + the page row itself,
// which the frontend already has.
type WikiPageRevisionListResponse struct {
	// Revisions contains historical versions matching the pagination conditions.
	Revisions []*WikiPageRevision `json:"revisions"`
	// Total is the complete filtered record count.
	Total int64 `json:"total" extensions:"x-agent"`
	// CurrentVersion is the page's current version number.
	CurrentVersion int `json:"current_version" extensions:"x-agent"`
}

// WikiPageRevertRequest rolls a page back to one of its stored revisions.
// Slug travels in the body (not the path) for the same reason as
// WikiPageMoveRequest: hierarchical slugs collide with gin's catch-all.
type WikiPageRevertRequest struct {
	// Slug of the page to revert.
	// Slug is the page's stable readable identifier.
	Slug string `json:"slug" binding:"required" extensions:"x-agent"`
	// Revision to restore. Versions start at 1.
	// Version records the corresponding version number for explicit history and concurrency boundaries.
	Version int `json:"version" binding:"required" minimum:"1" extensions:"x-agent"`
}

// WikiFolderRootID is the sentinel parent/folder id meaning "the wiki root"
// (a page or folder directly under the top level, with no parent folder).
const WikiFolderRootID = ""

// WikiFolder is a first-class directory node in the wiki browser. Folders
// exist independently of pages — an empty folder persists so users can lay
// out a skeleton and file pages into it later. The tree is an adjacency list
// (ParentID, "" = root); Path is the materialized "/"-joined name chain kept
// purely for cheap display/sort. A wiki page's placement is WikiPage.FolderID.
type WikiFolder struct {
	// SystemKey identifies the unique system theme and is empty for ordinary manual themes.
	SystemKey string `json:"systemKey"`
	// Manual prevents generation from overwriting the manual theme name, placement, and order.
	Manual bool `json:"manual"`
	// ID is the stable record identifier.
	ID string `json:"id" gorm:"type:varchar(36);primaryKey" extensions:"x-agent=folder_id"`
	// TenantID is retained for the upstream algorithm and is fixed at zero in the standalone core.
	TenantID uint64 `json:"tenant_id" gorm:"index"`
	// KnowledgeBaseID is the stable identifier of the owning knowledge library.
	KnowledgeBaseID string `json:"knowledge_base_id" gorm:"type:varchar(36);index"`
	// ParentID is the parent theme identifier; empty denotes the root.
	ParentID string `json:"parent_id" gorm:"column:parent_id;type:varchar(36);index;default:''" extensions:"x-agent"`
	// Name is the name of a theme, stage, or test scenario.
	Name string `json:"name" gorm:"type:varchar(255)" extensions:"x-agent"`
	// Path is a display path derived from the theme name chain.
	Path string `json:"path" gorm:"type:varchar(1024)" extensions:"x-agent"`
	// Depth is the number of levels in the current theme or category path.
	Depth int `json:"depth" gorm:"default:0"`
	// SortOrder controls sibling display order; smaller values come first.
	SortOrder int `json:"sort_order" gorm:"default:0"`
	// CreatedAt is the record creation time.
	CreatedAt time.Time `json:"created_at"`
	// UpdatedAt is the most recent record update time.
	UpdatedAt time.Time `json:"updated_at"`
	// DeletedAt is the upstream soft-deletion time and does not indicate completion of physical original-file cleanup.
	DeletedAt gorm.DeletedAt `json:"deleted_at" gorm:"index"`
}

// TableName specifies the database table name
func (WikiFolder) TableName() string {
	return "wiki_folders"
}

// WikiFolderNode is one directory node returned to the browser, enriched with
// the live page count directly under it and whether it has child folders so
// the UI can render an expand affordance without a second round-trip.
type WikiFolderNode struct {
	// embedded contains the page body or common request conditions embedded in this entry.
	WikiFolder
	// PageCount is the number of pages in the theme.
	PageCount int64 `json:"page_count" extensions:"x-agent"`
	// HasChildren reports whether direct child themes exist.
	HasChildren bool `json:"has_children" extensions:"x-agent"`
}

// WikiFolderListResponse is the payload for listing the direct children of a
// folder (parent_id="" = root level).
type WikiFolderListResponse struct {
	// ParentID is the parent theme identifier; empty denotes the root.
	ParentID string `json:"parent_id"`
	// Folders contains the actual directory nodes.
	Folders []WikiFolderNode `json:"folders"`
}

// WikiFolderCreateRequest creates a new (initially empty) folder under ParentID.
type WikiFolderCreateRequest struct {
	// Parent folder id; empty means the top level.
	// ParentID is the parent theme identifier; empty denotes the root.
	ParentID string `json:"parent_id" extensions:"x-agent"`
	// Folder name.
	// Name is the name of a theme, stage, or test scenario.
	Name string `json:"name" extensions:"x-agent"`
}

// WikiFolderUpdateRequest renames and/or reparents a folder. ParentID is
// applied only when MoveParent is true so a pure rename doesn't have to
// re-send the (possibly root "") parent and risk an accidental move.
type WikiFolderUpdateRequest struct {
	// Name is the name of a theme, stage, or test scenario.
	Name string `json:"name,omitempty"`
	// ParentID is the parent theme identifier; empty denotes the root.
	ParentID string `json:"parent_id,omitempty"`
	// MoveParent explicitly requests a parent theme change.
	MoveParent bool `json:"move_parent,omitempty"`
}

// WikiPageMoveRequest relocates the page identified by Slug into FolderID
// ("" = root). Slug is carried in the body (not the path) because wiki slugs
// are hierarchical ("entity/acme") and would collide with gin's catch-all.
type WikiPageMoveRequest struct {
	// Slug of the page to move.
	// Slug is the page's stable readable identifier.
	Slug string `json:"slug" binding:"required" extensions:"x-agent"`
	// Target folder id; empty means the top level.
	// FolderID is the stable identifier of the actual owning theme.
	FolderID string `json:"folder_id" extensions:"x-agent"`
}

// WikiExtractionGranularity controls how aggressive Pass 0 (candidate slug
// extraction) is. Higher granularity = more slugs, lower = tighter focus on
// the document's main subjects.
type WikiExtractionGranularity string

const (
	// WikiExtractionFocused keeps only the document's main subjects (e.g.
	// a resume yields the person + their projects, nothing else). Most
	// aggressive slug pruning; avoids index bloat from incidental technology
	// names and generic concepts.
	WikiExtractionFocused WikiExtractionGranularity = "focused"

	// WikiExtractionStandard is the default: main subjects plus entities /
	// concepts that are substantively discussed (a dedicated paragraph or
	// multiple bullet points). Skips one-off mentions and commodity terms.
	WikiExtractionStandard WikiExtractionGranularity = "standard"

	// WikiExtractionExhaustive extracts every named entity and recognizable
	// concept, including stacks/libs mentioned in passing. Matches the
	// pre-granularity behavior. Useful when the KB is being used as a
	// glossary rather than a curated wiki.
	WikiExtractionExhaustive WikiExtractionGranularity = "exhaustive"
)

// IsValid reports whether g is one of the three recognized levels.
func (g WikiExtractionGranularity) IsValid() bool {
	switch g {
	case WikiExtractionFocused, WikiExtractionStandard, WikiExtractionExhaustive:
		return true
	}
	return false
}

// Normalize returns g if valid, otherwise WikiExtractionStandard. Callers
// pipe config through this so historical rows with empty / unknown values
// don't surprise the extraction prompt.
func (g WikiExtractionGranularity) Normalize() WikiExtractionGranularity {
	if g.IsValid() {
		return g
	}
	return WikiExtractionStandard
}

// WikiConfig stores wiki-specific configuration for a knowledge base.
// Applicable to document-type knowledge bases with wiki feature enabled.
// Whether the wiki feature is turned on is controlled by IndexingStrategy.WikiEnabled;
// this struct only carries wiki-specific tunables.
type WikiConfig struct {
	// SynthesisModelID is the model that writes Wiki pages. Empty uses summary_model_id.
	// SynthesisModelID is the upstream generation model configuration identifier; the standalone core does not construct providers.
	SynthesisModelID string `yaml:"synthesis_model_id" json:"synthesis_model_id" extensions:"x-agent=wiki_synthesis_model_id"`
	// MaxPagesPerIngest limits pages created or updated per ingest. 0 means no limit.
	// MaxPagesPerIngest limits pages created or modified by one organization run.
	MaxPagesPerIngest int `yaml:"max_pages_per_ingest" json:"max_pages_per_ingest" minimum:"0" maximum:"1000" extensions:"x-agent=wiki_max_pages"`
	// ExtractionGranularity is focused, standard or exhaustive. Empty means standard.
	// ExtractionGranularity controls candidate knowledge extraction granularity.
	ExtractionGranularity WikiExtractionGranularity `yaml:"extraction_granularity" json:"extraction_granularity,omitempty" enums:"focused,standard,exhaustive" extensions:"x-agent=wiki_granularity"`
	// ContentInstructions sets tone and emphasis for generated Wiki prose. Citations stay fixed.
	// ContentInstructions supplies extra instructions for content style and emphasis.
	ContentInstructions string `yaml:"content_instructions,omitempty" json:"content_instructions,omitempty" extensions:"x-agent=wiki_content_instructions,x-agent-detail,x-agent-max-bytes=4000"`
	// ExtractionInstructions tells page extraction which concepts to keep. The output format stays fixed.
	// ExtractionInstructions supplies instructions for candidate knowledge extraction priorities.
	ExtractionInstructions string `yaml:"extraction_instructions,omitempty" json:"extraction_instructions,omitempty" extensions:"x-agent=wiki_extraction_instructions,x-agent-detail,x-agent-max-bytes=4000"`

	// Wiki ingest concurrency is two-level:
	//   1. batch-level: multiple batches per KB run concurrently in the wiki
	//      worker pool, capped by IngestMaxInflight (below).
	//   2. batch-internal: within one batch, Map and Reduce fan out with
	//      errgroups sized by IngestMapParallel / IngestReduceParallel.
	// Effective peak in-flight LLM calls for one KB ≈
	//   IngestMaxInflight × max(IngestMapParallel, IngestReduceParallel),
	// further bounded by the wiki pool size (WEKNORA_WIKI_ASYNQ_CONCURRENCY).

	// IngestBatchSize controls how many pending ops a single batch claims and
	// processes before scheduling a follow-up. 0 falls back to the hard-coded
	// default (5). Larger batches amortize per-batch setup and let more docs
	// share the batch-internal Map/Reduce fan-out; smaller batches spread a
	// KB's backlog across more concurrent batches (finer scheduling grain).
	// IngestBatchSize is the number of sources accepted per batch.
	IngestBatchSize int `yaml:"ingest_batch_size" json:"ingest_batch_size,omitempty"`

	// IngestMapParallel sets the errgroup limit for the Map phase
	// (per-document extraction + summary + chunk citation) WITHIN one batch.
	// 0 falls back to 10. Bound by the LLM provider's concurrency limit and
	// the worker's outbound HTTP pool. Remember it multiplies with the number
	// of concurrent batches (IngestMaxInflight).
	// IngestMapParallel limits concurrency in the source extraction stage of one batch.
	IngestMapParallel int `yaml:"ingest_map_parallel" json:"ingest_map_parallel,omitempty"`

	// IngestReduceParallel sets the errgroup limit for the Reduce phase
	// (per-slug page write) WITHIN one batch. 0 falls back to 10. Bound by the
	// same LLM concurrency / HTTP pool considerations as the Map phase, plus
	// DB connection pool size. Same multiplier caveat as IngestMapParallel.
	// IngestReduceParallel limits concurrency in the per-page merge stage of one batch.
	IngestReduceParallel int `yaml:"ingest_reduce_parallel" json:"ingest_reduce_parallel,omitempty"`

	// IngestMaxInflight caps how many ingest batches for THIS KB may run
	// concurrently in the shared wiki worker pool (standard/Redis mode
	// only). 0 falls back to the hard-coded default (4). Since Phase 3
	// removed the exclusive per-KB lock, one KB's backlog could otherwise
	// monopolize the whole pool during a bulk import and starve other KBs;
	// this knob trades a single KB's peak throughput for cross-KB fairness.
	// Set it >= the wiki pool size to effectively disable the cap.
	// IngestMaxInflight limits concurrent organization batches within the library.
	IngestMaxInflight int `yaml:"ingest_max_inflight" json:"ingest_max_inflight,omitempty"`
}

// IngestBatchSizeOrDefault returns IngestBatchSize when set (> 0),
// otherwise the hard-coded fallback. Centralized so callers don't have
// to repeat the 0-check.
func (c *WikiConfig) IngestBatchSizeOrDefault(fallback int) int {
	if c == nil || c.IngestBatchSize <= 0 {
		return fallback
	}
	return c.IngestBatchSize
}

// IngestMapParallelOrDefault returns IngestMapParallel when set,
// otherwise the hard-coded fallback.
func (c *WikiConfig) IngestMapParallelOrDefault(fallback int) int {
	if c == nil || c.IngestMapParallel <= 0 {
		return fallback
	}
	return c.IngestMapParallel
}

// IngestReduceParallelOrDefault returns IngestReduceParallel when set,
// otherwise the hard-coded fallback.
func (c *WikiConfig) IngestReduceParallelOrDefault(fallback int) int {
	if c == nil || c.IngestReduceParallel <= 0 {
		return fallback
	}
	return c.IngestReduceParallel
}

// IngestMaxInflightOrDefault returns IngestMaxInflight when set,
// otherwise the hard-coded fallback.
func (c *WikiConfig) IngestMaxInflightOrDefault(fallback int) int {
	if c == nil || c.IngestMaxInflight <= 0 {
		return fallback
	}
	return c.IngestMaxInflight
}

// Value implements the driver.Valuer interface
func (c WikiConfig) Value() (driver.Value, error) {
	return json.Marshal(c)
}

// Scan implements the sql.Scanner interface
func (c *WikiConfig) Scan(value interface{}) error {
	if value == nil {
		return nil
	}
	b, ok := value.([]byte)
	if !ok {
		return nil
	}
	return json.Unmarshal(b, c)
}

// WikiPageListRequest represents a request to list wiki pages with filtering
type WikiPageListRequest struct {
	// KnowledgeBaseID is the stable identifier of the owning knowledge library.
	KnowledgeBaseID string `json:"knowledge_base_id"`
	// PageType is the page's knowledge type.
	PageType string `json:"page_type,omitempty"` // filter by type
	// Status is the current state of the page or issue.
	Status string `json:"status,omitempty"` // filter by status
	// Query contains plain keyword query text.
	Query string `json:"query,omitempty"` // full-text search
	// FolderID is the stable identifier of the actual owning theme.
	FolderID *string `json:"folder_id,omitempty"` // exact folder placement ("" = root)
	// CategoryPath is a category path derived from the current theme chain.
	CategoryPath StringArray `json:"category_path,omitempty"` // exact directory path
	// CategoryDepth optionally filters by exact category depth.
	CategoryDepth *int `json:"category_depth,omitempty"` // exact directory depth, including 0 for root
	// Page is the one-based page number.
	Page int `json:"page,omitempty"` // pagination page (1-based)
	// PageSize limits the records returned per page.
	PageSize int `json:"page_size,omitempty"` // pagination size
	// SortBy is the allowlist-validated sort field.
	SortBy string `json:"sort_by,omitempty"` // "updated_at", "created_at", "title"
	// SortOrder controls sibling display order; smaller values come first.
	SortOrder string `json:"sort_order,omitempty"` // "asc" or "desc"
}

// WikiPageListResponse represents a paginated list of wiki pages
type WikiPageListResponse struct {
	// Pages contains pages within the current window.
	Pages []*WikiPage `json:"pages"`
	// Total is the complete filtered record count.
	Total int64 `json:"total"`
	// Page is the one-based page number.
	Page int `json:"page"`
	// PageSize limits the records returned per page.
	PageSize int `json:"page_size"`
	// TotalPages is the page count calculated from the complete total.
	TotalPages int `json:"total_pages"`
}

// WikiGraphMode enumerates the graph query modes exposed to the API.
const (
	// WikiGraphModeOverview returns the top-N most-connected pages as an
	// overview of the knowledge base. Intended for the first graph open.
	WikiGraphModeOverview = "overview"
	// WikiGraphModeEgo returns the neighborhood around a center page up to a
	// configurable depth. Intended for drill-down interactions.
	WikiGraphModeEgo = "ego"
)

// WikiGraphRequest is the service-layer input for graph queries. It is
// populated by the HTTP handler from query params and passed down to the
// service, which is responsible for enforcing mode-specific semantics.
//
// Limit policy: a non-positive `Limit` means "no cap" and is reserved for
// internal callers (e.g. wiki lint) that need the full graph. The HTTP
// handler always clamps `Limit` into a safe range before calling the
// service so external traffic can never request an uncapped graph.
type WikiGraphRequest struct {
	// KnowledgeBaseID is the stable identifier of the owning knowledge library.
	KnowledgeBaseID string
	// Mode selects the graph query scope.
	Mode string // "overview" (default) | "ego"
	// Center is the central page slug for a local relationship graph.
	Center string // ego mode center slug (required when Mode == "ego")
	// Depth is the number of levels in the current theme or category path.
	Depth int // ego mode BFS depth, >= 1
	// Types filters by allowed page knowledge types.
	Types []string // optional page_type filter; empty = no filter
	// Limit caps the records returned by this query.
	Limit int // max nodes to return; <= 0 means uncapped
	// FamiliarKnowledgeIDs are documents this person keeps drawing answers
	// from. Pages whose source_refs intersect the set are marked Familiar so
	// the existing Wiki graph can light them up without cloning a second graph.
	// FamiliarKnowledgeIDs contains source identifiers used for personal familiarity display.
	FamiliarKnowledgeIDs []string
}

// WikiGraphData represents the link graph structure for visualization.
type WikiGraphData struct {
	// Nodes contains page nodes in the relationship graph.
	Nodes []WikiGraphNode `json:"nodes"`
	// Edges contains directed link edges in the relationship graph.
	Edges []WikiGraphEdge `json:"edges"`
	// Meta describes the graph scope or additional handle metadata.
	Meta WikiGraphMeta `json:"meta"`
}

// WikiGraphMeta describes how the returned subgraph relates to the full
// knowledge base graph. The frontend uses `Truncated` to decide whether to
// surface a "showing X of Y" hint and to enable ego-expansion UI.
type WikiGraphMeta struct {
	// Mode selects the graph query scope.
	Mode string `json:"mode"`
	// Total is the complete filtered record count.
	Total int `json:"total"` // total node count in the KB before filtering/limit
	// Returned is the actual number of nodes currently returned.
	Returned int `json:"returned"` // number of nodes actually returned
	// Truncated reports whether an explicit limit truncated the current result.
	Truncated bool `json:"truncated"` // true when Returned < Total (after filters)
	// Center is the central page slug for a local relationship graph.
	Center string `json:"center,omitempty"` // populated in ego mode
	// Depth is the number of levels in the current theme or category path.
	Depth int `json:"depth,omitempty"` // populated in ego mode
	// FamiliarCount is how many returned nodes are lit up for this person.
	// FamiliarCount is the number of familiar pages among the currently returned nodes.
	FamiliarCount int `json:"familiar_count,omitempty"`
}

// WikiGraphNode represents a node in the wiki link graph
type WikiGraphNode struct {
	// Slug is the page's stable readable identifier.
	Slug string `json:"slug"`
	// Title is the visible page title.
	Title string `json:"title"`
	// PageType is the page's knowledge type.
	PageType string `json:"page_type"`
	// Number of inbound + outbound links
	// LinkCount is the total number of inbound and outbound page links.
	LinkCount int `json:"link_count"`
	// Familiar is true when this page was built from a document this person
	// keeps citing in answers. It is a personal overlay, not a property of
	// the page: two people looking at the same wiki see different highlights.
	// Familiar reports whether this page matches the current personal source set.
	Familiar bool `json:"familiar,omitempty"`
}

// WikiGraphEdge represents a directed edge in the wiki link graph
type WikiGraphEdge struct {
	// Source is the starting page slug of a directed edge.
	Source string `json:"source"` // source slug
	// Target is the target page slug of a directed edge.
	Target string `json:"target"` // target slug
}

// WikiStats provides aggregate statistics about the wiki
type WikiStats struct {
	// TotalPages is the page count calculated from the complete total.
	TotalPages int64 `json:"total_pages" extensions:"x-agent"`
	// PagesByType contains counts grouped by page knowledge type.
	PagesByType map[string]int64 `json:"pages_by_type"`
	// TotalLinks is the total number of links in the library.
	TotalLinks int64 `json:"total_links" extensions:"x-agent"`
	// OrphanCount is the number of pages without backlinks.
	OrphanCount int64 `json:"orphan_count" extensions:"x-agent"` // pages with no inbound links
	// RecentUpdates contains actual recently modified pages.
	RecentUpdates []*WikiPage `json:"recent_updates"` // last N updated pages
	// PendingTasks is the number of sources still awaiting processing.
	PendingTasks int64 `json:"pending_tasks" extensions:"x-agent"` // number of documents waiting to be ingested
	// IsActive reports whether organization is actually in progress.
	IsActive bool `json:"is_active" extensions:"x-agent"` // whether wiki ingestion is currently running
}

// WikiPageIssue retains the existing issue record schema so page renames and explicit cleanup can synchronize historical data.
type WikiPageIssue struct {
	// ID is the stable record identifier.
	ID string `json:"id" gorm:"type:varchar(36);primaryKey" extensions:"x-agent=issue_id"`
	// TenantID is retained for the upstream algorithm and is fixed at zero in the standalone core.
	TenantID uint64 `json:"tenant_id" gorm:"index"`
	// KnowledgeBaseID is the stable identifier of the owning knowledge library.
	KnowledgeBaseID string `json:"knowledge_base_id" gorm:"type:varchar(36);index"`
	// Slug is the page's stable readable identifier.
	Slug string `json:"slug" gorm:"type:varchar(255);index" extensions:"x-agent"`
	// IssueType is the stable issue category.
	IssueType string `json:"issue_type" gorm:"type:varchar(50)" extensions:"x-agent"`
	// Description is the factual issue description.
	Description string `json:"description" gorm:"type:text" extensions:"x-agent"`
	// SuspectedKnowledgeIDs contains suspicious source identifiers requiring review.
	SuspectedKnowledgeIDs StringArray `json:"suspected_knowledge_ids" gorm:"type:json" extensions:"x-agent=knowledge_ids"`
	// Status is the state of the historical issue record.
	Status string `json:"status" gorm:"type:varchar(20);default:'pending';index" extensions:"x-agent"`
	// ReportedBy is the actual principal reporting the issue.
	ReportedBy string `json:"reported_by" gorm:"type:varchar(100)" extensions:"x-agent"`
	// CreatedAt is the record creation time.
	CreatedAt time.Time `json:"created_at" extensions:"x-agent"`
	// UpdatedAt is the most recent record update time.
	UpdatedAt time.Time `json:"updated_at"`
	// DeletedAt is the upstream soft-deletion time and does not indicate completion of physical original-file cleanup.
	DeletedAt gorm.DeletedAt `json:"deleted_at" gorm:"index"`
}

// TableName specifies the database table name
func (WikiPageIssue) TableName() string {
	return "wiki_page_issues"
}

// WikiIndexEntry is a single row in the structured wiki index response.
// Only the columns needed to render a clickable directory entry are
// carried — the backend projects SELECT slug, title, summary so a 40k-
// page KB does not pay for TEXT content transport on every index open.
type WikiIndexEntry struct {
	// Slug is the page's stable readable identifier.
	Slug string `json:"slug"`
	// Title is the visible page title.
	Title string `json:"title"`
	// Summary stores the page introduction for directory entries without the full content.
	Summary string `json:"summary"`
	// ParentSlug is the upstream parent page identifier.
	ParentSlug string `json:"parent_slug,omitempty"`
	// CategoryPath is a category path derived from the current theme chain.
	CategoryPath StringArray `json:"category_path,omitempty"`
	// WikiPath is a derived path for directory display and sorting.
	WikiPath string `json:"wiki_path,omitempty"`
	// Depth is the number of levels in the current theme or category path.
	Depth int `json:"depth,omitempty"`
	// SortOrder controls sibling display order; smaller values come first.
	SortOrder int `json:"sort_order,omitempty"`
}

// WikiIndexGroup bundles the entries for one page_type into a page-sized
// slice. `Total` is the full count across the KB for the type; `Items`
// holds the current paginated window starting at `NextOffset - len(Items)`.
// An empty NextCursor means the window is already at the end of the type.
type WikiIndexGroup struct {
	// Type is the page knowledge type of the current group.
	Type string `json:"type"`
	// Total is the complete filtered record count.
	Total int64 `json:"total"`
	// Items contains actual entries in the current pagination window.
	Items []WikiIndexEntry `json:"items"`
	// NextCursor is the stable position of the next pagination window.
	NextCursor string `json:"next_cursor,omitempty"`
}

// WikiIndexResponse is what GET /wiki/index returns. The heavy directory
// markdown that used to sit in wiki_pages.content is gone — only the LLM-
// generated intro survives there. Everything else is assembled on demand
// from the index repo's light-column projection, keeping index reads
// O(page_size) regardless of KB size.
type WikiIndexResponse struct {
	// Intro is the Markdown introduction stored in the upstream index row, returned independently of grouped directory entries.
	Intro string `json:"intro"`
	// Version records the corresponding version number for explicit history and concurrency boundaries.
	Version int `json:"version"`
	// Groups contains directory windows organized by page knowledge type.
	Groups []WikiIndexGroup `json:"groups"`
}

// WikiPageLite is a slim projection of WikiPage carrying only the fields
// the wiki ingest pipeline reaches for during Map / Reduce. It exists so
// per-batch fetcher queries don't have to load the full multi-MB content
// column for every page they want a title or out-link from.
//
// Use cases:
//
//   - SlugTitleFetcher: resolve slug -> title for cross-link injection.
//
//   - cleanDeadLinks: read out_links + status without pulling content.
//
//   - dedup pre-filter: title + aliases + page_type for the trgm /
//     surface-similarity comparisons.
//
// Aliases is included because dedup and cross-link injection both treat
// the alias surface forms as first-class match targets; OutLinks is
// included so dead-link cleanup can determine which pages reference a
// given dead slug without a second query.
type WikiPageLite struct {
	// Slug is the page's stable readable identifier.
	Slug string `json:"slug"`
	// Title is the visible page title.
	Title string `json:"title"`
	// PageType is the page's knowledge type.
	PageType string `json:"page_type"`
	// Status is the current state of the page or issue.
	Status string `json:"status"`
	// Aliases supports exact identity matching and keyword queries.
	Aliases StringArray `json:"aliases,omitempty"`
	// OutLinks contains stable slugs referenced by this page.
	OutLinks StringArray `json:"out_links,omitempty"`
}

// ParseWikiSourceRef splits a source_refs entry into its durable knowledge ID
// and optional display title. Entries are stored as "uuid" or "uuid|title";
// both halves are trimmed and a leading "|" yields an empty ID. This is the
// only parser of the format: every reader must go through it so that a ref
// with stray whitespace resolves identically on the scope-authorization,
// deletion, lint, and rendering paths.
func ParseWikiSourceRef(ref string) (knowledgeID, title string) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", ""
	}
	i := strings.IndexByte(ref, '|')
	if i < 0 {
		return ref, ""
	}
	return strings.TrimSpace(ref[:i]), strings.TrimSpace(ref[i+1:])
}

// FormatWikiSourceRef builds the stored source_refs entry for a knowledge ID
// and optional title. A title that contains "|" is kept verbatim: the parser
// splits on the first pipe only, so the ID half stays unambiguous.
func FormatWikiSourceRef(knowledgeID, title string) string {
	knowledgeID = strings.TrimSpace(knowledgeID)
	title = strings.TrimSpace(title)
	if knowledgeID == "" {
		return ""
	}
	if title == "" {
		return knowledgeID
	}
	return knowledgeID + "|" + title
}

// WikiSourceKnowledgeID extracts the knowledge id from a source_refs entry,
// stored as "uuid" or "uuid|title".
func WikiSourceKnowledgeID(ref string) string {
	knowledgeID, _ := ParseWikiSourceRef(ref)
	return knowledgeID
}

// WikiSourceRefMatchesKnowledge reports whether a source_refs entry points at
// the given knowledge ID, in either the bare or the "uuid|title" form.
func WikiSourceRefMatchesKnowledge(ref, knowledgeID string) bool {
	knowledgeID = strings.TrimSpace(knowledgeID)
	return knowledgeID != "" && WikiSourceKnowledgeID(ref) == knowledgeID
}

// SourceKnowledgeIDs returns the document ids this page was built from.
func (p *WikiPage) SourceKnowledgeIDs() []string {
	if p == nil || len(p.SourceRefs) == 0 {
		return nil
	}
	ids := make([]string, 0, len(p.SourceRefs))
	for _, ref := range p.SourceRefs {
		if id := WikiSourceKnowledgeID(ref); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

// BuiltFrom reports whether any of this page's sources is in the given set.
func (p *WikiPage) BuiltFrom(knowledgeIDs map[string]struct{}) bool {
	if p == nil || len(knowledgeIDs) == 0 {
		return false
	}
	for _, id := range p.SourceKnowledgeIDs() {
		if _, ok := knowledgeIDs[id]; ok {
			return true
		}
	}
	return false
}
