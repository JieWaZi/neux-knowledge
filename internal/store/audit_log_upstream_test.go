package store

import "testing"

func TestKBActivityAppendSampleTitles(t *testing.T) {
	details := map[string]any{"count": 3}
	kbActivityAppendSampleTitles(details, "  Alpha  ", "Beta", "Alpha", "Gamma")
	if details["title"] != "Alpha" {
		t.Fatalf("title = %#v", details["title"])
	}
	titles, ok := details["titles"].([]string)
	if !ok || len(titles) != 3 {
		t.Fatalf("titles = %#v", details["titles"])
	}

	single := map[string]any{"count": 1}
	kbActivityAppendSampleTitles(single, "Only one")
	if single["title"] != "Only one" {
		t.Fatalf("single title = %#v", single["title"])
	}
	if _, exists := single["titles"]; exists {
		t.Fatalf("single titles should be omitted: %#v", single)
	}
}
