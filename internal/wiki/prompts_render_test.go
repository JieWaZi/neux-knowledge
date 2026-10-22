package wiki

import (
	"strings"
	"testing"
)

func TestLiteMapPromptUsesExtractionInstructionScope(t *testing.T) {
	prompt, err := RenderLitePrompt(WikiCandidateSlugPrompt, map[string]string{"Content": "长期记忆保留30天。", "PreviousSlugs": "concept/existing", "Language": "Chinese", "Granularity": "focused", "GranularityGuidance": WikiGranularityGuidance("focused"), "CustomInstructions": "只保留制度条款。", "InstructionScope": "wiki_extraction"})
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{"FOCUSED mode", "长期记忆保留30天。", "concept/existing", "<wiki_extraction_business_instructions>", "只保留制度条款。"} {
		if !strings.Contains(prompt.User, part) {
			t.Fatalf("actual Lite prompt lost %q", part)
		}
	}
	if prompt.System != "" {
		t.Fatal("Pass0 unexpectedly gained a system message")
	}
}
func TestLiteReducePromptKeepsSystemAndUserRoles(t *testing.T) {
	prompt, err := RenderLitePrompt(WikiPageModifyUserPrompt, map[string]string{"PageSlug": "concept/memory", "PageTitle": "记忆", "PageType": "concept", "HasAdditions": "1", "NewContent": "记忆事实", "CustomInstructions": "使用客观语气。", "InstructionScope": "wiki_content"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt.User, "<type>concept</type>") || !strings.Contains(prompt.User, "记忆事实") {
		t.Fatal("page data missing")
	}
	if !strings.HasPrefix(prompt.System, WikiPageModifySystemPrompt) || !strings.Contains(prompt.System, "使用客观语气。") || strings.Contains(prompt.User, "使用客观语气。") {
		t.Fatal("original system/user instruction scope changed")
	}
}
