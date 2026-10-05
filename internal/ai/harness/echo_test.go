package harness

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/llm"
)

// echoSkill is the trivial test skill: it asks the model to repeat a word.
// Its prompt is prompts/echo.v1.tmpl.
type echoSkill struct{}

type echoInput struct {
	Word    string
	Context []ContextSection
}

type echoOutput struct {
	Echo string `json:"echo"`
}

var _ Skill[echoInput, echoOutput] = echoSkill{}

func (echoSkill) Name() string { return "echo" }

func (echoSkill) Version() int { return 1 }

func (echoSkill) PromptInput(in echoInput) (PromptInput, error) {
	if in.Word == "" {
		return PromptInput{}, errors.New("no word to echo")
	}
	return PromptInput{Data: in, Context: in.Context}, nil
}

func (echoSkill) Schema() llm.JSONSchema {
	return llm.JSONSchema{Name: "echo", Schema: json.RawMessage(
		`{"type":"object","properties":{"echo":{"type":"string"}},"required":["echo"],"additionalProperties":false}`)}
}

func (echoSkill) Validate(out echoOutput) []string {
	if out.Echo == "" {
		return []string{"echo must not be empty"}
	}
	if out.Echo != strings.ToLower(out.Echo) {
		return []string{"echo must be lower case"}
	}
	return nil
}

func (echoSkill) PostProcess(out echoOutput) (echoOutput, error) {
	out.Echo = strings.TrimSpace(out.Echo)
	return out, nil
}

func (echoSkill) Timeout() time.Duration { return DefaultTimeout }
