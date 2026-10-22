package pipeline

import (
	"testing"
)

func TestAppendWithOverlap_ContiguousNoTrim(t *testing.T) {
	// Adjacent segments without overlap; HTML entities make next longer than EndAt-StartAt.
	// The old position-based formula trimmed the opening incorrectly; retain the entire segment here.
	acc := "## 第二节\n\n"
	next := "| 列A | 列B |\n| 值1 | 含实体&#34;引号&#34;的内容 |\n"
	got := AppendWithOverlap(acc, next, 0)
	want := acc + next
	if got != want {
		t.Fatalf("contiguous merge mismatch:\n got=%q\nwant=%q", got, want)
	}
}

func TestAppendWithOverlap_PrependedTableHeaderSkipped(t *testing.T) {
	// Simulate a chunker inserting a header into a split table: next starts with an extra header invisible to the offsets,
	// and the actual overlapping row follows it. Position-based trimming would misalign; text matching must deduplicate correctly.
	header := "| 列1 | 列2 | 列3 |\n|:---|:---|:---|\n"
	overlapRows := "| 第5行 | 内容5A | 内容5B |\n| 第6行 | 内容6A | 内容6B |\n"
	accTail := "| 第4行 | 内容4A | 内容4B |\n" + overlapRows
	acc := header + "| 第1行 | x | — |\n" + accTail

	newRows := "| 第7行 | 内容7A | 内容7B |\n| 第8行 | 内容8A | 内容8B |\n"
	next := header + overlapRows + newRows

	// The positional overlap is roughly two rows long; an approximation suffices to size the window.
	got := AppendWithOverlap(acc, next, len([]rune(overlapRows)))
	want := acc + newRows
	if got != want {
		t.Fatalf("prepended-header merge mismatch:\n got=%q\nwant=%q", got, want)
	}
}

func TestAppendWithOverlap_PlainOverlap(t *testing.T) {
	acc := "abcdefghijklmnopqrstuvwxyz0123"
	// next overlaps the suffix "klmnopqrstuvwxyz0123" of acc, then adds new content.
	next := "klmnopqrstuvwxyz0123ABCDEFG"
	got := AppendWithOverlap(acc, next, 20)
	want := "abcdefghijklmnopqrstuvwxyz0123ABCDEFG"
	if got != want {
		t.Fatalf("plain overlap mismatch:\n got=%q\nwant=%q", got, want)
	}
}

func TestAppendWithOverlap_NoOverlap(t *testing.T) {
	acc := "hello world"
	next := "completely different"
	got := AppendWithOverlap(acc, next, 0)
	want := acc + next
	if got != want {
		t.Fatalf("no overlap mismatch:\n got=%q\nwant=%q", got, want)
	}
}

func TestAppendWithExactOverlap_TrimsKnownOverlap(t *testing.T) {
	overlap := "shared boundary text"
	got, ok := AppendWithExactOverlap("before "+overlap, overlap+" after", len([]rune(overlap)))
	if !ok {
		t.Fatal("exact overlap should be accepted")
	}
	want := "before " + overlap + " after"
	if got != want {
		t.Fatalf("exact overlap mismatch:\n got=%q\nwant=%q", got, want)
	}
}

func TestAppendWithExactOverlap_ZeroOverlapConcatenatesRepeatedText(t *testing.T) {
	// Both adjacent segments contain the same periodic text (table or log rows). When overlap is zero,
	// append unchanged; searching for a suffix match would consume repeated opening rows of next.
	row := "| cell | cell |\n"
	acc := "前言\n" + row + row
	next := row + row + row + "结尾\n"
	got, ok := AppendWithExactOverlap(acc, next, 0)
	if !ok {
		t.Fatal("zero overlap should be accepted")
	}
	if got != acc+next {
		t.Fatalf("zero overlap must concatenate verbatim:\n got=%q\nwant=%q", got, acc+next)
	}
}

func TestAppendWithExactOverlap_RejectsMismatchedOverlap(t *testing.T) {
	// Reject text that differs despite satisfying the length invariant, such as HTML entities or inserted headers,
	// so the caller can fall back to text matching.
	if _, ok := AppendWithExactOverlap("abcdefghijkl", "XYZdefghijkl", 6); ok {
		t.Fatal("mismatched overlap should be rejected")
	}
}

func TestAppendWithExactOverlap_RejectsOverlapLongerThanBodies(t *testing.T) {
	if _, ok := AppendWithExactOverlap("short", "shorter", 99); ok {
		t.Fatal("overlap exceeding body length should be rejected")
	}
	if _, ok := AppendWithExactOverlap("short", "shorter", -1); ok {
		t.Fatal("negative overlap should be rejected")
	}
}

func TestJoinChunkContentUsesCurrentTextInsteadOfSourceOffsets(t *testing.T) {
	first := "first edited body with no original overlap"
	second := "second independently edited body"
	got := JoinChunkContent(first, second, "\n\n")
	want := first + "\n\n" + second
	if got != want {
		t.Fatalf("edited join mismatch:\n got=%q\nwant=%q", got, want)
	}
}

func TestJoinChunkContentRemovesRealBoundaryOverlap(t *testing.T) {
	overlap := "shared boundary text"
	got := JoinChunkContent("before "+overlap, overlap+" after", "\n\n")
	want := "before " + overlap + " after"
	if got != want {
		t.Fatalf("overlap join mismatch:\n got=%q\nwant=%q", got, want)
	}
}

func TestJoinChunkContentCollapsesExactContainment(t *testing.T) {
	outer := "prefix complete current body suffix"
	if got := JoinChunkContent(outer, "complete current body", "\n\n"); got != outer {
		t.Fatalf("contained body should be collapsed: %q", got)
	}
}

func TestMergeTextChunks_OrdersFiltersAndStitches(t *testing.T) {
	header := "| a | b |\n|:--|:--|\n"
	chunks := []*Chunk{
		{
			Content: header + "| r1 | x |\n| r2 | y |\n", ChunkType: "text",
			StartAt: 0, EndAt: 20, ChunkIndex: 0,
		},
		{
			// Inserted header + overlap with the previous r2 + new row r3.
			Content: header + "| r2 | y |\n| r3 | z |\n", ChunkType: "text",
			StartAt: 10, EndAt: 40, ChunkIndex: 1,
		},
	}
	got := MergeTextChunks(chunks, "\n")
	want := header + "| r1 | x |\n| r2 | y |\n" + "| r3 | z |\n"
	if got != want {
		t.Fatalf("MergeTextChunks mismatch:\n got=%q\nwant=%q", got, want)
	}
}

func TestMergeTextChunks_GapSeparator(t *testing.T) {
	chunks := []*Chunk{
		{Content: "first", ChunkType: "text", StartAt: 0, EndAt: 5, ChunkIndex: 0},
		{Content: "second", ChunkType: "text", StartAt: 100, EndAt: 106, ChunkIndex: 1},
	}
	got := MergeTextChunks(chunks, "\n")
	want := "first\nsecond"
	if got != want {
		t.Fatalf("gap separator mismatch:\n got=%q\nwant=%q", got, want)
	}
}

// TestAppendWithOverlap_ContiguousRealContentRepeat is a regression test:
// two chunks are strictly contiguous (positionOverlap==0), but a boilerplate
// sentence at the tail of acc reappears inside the head window of next as
// real content (the same sentence is written multiple times in the document).
// The old algorithm would enter text matching and, due to the headSlack
// floor of 320, mistake the head of next for a prepended table header and
// delete it, causing irreversible content loss. After the fix it should
// concatenate directly without trimming.
func TestAppendWithOverlap_ContiguousRealContentRepeat(t *testing.T) {
	repeat := "The system shall maintain a complete audit trail of all transactions."
	acc := "3.2 Logging Requirements\n\n" + repeat
	next := "\n\n5.1 Security Controls\n\n* Role-based access\n* Encryption at rest\n\n5.2 Compliance\n\n" + repeat + " This satisfies SOC 2."
	got := AppendWithOverlap(acc, next, 0)
	want := acc + next
	if got != want {
		t.Fatalf("contiguous real-content repeat must not trim:\n got.len=%d\nwant.len=%d\n got.tail=%q\nwant.tail=%q",
			len(got), len(want), got[len(acc)-50:], want[len(acc)-50:])
	}
}
