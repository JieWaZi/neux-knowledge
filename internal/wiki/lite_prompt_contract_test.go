package wiki

import (
	"strings"
	"testing"
)

func TestLiteStagePromptRequestsRawModelOutput(t *testing.T) {
	prompt, err := RenderLitePrompt(WikiSummaryPrompt, map[string]string{"Content": "会话由服务端校验，离线验证另有适用条件。", "Language": "Chinese", "ExtractedSlugs": ""})
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"allycli", "outputPath", "schema.json", "ready_to_finish", "Host file delivery"} {
		if strings.Contains(prompt.User+prompt.System, forbidden) {
			t.Fatalf("Lite prompt still requires removed host protocol: %s", forbidden)
		}
	}
	if !strings.Contains(prompt.User, "SUMMARY:") {
		t.Fatal("upstream raw summary contract missing")
	}
}
