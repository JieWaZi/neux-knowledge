package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JieWaZi/neux-knowledge/core"
)

func TestCLIAuditAndMarkdownDirectoryExport(t *testing.T) {
	dir := t.TempDir()
	dbPath, rawPath := filepath.Join(dir, "knowledge.db"), filepath.Join(dir, "raw")
	e, err := core.Open(context.Background(), core.Options{DBPath: dbPath, RawDir: rawPath})
	if err != nil {
		t.Fatal(err)
	}
	rec, err := e.CreateLibrary(context.Background(), core.CreateLibraryInput{RequestID: "lib", Name: "导出", Kind: "general", WikiLanguage: "zh-CN"})
	if err != nil {
		t.Fatal(err)
	}
	l, err := e.GetLibrary(context.Background(), rec.ResourceID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.WritePage(context.Background(), core.WritePageInput{RequestID: "page", LibraryID: l.ID, StructureVersion: l.StructureVersion, Page: core.Page{Slug: "../../escape", Title: "导出知识", PageType: "concept", Content: "# 导出知识\n\n正文。"}})
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Close(); err != nil {
		t.Fatal(err)
	}
	base := []string{"--db", dbPath, "--raw", rawPath}
	input := fmt.Sprintf(`{"libraryId":%q}`, l.ID)
	var out bytes.Buffer
	if err = run(append(append([]string{}, base...), "audit"), strings.NewReader(input), &out); err != nil {
		t.Fatal(err)
	}
	var report core.LintReport
	if err = json.Unmarshal(out.Bytes(), &report); err != nil || report.PageCount != 1 || len(report.Issues) == 0 {
		t.Fatalf("audit=%s %v", out.String(), err)
	}
	target := filepath.Join(dir, "exported")
	out.Reset()
	args := append(append([]string{}, base...), "--out", target, "export")
	if err = run(args, strings.NewReader(input), &out); err != nil {
		t.Fatal(err)
	}
	manifestBytes, err := os.ReadFile(filepath.Join(target, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest core.MarkdownExport
	if err = json.Unmarshal(manifestBytes, &manifest); err != nil || len(manifest.Files) != 1 {
		t.Fatalf("manifest=%s %v", manifestBytes, err)
	}
	file := manifest.Files[0]
	actual, err := os.ReadFile(filepath.Join(target, file.Path))
	if err != nil {
		t.Fatal(err)
	}
	if file.Markdown != "" || file.SHA256 != fmt.Sprintf("%x", sha256.Sum256(actual)) || !strings.HasSuffix(string(actual), "正文。") {
		t.Fatal("exported file and manifest disagree")
	}
	if err = run(args, strings.NewReader(input), &out); err == nil {
		t.Fatal("existing output overwritten")
	}
	if _, err = os.Stat(filepath.Join(dir, "escape")); !os.IsNotExist(err) {
		t.Fatal("slug escaped export directory")
	}
	for _, target := range []string{rawPath, filepath.Join(rawPath, "exports"), dbPath} {
		if err = run(append(append([]string{}, base...), "--out", target, "export"), strings.NewReader(input), &out); err == nil {
			t.Fatalf("database/raw output accepted: %s", target)
		}
	}
	alias := filepath.Join(dir, "raw-alias")
	if err = os.Symlink(rawPath, alias); err == nil {
		if err = run(append(append([]string{}, base...), "--out", filepath.Join(alias, "exports"), "export"), strings.NewReader(input), &out); err == nil {
			t.Fatal("raw symlink bypassed export boundary")
		}
	}
	out.Reset()
	if err = run(append(append([]string{}, base...), "export"), strings.NewReader(input), &out); err != nil {
		t.Fatal(err)
	}
	var bundle core.MarkdownExport
	if err = json.Unmarshal(out.Bytes(), &bundle); err != nil || len(bundle.Files) != 1 || bundle.Files[0].Markdown == "" {
		t.Fatal("JSON export missing file contents")
	}
	if err = run(append(append([]string{}, base...), "audit"), strings.NewReader(fmt.Sprintf(`{"libraryId":%q,"autoFix":true}`, l.ID)), &out); err == nil {
		t.Fatal("audit silently accepted autofix")
	}
	var discovery bytes.Buffer
	if err = run([]string{"discover"}, strings.NewReader(""), &discovery); err != nil || !strings.Contains(discovery.String(), `"name":"audit"`) || !strings.Contains(discovery.String(), `"name":"export"`) {
		t.Fatal("read capabilities missing from discovery")
	}
}

func TestMarkdownDirectoryRejectsCollisionsAndTraversalBeforeCreation(t *testing.T) {
	dir := t.TempDir()
	dbPath, rawPath := filepath.Join(dir, "db"), filepath.Join(dir, "raw")
	e, err := core.Open(context.Background(), core.Options{DBPath: dbPath, RawDir: rawPath})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	content := "markdown"
	file := core.MarkdownFile{Path: "safe.md", Markdown: content, SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(content)))}
	for name, files := range map[string][]core.MarkdownFile{"collision": {file, file}, "traversal": {{Path: "../outside.md", Markdown: content, SHA256: file.SHA256}}, "digest": {{Path: "safe.md", Markdown: content, SHA256: "wrong"}}} {
		t.Run(name, func(t *testing.T) {
			target := filepath.Join(dir, name)
			_, err := writeMarkdownExport(context.Background(), target, dbPath, rawPath, &core.MarkdownExport{Files: files})
			if err == nil {
				t.Fatal("invalid bundle written")
			}
			if _, err = os.Stat(target); !os.IsNotExist(err) {
				t.Fatal("invalid bundle created output directory")
			}
		})
	}
}
