package pipeline

import (
	"context"
	"encoding/json"
	"testing"
)

func TestMarkdownReaderActualCoverageAndIdentity(t *testing.T) {
	raw := []byte("# 原件\n\n长期记忆需要可复用依据。\n")
	req := ReadRequest{SourceVersionID: "v1", SourceID: "s1", SHA256: hash(raw), MIME: "text/markdown", Raw: raw, Scope: "all"}
	a, err := (MarkdownReader{}).Read(context.Background(), req)
	if err != nil || a.Status != "complete" || a.SHA256 != hash(raw) || len(a.Blocks) == 0 {
		t.Fatalf("artifact=%+v err=%v", a, err)
	}
	if !json.Valid(a.Coverage) {
		t.Fatal("missing coverage")
	}
	req.SHA256 = "forged"
	if _, err = (MarkdownReader{}).Read(context.Background(), req); err == nil {
		t.Fatal("forged source identity accepted")
	}
	req.SHA256 = hash(raw)
	req.MIME = "application/pdf"
	a, err = (MarkdownReader{}).Read(context.Background(), req)
	if err != nil || a.Status != "unsupported" {
		t.Fatal("pdf fabricated", a, err)
	}
}
