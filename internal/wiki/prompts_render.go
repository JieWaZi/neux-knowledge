package wiki

import (
	"bytes"
	"text/template"
)

// StagePromptVersion fixes the organization-stage prompt source used in the task configDigest.
const StagePromptVersion = "weknora-8776a2d-lite-direct-v3"

// StagePrompt holds fixed upstream system and user messages for the direct model port.
type StagePrompt struct {
	System string // Stable system-side rules.
	User   string // Projection of frozen user-side input.
}

func renderTemplate(text string, data any) (string, error) {
	t, err := template.New("prompt").Parse(text)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// RenderLitePrompt renders upstream templates and instruction scopes unchanged without adding host tool protocols.
func RenderLitePrompt(prompt string, data map[string]string) (StagePrompt, error) {
	user, err := renderTemplate(prompt, data)
	if err != nil {
		return StagePrompt{}, err
	}
	if prompt == WikiPageModifyUserPrompt {
		return StagePrompt{System: AppendCustomPromptInstructions(WikiPageModifySystemPrompt, data["CustomInstructions"], data["InstructionScope"]), User: user}, nil
	}
	return StagePrompt{User: AppendCustomPromptInstructions(user, data["CustomInstructions"], data["InstructionScope"])}, nil
}
