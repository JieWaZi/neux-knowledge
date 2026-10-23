// Package remote accesses the authority only through HTTP and owns no local database or model ports.
package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/JieWaZi/neux-knowledge/core"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// WeKnoraProfile is a contract verified against upstream source; it does not indicate detection of a running server's version.
const WeKnoraProfile = "weknora-8776a2d92d11ad3e76865d51cf3192e37e2a9976"

// Options configures a standalone HTTP client with authentication scoped to this connection.
type Options struct {
	// BaseURL is the deployment address and may include a reverse-proxy prefix.
	BaseURL string
	// Token is an in-memory authentication credential never returned in errors or responses.
	Token string
	// AuthMode is bearer or api_key, defaulting to bearer.
	AuthMode string
	// HTTPClient allows transport injection; redirects are always disabled to prevent credentials crossing sites.
	HTTPClient *http.Client
}

// Error distinguishes network, authentication, permission, resource, and protocol failures without including sensitive remote response bodies.
type Error struct {
	// Code is a stable machine error code.
	Code string `json:"code"`
	// Status is the HTTP status, or zero for a network failure.
	Status int `json:"status"`
}

func (e *Error) Error() string {
	return fmt.Sprintf("knowledge remote: %s (HTTP %d)", e.Code, e.Status)
}

// Capability describes server capabilities in the fixed contract; the server still determines permissions on every request.
type Capability struct {
	// Supported indicates known protocol support, not credential authorization.
	Supported bool `json:"supported"`
	// Permission defaults to unknown; a successful connection does not imply write access.
	Permission string `json:"permission"`
	// Reason explains contract differences for unavailable actions.
	Reason string `json:"reason,omitempty"`
}

// WeKnora adapts the fixed official protocol without references to local Readers or Generators.
type WeKnora struct {
	// base holds the validated deployment address.
	base *url.URL
	// token is injected only into request headers for the current server.
	token string
	// authMode selects the authentication header supported by the service.
	authMode string
	// http performs bounded requests with redirects disabled.
	http *http.Client
}

func NewWeKnora(o Options) (*WeKnora, error) {
	u, err := url.Parse(o.BaseURL)
	if err != nil || u == nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.TrimSpace(o.Token) == "" {
		return nil, &Error{Code: "invalid_connection"}
	}
	// 标准接入接受完整 API 基址，内部路径仍统一从部署根拼接。
	if strings.HasSuffix(strings.TrimRight(u.Path, "/"), "/api/v1") {
		u.Path = strings.TrimSuffix(strings.TrimRight(u.Path, "/"), "/api/v1")
		u.RawPath = ""
	}
	if o.AuthMode == "" {
		o.AuthMode = "bearer"
	}
	if o.AuthMode != "bearer" && o.AuthMode != "api_key" {
		return nil, &Error{Code: "invalid_connection"}
	}
	c := http.Client{Timeout: 30 * time.Second}
	if o.HTTPClient != nil {
		c = *o.HTTPClient
		if c.Timeout == 0 {
			c.Timeout = 30 * time.Second
		}
	}
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &WeKnora{base: u, token: o.Token, authMode: o.AuthMode, http: &c}, nil
}
func (c *WeKnora) Capabilities() map[string]Capability {
	caps := map[string]Capability{}
	for _, op := range []string{"libraries", "pages", "folders", "graph", "revisions", "search", "sources", "source_download"} {
		caps[op] = Capability{Supported: true, Permission: "unknown"}
	}
	for _, op := range []string{"write", "agentWrite", "replaceText", "renamePage", "deletePage", "libraryDelete", "library_delete", "raw_upload", "organize", "archive", "atomic_restore", "versioned_edit", "software_rules", "manual_placement_protection", "no_source_summaries"} {
		caps[op] = Capability{Permission: "unknown", Reason: "official_profile_has_no_verified_custom_contract"}
	}
	return caps
}

func (c *WeKnora) request(ctx context.Context, path string, q url.Values, max int64) ([]byte, http.Header, error) {
	u := *c.base
	u.Path = strings.TrimRight(c.base.Path, "/") + path
	u.RawPath = ""
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, nil, &Error{Code: "invalid_request"}
	}
	if c.authMode == "api_key" {
		req.Header.Set("X-API-Key", c.token)
	} else {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	req.Header.Set("Accept", "application/json, application/octet-stream")
	resp, err := c.http.Do(req)
	if err != nil {
		code := "unreachable"
		if ctx.Err() != nil {
			code = "cancelled"
		}
		return nil, nil, &Error{Code: code}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		code := "server_error"
		switch resp.StatusCode {
		case 401:
			code = "unauthenticated"
		case 403:
			code = "forbidden"
		case 404:
			code = "not_found"
		case 409:
			code = "version_conflict"
		case 429:
			code = "rate_limited"
		}
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			code = "redirect_rejected"
		}
		return nil, nil, &Error{Code: code, Status: resp.StatusCode}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, nil, &Error{Code: "response_incomplete"}
	}
	if int64(len(data)) > max {
		return nil, nil, &Error{Code: "response_too_large"}
	}
	return data, resp.Header, nil
}
func protocolError() error { return &Error{Code: "protocol_incompatible"} }
func (c *WeKnora) object(ctx context.Context, path string, q url.Values, required []string, out any) error {
	b, _, err := c.request(ctx, path, q, 16<<20)
	if err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(b, &fields) != nil || fields == nil {
		return protocolError()
	}
	for _, key := range required {
		v, ok := fields[key]
		if !ok || string(v) == "null" {
			return protocolError()
		}
	}
	if json.Unmarshal(b, out) != nil {
		return protocolError()
	}
	return nil
}
func safeID(id string) bool {
	return id != "" && id != "." && id != ".." && !strings.ContainsAny(id, "/\\\x00\r\n")
}
func safeSlug(slug string) bool {
	for _, p := range strings.Split(slug, "/") {
		if !safeID(p) {
			return false
		}
	}
	return true
}
func wikiPath(kb, suffix string) (string, error) {
	if !safeID(kb) {
		return "", &Error{Code: "invalid_resource"}
	}
	return "/api/v1/knowledgebase/" + kb + "/wiki/" + suffix, nil
}
func positivePagination(page, size int) (int, int, error) {
	if page == 0 {
		page = 1
	}
	if size == 0 {
		size = 50
	}
	if page < 1 || size < 1 || size > 100 {
		return 0, 0, &Error{Code: "invalid_pagination"}
	}
	return page, size, nil
}

// Library preserves official library types and permission hints without presenting them as custom general/software types.
type Library struct {
	// ID is the original remote identity.
	ID string `json:"id"`
	// Name is the remote name.
	Name string `json:"name"`
	// Type is an official type such as wiki.
	Type string `json:"type"`
	// Permission is an optional sharing-permission hint returned by the server.
	Permission string `json:"my_permission"`
}

func (c *WeKnora) ListLibraries(ctx context.Context) ([]Library, error) {
	var v struct {
		// Success is the official business success flag.
		Success bool `json:"success"`
		// Data contains libraries visible to the server.
		Data []Library `json:"data"`
	}
	err := c.object(ctx, "/api/v1/knowledge-bases", nil, []string{"success", "data"}, &v)
	if err != nil {
		return nil, err
	}
	if !v.Success {
		return nil, protocolError()
	}
	for _, x := range v.Data {
		if x.ID == "" || x.Name == "" {
			return nil, protocolError()
		}
	}
	return v.Data, nil
}
func (c *WeKnora) GetLibrary(ctx context.Context, id string) (*Library, error) {
	if !safeID(id) {
		return nil, &Error{Code: "invalid_resource"}
	}
	var v struct {
		// Success is the official business success flag.
		Success bool `json:"success"`
		// Data contains library details.
		Data Library `json:"data"`
	}
	err := c.object(ctx, "/api/v1/knowledge-bases/"+id, nil, []string{"success", "data"}, &v)
	if err != nil {
		return nil, err
	}
	if !v.Success || v.Data.ID != id {
		return nil, protocolError()
	}
	return &v.Data, nil
}

// Page contains displayable fields from the fixed official Wiki DTO.
type Page struct {
	// ID is the stable page identity.
	ID string `json:"id"`
	// KnowledgeBaseID is the authoritative library identity.
	KnowledgeBaseID string `json:"knowledge_base_id"`
	// Slug preserves the original hierarchical value.
	Slug string `json:"slug"`
	// Title is the title.
	Title string `json:"title"`
	// Summary is the current page description, compared using the same field in historical snapshots.
	Summary string `json:"summary"`

	// PageType is the official page type.
	PageType string `json:"page_type"`
	// Content is the Markdown returned when reading.
	Content string `json:"content"`
	// Version is the remote content version.
	Version int `json:"version"`
	// FolderID is the remote topic identity.
	FolderID string `json:"folder_id"`
	// Aliases contains query aliases.
	Aliases []string `json:"aliases"`
	// SourceRefs contains official source references.
	SourceRefs []string `json:"source_refs"`
	// InLinks contains inbound link handles.
	InLinks []string `json:"in_links"`
	// OutLinks contains outbound link handles.
	OutLinks []string `json:"out_links"`
}

// PageOptions uses official one-based page numbers; an offset must not be passed directly as page.
type PageOptions struct {
	// Page is the page number.
	Page int
	// PageSize is the bounded page size.
	PageSize int
	// Query is the server-side keyword query.
	Query string
	// PageType is the page filter.
	PageType string
	// FolderID is an optional exact topic filter; nil means no filtering.
	FolderID *string
}

// PageList preserves official pagination facts.
type PageList struct {
	// Pages is the list on this page.
	Pages []Page `json:"pages"`
	// Total is the total count.
	Total int64 `json:"total"`
	// Page is the server page number.
	Page int `json:"page"`
	// PageSize is the page limit.
	PageSize int `json:"page_size"`
	// TotalPages is the server's total page count.
	TotalPages int `json:"total_pages"`
}

func (c *WeKnora) ListPages(ctx context.Context, kb string, o PageOptions) (*PageList, error) {
	path, err := wikiPath(kb, "pages")
	if err != nil {
		return nil, err
	}
	page, size, err := positivePagination(o.Page, o.PageSize)
	if err != nil {
		return nil, err
	}
	q := url.Values{"page": {strconv.Itoa(page)}, "page_size": {strconv.Itoa(size)}, "query": {o.Query}, "page_type": {o.PageType}}
	if o.FolderID != nil {
		q.Set("folder_id", *o.FolderID)
	}
	var v PageList
	if err = c.object(ctx, path, q, []string{"pages", "total", "page", "page_size", "total_pages"}, &v); err != nil {
		return nil, err
	}
	if v.Page < 1 || v.PageSize < 1 || v.Total < 0 || v.TotalPages < 0 {
		return nil, protocolError()
	}
	for _, p := range v.Pages {
		if p.ID == "" || p.Slug == "" || p.KnowledgeBaseID != kb || p.Version < 1 {
			return nil, protocolError()
		}
	}
	return &v, nil
}
func (c *WeKnora) GetPage(ctx context.Context, kb, slug string) (*Page, error) {
	if !safeSlug(slug) {
		return nil, &Error{Code: "invalid_resource"}
	}
	path, err := wikiPath(kb, "pages/"+slug)
	if err != nil {
		return nil, err
	}
	var v Page
	if err = c.object(ctx, path, nil, []string{"id", "knowledge_base_id", "slug", "content", "version"}, &v); err != nil {
		return nil, err
	}
	if v.ID == "" || v.KnowledgeBaseID != kb || v.Slug != slug || v.Version < 1 {
		return nil, protocolError()
	}
	return &v, nil
}

// Revision preserves official history identity and content; the current page is excluded from the history list.
type Revision struct {
	// ID is the revision identity.
	ID string `json:"id"`
	// PageID is the owning page.
	PageID string `json:"page_id"`
	// Version is the historical content version.
	Version int `json:"version"`
	// Title is the title at that time.
	Title string `json:"title"`
	// Summary is the page description at that time and participates in historical diff comparisons.
	Summary string `json:"summary"`

	// Content returns full content only when content_version is requested.
	Content string `json:"content"`
	// ContentPresent distinguishes valid empty content from omitted content.
	ContentPresent bool `json:"-"`
	// EditSource is the remote edit source.
	EditSource string `json:"edit_source"`
	// EditedAt is the remote timestamp.
	EditedAt string `json:"edited_at"`
}

// RevisionOptions selects a history range or explicitly reads a single version's content.
type RevisionOptions struct {
	// Offset is the starting position.
	Offset int
	// Limit is the upper bound.
	Limit int
	// ContentVersion selects the version whose content accompanies the list.
	ContentVersion int
}

// RevisionList preserves the current version and history total.
type RevisionList struct {
	// Revisions is the history list.
	Revisions []Revision `json:"revisions"`
	// Total is the historical revision count.
	Total int64 `json:"total"`
	// CurrentVersion is the current content version.
	CurrentVersion int `json:"current_version"`
}

func (c *WeKnora) ListRevisions(ctx context.Context, kb, slug string, o RevisionOptions) (*RevisionList, error) {
	if !safeSlug(slug) || o.Offset < 0 || o.ContentVersion < 0 {
		return nil, &Error{Code: "invalid_resource"}
	}
	if o.Limit == 0 {
		o.Limit = 50
	}
	if o.Limit < 1 || o.Limit > 100 {
		return nil, &Error{Code: "invalid_pagination"}
	}
	path, err := wikiPath(kb, "revisions/"+slug)
	if err != nil {
		return nil, err
	}
	q := url.Values{"offset": {strconv.Itoa(o.Offset)}, "limit": {strconv.Itoa(o.Limit)}}
	if o.ContentVersion > 0 {
		q.Set("content_version", strconv.Itoa(o.ContentVersion))
	}
	var v RevisionList
	if err = c.object(ctx, path, q, []string{"revisions", "total", "current_version"}, &v); err != nil {
		return nil, err
	}
	if v.CurrentVersion < 1 || v.Total < 0 {
		return nil, protocolError()
	}
	for _, r := range v.Revisions {
		if r.ID == "" || r.PageID == "" || r.Version < 1 {
			return nil, protocolError()
		}
	}
	if o.ContentVersion > 0 {
		if len(v.Revisions) != 1 || v.Revisions[0].Version != o.ContentVersion || !v.Revisions[0].ContentPresent {
			return nil, protocolError()
		}
	}
	return &v, nil
}

// Folder is an official tree node; a single response is not treated as the entire tree.
type Folder struct {
	// ID is the folder identity.
	ID string `json:"id"`
	// KnowledgeBaseID is the owning library.
	KnowledgeBaseID string `json:"knowledge_base_id"`
	// ParentID is the parent folder.
	ParentID string `json:"parent_id"`
	// Name is the folder name.
	Name string `json:"name"`
	// Path is the display path.
	Path string `json:"path"`
	// SortOrder is the sort order.
	SortOrder int `json:"sort_order"`
	// PageCount is the direct page count.
	PageCount int64 `json:"page_count"`
	// HasChildren indicates whether further reads are needed.
	HasChildren bool `json:"has_children"`
}

// FolderList returns nodes under one parent folder.
type FolderList struct {
	// ParentID is the parent for this request.
	ParentID string `json:"parent_id"`
	// Folders contains direct child folders.
	Folders []Folder `json:"folders"`
}

func (c *WeKnora) ListFolders(ctx context.Context, kb, parent string) (*FolderList, error) {
	path, err := wikiPath(kb, "folders")
	if err != nil {
		return nil, err
	}
	var v FolderList
	if err = c.object(ctx, path, url.Values{"parent_id": {parent}}, []string{"parent_id", "folders"}, &v); err != nil {
		return nil, err
	}
	if v.ParentID != parent {
		return nil, protocolError()
	}
	for _, f := range v.Folders {
		if f.ID == "" || f.KnowledgeBaseID != kb {
			return nil, protocolError()
		}
	}
	return &v, nil
}

// Download preserves source binary data; download permission is independent of Wiki read permission.
type Download struct {
	// Bytes contains unconverted original bytes.
	Bytes []byte
	// Filename comes from a valid Content-Disposition and is not used as a local file path.
	Filename string
	// MIME is the remote content type.
	MIME string
}

func (c *WeKnora) DownloadSource(ctx context.Context, id string) (*Download, error) {
	if !safeID(id) {
		return nil, &Error{Code: "invalid_resource"}
	}
	b, h, err := c.request(ctx, "/api/v1/knowledge/"+id+"/download", nil, 32<<20)
	if err != nil {
		return nil, err
	}
	_, params, _ := mime.ParseMediaType(h.Get("Content-Disposition"))
	return &Download{Bytes: b, Filename: params["filename"], MIME: h.Get("Content-Type")}, nil
}

func (c *WeKnora) Search(ctx context.Context, kb, query string, limit int) ([]Page, error) {
	if strings.TrimSpace(query) == "" || limit < 1 || limit > 100 {
		return nil, &Error{Code: "invalid_query"}
	}
	path, err := wikiPath(kb, "search")
	if err != nil {
		return nil, err
	}
	var v struct {
		// Pages contains actual matched official pages.
		Pages []Page `json:"pages"`
	}
	if err = c.object(ctx, path, url.Values{"q": {query}, "limit": {strconv.Itoa(limit)}}, []string{"pages"}, &v); err != nil {
		return nil, err
	}
	for _, p := range v.Pages {
		if p.ID == "" || p.KnowledgeBaseID != kb || p.Slug == "" {
			return nil, protocolError()
		}
	}
	return v.Pages, nil
}

// GraphOptions specifies a bounded official relationship graph without claiming full graph coverage.
type GraphOptions struct {
	// Mode is overview or ego.
	Mode string
	// Center is the neighborhood's central page handle.
	Center string
	// Depth is the neighborhood depth.
	Depth int
	// Limit is the maximum node count.
	Limit int
}

// GraphNode is an official relationship node.
type GraphNode struct {
	// Slug is the original page handle.
	Slug string `json:"slug"`
	// Title is the title.
	Title string `json:"title"`
	// PageType is the type.
	PageType string `json:"page_type"`
	// LinkCount is the total relationship count.
	LinkCount int `json:"link_count"`
}

// GraphEdge is a directed knowledge relationship.
type GraphEdge struct {
	// Source is the origin handle.
	Source string `json:"source"`
	// Target is the destination handle.
	Target string `json:"target"`
}

// GraphMeta preserves partial-response facts.
type GraphMeta struct {
	// Mode is the server's actual mode.
	Mode string `json:"mode"`
	// Total is the total node count.
	Total int `json:"total"`
	// Returned is the node count in this response.
	Returned int `json:"returned"`
	// Truncated indicates incomplete graph coverage.
	Truncated bool `json:"truncated"`
}

// Graph is the relationship graph returned by the server.
type Graph struct {
	// Nodes contains actual nodes.
	Nodes []GraphNode `json:"nodes"`
	// Edges contains actual relationships.
	Edges []GraphEdge `json:"edges"`
	// Meta describes coverage.
	Meta GraphMeta `json:"meta"`
}

func (c *WeKnora) Graph(ctx context.Context, kb string, o GraphOptions) (*Graph, error) {
	path, err := wikiPath(kb, "graph")
	if err != nil {
		return nil, err
	}
	if o.Mode == "" {
		o.Mode = "overview"
	}
	if o.Limit == 0 {
		o.Limit = 100
	}
	if o.Depth == 0 {
		o.Depth = 1
	}
	if (o.Mode != "overview" && o.Mode != "ego") || o.Limit < 1 || o.Limit > 200 || o.Depth < 1 || o.Depth > 3 || (o.Mode == "ego" && !safeSlug(o.Center)) {
		return nil, &Error{Code: "invalid_query"}
	}
	q := url.Values{"mode": {o.Mode}, "limit": {strconv.Itoa(o.Limit)}, "depth": {strconv.Itoa(o.Depth)}, "center": {o.Center}}
	var v Graph
	if err = c.object(ctx, path, q, []string{"nodes", "edges", "meta"}, &v); err != nil {
		return nil, err
	}
	if v.Meta.Mode == "" || v.Meta.Total < 0 || v.Meta.Returned != len(v.Nodes) {
		return nil, protocolError()
	}
	for _, n := range v.Nodes {
		if n.Slug == "" {
			return nil, protocolError()
		}
	}
	return &v, nil
}

// Source preserves the official source identity and raw processing status without mapping it to custom Wiki publication status.
type Source struct {
	// ID is the official knowledge source identity.
	ID string `json:"id"`
	// KnowledgeBaseID is the owning library.
	KnowledgeBaseID string `json:"knowledge_base_id"`
	// Filename is the original filename.
	Filename string `json:"file_name"`
	// FileType is the official file type.
	FileType string `json:"file_type"`
	// ParseStatus is the original official parsing status.
	ParseStatus string `json:"parse_status"`
	// Title is the display title.
	Title string `json:"title"`
}

// SourceList matches the official response with success/data and pagination at the same level.
type SourceList struct {
	// Success is the business success flag.
	Success bool `json:"success"`
	// Data contains sources on the current page.
	Data []Source `json:"data"`
	// Total is the total count.
	Total int64 `json:"total"`
	// Page is the current page number.
	Page int `json:"page"`
	// PageSize is the page size.
	PageSize int `json:"page_size"`
}

func (c *WeKnora) ListSources(ctx context.Context, kb string, page, size int, options ...core.SourceQuery) (*SourceList, error) {
	if !safeID(kb) {
		return nil, &Error{Code: "invalid_resource"}
	}
	page, size, err := positivePagination(page, size)
	if err != nil {
		return nil, err
	}
	if len(options) > 1 {
		return nil, &Error{Code: "invalid_query"}
	}
	q := url.Values{"page": {strconv.Itoa(page)}, "page_size": {strconv.Itoa(size)}}
	if len(options) == 1 {
		in := options[0]
		q.Set("keyword", in.Query)
		switch in.State {
		case "", "all":
		case "pending", "processing", "failed", "cancelled":
			q.Set("parse_status", in.State)
		case "queued", "processed", "unchanged":
			return nil, &Error{Code: "unsupported_source_state"}
		default:
			return nil, &Error{Code: "invalid_query"}
		}
		q.Set("sort_by", "created_at")
		switch in.Sort {
		case "", "newest":
			q.Set("sort_order", "desc")
		case "oldest":
			q.Set("sort_order", "asc")
		default:
			return nil, &Error{Code: "invalid_query"}
		}
	}
	var v SourceList
	if err = c.object(ctx, "/api/v1/knowledge-bases/"+kb+"/knowledge", q, []string{"success", "data", "total", "page", "page_size"}, &v); err != nil {
		return nil, err
	}
	if !v.Success || v.Total < 0 || v.Page < 1 || v.PageSize < 1 {
		return nil, protocolError()
	}
	for _, s := range v.Data {
		if s.ID == "" || s.KnowledgeBaseID != kb {
			return nil, protocolError()
		}
	}
	return &v, nil
}

// GetSource verifies an official source's actual owning library and rejects attaching an arbitrary source identity to the current library.
func (c *WeKnora) GetSource(ctx context.Context, kb, id string) (*Source, error) {
	if !safeID(kb) || !safeID(id) {
		return nil, &Error{Code: "invalid_resource"}
	}
	var response struct {
		Success bool   `json:"success"` // Official business success flag.
		Data    Source `json:"data"`    // Source identity and actual ownership.
	}
	if err := c.object(ctx, "/api/v1/knowledge/"+id, nil, []string{"success", "data"}, &response); err != nil {
		return nil, err
	}
	if !response.Success || response.Data.ID != id {
		return nil, protocolError()
	}
	if response.Data.KnowledgeBaseID != kb {
		return nil, &Error{Code: "scope_violation", Status: 403}
	}
	return &response.Data, nil
}

// DownloadSourceInLibrary checks actual ownership before and after download; the official server has no atomic scoped-download contract, so a version snapshot cannot be claimed.
func (c *WeKnora) DownloadSourceInLibrary(ctx context.Context, kb, id string) (*Download, error) {
	if _, err := c.GetSource(ctx, kb, id); err != nil {
		return nil, err
	}
	value, err := c.DownloadSource(ctx, id)
	if err != nil {
		return nil, err
	}
	if _, err := c.GetSource(ctx, kb, id); err != nil {
		return nil, err
	}
	return value, nil
}

// UnmarshalJSON preserves whether official fields were actually present, preventing omitted historical content from being interpreted as cleared content.
func (r *Revision) UnmarshalJSON(b []byte) error {
	type plain Revision
	var value plain
	if err := json.Unmarshal(b, &value); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(b, &fields); err != nil {
		return err
	}
	raw, ok := fields["content"]
	value.ContentPresent = ok && string(raw) != "null"
	*r = Revision(value)
	return nil
}
