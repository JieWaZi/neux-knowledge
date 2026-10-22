package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JieWaZi/neux-knowledge/core"
)

func TestCLIExplicitPurgePreviewConfirmRecover(t *testing.T) {
	dir := t.TempDir()
	options := core.Options{DBPath: filepath.Join(dir, "db"), RawDir: filepath.Join(dir, "raw")}
	ctx := context.Background()
	e, err := core.Open(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	lib, err := e.CreateLibrary(ctx, core.CreateLibraryInput{RequestID: "lib", Name: "清理", Kind: "general", WikiLanguage: "zh-CN"})
	if err != nil {
		t.Fatal(err)
	}
	r, err := e.ImportSource(ctx, core.ImportSourceInput{RequestID: "raw", LibraryID: lib.ResourceID, Filename: "secret.md", Bytes: []byte("CLI_PURGE_SECRET")})
	if err != nil {
		t.Fatal(err)
	}
	v, err := e.GetSource(ctx, lib.ResourceID, r.ResourceID)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Close(); err != nil {
		t.Fatal(err)
	}
	base := []string{"--db", options.DBPath, "--raw", options.RawDir}
	invoke := func(command string, in, out any) error {
		data, _ := json.Marshal(in)
		var b bytes.Buffer
		err := run(append(append([]string{}, base...), command), bytes.NewReader(data), &b)
		if err != nil {
			return err
		}
		if strings.Contains(b.String(), "CLI_PURGE_SECRET") || strings.Contains(b.String(), "secret.md") {
			t.Fatal("purge CLI leaked source")
		}
		return json.Unmarshal(b.Bytes(), out)
	}
	var preview core.SourcePurgePreview
	if err = invoke("source.purge.preview", core.PurgePreviewInput{LibraryID: lib.ResourceID, SourceID: v.SourceID}, &preview); err != nil {
		t.Fatal(err)
	}
	var rec core.Receipt
	in := core.ConfirmSourcePurgeInput{RequestID: "confirm", LibraryID: lib.ResourceID, SourceID: v.SourceID, PreviewDigest: preview.Digest}
	if err = invoke("source.purge.confirm", in, &rec); err == nil {
		t.Fatal("CLI inferred confirmation")
	}
	in.Confirmed = true
	if err = invoke("source.purge.confirm", in, &rec); err != nil {
		t.Fatal(err)
	}
	var status core.SourcePurgeStatus
	query := map[string]string{"libraryId": lib.ResourceID, "purgeId": rec.ResourceID}
	if err = invoke("source.purge.get", query, &status); err != nil || status.State != "pending" {
		t.Fatalf("confirmation silently completed: %+v %v", status, err)
	}
	var records core.SourcePurgeList
	if err = invoke("source.purge.list", map[string]any{"libraryId": lib.ResourceID, "offset": 0, "limit": 50}, &records); err != nil || records.Total != 1 || len(records.Items) != 1 || records.Items[0].ID != rec.ResourceID {
		t.Fatalf("lost persisted CLI cleanup: %+v %v", records, err)
	}
	if err = invoke("source.purge.recover", core.RecoverSourcePurgeInput{RequestID: "recover", LibraryID: lib.ResourceID, PurgeID: rec.ResourceID, ExpectedPolicyVersion: 1}, &rec); err != nil {
		t.Fatal(err)
	}
	if err = invoke("source.purge.get", query, &status); err != nil || status.State != "completed" {
		t.Fatalf("recover=%+v %v", status, err)
	}
}
