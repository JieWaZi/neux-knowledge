package remote

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// detectionTransport 在真实客户端边界记录目标和鉴权，不启动网络。
type detectionTransport func(*http.Request) (*http.Response, error)

func (f detectionTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func detectionResponse(r *http.Request, status int, value any) *http.Response {
	raw, _ := json.Marshal(value)
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(string(raw))), Header: http.Header{}, Request: r}
}
func TestRemoteDetectionPreservesTarget(t *testing.T) {
	calls := []string{}
	client := &http.Client{Transport: detectionTransport(func(r *http.Request) (*http.Response, error) {
		calls = append(calls, r.URL.String())
		if r.Header.Get("X-API-Key") != "fixture-only" || r.Header.Get("Authorization") != "" {
			t.Fatal("wrong auth")
		}
		switch r.URL.Path {
		case "/proxy/api/v1/knowledge-bases/a":
			return detectionResponse(r, 200, map[string]any{"success": true, "data": map[string]any{"id": "a", "name": "目标", "type": "wiki"}}), nil
		case "/proxy/api/v1/knowledge-bases/a/integration-capabilities":
			return detectionResponse(r, 200, map[string]any{"service_kind": "neuxally", "protocol_version": "1", "upload_mode": "separate", "atomic_conditional_write": true, "receipt_recovery": true, "details": map[string]any{"read": map[string]any{"supported": true, "permission": "allowed", "condition": "ready"}}}), nil
		default:
			t.Fatal("unexpected target", r.URL)
			return nil, nil
		}
	})}
	result, err := Detect(context.Background(), Options{BaseURL: "https://example.test/proxy/api/v1/", Token: "fixture-only", HTTPClient: client}, "a")
	if err != nil || result.BaseURL != "https://example.test/proxy/api/v1" || result.ServiceKind != "neuxally" || result.UploadMode != "separate" || result.Library.ID != "a" {
		t.Fatal(result, err)
	}
	if len(calls) != 2 {
		t.Fatal(calls)
	}
	for _, bad := range []string{"https://user@example.test", "https://example.test/?q=1", "https://example.test/#fragment"} {
		before := len(calls)
		if _, err := Detect(context.Background(), Options{BaseURL: bad, Token: "fixture-only", HTTPClient: client}, "a"); err == nil || len(calls) != before {
			t.Fatal("unsafe URL reached transport", bad, err)
		}
	}
}
func TestRemoteDetectionRequiresPositiveVersionEvidence(t *testing.T) {
	for _, mode := range []string{"forbidden-info", "unverified-version", "verified", "conflicting", "mismatched-id", "redirect"} {
		t.Run(mode, func(t *testing.T) {
			client := &http.Client{Transport: detectionTransport(func(r *http.Request) (*http.Response, error) {
				switch r.URL.Path {
				case "/api/v1/knowledge-bases/a":
					id := "a"
					if mode == "mismatched-id" {
						id = "b"
					}
					if mode == "redirect" {
						response := detectionResponse(r, 302, nil)
						response.Header.Set("Location", "https://other.test")
						return response, nil
					}
					return detectionResponse(r, 200, map[string]any{"success": true, "data": map[string]any{"id": id, "name": "目标", "type": "wiki"}}), nil
				case "/api/v1/knowledge-bases/a/integration-capabilities":
					if mode == "conflicting" {
						return detectionResponse(r, 200, map[string]any{"service_kind": "neuxally", "protocol_version": "1", "upload_mode": "automatic"}), nil
					}
					return detectionResponse(r, 404, nil), nil
				case "/api/v1/system/info":
					if mode == "forbidden-info" {
						return detectionResponse(r, 403, nil), nil
					}
					commit := "different"
					if mode == "verified" {
						commit = "8776a2d92d11ad3e76865d51cf3192e37e2a9976"
					}
					return detectionResponse(r, 200, map[string]any{"success": true, "data": map[string]any{"version": "v0.0.0-fixture", "commit_id": commit}}), nil
				default:
					t.Fatal("unexpected probe", r.URL)
					return nil, nil
				}
			})}
			result, err := Detect(context.Background(), Options{BaseURL: "https://example.test", Token: "fixture-only", HTTPClient: client}, "a")
			if mode == "mismatched-id" || mode == "redirect" {
				if err == nil {
					t.Fatal("invalid detail accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			kind, upload := "unknown", "unknown"
			if mode == "verified" {
				kind, upload = "weknora", "automatic"
			}
			if result.ServiceKind != kind || result.UploadMode != upload {
				t.Fatal(mode, result)
			}
		})
	}
}
