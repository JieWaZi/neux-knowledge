package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"strings"
)

// NativeUploadInput 是用户明确提交的原件，不用于探测写权限。
type NativeUploadInput struct {
	// RequestID 是本机保留的原请求，第三方不保证据此幂等。
	RequestID string
	// LibraryID 是已验证的明确库身份。
	LibraryID string
	// Filename 是实际文件名，不能包含路径。
	Filename string
	// MIME 是用户原件的类型提示。
	MIME string
	// Bytes 是完整原件，发送前有大小上限。
	Bytes []byte
}

// NativeUploadResult 只表达远端已接纳及已观察处理状态，不伪造知识发布回执。
type NativeUploadResult struct {
	// Source 是远端返回的真实资料身份。
	Source Source `json:"source"`
	// Accepted 仅在原生上传响应已验证时为真。
	Accepted bool `json:"accepted"`
	// UploadMode 保留官方上传自动处理的语义。
	UploadMode string `json:"uploadMode"`
	// ProcessingState 是原样状态或 unknown，不推导整个 Wiki 整理终态。
	ProcessingState string `json:"processingState"`
}

// UploadNative 以 multipart 上传原件；每次调用只发送一次，未知结果不重试。
func (c *WeKnora) UploadNative(ctx context.Context, in NativeUploadInput) (*NativeUploadResult, error) {
	if c == nil || !safeID(in.LibraryID) || in.RequestID == "" || len(in.RequestID) > 200 || strings.ContainsAny(in.RequestID, "\r\n\t") || in.Filename == "" || len(in.Filename) > 255 || strings.ContainsAny(in.Filename, "/\\\x00\r\n") || len(in.Bytes) > 32<<20 {
		return nil, &Error{Code: "invalid_request"}
	}
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	file, err := form.CreateFormFile("file", in.Filename)
	if err != nil {
		return nil, &Error{Code: "invalid_request"}
	}
	if _, err = file.Write(in.Bytes); err != nil {
		return nil, &Error{Code: "invalid_request"}
	}
	if form.Close() != nil {
		return nil, &Error{Code: "invalid_request"}
	}
	client := &Custom{transport: c}
	raw, _, err := client.request(ctx, "POST", "/api/v1/knowledge-bases/"+in.LibraryID+"/knowledge/file", nil, rawPayload{Reader: bytes.NewReader(body.Bytes()), ContentType: form.FormDataContentType()}, in.RequestID)
	if err != nil {
		return nil, err
	}
	var response struct {
		// Success 是服务器明确的业务接纳事实。
		Success bool `json:"success"`
		// Data 是服务器返回的资料身份与状态。
		Data Source `json:"data"`
	}
	if json.Unmarshal(raw, &response) != nil || !response.Success || response.Data.ID == "" || response.Data.KnowledgeBaseID != in.LibraryID {
		return nil, &OutcomeUnknownError{RequestID: in.RequestID}
	}
	state := response.Data.ParseStatus
	if state == "" {
		state = "unknown"
	}
	return &NativeUploadResult{Source: response.Data, Accepted: true, UploadMode: "automatic", ProcessingState: state}, nil
}
