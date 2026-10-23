package remote

import (
	"context"
	"encoding/json"
	"github.com/JieWaZi/neux-knowledge/core"
	"net/http"
	"strings"
	"testing"
)

func TestRemoteNativeSeparateUploadAndOrganizeContracts(t *testing.T) {
	calls := []string{}
	client := &http.Client{Transport: detectionTransport(func(r *http.Request) (*http.Response, error) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		if r.Header.Get("X-API-Key") != "fixture-only" || r.Header.Get("Authorization") != "" {
			t.Fatal("credential header changed")
		}
		switch r.URL.Path {
		case "/proxy/api/v1/knowledge-bases/kb/integration-capabilities":
			return detectionResponse(r, 200, map[string]any{"service_kind": "neuxally", "protocol_version": "1", "upload_mode": "separate", "profile": "neuxally-integration-v1", "operations": map[string]bool{"read": true, "rawOnly": true, "organize": true}, "details": map[string]any{"read": OperationStatus{Supported: true, Permission: "allowed", Condition: "ready"}, "rawOnly": OperationStatus{Supported: true, Permission: "allowed", Condition: "ready"}, "organize": OperationStatus{Supported: true, Permission: "allowed", Condition: "ready"}}}), nil
		case "/proxy/api/v1/knowledge-bases/kb/knowledge/file":
			if r.Method != "POST" || r.Header.Get("Idempotency-Key") != "upload-a" {
				t.Fatal("wrong upload identity")
			}
			file, _, err := r.FormFile("file")
			if err != nil {
				t.Fatal(err)
			}
			file.Close()
			return detectionResponse(r, 200, map[string]any{"success": true, "data": map[string]any{"id": "s"}, "receipt": core.Receipt{RequestID: "upload-a", LibraryID: "kb", ResourceID: "s", Operation: "source.import", State: "saved", Version: 1}}), nil
		case "/proxy/api/v1/knowledge-bases/kb/organize":
			var body map[string]json.RawMessage
			if json.NewDecoder(r.Body).Decode(&body) != nil || body["profileId"] != nil || body["ProfileID"] != nil || body["runtimeId"] != nil || body["source_version_ids"] == nil {
				t.Fatal("client execution configuration leaked", body)
			}
			return detectionResponse(r, 200, map[string]any{"receipt": core.Receipt{RequestID: "organize-a", LibraryID: "kb", ResourceID: "j", JobID: "j", Operation: "job.accept", State: "accepted", Version: 1}}), nil
		default:
			t.Fatal("unexpected endpoint", r.URL)
			return nil, nil
		}
	})}
	c, err := NewIntegration(Options{BaseURL: "https://example.test/proxy/api/v1", Token: "fixture-only", HTTPClient: client}, ResourceRef{Backend: "integration", ConnectionID: "c", KnowledgeBaseID: "kb"})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := c.ImportSource(context.Background(), core.ImportSourceInput{RequestID: "upload-a", LibraryID: "kb", Filename: "raw.md", Bytes: []byte("# raw")})
	if err != nil || receipt.ResourceID != "s" {
		t.Fatal(receipt, err)
	}
	for _, call := range calls {
		if strings.Contains(call, "/organize") {
			t.Fatal("upload triggered organize", calls)
		}
	}
	receipt, err = c.Organize(context.Background(), OrganizeRequest{RequestID: "organize-a", LibraryID: "kb", ExpectedVersion: 1, StructureVersion: 1, SourceVersionIDs: []string{"s"}})
	if err != nil || receipt.JobID != "j" {
		t.Fatal(receipt, err)
	}
}
func TestRemoteNativeUnknownPermissionDoesNotProbeWrite(t *testing.T) {
	posts := 0
	client := &http.Client{Transport: detectionTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method != "GET" {
			posts++
		}
		return detectionResponse(r, 200, map[string]any{"service_kind": "neuxally", "protocol_version": "1", "upload_mode": "separate", "details": map[string]any{"rawOnly": OperationStatus{Supported: true, Permission: "unknown", Condition: "ready"}}}), nil
	})}
	c, err := NewIntegration(Options{BaseURL: "https://example.test", Token: "fixture-only", HTTPClient: client}, ResourceRef{Backend: "integration", ConnectionID: "c", KnowledgeBaseID: "kb"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.ImportSource(context.Background(), core.ImportSourceInput{RequestID: "upload", LibraryID: "kb", Filename: "raw.md", Bytes: []byte("raw")}); err == nil || posts != 0 {
		t.Fatal("unknown permission issued business write", err, posts)
	}
}
