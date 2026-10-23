package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/JieWaZi/neux-knowledge/core"
)

const customPath = "/api/knowledge/v1"

// ResourceRef freezes the complete remote resource target, which cannot be implicitly overridden by the library selected in the UI.
type ResourceRef struct {
	Backend         string `json:"backend"`         // custom for the owned service; weknora for the official service.
	ConnectionID    string `json:"connectionId"`    // Host-persisted connection identity.
	KnowledgeBaseID string `json:"knowledgeBaseId"` // Authoritative library identity; may be empty only during connection discovery.
}

// Custom stores only an HTTP connection and resource reference, with no local model, reader, or index.
type Custom struct {
	// integration 仅选择新公开合同，不在页面散落后端分支。
	integration bool
	transport   *WeKnora    // Reuse bounded HTTP transport configuration with redirects disabled.
	ref         ResourceRef // Complete frozen resource target.
}

// OutcomeUnknownError means a write may have committed; callers must query the original receipt rather than retry with a new request identity.
type OutcomeUnknownError struct {
	RequestID string // Original request identity for querying the actual receipt.
}

func (e *OutcomeUnknownError) Error() string { return "knowledge remote: outcome_unknown" }
func (e *OutcomeUnknownError) Unwrap() error { return &Error{Code: "outcome_unknown"} }
func NewCustom(o Options, ref ResourceRef) (*Custom, error) {
	if ref.Backend != "custom" || !safeID(ref.ConnectionID) || (ref.KnowledgeBaseID != "" && !safeID(ref.KnowledgeBaseID)) || (o.AuthMode != "" && o.AuthMode != "bearer") {
		return nil, &Error{Code: "invalid_connection"}
	}
	transport, err := NewWeKnora(o)
	if err != nil {
		return nil, err
	}
	// 存量 custom 的完整部署路径保持原样，不能按新 API 后缀自动改指目标。
	original, _ := url.Parse(o.BaseURL)
	transport.base = original
	return &Custom{transport: transport, ref: ref}, nil
}
func (c *Custom) Resource() ResourceRef { return c.ref }

// CreateLibrary does not automatically expand credential library scope; the host must explicitly bind new resources using actual receipts.
func (c *Custom) CreateLibrary(ctx context.Context, in core.CreateLibraryInput) (*core.Receipt, error) {
	if in.RequestID == "" {
		return nil, &Error{Code: "invalid_request"}
	}
	b, _, err := c.request(ctx, "POST", customPath+"/libraries", nil, in, in.RequestID)
	if err != nil {
		return nil, err
	}
	r, err := customDecode[core.Receipt](b, "requestId", "libraryId", "resourceId", "operation", "state", "version")
	if err != nil || r.RequestID != in.RequestID || r.LibraryID == "" || r.LibraryID != r.ResourceID || r.State != "saved" {
		return nil, &OutcomeUnknownError{RequestID: in.RequestID}
	}
	return r, nil
}

// GetCreationReceipt queries only connection-level creation receipts whose current creation permission and original principal the server has verified.
func (c *Custom) GetCreationReceipt(ctx context.Context, requestID string) (*core.Receipt, error) {
	if requestID == "" {
		return nil, &Error{Code: "invalid_resource"}
	}
	b, _, err := c.request(ctx, "GET", customPath+"/libraries/creation-receipts", url.Values{"requestId": {requestID}}, nil, "")
	if err != nil {
		return nil, err
	}
	v, err := customDecode[core.Receipt](b, "requestId", "libraryId", "resourceId", "operation", "state", "version")
	if err != nil || v.RequestID != requestID || v.Operation != "library.create" || !safeID(v.LibraryID) || v.ResourceID != v.LibraryID || v.State != "saved" || v.Version < 1 {
		return nil, protocolError()
	}
	return v, nil
}

func (c *Custom) path(suffix string) (string, error) {
	if !safeID(c.ref.KnowledgeBaseID) {
		return "", &Error{Code: "invalid_resource"}
	}
	return customPath + "/libraries/" + c.ref.KnowledgeBaseID + suffix, nil
}
func (c *Custom) target(library string) error {
	if !safeID(c.ref.KnowledgeBaseID) || library != c.ref.KnowledgeBaseID {
		return &Error{Code: "scope_violation"}
	}
	return nil
}
func (c *Custom) request(ctx context.Context, method, path string, q url.Values, in any, requestID string) ([]byte, http.Header, error) {
	if c.integration {
		return c.requestIntegration(ctx, method, path, q, in, requestID)
	}
	var reader io.Reader
	contentType := "application/json"
	if raw, ok := in.(rawPayload); ok {
		reader = raw.Reader
		contentType = raw.ContentType
	} else if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return nil, nil, &Error{Code: "invalid_request"}
		}
		reader = bytes.NewReader(b)
	}
	u := *c.transport.base
	u.Path = strings.TrimRight(u.Path, "/") + path
	u.RawPath = ""
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, method, u.String(), reader)
	if err != nil {
		return nil, nil, &Error{Code: "invalid_request"}
	}
	if c.transport.authMode == "api_key" {
		req.Header.Set("X-API-Key", c.transport.token)
	} else {
		req.Header.Set("Authorization", "Bearer "+c.transport.token)
	}
	if requestID != "" {
		req.Header.Set("Idempotency-Key", requestID)
	}
	req.Header.Set("Accept", "application/json, application/octet-stream")
	if in != nil {
		req.Header.Set("Content-Type", contentType)
	}
	// Automatic retries are forbidden because the server may have committed before the response was lost; callers may only query the original requestId.
	resp, err := c.transport.http.Do(req)
	unknown := func() ([]byte, http.Header, error) { return nil, nil, &OutcomeUnknownError{RequestID: requestID} }
	if err != nil {
		if method != "GET" {
			return unknown()
		}
		return nil, nil, &Error{Code: "unreachable"}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, (48<<20)+1))
	if err != nil || len(data) > 48<<20 {
		if method != "GET" {
			return unknown()
		}
		return nil, nil, &Error{Code: "response_incomplete", Status: resp.StatusCode}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if method != "GET" && resp.StatusCode >= 500 && resp.StatusCode != 501 {
			return unknown()
		}
		code := "server_error"
		switch resp.StatusCode {
		case 400:
			code = "invalid"
		case 401:
			code = "unauthenticated"
		case 403:
			code = "forbidden"
		case 404:
			code = "not_found"
		case 409:
			code = "version_conflict"
		case 501:
			code = "unsupported"
		}
		var v struct {
			Code string `json:"code"` // Stable server machine error code.
		}
		if json.Unmarshal(data, &v) == nil {
			switch v.Code {
			case "invalid", "unauthenticated", "forbidden", "not_found", "scope_violation", "version_conflict", "idempotency_conflict", "cancelled", "stale_attempt", "policy_changed", "write_ip_forbidden", "unsupported", "unsupported_profile", "manual_protected", "candidate_invalid", "execution_not_successful":
				code = v.Code
			}
		}
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			code = "redirect_rejected"
		}
		return nil, nil, &Error{Code: code, Status: resp.StatusCode}
	}
	return data, resp.Header, nil
}
func customDecode[T any](b []byte, required ...string) (*T, error) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(b, &fields) != nil || fields == nil {
		return nil, protocolError()
	}
	for _, key := range required {
		if v, ok := fields[key]; !ok || string(v) == "null" {
			return nil, protocolError()
		}
	}
	var v T
	if json.Unmarshal(b, &v) != nil {
		return nil, protocolError()
	}
	return &v, nil
}
func customGet[T any](ctx context.Context, c *Custom, suffix string, q url.Values, required ...string) (*T, error) {
	path, err := c.path(suffix)
	if err != nil {
		return nil, err
	}
	b, _, err := c.request(ctx, "GET", path, q, nil, "")
	if err != nil {
		return nil, err
	}
	return customDecode[T](b, required...)
}

// CustomList preserves the owned server's actual pagination and total.
type CustomList[T any] struct {
	Items  []T   `json:"items"`  // Actual records on this page.
	Total  int64 `json:"total"`  // Total within the server-authorized scope.
	Offset int   `json:"offset"` // Starting offset.
	Limit  int   `json:"limit"`  // Bounded limit.
}

func customList[T any](ctx context.Context, c *Custom, suffix string, q url.Values, offset, limit int) (*CustomList[T], error) {
	if offset < 0 || limit < 0 || limit > 100 {
		return nil, &Error{Code: "invalid_pagination"}
	}
	if limit == 0 {
		limit = 50
	}
	if q == nil {
		q = url.Values{}
	}
	q.Set("offset", strconv.Itoa(offset))
	q.Set("limit", strconv.Itoa(limit))
	v, err := customGet[CustomList[T]](ctx, c, suffix, q, "items", "total", "offset", "limit")
	if err == nil && (v.Total < 0 || v.Offset != offset || v.Limit != limit || len(v.Items) > limit) {
		return nil, protocolError()
	}
	return v, err
}
func (c *Custom) write(ctx context.Context, suffix, requestID string, in any) (*core.Receipt, error) {
	if requestID == "" {
		return nil, &Error{Code: "invalid_request"}
	}
	path, err := c.path(suffix)
	if err != nil {
		return nil, err
	}
	b, _, err := c.request(ctx, "POST", path, nil, in, requestID)
	if err != nil {
		return nil, err
	}
	v, err := customDecode[core.Receipt](b, "requestId", "libraryId", "resourceId", "operation", "state", "version")
	if err != nil || v.RequestID != requestID || v.LibraryID != c.ref.KnowledgeBaseID || !validReceiptResource(v) || (v.ResourceID == "" && suffix != "/organize") || v.Operation == "" || v.State == "" {
		return nil, &OutcomeUnknownError{RequestID: requestID}
	}
	return v, nil
}
func (c *Custom) ListLibraries(ctx context.Context, offset, limit int) (*CustomList[*core.Library], error) {
	if offset < 0 || limit < 0 || limit > 100 {
		return nil, &Error{Code: "invalid_pagination"}
	}
	if limit == 0 {
		limit = 50
	}
	b, _, err := c.request(ctx, "GET", customPath+"/libraries", url.Values{"offset": {strconv.Itoa(offset)}, "limit": {strconv.Itoa(limit)}}, nil, "")
	if err != nil {
		return nil, err
	}
	v, err := customDecode[CustomList[*core.Library]](b, "items", "total", "offset", "limit")
	if err == nil && (v.Total < 0 || v.Offset != offset || v.Limit != limit || len(v.Items) > limit) {
		return nil, protocolError()
	}
	if err == nil {
		for _, x := range v.Items {
			if x == nil || x.ID == "" {
				return nil, protocolError()
			}
		}
	}
	return v, err
}
func (c *Custom) GetLibrary(ctx context.Context) (*core.Library, error) {
	v, err := customGet[core.Library](ctx, c, "", nil, "id", "kind", "version", "policyVersion", "structureVersion")
	if err == nil && v.ID != c.ref.KnowledgeBaseID {
		return nil, protocolError()
	}
	return v, err
}
func (c *Custom) UpdateLibrary(ctx context.Context, in core.UpdateLibraryInput) (*core.Receipt, error) {
	if err := c.target(in.LibraryID); err != nil {
		return nil, err
	}
	return c.write(ctx, "", in.RequestID, in)
}
func (c *Custom) ListPages(ctx context.Context, q core.PageQuery) (*CustomList[*core.Page], error) {
	if err := c.target(q.LibraryID); err != nil {
		return nil, err
	}
	values := url.Values{"query": {q.Query}, "pageType": {q.PageType}, "status": {q.Status}}
	if q.ThemeID != nil {
		values.Set("themeId", *q.ThemeID)
	}
	v, err := customList[*core.Page](ctx, c, "/pages", values, q.Offset, q.Limit)
	if err == nil {
		for _, p := range v.Items {
			if p == nil || p.KnowledgeBaseID != c.ref.KnowledgeBaseID || p.ID == "" || p.Version < 1 {
				return nil, protocolError()
			}
		}
	}
	return v, err
}
func (c *Custom) GetPage(ctx context.Context, id string) (*core.Page, error) {
	if !safeID(id) {
		return nil, &Error{Code: "invalid_resource"}
	}
	v, err := customGet[core.Page](ctx, c, "/pages/"+id, nil, "id", "knowledge_base_id", "version", "content")
	if err == nil && (v.ID != id || v.KnowledgeBaseID != c.ref.KnowledgeBaseID || v.Version < 1) {
		return nil, protocolError()
	}
	return v, err
}
func (c *Custom) GetPageBySlug(ctx context.Context, slug string) (*core.Page, error) {
	if !safeSlug(slug) {
		return nil, &Error{Code: "invalid_resource"}
	}
	v, err := customGet[core.Page](ctx, c, "/page-by-slug", url.Values{"slug": {slug}}, "id", "knowledge_base_id", "version", "content")
	if err == nil && (v.Slug != slug || v.KnowledgeBaseID != c.ref.KnowledgeBaseID || v.ID == "" || v.Version < 1) {
		return nil, protocolError()
	}
	return v, err
}
func (c *Custom) WritePage(ctx context.Context, in core.WritePageInput) (*core.Receipt, error) {
	if err := c.target(in.LibraryID); err != nil {
		return nil, err
	}
	return c.write(ctx, "/pages", in.RequestID, in)
}
func (c *Custom) RestorePage(ctx context.Context, in core.RestorePageInput) (*core.Receipt, error) {
	if err := c.target(in.LibraryID); err != nil {
		return nil, err
	}
	if !safeID(in.PageID) {
		return nil, &Error{Code: "invalid_resource"}
	}
	return c.write(ctx, "/pages/"+in.PageID+"/restore", in.RequestID, in)
}
func (c *Custom) ListRevisions(ctx context.Context, pageID string, offset, limit int) (*CustomList[*core.Revision], error) {
	if !safeID(pageID) {
		return nil, &Error{Code: "invalid_resource"}
	}
	return customList[*core.Revision](ctx, c, "/pages/"+pageID+"/revisions", nil, offset, limit)
}
func (c *Custom) GetRevision(ctx context.Context, pageID string, version int) (*core.Revision, error) {
	if !safeID(pageID) || version < 1 {
		return nil, &Error{Code: "invalid_resource"}
	}
	v, err := customGet[core.Revision](ctx, c, "/pages/"+pageID+"/revisions/"+strconv.Itoa(version), nil, "id", "page_id", "version", "content")
	if err == nil && (v.PageID != pageID || v.Version != version) {
		return nil, protocolError()
	}
	return v, err
}
func (c *Custom) ListSources(ctx context.Context, offset, limit int, options ...core.SourceQuery) (*CustomList[*core.SourceVersion], error) {
	if len(options) > 1 {
		return nil, &Error{Code: "invalid_query"}
	}
	q := url.Values{}
	if len(options) == 1 {
		q.Set("query", options[0].Query)
		q.Set("state", options[0].State)
		q.Set("sort", options[0].Sort)
	}
	v, err := customList[*core.SourceVersion](ctx, c, "/sources", q, offset, limit)
	if err == nil {
		for _, s := range v.Items {
			if s == nil || s.LibraryID != c.ref.KnowledgeBaseID || s.ID == "" {
				return nil, protocolError()
			}
		}
	}
	return v, err
}
func (c *Custom) GetSource(ctx context.Context, id string) (*core.SourceVersion, error) {
	if !safeID(id) {
		return nil, &Error{Code: "invalid_resource"}
	}
	v, err := customGet[core.SourceVersion](ctx, c, "/sources/"+id, nil, "id", "libraryId", "sha256", "rowVersion")
	if err == nil && (v.ID != id || v.LibraryID != c.ref.KnowledgeBaseID) {
		return nil, protocolError()
	}
	return v, err
}
func (c *Custom) ImportSource(ctx context.Context, in core.ImportSourceInput) (*core.Receipt, error) {
	if err := c.target(in.LibraryID); err != nil {
		return nil, err
	}
	return c.write(ctx, "/sources", in.RequestID, in)
}
func (c *Custom) ArchiveSource(ctx context.Context, in core.ArchiveSourceInput) (*core.Receipt, error) {
	if err := c.target(in.LibraryID); err != nil {
		return nil, err
	}
	if !safeID(in.SourceVersionID) {
		return nil, &Error{Code: "invalid_resource"}
	}
	return c.write(ctx, "/sources/"+in.SourceVersionID+"/archive", in.RequestID, in)
}
func (c *Custom) DownloadSource(ctx context.Context, id string) (*Download, error) {
	if !safeID(id) {
		return nil, &Error{Code: "invalid_resource"}
	}
	path, err := c.path("/sources/" + id + "/raw")
	if err != nil {
		return nil, err
	}
	b, h, err := c.request(ctx, "GET", path, nil, nil, "")
	if err != nil {
		return nil, err
	}
	_, params, err := mime.ParseMediaType(h.Get("Content-Disposition"))
	if err != nil || params["filename"] == "" {
		return nil, protocolError()
	}
	return &Download{Bytes: b, Filename: params["filename"], MIME: h.Get("Content-Type")}, nil
}

// ListSourceChunks reads only actual parse results for the same source version without reparsing or running models on the client.
func (c *Custom) ListSourceChunks(ctx context.Context, sourceID string) ([]core.IndexedDocumentChunk, error) {
	if !safeID(sourceID) {
		return nil, &Error{Code: "invalid_resource"}
	}
	path, err := c.path("/sources/" + sourceID + "/chunks")
	if err != nil {
		return nil, err
	}
	b, _, err := c.request(ctx, "GET", path, nil, nil, "")
	if err != nil {
		return nil, err
	}
	var rows []core.IndexedDocumentChunk
	if json.Unmarshal(b, &rows) != nil || rows == nil {
		return nil, protocolError()
	}
	for _, row := range rows {
		if row.ID == "" || row.LibraryID != c.ref.KnowledgeBaseID || row.SourceVersionID != sourceID {
			return nil, protocolError()
		}
	}
	return rows, nil
}

func (c *Custom) ListThemes(ctx context.Context) ([]*core.Theme, error) {
	path, err := c.path("/themes")
	if err != nil {
		return nil, err
	}
	b, _, err := c.request(ctx, "GET", path, nil, nil, "")
	if err != nil {
		return nil, err
	}
	var v []*core.Theme
	if json.Unmarshal(b, &v) != nil || v == nil {
		return nil, protocolError()
	}
	for _, t := range v {
		if t == nil || t.KnowledgeBaseID != c.ref.KnowledgeBaseID || t.ID == "" {
			return nil, protocolError()
		}
	}
	return v, nil
}
func (c *Custom) WriteTheme(ctx context.Context, in core.WriteThemeInput) (*core.Receipt, error) {
	if err := c.target(in.LibraryID); err != nil {
		return nil, err
	}
	return c.write(ctx, "/themes", in.RequestID, in)
}
func (c *Custom) ListJobs(ctx context.Context, offset, limit int) (*CustomList[*core.Job], error) {
	v, err := customList[*core.Job](ctx, c, "/jobs", nil, offset, limit)
	if err == nil {
		for _, j := range v.Items {
			if j == nil || j.ID == "" || j.LibraryID != c.ref.KnowledgeBaseID {
				return nil, protocolError()
			}
		}
	}
	return v, err
}
func (c *Custom) GetJob(ctx context.Context, id string) (*core.Job, error) {
	if !safeID(id) {
		return nil, &Error{Code: "invalid_resource"}
	}
	v, err := customGet[core.Job](ctx, c, "/jobs/"+id, nil, "id", "libraryId", "state", "version", "attempt")
	if err == nil && (v.ID != id || v.LibraryID != c.ref.KnowledgeBaseID || v.State == "") {
		return nil, protocolError()
	}
	return v, err
}
func (c *Custom) GetReceipt(ctx context.Context, requestID string) (*core.Receipt, error) {
	if requestID == "" {
		return nil, &Error{Code: "invalid_resource"}
	}
	v, err := customGet[core.Receipt](ctx, c, "/receipts", url.Values{"requestId": {requestID}}, "requestId", "libraryId", "resourceId", "operation", "state")
	if err == nil && (v.RequestID != requestID || v.LibraryID != c.ref.KnowledgeBaseID || !validReceiptResource(v)) {
		return nil, protocolError()
	}
	return v, err
}

// OrganizeRequest uses a server-provisioned identity and contains no client model configuration.
type OrganizeRequest struct {
	RequestID        string   `json:"requestId"`             // Idempotent request identity.
	LibraryID        string   `json:"libraryId"`             // Explicit library target.
	ExpectedVersion  int64    `json:"expectedVersion"`       // Library version.
	StructureVersion int64    `json:"structureVersion"`      // Catalog generation.
	SourceVersionIDs []string `json:"sourceVersionIds"`      // Frozen source set.
	ProfileID        string   `json:"profileId"`             // Server profile identity.
	Rebuild          bool     `json:"rebuild,omitempty"`     // Explicitly rebuild selected sources; ordinary organization is not a substitute.
	Instruction      string   `json:"instruction,omitempty"` // User requirements for this organization run without local execution configuration.
}

// MaintenanceRequest freezes the exact target page and version to prevent natural language from expanding the write scope.
type MaintenanceRequest struct {
	RequestID           string `json:"requestId"`           // Idempotent request identity.
	LibraryID           string `json:"libraryId"`           // Explicit library target.
	ExpectedVersion     int64  `json:"expectedVersion"`     // Library version.
	StructureVersion    int64  `json:"structureVersion"`    // Catalog generation.
	ProfileID           string `json:"profileId"`           // Server profile identity.
	Instruction         string `json:"instruction"`         // User maintenance requirements.
	PageID              string `json:"pageId"`              // Exact target page.
	ExpectedPageVersion int    `json:"expectedPageVersion"` // Previously read target page version.
}

func (c *Custom) Organize(ctx context.Context, in OrganizeRequest) (*core.Receipt, error) {
	if err := c.target(in.LibraryID); err != nil {
		return nil, err
	}
	return c.write(ctx, "/organize", in.RequestID, in)
}
func (c *Custom) Maintain(ctx context.Context, in MaintenanceRequest) (*core.Receipt, error) {
	if err := c.target(in.LibraryID); err != nil {
		return nil, err
	}
	return c.write(ctx, "/maintenance", in.RequestID, in)
}

// ServerProfile is a non-secret configuration summary published by the server.
type ServerProfile struct {
	ID   string `json:"id"`   // Fixed server profile identity.
	Name string `json:"name"` // User-visible name.
}

// CustomCapabilities preserves server implementation and current authorization facts; each request is still reauthorized.
type CustomCapabilities struct {
	Profile    string                            `json:"profile"`    // Fixed protocol identity.
	Operations map[string]bool                   `json:"operations"` // Actions actually available for the current resource.
	Profiles   []ServerProfile                   `json:"profiles"`   // Selectable server profiles without secrets.
	Details    map[string]CustomCapabilityStatus `json:"details"`    // Preserve support, permissions, and resource conditions separately.
}

// CustomCapabilityStatus does not infer write access from a successful connection or protocol support.
type CustomCapabilityStatus struct {
	Supported  bool   `json:"supported"`  // Capability actually implemented by the server.
	Permission string `json:"permission"` // allowed or denied for the current principal.
	Condition  string `json:"condition"`  // Current policy and execution profile status.
}

func (c *Custom) Capabilities(ctx context.Context) (*CustomCapabilities, error) {
	v, err := customGet[CustomCapabilities](ctx, c, "/capabilities", nil, "profile", "operations", "details")
	if err == nil && ((!c.integration && v.Profile != "neux-knowledge-v1") || (c.integration && v.Profile != "neuxally-integration-v1")) {
		return nil, protocolError()
	}
	return v, err
}

// validReceiptResource permits an acceptance receipt without a job identity only when the core explicitly confirms there are no pending sources.
func validReceiptResource(v *core.Receipt) bool {
	if v.ResourceID != "" {
		return true
	}
	return v.Operation == "job.accept" && v.State == "no-op" && v.JobID == "" && len(v.PageIDs) == 0
}
