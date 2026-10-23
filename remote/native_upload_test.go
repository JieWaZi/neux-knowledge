package remote

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestRemoteNativeAutomaticUploadDoesNotInventReceipt(t *testing.T) {
	writes := 0
	c, err := NewWeKnora(Options{BaseURL: "https://example.test/proxy/api/v1", Token: "fixture-only", AuthMode: "api_key", HTTPClient: &http.Client{Transport: detectionTransport(func(r *http.Request) (*http.Response, error) {
		writes++
		if r.URL.Path != "/proxy/api/v1/knowledge-bases/kb/knowledge/file" || r.Method != "POST" {
			t.Fatal(r.URL, r.Method)
		}
		if r.Header.Get("X-API-Key") != "fixture-only" || r.Header.Get("Authorization") != "" {
			t.Fatal("wrong auth")
		}
		file, _, err := r.FormFile("file")
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(file)
		file.Close()
		if string(raw) != "# original" {
			t.Fatal("wrong original")
		}
		return detectionResponse(r, 200, map[string]any{"success": true, "data": map[string]any{"id": "source", "knowledge_base_id": "kb", "file_name": "original.md", "parse_status": "pending"}}), nil
	})}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := c.UploadNative(context.Background(), NativeUploadInput{RequestID: "upload-a", LibraryID: "kb", Filename: "original.md", Bytes: []byte("# original")})
	if err != nil || !result.Accepted || result.Source.ID != "source" || result.UploadMode != "automatic" || result.ProcessingState != "pending" || writes != 1 {
		t.Fatal(result, err, writes)
	}
}
func TestRemoteWriteAutomaticUploadLostResponseStaysUnknown(t *testing.T) {
	writes := 0
	c, _ := NewWeKnora(Options{BaseURL: "https://example.test", Token: "fixture-only", AuthMode: "api_key", HTTPClient: &http.Client{Transport: detectionTransport(func(r *http.Request) (*http.Response, error) { writes++; return nil, io.ErrUnexpectedEOF })}})
	_, err := c.UploadNative(context.Background(), NativeUploadInput{RequestID: "original", LibraryID: "kb", Filename: "raw.md", Bytes: []byte("raw")})
	if err == nil || !strings.Contains(err.Error(), "outcome_unknown") || writes != 1 {
		t.Fatal(err, writes)
	}
}
