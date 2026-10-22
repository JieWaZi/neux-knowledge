package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestCheckSufficientSummaryContent(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name      string // Scenario name.
		content   string // Source content.
		wantError bool   // Whether to reject insufficient body content.
	}{
		{
			name:      "empty content rejected",
			content:   "",
			wantError: true,
		},
		{
			name:      "only whitespace rejected",
			content:   "   \n\n\t  ",
			wantError: true,
		},
		{
			name:      "below threshold rejected",
			content:   "hi",
			wantError: true,
		},
		{
			name: "scanned PDF with no OCR (image-only) rejected",
			content: "![MX5280_page_1.png](images/MX5280_page_1.png)\n" +
				"![MX5280_page_2.png](images/MX5280_page_2.png)",
			wantError: true,
		},
		{
			name:      "scanned PDF with empty <image> wrapper rejected",
			content:   `<image url="x"><image_original>![a](x)</image_original></image>`,
			wantError: true,
		},
		{
			name:      "short legitimate note above threshold accepted",
			content:   "Meeting at 3pm tomorrow.",
			wantError: false,
		},
		{
			name: "scanned PDF with successful VLM OCR accepted",
			content: `<image url="images/p1.png">
<image_original>![p1](images/p1.png)</image_original>
<image_caption>scanned letter</image_caption>
<image_ocr>Sehr geehrter Herr Mustermann, in der Sache 4711/2024 ...</image_ocr>
</image>`,
			wantError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkSufficientSummaryContent(ctx, "test-knowledge-id", tt.content)
			if tt.wantError {
				if err == nil {
					t.Errorf("expected errInsufficientSummaryContent, got nil")
					return
				}
				if !errors.Is(err, errInsufficientSummaryContent) {
					t.Errorf("expected errInsufficientSummaryContent sentinel, got %v", err)
				}
			} else {
				if err != nil {
					t.Errorf("expected nil error, got %v", err)
				}
			}
		})
	}
}

// TestCheckSufficientSummaryContent_ThresholdOverride verifies that
func TestSummaryInputSamplingPreservesChineseHeadMiddleTail(t *testing.T) {
	text := strings.Repeat("头", 9000) + strings.Repeat("中", 9000) + strings.Repeat("尾", 9000)
	got := sampleLongContent(text, 8192)
	if len([]rune(got)) != 8192 || !strings.Contains(got, "头") || !strings.Contains(got, "中") || !strings.Contains(got, "尾") {
		t.Fatal("sampling lost document regions or rune budget")
	}
	if got := sampleLongContent("中文原文", 8192); got != "中文原文" {
		t.Fatal(got)
	}
}
