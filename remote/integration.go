package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/JieWaZi/neux-knowledge/core"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// NewIntegration 通过相同知识端口使用公开兼容合同；存量 Custom 合同保持原入口。
func NewIntegration(options Options, ref ResourceRef) (*Custom, error) {
	if ref.Backend != "integration" || !safeID(ref.ConnectionID) || (ref.KnowledgeBaseID != "" && !safeID(ref.KnowledgeBaseID)) {
		return nil, &Error{Code: "invalid_connection"}
	}
	base, err := NormalizeAPIBaseURL(options.BaseURL)
	if err != nil {
		return nil, err
	}
	options.BaseURL = base
	options.AuthMode = "api_key"
	transport, err := NewWeKnora(options)
	if err != nil {
		return nil, err
	}
	return &Custom{transport: transport, ref: ref, integration: true}, nil
}

// rawPayload 只在内存中传递有界 multipart，不写入本机暂存知识层。
type rawPayload struct {
	// Reader 是待发送的原件请求体。
	Reader io.Reader
	// ContentType 是 multipart 生成的实际边界。
	ContentType string
}

// integrationCapabilities 读取服务端事实，不用写入或 Key 前缀试探权限。
func (c *Custom) integrationCapabilities(ctx context.Context) (*integrationCapabilityResponse, error) {
	transport := *c
	transport.integration = false
	raw, _, err := transport.request(ctx, "GET", "/api/v1/knowledge-bases/"+c.ref.KnowledgeBaseID+"/integration-capabilities", nil, nil, "")
	if err != nil {
		return nil, err
	}
	var result integrationCapabilityResponse
	if json.Unmarshal(raw, &result) != nil || result.ServiceKind != "neuxally" || result.ProtocolVersion != "1" || result.UploadMode != "separate" {
		return nil, protocolError()
	}
	return &result, nil
}

// integrationCapabilityResponse 只保存本次请求观察到的服务端能力。
type integrationCapabilityResponse struct {
	// ServiceKind 是服务器明确声明的产品。
	ServiceKind string `json:"service_kind"`
	// ProtocolVersion 是已验证协议代次。
	ProtocolVersion string `json:"protocol_version"`
	// UploadMode 是上传实际语义。
	UploadMode string `json:"upload_mode"`
	// AtomicConditionalWrite 表示真正原子条件写。
	AtomicConditionalWrite bool `json:"atomic_conditional_write"`
	// Details 区分支持、权限和必要配置。
	Details map[string]OperationStatus `json:"details"`
}

// requestIntegration 集中映射已有知识端口，页面不能自行分支或拼接业务请求。
func (c *Custom) requestIntegration(ctx context.Context, method, path string, q url.Values, in any, requestID string) ([]byte, http.Header, error) {
	if !safeID(c.ref.KnowledgeBaseID) {
		return nil, nil, &Error{Code: "unsupported"}
	}
	prefix := customPath + "/libraries/" + c.ref.KnowledgeBaseID
	if !strings.HasPrefix(path, prefix) || (len(path) > len(prefix) && path[len(prefix)] != '/') {
		return nil, nil, &Error{Code: "unsupported"}
	}
	suffix := strings.TrimPrefix(path, prefix)
	kb := c.ref.KnowledgeBaseID
	base := "/api/v1/knowledge-bases/" + kb
	wiki := "/api/v1/knowledgebase/" + kb + "/wiki"
	values := url.Values{}
	for key, rows := range q {
		values[key] = append([]string(nil), rows...)
	}
	mapped, shape, capability := "", "", ""
	if method == http.MethodGet {
		switch {
		case suffix == "":
			mapped, shape = base, "data"
		case suffix == "/capabilities":
			mapped = base + "/integration-capabilities"
		case suffix == "/pages":
			mapped, shape = wiki+"/pages", "pages"
			values.Set("page_type", q.Get("pageType"))
			if q.Has("themeId") {
				values.Set("folder_id", q.Get("themeId"))
			}
		case suffix == "/page-by-slug":
			if !safeSlug(q.Get("slug")) {
				return nil, nil, &Error{Code: "invalid_resource"}
			}
			mapped = wiki + "/pages/" + q.Get("slug")
			values.Del("slug")
		case strings.HasPrefix(suffix, "/pages/"):
			rest := strings.TrimPrefix(suffix, "/pages/")
			parts := strings.Split(rest, "/")
			if !safeID(parts[0]) {
				return nil, nil, &Error{Code: "invalid_resource"}
			}
			if len(parts) == 1 {
				mapped = wiki + "/page-by-id/" + parts[0]
			} else if len(parts) >= 2 && parts[1] == "revisions" {
				page, err := c.GetPage(ctx, parts[0])
				if err != nil {
					return nil, nil, err
				}
				mapped = wiki + "/revisions/" + page.Slug
				shape = "revisions"
				if len(parts) == 3 {
					values.Set("content_version", parts[2])
					shape = "revision"
				} else if len(parts) != 2 {
					return nil, nil, &Error{Code: "unsupported"}
				}
			}
		case suffix == "/sources":
			mapped, shape = base+"/knowledge", "data-list"
			values.Set("keyword", q.Get("query"))
			if q.Get("sort") == "oldest" {
				values.Set("sort_order", "asc")
			}
		case strings.HasPrefix(suffix, "/sources/"):
			parts := strings.Split(strings.TrimPrefix(suffix, "/sources/"), "/")
			if !safeID(parts[0]) {
				return nil, nil, &Error{Code: "invalid_resource"}
			}
			if len(parts) == 1 {
				mapped, shape = "/api/v1/knowledge/"+parts[0], "data"
			} else if len(parts) == 2 && parts[1] == "raw" {
				mapped = "/api/v1/knowledge/" + parts[0] + "/download"
			}
		case suffix == "/themes":
			mapped, shape = wiki+"/folders", "folders"
			values.Set("all", "true")
		case suffix == "/jobs":
			mapped, shape = base+"/integration-jobs", "items"
		case strings.HasPrefix(suffix, "/jobs/"):
			mapped = base + "/integration-jobs/" + strings.TrimPrefix(suffix, "/jobs/")
		case suffix == "/receipts":
			mapped = base + "/integration-receipts/" + q.Get("requestId")
			values.Del("requestId")
		}
	} else if method == http.MethodPost {
		switch suffix {
		case "/sources":
			input, ok := in.(core.ImportSourceInput)
			if !ok {
				return nil, nil, &Error{Code: "invalid_request"}
			}
			var body bytes.Buffer
			form := multipart.NewWriter(&body)
			file, err := form.CreateFormFile("file", input.Filename)
			if err != nil {
				return nil, nil, &Error{Code: "invalid_request"}
			}
			if _, err = file.Write(input.Bytes); err != nil {
				return nil, nil, &Error{Code: "invalid_request"}
			}
			if input.SourceID != "" {
				_ = form.WriteField("source_id", input.SourceID)
				_ = form.WriteField("expected_version", strconv.FormatInt(input.ExpectedVersion, 10))
			}
			if form.Close() != nil {
				return nil, nil, &Error{Code: "invalid_request"}
			}
			in = rawPayload{Reader: bytes.NewReader(body.Bytes()), ContentType: form.FormDataContentType()}
			mapped, shape, capability = base+"/knowledge/file", "receipt", "rawOnly"
		case "/pages":
			input, ok := in.(core.WritePageInput)
			if !ok || !safeSlug(input.Page.Slug) {
				return nil, nil, &Error{Code: "invalid_request"}
			}
			in = map[string]any{"version": input.ExpectedVersion, "structure_version": input.StructureVersion, "title": input.Page.Title, "content": input.Page.Content, "summary": input.Page.Summary, "page_type": input.Page.PageType, "aliases": input.Page.Aliases, "folder_id": input.Page.FolderID, "sort_order": input.Page.SortOrder}
			if input.ExpectedVersion > 0 {
				mapped = wiki + "/pages/" + input.Page.Slug
				method = http.MethodPut
			} else {
				mapped = wiki + "/pages"
				in.(map[string]any)["slug"] = input.Page.Slug
				delete(in.(map[string]any), "version")
			}
			shape, capability = "receipt", "write"
		case "/themes":
			input, ok := in.(core.WriteThemeInput)
			if !ok {
				return nil, nil, &Error{Code: "invalid_request"}
			}
			mapped = wiki + "/folders"
			if input.Theme.ID != "" {
				if !safeID(input.Theme.ID) {
					return nil, nil, &Error{Code: "invalid_resource"}
				}
				method = http.MethodPut
				mapped += "/" + input.Theme.ID
			}
			in = map[string]any{"structure_version": input.StructureVersion, "name": input.Theme.Name, "parent_id": input.Theme.ParentID, "sort_order": input.Theme.SortOrder}
			if input.Delete {
				if input.Theme.ID == "" {
					return nil, nil, &Error{Code: "invalid_resource"}
				}
				method = http.MethodDelete
				values.Set("structure_version", strconv.FormatInt(input.StructureVersion, 10))
				in = nil
				shape = ""
			} else {
				shape = "receipt"
			}
			capability = "themeWrite"
		case "/organize":
			input, ok := in.(OrganizeRequest)
			if !ok {
				return nil, nil, &Error{Code: "invalid_request"}
			}
			mapped, shape, capability = base+"/organize", "receipt", "organize"
			in = map[string]any{"request_id": input.RequestID, "expected_version": input.ExpectedVersion, "structure_version": input.StructureVersion, "source_version_ids": input.SourceVersionIDs, "instruction": input.Instruction, "rebuild": input.Rebuild}
			if input.Rebuild {
				capability = "rebuild"
			}
		case "/maintenance":
			input, ok := in.(MaintenanceRequest)
			if !ok || !safeID(input.PageID) {
				return nil, nil, &Error{Code: "invalid_request"}
			}
			mapped, shape, capability = wiki+"/maintenance/"+input.PageID, "receipt", "maintain"
			in = map[string]any{"request_id": input.RequestID, "expected_version": input.ExpectedVersion, "structure_version": input.StructureVersion, "expected_page_version": input.ExpectedPageVersion, "instruction": input.Instruction}
		}
	}
	if mapped == "" {
		return nil, nil, &Error{Code: "unsupported"}
	}
	if method != http.MethodGet {
		caps, err := c.integrationCapabilities(ctx)
		if err != nil {
			return nil, nil, err
		}
		value, ok := caps.Details[capability]
		if !ok || !value.Supported || value.Condition != "ready" {
			return nil, nil, &Error{Code: "unsupported"}
		}
		if value.Permission != "allowed" {
			return nil, nil, &Error{Code: "forbidden"}
		}
		if capability == "write" && !caps.AtomicConditionalWrite {
			return nil, nil, &Error{Code: "unsupported"}
		}
	}
	transport := *c
	transport.integration = false
	raw, headers, err := transport.request(ctx, method, mapped, values, in, requestID)
	if err != nil {
		return nil, nil, err
	}
	if shape == "" {
		return raw, headers, nil
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil {
		return nil, nil, protocolError()
	}
	switch shape {
	case "data":
		if object["data"] == nil || string(object["success"]) != "true" {
			return nil, nil, protocolError()
		}
		raw = object["data"]
	case "receipt":
		if object["receipt"] == nil {
			return nil, nil, &OutcomeUnknownError{RequestID: requestID}
		}
		raw = object["receipt"]
	case "folders":
		if object["folders"] == nil {
			return nil, nil, protocolError()
		}
		raw = object["folders"]
	case "revision":
		var rows []json.RawMessage
		if json.Unmarshal(object["revisions"], &rows) != nil || len(rows) != 1 {
			return nil, nil, protocolError()
		}
		raw = rows[0]
	case "pages", "data-list", "revisions", "items":
		field := shape
		if shape == "data-list" {
			field = "data"
		}
		if object[field] == nil || object["total"] == nil {
			return nil, nil, protocolError()
		}
		offset, _ := strconv.Atoi(q.Get("offset"))
		limit, _ := strconv.Atoi(q.Get("limit"))
		if limit == 0 {
			limit = 50
		}
		raw, _ = json.Marshal(map[string]any{"items": object[field], "total": object["total"], "offset": offset, "limit": limit})
	}
	return raw, headers, nil
}
