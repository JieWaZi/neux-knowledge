package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	types "github.com/JieWaZi/neux-knowledge/internal/domain"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

func hash(b []byte) string          { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func marshal(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
func invalid(detail string) error {
	return &types.Error{Code: types.ErrCandidateInvalid, Operation: "organize", Detail: detail}
}
func jaccard(a, b map[string]struct{}) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 0
	}
	n := 0
	for k := range a {
		if _, ok := b[k]; ok {
			n++
		}
	}
	return float64(n) / float64(len(a)+len(b)-n)
}

// MarkdownReader deterministically reads UTF-8 Markdown/text; the host must provide capabilities for other formats.
type MarkdownReader struct{}

func (MarkdownReader) Read(ctx context.Context, in ReadRequest) (ReadArtifact, error) {
	a := ReadArtifact{SourceVersionID: in.SourceVersionID, SHA256: hash(in.Raw), Scope: in.Scope}
	if err := ctx.Err(); err != nil {
		return a, err
	}
	if in.SourceVersionID == "" || in.SourceID == "" || a.SHA256 != in.SHA256 || in.Scope != "all" {
		return a, invalid("Reader source identity or scope mismatch")
	}
	mime := strings.Split(in.MIME, ";")[0]
	ext := strings.ToLower(filepath.Ext(in.Filename))
	if mime != "text/markdown" && mime != "text/plain" && !(mime == "" && (ext == ".md" || ext == ".txt")) {
		a.Status = "unsupported"
		a.Diagnostic = "MarkdownReader does not support this media type"
		a.Coverage = marshal(map[string]any{"reader": "utf8-markdown-v1", "complete": false})
		return a, nil
	}
	if !utf8.Valid(in.Raw) {
		a.Status = "failed"
		a.Diagnostic = "source is not valid UTF-8"
		a.Coverage = marshal(map[string]any{"reader": "utf8-markdown-v1", "complete": false})
		return a, nil
	}
	if strings.TrimSpace(string(in.Raw)) == "" {
		a.Status = "failed"
		a.Diagnostic = "source contains no substantive text"
		a.Coverage = marshal(map[string]any{"reader": "utf8-markdown-v1", "complete": false})
		return a, nil
	}
	a.Status = "complete"
	lines := strings.SplitAfter(string(in.Raw), "\n")
	offset, lineStart, runes := 0, 1, 0
	var text strings.Builder
	flush := func(end int) {
		if text.Len() == 0 {
			return
		}
		content := text.String()
		a.Blocks = append(a.Blocks, &Chunk{ID: fmt.Sprintf("%s:%d", in.SourceVersionID, len(a.Blocks)), ChunkIndex: len(a.Blocks), StartAt: runes - utf8.RuneCountInString(content), EndAt: runes, Content: content, ChunkType: "text", Locator: types.SourceLocator{Kind: "line", StartLine: lineStart, EndLine: end}})
		offset += len(content)
		lineStart = end + 1
		text.Reset()
	}
	for i, line := range lines {
		if err := ctx.Err(); err != nil {
			return a, err
		}
		n := utf8.RuneCountInString(line)
		if in.MaxRunes > 0 && runes+n > in.MaxRunes {
			a.Status = "partial"
			a.Diagnostic = "reading budget exceeded"
			break
		}
		runes += n
		text.WriteString(line)
		if text.Len() >= 6000 || i == len(lines)-1 {
			flush(i + 1)
		}
	}
	if text.Len() > 0 {
		flush(lineStart + strings.Count(text.String(), "\n"))
	}
	a.Coverage = marshal(map[string]any{"reader": "utf8-markdown-v1", "scope": in.Scope, "sha256": a.SHA256, "totalBytes": len(in.Raw), "readBytes": offset, "totalLines": len(lines), "complete": a.Status == "complete"})
	return a, nil
}

func validateRead(in ReadRequest, a ReadArtifact) error {
	if a.SourceVersionID != in.SourceVersionID || a.SHA256 != in.SHA256 || a.Scope != in.Scope {
		return invalid("Reader returned different source identity or scope")
	}
	if a.Status != "complete" && a.Status != "partial" && a.Status != "unsupported" && a.Status != "failed" {
		return invalid("Reader returned unknown status")
	}
	if len(a.Coverage) == 0 || !json.Valid(a.Coverage) {
		return invalid("Reader must provide actual coverage")
	}
	if a.Status == "complete" && len(a.Blocks) == 0 {
		return invalid("complete Reader result has no evidence")
	}
	seen := map[string]bool{}
	for _, b := range a.Blocks {
		if b == nil || b.ID == "" || seen[b.ID] || strings.TrimSpace(b.Content) == "" {
			return invalid("invalid Reader evidence block")
		}
		seen[b.ID] = true
		if b.Locator.Kind == "line" && (b.Locator.StartLine < 1 || b.Locator.EndLine < b.Locator.StartLine) {
			return invalid("invalid confirmed line locator")
		}
	}
	return nil
}
