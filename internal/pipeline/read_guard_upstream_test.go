package pipeline

import "testing"

func TestStripImageMarkup(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"plain text untouched", "Hello world.", "Hello world."},
		{"single markdown image removed", "![alt](images/page_1.png)", ""},
		{
			"scanned-pdf style page references all stripped",
			"![MX5280_page_1.png](images/MX5280_page_1.png)\n\n![MX5280_page_2.png](images/MX5280_page_2.png)",
			"\n\n",
		},
		{"mixed text and image keeps text", "Intro paragraph.\n![fig](a.png)\nConclusion.", "Intro paragraph.\n\nConclusion."},
		{"html img tag stripped", `Before <img src="x.png" alt="y"/> after`, "Before  after"},
		{
			// Regression guard: an earlier version stripped the WHOLE
			// <image>...</image> block (including <image_ocr> content),
			// silently destroying successful VLM OCR results. The fix must
			// preserve the inner OCR / caption text.
			"enriched <image> block keeps inner OCR + caption text",
			`<image url="images/page_1.png">
<image_original>![p1](images/page_1.png)</image_original>
<image_caption>scanned letter on letterhead</image_caption>
<image_ocr>SEHR GEEHRTER HERR MUSTERMANN, ...</image_ocr>
</image>`,
			"\n\nscanned letter on letterhead\nSEHR GEEHRTER HERR MUSTERMANN, ...\n",
		},
		{
			"empty <image> block (OCR failed) reduces to whitespace",
			`<image url="x"><image_original>![a](x)</image_original></image>`,
			"",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripImageMarkup(tt.input)
			if got != tt.want {
				t.Errorf("stripImageMarkup(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestHasSufficientTextContent(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"empty string", "", false},
		{"only whitespace", "   \n\n\t  ", false},
		{
			"only image references (scanned PDF without OCR)",
			"![MX5280_page_1.png](images/MX5280_page_1.png)\n![MX5280_page_2.png](images/MX5280_page_2.png)",
			false,
		},
		{"too-short text below 10-rune threshold", "hi", false},
		{
			"short legitimate note above threshold",
			"Meeting at 3pm tomorrow.",
			true,
		},
		{
			"image-only with successful VLM OCR (the fix)",
			`<image url="images/p1.png">
<image_original>![p1](images/p1.png)</image_original>
<image_caption>scanned letter</image_caption>
<image_ocr>Sehr geehrter Herr Mustermann, in der Sache 4711/2024 ...</image_ocr>
</image>`,
			true,
		},
		{
			"image-only with failed VLM OCR (still rejected)",
			`<image url="images/p1.png">
<image_original>![p1](images/p1.png)</image_original>
</image>`,
			false,
		},
		{
			"sufficient text mixed with images still passes",
			"![cover](cover.png)\nDie Beklagte hat die Klage anerkannt.\n![sig](sig.png)",
			true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hasSufficientTextContent(tt.input)
			if got != tt.want {
				t.Errorf("hasSufficientTextContent(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}
