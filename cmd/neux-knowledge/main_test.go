package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIRealSQLitePersistenceDiscoveryAndRawOnly(t *testing.T) {
	dir := t.TempDir()
	base := []string{"--db", filepath.Join(dir, "knowledge.db"), "--raw", filepath.Join(dir, "raw")}
	invoke := func(command, input string) map[string]json.RawMessage {
		t.Helper()
		var out bytes.Buffer
		if e := run(append(append([]string{}, base...), command), strings.NewReader(input), &out); e != nil {
			t.Fatal(e)
		}
		var got map[string]json.RawMessage
		if e := json.Unmarshal(out.Bytes(), &got); e != nil {
			t.Fatal(e)
		}
		return got
	}
	var discover bytes.Buffer
	if e := run([]string{"discover"}, strings.NewReader(""), &discover); e != nil || !strings.Contains(discover.String(), "source.import") {
		t.Fatal("discovery", e, discover.String())
	}
	created := invoke("library.create", `{"requestId":"lib","name":"CLI","kind":"general","wikiLanguage":"zh-CN"}`)
	var libraryID string
	_ = json.Unmarshal(created["resourceId"], &libraryID)
	if libraryID == "" {
		t.Fatal("missing persistent identity", created)
	}
	listed := invoke("library.list", `{}`)
	if !strings.Contains(string(listed["items"]), libraryID) {
		t.Fatal("library lost between processes")
	}
	imported := invoke("source.import", `{"requestId":"source","libraryId":"`+libraryID+`","filename":"notes.md","mime":"text/markdown","bytes":"IyDljp/ku7Y="}`)
	var sourceID string
	_ = json.Unmarshal(imported["resourceId"], &sourceID)
	raw := invoke("source.raw", `{"libraryId":"`+libraryID+`","sourceVersionId":"`+sourceID+`"}`)
	if len(raw["bytes"]) == 0 {
		t.Fatal("raw read missing")
	}
	pages := invoke("page.list", `{"libraryId":"`+libraryID+`"}`)
	if string(pages["total"]) != "0" {
		t.Fatal("raw import fabricated wiki", pages)
	}
}
