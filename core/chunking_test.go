package core_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/JieWaZi/neux-knowledge/core"
	"github.com/JieWaZi/neux-knowledge/internal/chunker"
)

func TestSplitDocumentUsesUpstreamDefaultsAndExplicitZero(t *testing.T) {
	text := strings.Repeat("甲乙丙丁。", 400)
	defaults := core.DefaultChunkingConfig()
	if defaults.ChunkSize != 512 || defaults.ChunkOverlap != 80 || !reflect.DeepEqual(defaults.Separators, []string{"\n\n", "\n", "。", "！", "？", ";", "；"}) {
		t.Fatalf("upstream defaults changed: %+v", defaults)
	}
	a, err := core.SplitDocument(text, nil)
	if err != nil {
		t.Fatal(err)
	}
	defaults.ChunkOverlap = 0
	b, err := core.SplitDocument(text, &defaults)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Fatal("8776 treats zero overlap as its default overlap")
	}
	if len(a.Children) < 2 || len(a.Parents) == 0 {
		t.Fatalf("unexpected flat chunks: %+v", a)
	}
}

func TestSplitDocumentPreservesParentChildRuneOffsetsAndContext(t *testing.T) {
	text := "# 第一章\n\n" + strings.Repeat("中文🍎正文。\n", 40) + "\n## 第二节\n\n" + strings.Repeat("另一段🍐内容。\n", 40)
	cfg := core.ChunkingConfig{ChunkSize: 512, ChunkOverlap: 5, Separators: []string{"\n\n", "\n", "。"}, Strategy: "heading", EnableParentChild: true, ParentChunkSize: 160, ChildChunkSize: 48}
	got, err := core.SplitDocument(text, &cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Parents) == 0 || len(got.Children) == 0 {
		t.Fatal("parent/child relationships were discarded")
	}
	runes := []rune(text)
	contextSeen := false
	for i, c := range got.Children {
		if c.Seq != i || c.Start < 0 || c.End > len(runes) || c.End <= c.Start {
			t.Fatalf("invalid source rune offsets: %+v", c)
		}
		content := []rune(c.Content)
		span := c.End - c.Start
		if len(content) < span || string(content[len(content)-span:]) != string(runes[c.Start:c.End]) {
			t.Fatalf("source offsets do not map to original runes: %+v", c)
		}
		if c.ParentIndex >= len(got.Parents) {
			t.Fatal("invalid parent index")
		}
		if c.ContextHeader != "" {
			contextSeen = true
			if !strings.Contains(c.EmbeddingContent(), c.ContextHeader) {
				t.Fatal("embedding context lost")
			}
		}
	}
	if !contextSeen {
		t.Fatal("heading context lost")
	}
}

func TestSplitDocumentRejectsUnavailableParserAndTableGuidance(t *testing.T) {
	cases := []core.ChunkingConfig{
		{ParserEngineRules: []core.ParserEngineRule{{FileTypes: []string{"pdf"}, Engine: "anydoc"}}},
		{TableMetadataInstructions: "summarize columns"},
	}
	for _, cfg := range cases {
		_, err := core.SplitDocument("document", &cfg)
		code(t, err, core.ErrInvalid)
	}
}

func TestSplitDocumentForwardsAllUpstreamAlgorithmSettings(t *testing.T) {
	text := "# 示例\n\n" + strings.Repeat("中文句子；下一句。\n", 100)
	for _, strategy := range []string{"auto", "heading", "heuristic", "recursive", "legacy"} {
		cfg := core.ChunkingConfig{ChunkSize: 512, ChunkOverlap: 11, Separators: []string{"；", "。"}, Strategy: strategy, TokenLimit: 100, Languages: []string{"zh"}, EnableParentChild: true, ParentChunkSize: 800, ChildChunkSize: 120}
		got, err := core.SplitDocument(text, &cfg)
		if err != nil {
			t.Fatal(err)
		}
		base := chunker.NormalizeSplitterConfig(chunker.SplitterConfig{ChunkSize: cfg.ChunkSize, ChunkOverlap: cfg.ChunkOverlap, Separators: cfg.Separators, Strategy: cfg.Strategy, TokenLimit: cfg.TokenLimit, Languages: cfg.Languages})
		parent, child := chunker.DeriveParentChildConfigs(base, cfg.ParentChunkSize, cfg.ChildChunkSize)
		want := chunker.SplitParentChild(text, parent, child)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s settings changed at public boundary", strategy)
		}
	}
}
