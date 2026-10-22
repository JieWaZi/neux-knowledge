package types

// Source locator types. A locator points from a chunk back into the original
// uploaded file so a citation can open the file at the cited place. Every
// ordinal (page, slide, block, section, row) is 1-based.
const (
	// SourceLocatorPDF is a PDF page, optionally narrowed to a region.
	SourceLocatorPDF = "pdf"
	// SourceLocatorDocx is the Nth body-level paragraph or table of a Word file.
	SourceLocatorDocx = "docx"
	// SourceLocatorSlide is a presentation slide.
	SourceLocatorSlide = "slide"
	// SourceLocatorSheet is a row range of a spreadsheet sheet.
	SourceLocatorSheet = "sheet"
	// SourceLocatorText is a rune range of a plain-text original (md, txt, json).
	SourceLocatorText = "text"
	// SourceLocatorTime is a time range of an audio original.
	SourceLocatorTime = "time"
	// SourceLocatorSection is an EPUB spine item.
	SourceLocatorSection = "section"
)

// SourceLocatorQuoteMax caps display snippets, not stored source evidence.
const SourceLocatorQuoteMax = 300

// SourceLocator is one position in an original file. Only the fields of its
// Type are set; zero values are omitted from JSON and read back as zero.
type SourceLocator struct {
	// Type is the location type within the original file.
	Type string `json:"type"`
	// Mapping records verified text-to-source alignment. Empty means legacy.
	// Mapping is the verified precision of the text-to-source mapping.
	Mapping string `json:"mapping,omitempty"`
	// Partial means some of the chunk has no source mapping.
	// Partial indicates whether any part of the current range lacks a source mapping.
	Partial bool `json:"partial,omitempty"`
	// SourceID identifies a block within the parser result, not a rendered item.
	// SourceID identifies a source structural block in the parse results.
	SourceID string `json:"source_id,omitempty"`
	// SourceHash binds the locator to the knowledge record’s source revision hash.
	// SourceHash is the digest binding this source version.
	SourceHash string `json:"source_hash,omitempty"`
	// Page is the 1-based PDF page.
	// Page is the one-based PDF page number.
	Page int `json:"page,omitempty"`
	// BBox is [x0, y0, x1, y1] as fractions of the page width and height,
	// origin at the top-left corner of the rendered page.
	// BBox is the normalized bounding box on the PDF page.
	BBox []float64 `json:"bbox,omitempty"`
	// Block is the 1-based index among a Word file's body paragraphs and tables.
	// Block is the one-based Word paragraph or table position.
	Block int `json:"block,omitempty"`
	// Slide is the 1-based slide number in presentation order.
	// Slide is the one-based slide number.
	Slide int `json:"slide,omitempty"`
	// Sheet is the spreadsheet sheet name; RowStart/RowEnd are its 1-based
	// row numbers as shown by spreadsheet applications.
	// Sheet is the original spreadsheet sheet name.
	Sheet string `json:"sheet,omitempty"`
	// RowStart is the one-based starting row within the sheet.
	RowStart int `json:"row_start,omitempty"`
	// RowEnd is the one-based ending row within the sheet.
	RowEnd int `json:"row_end,omitempty"`
	// Start/End are rune offsets into the original text file.
	// Start is the starting rune offset in the source.
	Start int `json:"start,omitempty"`
	// End is the ending rune offset in the source.
	End int `json:"end,omitempty"`
	// StartMs/EndMs bound an audio segment.
	// StartMs is the audio or video segment's starting time in milliseconds.
	StartMs int64 `json:"start_ms,omitempty"`
	// EndMs is the audio or video segment's ending time in milliseconds.
	EndMs int64 `json:"end_ms,omitempty"`
	// Section is the 1-based EPUB spine index.
	// Section is the one-based EPUB chapter position.
	Section int `json:"section,omitempty"`
	// Title names the section or slide when the source has one.
	// Title is the source chapter or slide title.
	Title string `json:"title,omitempty"`
	// Quote is the complete cited overlap as parsed. Viewers match it
	// inside the rendered original and against the
	// answer sentence that cites the chunk.
	// Quote is the complete quoted text for the overlapping source range.
	Quote string `json:"quote,omitempty"`
}

// SourceBlock ties a rune range of a parser's Markdown output to the place in
// the original file it came from.
type SourceBlock struct {
	// Start is the starting rune offset in the source.
	Start int
	// End is the ending rune offset in the source.
	End int
	// Locator is the actual source location corresponding to this text.
	Locator SourceLocator
}

// SourceLocators is the per-chunk locator list, stored as a JSON column.
type SourceLocators []SourceLocator
