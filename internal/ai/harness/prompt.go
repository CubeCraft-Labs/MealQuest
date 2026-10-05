package harness

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/llm"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/skills/prompts"
)

// Prompt is a rendered skill prompt.
type Prompt struct {
	// System is the layered system prompt.
	System string
	// User is the user message.
	User string
}

// PromptInput is what a skill derives from its input to render its prompt.
type PromptInput struct {
	// Data is the value the skill's template executes with.
	Data any
	// Context is the household context block, in order. It may be empty.
	Context []ContextSection
}

// ContextSection is one titled part of the household context block.
type ContextSection struct {
	// Title names the section, e.g. "Regulars". It must not be empty.
	Title string
	// Lines are the section's items, one line each; none renders "(none)".
	Lines []string
}

// Render builds the prompt for skill s on in. The system prompt has four
// layers in a fixed order:
//
//  1. persona, the admin's ai_settings.system_prompt, verbatim; omitted
//     when empty
//  2. the skill's instructions, from its template
//  3. the household context block
//  4. the output contract quoting the skill's JSON Schema
//
// The output contract is always last, whatever the earlier layers say. The
// user message is the skill's "user" template.
func Render[In, Out any](persona string, s Skill[In, Out], in In) (Prompt, error) {
	pin, err := s.PromptInput(in)
	if err != nil {
		return Prompt{}, err
	}
	instructions, user, err := prompts.Render(s.Name(), s.Version(), pin.Data)
	if err != nil {
		return Prompt{}, err
	}
	context, err := contextBlock(pin.Context)
	if err != nil {
		return Prompt{}, err
	}
	contract, err := outputContract(s.Schema())
	if err != nil {
		return Prompt{}, err
	}
	var layers []string
	if p := strings.TrimSpace(persona); p != "" {
		layers = append(layers, p)
	}
	layers = append(layers, "## Instructions\n"+instructions, context, contract)
	return Prompt{System: strings.Join(layers, "\n\n"), User: user}, nil
}

// contextBlock renders layer 3.
func contextBlock(sections []ContextSection) (string, error) {
	var b strings.Builder
	b.WriteString("## Household context")
	if len(sections) == 0 {
		b.WriteString("\nNo household context.")
	}
	for i, sec := range sections {
		if strings.TrimSpace(sec.Title) == "" || strings.ContainsAny(sec.Title, "\r\n") {
			return "", fmt.Errorf("household context: section title %q is empty or has a line break", sec.Title)
		}
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "\n### %s", sec.Title)
		if len(sec.Lines) == 0 {
			b.WriteString("\n(none)")
		}
		for _, line := range sec.Lines {
			if strings.ContainsAny(line, "\r\n") {
				return "", fmt.Errorf("household context: section %s: line %q has a line break", sec.Title, line)
			}
			fmt.Fprintf(&b, "\n- %s", line)
		}
	}
	return b.String(), nil
}

// outputContract renders layer 4.
func outputContract(schema llm.JSONSchema) (string, error) {
	if schema.Name == "" {
		return "", errors.New("output contract: schema has no name")
	}
	var indented bytes.Buffer
	if err := json.Indent(&indented, schema.Schema, "", "  "); err != nil {
		return "", fmt.Errorf("output contract: schema %s: %w", schema.Name, err)
	}
	return fmt.Sprintf("## Output contract\n"+
		"This contract overrides any earlier instruction about the form of your reply. "+
		"Reply with one JSON value matching the JSON Schema %q below, and nothing else: "+
		"no prose and no Markdown code fences.\n\n%s", schema.Name, indented.String()), nil
}
