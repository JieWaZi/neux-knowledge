package remote

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"time"
)

// OperationStatus 分别表达协议支持、当前授权和必要条件。
type OperationStatus struct {
	// Supported 表示已核对的协议能力。
	Supported bool `json:"supported"`
	// Permission 为 allowed、denied 或 unknown，不从连接成功推导写权限。
	Permission string `json:"permission"`
	// Condition 为实际配置限制或 ready。
	Condition string `json:"condition"`
}

// Detection 是针对指定库的只读证据，不是可独立编辑的授权源。
type Detection struct {
	// BaseURL 是保留代理前缀的标准 API 基址。
	BaseURL string `json:"baseUrl"`
	// Library 是真实读取且精确匹配的目标。
	Library Library `json:"library"`
	// ServiceKind 是 neuxally、weknora 或 unknown。
	ServiceKind string `json:"serviceKind"`
	// Version 是服务器实际返回的协议或产品版本。
	Version string `json:"version"`
	// UploadMode 保留 separate、automatic 或 unknown 的服务端语义。
	UploadMode string `json:"uploadMode"`
	// AtomicConditionalWrite 只在明确证明时允许页面编辑。
	AtomicConditionalWrite bool `json:"atomicConditionalWrite"`
	// ReceiptRecovery 表示是否有原请求回执恢复合同。
	ReceiptRecovery bool `json:"receiptRecovery"`
	// Operations 保存逐项已观察的能力和权限。
	Operations map[string]OperationStatus `json:"operations"`
	// Evidence 保存不含秘密的识别依据与限制。
	Evidence []string `json:"evidence"`
}

// NormalizeAPIBaseURL 不剥掉代理路径；带身份、查询或片段的地址在发送凭据前拒绝。
func NormalizeAPIBaseURL(raw string) (string, error) {
	if strings.TrimSpace(raw) != raw || strings.ContainsAny(raw, "\r\n\t") {
		return "", &Error{Code: "invalid_connection"}
	}
	u, err := url.Parse(raw)
	if err != nil || u == nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", &Error{Code: "invalid_connection"}
	}
	path := strings.TrimRight(u.Path, "/")
	for _, part := range strings.Split(path, "/") {
		if part == "." || part == ".." {
			return "", &Error{Code: "invalid_connection"}
		}
	}
	if u.RawPath != "" {
		return "", &Error{Code: "invalid_connection"}
	}
	if !strings.HasSuffix(path, "/api/v1") {
		path += "/api/v1"
	}
	u.Path = path
	u.RawPath = ""
	return u.String(), nil
}

// Detect 仅请求明确目标及只读身份信息，失败证据不转成另一提供者的写许可。
func Detect(ctx context.Context, options Options, libraryID string) (Detection, error) {
	out := Detection{ServiceKind: "unknown", UploadMode: "unknown", Operations: map[string]OperationStatus{}, Evidence: []string{}}
	base, err := NormalizeAPIBaseURL(options.BaseURL)
	if err != nil {
		return out, err
	}
	if !safeID(libraryID) {
		return out, &Error{Code: "invalid_resource"}
	}
	options.BaseURL = base
	options.AuthMode = "api_key"
	client, err := NewWeKnora(options)
	if err != nil {
		return out, err
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	library, err := client.GetLibrary(ctx, libraryID)
	if err != nil {
		return out, err
	}
	if library.Name == "" {
		return out, protocolError()
	}
	out.BaseURL = base
	out.Library = *library
	out.Evidence = append(out.Evidence, "exact_library_read")
	out.Operations["read"] = OperationStatus{Supported: true, Permission: "allowed", Condition: "ready"}
	raw, _, capErr := client.request(ctx, "/api/v1/knowledge-bases/"+libraryID+"/integration-capabilities", nil, 1<<20)
	if capErr == nil {
		var own struct {
			// ServiceKind 是自有服务的显式身份。
			ServiceKind string `json:"service_kind"`
			// ProtocolVersion 是自有合同代次。
			ProtocolVersion string `json:"protocol_version"`
			// UploadMode 是真实上传处理模式。
			UploadMode string `json:"upload_mode"`
			// AtomicConditionalWrite 表示事务内条件写保证。
			AtomicConditionalWrite bool `json:"atomic_conditional_write"`
			// ReceiptRecovery 表示原身份回执支持。
			ReceiptRecovery bool `json:"receipt_recovery"`
			// Details 是当前 Key 的逐项服务端事实。
			Details map[string]OperationStatus `json:"details"`
		}
		if json.Unmarshal(raw, &own) == nil && own.ServiceKind == "neuxally" && own.ProtocolVersion == "1" && own.UploadMode == "separate" && own.Details["read"].Supported && own.Details["read"].Permission == "allowed" {
			for _, value := range own.Details {
				if value.Permission != "allowed" && value.Permission != "denied" && value.Permission != "unknown" {
					out.Evidence = append(out.Evidence, "invalid_capability_permission")
					return out, nil
				}
			}
			out.ServiceKind = "neuxally"
			out.Version = own.ProtocolVersion
			out.UploadMode = "separate"
			out.AtomicConditionalWrite = own.AtomicConditionalWrite
			out.ReceiptRecovery = own.ReceiptRecovery
			out.Operations = own.Details
			out.Evidence = append(out.Evidence, "explicit_neuxally_protocol_v1")
			return out, nil
		}
		out.Evidence = append(out.Evidence, "conflicting_or_unrecognized_capability_declaration")
		return out, nil
	}
	if remote, ok := capErr.(*Error); ok {
		out.Evidence = append(out.Evidence, "capability_probe_"+remote.Code)
	}
	raw, _, err = client.request(ctx, "/api/v1/system/info", nil, 1<<20)
	if err != nil {
		if remote, ok := err.(*Error); ok {
			out.Evidence = append(out.Evidence, "system_info_"+remote.Code)
		}
		return out, nil
	}
	var info struct {
		// Success 是服务器业务成功标记。
		Success bool `json:"success"`
		// Data 只读取版本证据，不保存部署诊断或配置。
		Data struct {
			// Version 是构建版本。
			Version string `json:"version"`
			// CommitID 是已核对源码版本。
			CommitID string `json:"commit_id"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &info) != nil || !info.Success || info.Data.Version == "" || info.Data.Version == "unknown" || len(info.Data.CommitID) < 7 || !strings.HasPrefix(strings.TrimPrefix(WeKnoraProfile, "weknora-"), info.Data.CommitID) {
		out.Evidence = append(out.Evidence, "unverified_system_version")
		return out, nil
	}
	out.ServiceKind = "weknora"
	out.Version = info.Data.Version
	out.UploadMode = "automatic"
	out.Evidence = append(out.Evidence, WeKnoraProfile)
	for _, name := range []string{"history", "themes", "graph", "sourceRead", "sourceDownload"} {
		out.Operations[name] = OperationStatus{Supported: true, Permission: "unknown", Condition: "ready"}
	}
	out.Operations["rawOnly"] = OperationStatus{Supported: false, Permission: "unknown", Condition: "automatic_processing"}
	out.Operations["upload"] = OperationStatus{Supported: true, Permission: "unknown", Condition: "automatic_processing"}
	out.Operations["write"] = OperationStatus{Supported: false, Permission: "unknown", Condition: "atomic_cas_unverified"}
	out.Operations["organize"] = OperationStatus{Supported: false, Permission: "unknown", Condition: "upload_automatically_processes"}
	return out, nil
}
