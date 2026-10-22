package wiki

import (
	_ "embed"
	"strings"

	"gopkg.in/yaml.v3"
)

//go:embed prompts/generate_summary.yaml
var documentProfileTemplates string

// RenderDocumentSummaryPrompt keeps the standard summary system template separate from the user body.
func RenderDocumentSummaryPrompt(language, content string) (StagePrompt, error) {
	var cfg struct {
		Templates []struct {
			Content string `yaml:"content"` /* The original upstream system prompt. */
		} `yaml:"templates"` /* The upstream template list. */
	}
	if err := yaml.Unmarshal([]byte(documentProfileTemplates), &cfg); err != nil {
		return StagePrompt{}, err
	}
	return StagePrompt{System: strings.ReplaceAll(cfg.Templates[0].Content, "{{language}}", language), User: content}, nil
}
