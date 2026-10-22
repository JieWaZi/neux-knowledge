package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/JieWaZi/neux-knowledge/core"
	"github.com/JieWaZi/neux-knowledge/httpapi"
)

func TestSourceChunksReadsActualVersionAndRejectsAnotherSource(t *testing.T) {
	ctx := context.Background()
	e, l, policy := fixture(t)
	raw := []byte("# 真实原件\n这一段来自同一个来源版本。")
	receipt, err := e.ImportSource(ctx, core.ImportSourceInput{RequestID: "chunk-source", LibraryID: l.ID, Filename: "source.md", Bytes: raw})
	if err != nil {
		t.Fatal(err)
	}
	source, _ := e.GetSource(ctx, l.ID, receipt.ResourceID)
	job, err := e.AcceptJob(ctx, core.AcceptJobInput{RequestID: "index", LibraryID: l.ID, ExpectedVersion: l.Version, StructureVersion: l.StructureVersion, SourceVersionIDs: []string{source.ID}, Permission: core.PermissionSnapshot{PolicyVersion: l.PolicyVersion, Operations: []string{"organize"}}, RulesVersion: "rules", ConfigDigest: "config"})
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := (core.MarkdownReader{}).Read(ctx, core.ReadRequest{SourceID: source.SourceID, SourceVersionID: source.ID, SHA256: source.SHA256, Filename: source.Filename, Raw: raw, Scope: "all"})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.IndexDocument(ctx, job.JobID, source.ID, artifact, core.DocumentEmbedder{}); err != nil {
		t.Fatal(err)
	}
	h := httpapi.New(e, httpapi.Options{Security: policy})
	w := request(t, h, "GET", "/libraries/"+l.ID+"/sources/"+source.ID+"/chunks", "secret", "192.0.2.1:1", "", nil)
	var chunks []core.IndexedDocumentChunk
	if err := json.Unmarshal(w.Body.Bytes(), &chunks); err != nil || w.Code != 200 || len(chunks) == 0 {
		t.Fatalf("actual chunks missing: %d %s %v", w.Code, w.Body, err)
	}
	if chunks[0].LibraryID != l.ID || chunks[0].SourceVersionID != source.ID || !strings.Contains(chunks[0].Content, "真实原件") || strings.Contains(w.Body.String(), `"Vector"`) || strings.Contains(w.Body.String(), `"vector"`) {
		t.Fatalf("chunk identity or content changed: %s", w.Body)
	}
	w = request(t, h, "GET", "/libraries/"+l.ID+"/sources/missing/chunks", "secret", "192.0.2.1:1", "", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("missing source became an empty success: %d %s", w.Code, w.Body)
	}
}
