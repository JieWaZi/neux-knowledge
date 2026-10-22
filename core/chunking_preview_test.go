package core

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestComputeChunkSizeStats_Empty(t *testing.T) {
	stats := computeChunkSizeStats(nil)
	if stats.Count != 0 || stats.AvgChars != 0 || stats.MaxChars != 0 {
		t.Errorf("empty input should yield zero stats, got %+v", stats)
	}
}

func TestComputeChunkSizeStats_SingleChunk(t *testing.T) {
	stats := computeChunkSizeStats([]int{500})
	if stats.Count != 1 {
		t.Errorf("count: got %d want 1", stats.Count)
	}
	if stats.AvgChars != 500 || stats.MinChars != 500 || stats.MaxChars != 500 {
		t.Errorf("single-chunk stats should all equal 500, got %+v", stats)
	}
	if stats.StddevChars != 0 {
		t.Errorf("stddev for one element should be 0, got %d", stats.StddevChars)
	}
}

func TestComputeChunkSizeStats_VaryingSizes(t *testing.T) {
	// 100, 200, 300, 400, 500 → avg 300, stddev ≈ 141
	stats := computeChunkSizeStats([]int{100, 200, 300, 400, 500})
	if stats.Count != 5 {
		t.Errorf("count: got %d want 5", stats.Count)
	}
	if stats.AvgChars != 300 {
		t.Errorf("avg: got %d want 300", stats.AvgChars)
	}
	if stats.MinChars != 100 || stats.MaxChars != 500 {
		t.Errorf("min/max: got %d/%d want 100/500", stats.MinChars, stats.MaxChars)
	}
	if stats.StddevChars < 130 || stats.StddevChars > 150 {
		t.Errorf("stddev: got %d, want ~141", stats.StddevChars)
	}
}

func TestComputeChunkSizeStats_NoVarianceUnderflow(t *testing.T) {
	// All identical — variance must clamp to 0 not flip negative on
	// float-precision rounding.
	stats := computeChunkSizeStats([]int{1234, 1234, 1234, 1234})
	if stats.StddevChars != 0 {
		t.Errorf("identical values must yield stddev=0, got %d", stats.StddevChars)
	}
}

func TestPreviewChunkingUsesRealParentChildAndProfile(t *testing.T) {
	text := strings.Repeat("## 记录\n这是需要保留的原始正文，包含章节与边界。\n\n", 100)
	cfg := PreviewChunkingPayload{ChunkSize: 300, ChunkOverlap: 30, Separators: []string{"\n\n", "\n"}, EnableParentChild: true, ParentChunkSize: 512, ChildChunkSize: 100, Strategy: "heading"}
	result, err := PreviewDocumentChunking(context.Background(), PreviewChunkingRequest{Text: text, ChunkingConfig: cfg})
	if err != nil {
		t.Fatal(err)
	}
	want, err := SplitDocument(text, &ChunkingConfig{ChunkSize: cfg.ChunkSize, ChunkOverlap: cfg.ChunkOverlap, Separators: cfg.Separators, EnableParentChild: true, ParentChunkSize: cfg.ParentChunkSize, ChildChunkSize: cfg.ChildChunkSize, Strategy: cfg.Strategy})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Chunks) != len(want.Children) || result.Profile == nil || result.SelectedTier == "" {
		t.Fatalf("preview mismatch: %+v", result)
	}
	for i, ch := range want.Children {
		if result.Chunks[i].Content != ch.Content || result.Chunks[i].Start != ch.Start || result.Chunks[i].End != ch.End {
			t.Fatalf("chunk %d differs from ingestion", i)
		}
	}
}
func TestPreviewChunkingBoundsAndTruncation(t *testing.T) {
	for _, text := range []string{" \n\t", strings.Repeat("中", 64*1024+1)} {
		if _, err := PreviewDocumentChunking(context.Background(), PreviewChunkingRequest{Text: text}); err == nil {
			t.Fatal("invalid sample accepted")
		}
	}
	result, err := PreviewDocumentChunking(context.Background(), PreviewChunkingRequest{Text: strings.Repeat("x.\n\n", 550), ChunkingConfig: PreviewChunkingPayload{ChunkSize: 3, Strategy: "legacy", Separators: []string{"\n\n"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Chunks) > 500 || result.Stats.TruncatedTo <= 500 || result.Stats.Count != result.Stats.TruncatedTo {
		t.Fatalf("truncation lost full statistics: %+v", result.Stats)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = PreviewDocumentChunking(ctx, PreviewChunkingRequest{Text: "valid text"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}

func TestPreviewPreservesOriginalLineEndingsAndUnicodeOffsets(t *testing.T) {
	for _, ending := range []string{"\r\n", "\r", "\n"} {
		for _, parentChild := range []bool{false, true} {
			text := strings.Repeat("标题甲"+ending+"原始文本乙"+ending, 40)
			cfg := PreviewChunkingPayload{ChunkSize: 32, ChunkOverlap: 4, Separators: []string{"\n"}, Strategy: "legacy", EnableParentChild: parentChild, ParentChunkSize: 64, ChildChunkSize: 24}
			got, err := PreviewDocumentChunking(context.Background(), PreviewChunkingRequest{Text: text, ChunkingConfig: cfg})
			if err != nil {
				t.Fatal(err)
			}
			want, err := SplitDocument(text, &ChunkingConfig{ChunkSize: cfg.ChunkSize, ChunkOverlap: cfg.ChunkOverlap, Separators: cfg.Separators, Strategy: cfg.Strategy, EnableParentChild: parentChild, ParentChunkSize: cfg.ParentChunkSize, ChildChunkSize: cfg.ChildChunkSize})
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Chunks) != len(want.Children) {
				t.Fatalf("ending %q parentChild %v: preview %d ingestion %d", ending, parentChild, len(got.Chunks), len(want.Children))
			}
			for i, c := range want.Children {
				if got.Chunks[i].Content != c.Content || got.Chunks[i].Start != c.Start || got.Chunks[i].End != c.End {
					t.Fatalf("ending %q parentChild %v chunk %d differs", ending, parentChild, i)
				}
			}
		}
	}
}
