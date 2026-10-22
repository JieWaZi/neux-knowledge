package pipeline

import (
	"strings"
	"testing"
)

func TestFormatExistingTaxonomyForPrompt(t *testing.T) {
	got := formatExistingTaxonomyForPrompt([][]string{
		{"春节", "传统习俗"},
		{"春节", "文化习俗", "节日习俗"},
		{"春节习俗"},
		{"产品定位"},
	})
	want := "产品定位\n" +
		"春节\n" +
		"  传统习俗\n" +
		"  文化习俗\n" +
		"    节日习俗\n" +
		"春节习俗"
	if got != want {
		t.Fatalf("formatExistingTaxonomyForPrompt():\n%q\nwant:\n%q", got, want)
	}
}

func TestFormatExistingTaxonomyForPromptEmpty(t *testing.T) {
	if got := formatExistingTaxonomyForPrompt(nil); got != "" {
		t.Fatalf("formatExistingTaxonomyForPrompt(nil) = %q, want empty", got)
	}
}

func TestParseTaxonomyAssignments(t *testing.T) {
	raw := "```json\n{\"assignments\":[" +
		"{\"slug\":\"entity/zhang-san\",\"path\":[\"人物\"]}," +
		"{\"slug\":\"concept/spring\",\"path\":[\"节日\",\"传统节日\"]}," +
		"{\"slug\":\"  \",\"path\":[\"X\"]}," +
		"{\"slug\":\"entity/unclassified\",\"path\":[]}" +
		"]}\n```"

	got := parseTaxonomyAssignments(raw)
	if len(got) != 3 {
		t.Fatalf("parseTaxonomyAssignments() returned %d entries, want 3 (blank slug dropped): %v", len(got), got)
	}
	if strings.Join(got["entity/zhang-san"], "/") != "人物" {
		t.Fatalf("zhang-san path = %v, want [人物]", got["entity/zhang-san"])
	}
	if strings.Join(got["concept/spring"], "/") != "节日/传统节日" {
		t.Fatalf("spring path = %v, want [节日 传统节日]", got["concept/spring"])
	}
	if p, ok := got["entity/unclassified"]; !ok || len(p) != 0 {
		t.Fatalf("unclassified path = %v (ok=%v), want empty slice present", p, ok)
	}
}

func TestParseTaxonomyAssignmentsMalformed(t *testing.T) {
	if got := parseTaxonomyAssignments("not json at all"); got != nil {
		t.Fatalf("parseTaxonomyAssignments(garbage) = %v, want nil", got)
	}
}
