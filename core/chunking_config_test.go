package core_test

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/JieWaZi/neux-knowledge/core"
)

func TestLibraryChunkingConfigurationPersists(t *testing.T) {
	e, _ := open(t)
	var in core.CreateLibraryInput
	raw := `{"RequestID":"chunk-create","Name":"分块配置","Kind":"general","WikiLanguage":"zh-CN","ChunkingConfig":{"chunk_size":640,"chunk_overlap":0,"separators":["；","。"],"enable_parent_child":true,"parent_chunk_size":2048,"child_chunk_size":256,"strategy":"heading","token_limit":1200,"languages":["zh"]}}`
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		t.Fatal(err)
	}
	rec, err := e.CreateLibrary(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	lib, err := e.GetLibrary(context.Background(), rec.ResourceID)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(lib)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	cfg, exists := fields["chunkingConfig"]
	if !exists || string(cfg) == "null" {
		t.Fatalf("saved library lost chunkingConfig: %s", encoded)
	}
	var settings map[string]any
	if err := json.Unmarshal(cfg, &settings); err != nil {
		t.Fatal(err)
	}
	if settings["chunk_size"] != float64(640) || settings["chunk_overlap"] != float64(0) || settings["strategy"] != "heading" || settings["enable_parent_child"] != true {
		t.Fatalf("chunking settings did not round trip: %s", cfg)
	}
}

func TestLibraryChunkingConfigurationUpdateAndReopen(t *testing.T) {
	ctx := context.Background()
	e, opt := open(t)
	defaults := core.DefaultChunkingConfig()
	rec, err := e.CreateLibrary(ctx, core.CreateLibraryInput{RequestID: "chunk-default", Name: "默认分块", Kind: "general", WikiLanguage: "zh-CN"})
	if err != nil {
		t.Fatal(err)
	}
	lib, err := e.GetLibrary(ctx, rec.ResourceID)
	if err != nil {
		t.Fatal(err)
	}
	defaultJSON, _ := json.Marshal(defaults)
	savedJSON, _ := json.Marshal(lib.ChunkingConfig)
	if lib.ChunkingConfig == nil || string(savedJSON) != string(defaultJSON) {
		t.Fatalf("creation defaults differ from upstream form: %+v", lib.ChunkingConfig)
	}
	cfg := core.ChunkingConfig{ChunkSize: 800, ChunkOverlap: 0, Separators: []string{"；", "。"}, EnableParentChild: true, ParentChunkSize: 3000, ChildChunkSize: 400, Strategy: "heuristic", TokenLimit: 1024, Languages: []string{"zh", "en"}}
	_, err = e.UpdateLibrary(ctx, core.UpdateLibraryInput{RequestID: "chunk-update", LibraryID: lib.ID, ExpectedVersion: lib.Version, Name: lib.Name, PolicyVersion: lib.PolicyVersion, ChunkingConfig: &cfg})
	if err != nil {
		t.Fatal(err)
	}
	lib, err = e.GetLibrary(ctx, lib.ID)
	if err != nil {
		t.Fatal(err)
	}
	if lib.ChunkingConfig == nil || !reflect.DeepEqual(*lib.ChunkingConfig, cfg) {
		t.Fatalf("updated settings lost fields: %+v", lib.ChunkingConfig)
	}
	_, err = e.UpdateLibrary(ctx, core.UpdateLibraryInput{RequestID: "chunk-update-omitted", LibraryID: lib.ID, ExpectedVersion: lib.Version, Name: lib.Name, PolicyVersion: lib.PolicyVersion})
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := core.Open(ctx, opt)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	after, err := reopened.GetLibrary(ctx, lib.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.ChunkingConfig == nil || !reflect.DeepEqual(*after.ChunkingConfig, cfg) {
		t.Fatalf("omitted update or reopen changed settings: %+v", after.ChunkingConfig)
	}
}

func TestLibraryRejectsUnavailableChunkingOptions(t *testing.T) {
	e, _ := open(t)
	cases := []core.ChunkingConfig{
		{ParserEngineRules: []core.ParserEngineRule{{FileTypes: []string{"pdf"}, Engine: "anydoc"}}},
		{TableMetadataInstructions: "describe table"},
	}
	for i, cfg := range cases {
		_, err := e.CreateLibrary(context.Background(), core.CreateLibraryInput{RequestID: fmt.Sprintf("chunk-unavailable-%d", i), Name: "拒绝未实现能力", Kind: "general", WikiLanguage: "zh-CN", ChunkingConfig: &cfg})
		code(t, err, core.ErrInvalid)
	}
}
