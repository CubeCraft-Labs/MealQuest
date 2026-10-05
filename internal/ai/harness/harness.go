// Package harness runs skills: versioned, typed prompts whose structured
// output Go validates. A run renders the skill's prompt, asks the provider
// for output matching the skill's schema, decodes and validates the reply,
// and on failure sends exactly one repair turn quoting the problems. If the
// repair fails too the run fails with llm.ErrBadOutput; there is no silent
// fallback (D-007).
package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/llm"
)

// Prompt is a rendered skill prompt. Prompt layering builds on it later.
type Prompt struct {
	// System is the system prompt; empty means none.
	System string
	// User is the user message. It must not be empty.
	User string
}

// Skill is one versioned AI task with input In and structured output Out.
type Skill[In, Out any] interface {
	// Name identifies the skill, e.g. "side_quests".
	Name() string
	// Version is bumped whenever the prompt, schema or validation changes.
	Version() int
	// Render builds the prompt for in.
	Render(in In) (Prompt, error)
	// Schema is the JSON Schema the reply must match; Out decodes from it.
	Schema() llm.JSONSchema
	// Validate returns every problem with out, or none when it is usable.
	// The problems are sent to the model in the repair turn, so each must
	// say what to fix.
	Validate(out Out) []string
	// PostProcess finishes a valid out, e.g. normalising names. Its result
	// is validated again; problems found then are repaired like any other.
	// Its error fails the run without a repair turn.
	PostProcess(out Out) (Out, error)
}

// Config is what a run needs from the household's AI settings.
type Config struct {
	// Provider answers the requests.
	Provider llm.Provider
	// Temperature is the household's sampling temperature, sent with
	// every request.
	Temperature float64
}

// BadOutputError is a run whose reply and repair reply were both unusable.
// It matches llm.ErrBadOutput.
type BadOutputError struct {
	// Skill and Version identify the skill that ran.
	Skill   string
	Version int
	// First lists the problems with the first reply, Repair those with
	// the reply to the repair turn.
	First  []string
	Repair []string
}

func (e *BadOutputError) Error() string {
	return fmt.Sprintf("harness: skill %s v%d: %s: first reply: %s; repair reply: %s",
		e.Skill, e.Version, llm.ErrBadOutput, strings.Join(e.First, "; "), strings.Join(e.Repair, "; "))
}

func (e *BadOutputError) Unwrap() error { return llm.ErrBadOutput }

// Run runs skill s on in with cfg and returns its validated, post-processed
// output. A provider error other than llm.ErrBadOutput is returned
// unchanged and never repaired.
func Run[In, Out any](ctx context.Context, cfg Config, s Skill[In, Out], in In) (Out, error) {
	var zero Out
	prompt, err := s.Render(in)
	if err != nil {
		return zero, fmt.Errorf("harness: skill %s v%d: render: %w", s.Name(), s.Version(), err)
	}
	if prompt.User == "" {
		return zero, fmt.Errorf("harness: skill %s v%d: render: empty user message", s.Name(), s.Version())
	}
	schema := s.Schema()
	temperature := cfg.Temperature
	req := llm.Request{
		System:      prompt.System,
		Messages:    []llm.Message{{Role: llm.RoleUser, Content: prompt.User}},
		Temperature: &temperature,
		Schema:      &schema,
	}

	out, reply, first, err := attempt(ctx, cfg.Provider, s, req)
	if err != nil {
		return zero, err
	}
	if len(first) > 0 {
		if reply != "" {
			req.Messages = append(req.Messages, llm.Message{Role: llm.RoleAssistant, Content: reply})
		}
		req.Messages = append(req.Messages, llm.Message{Role: llm.RoleUser, Content: repairMessage(schema.Name, first)})
		var second []string
		out, _, second, err = attempt(ctx, cfg.Provider, s, req)
		if err != nil {
			return zero, err
		}
		if len(second) > 0 {
			return zero, &BadOutputError{Skill: s.Name(), Version: s.Version(), First: first, Repair: second}
		}
	}
	return out, nil
}

// attempt sends req once, then decodes, validates, post-processes and
// validates the reply again. It returns the reply for the repair turn and
// the problems found; err is set only for a provider or post-process
// failure, neither of which is repairable.
func attempt[In, Out any](ctx context.Context, p llm.Provider, s Skill[In, Out], req llm.Request) (out Out, reply string, problems []string, err error) {
	resp, err := p.Generate(ctx, req)
	// The repair turn must quote what failed: the structured output when
	// there is any, otherwise the text.
	reply = string(resp.JSON)
	if reply == "" {
		reply = resp.Text
	}
	if err != nil {
		if errors.Is(err, llm.ErrBadOutput) {
			return out, reply, []string{err.Error()}, nil
		}
		return out, "", nil, err
	}
	if err := decode(resp.JSON, &out); err != nil {
		return out, reply, []string{err.Error()}, nil
	}
	if problems := s.Validate(out); len(problems) > 0 {
		return out, reply, problems, nil
	}
	out, err = s.PostProcess(out)
	if err != nil {
		return out, "", nil, fmt.Errorf("harness: skill %s v%d: post-process: %w", s.Name(), s.Version(), err)
	}
	return out, reply, s.Validate(out), nil
}

// decode strictly decodes data into out: one JSON value, no unknown fields.
func decode(data json.RawMessage, out any) error {
	if len(data) == 0 {
		return errors.New("reply holds no JSON")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("reply does not decode: %v", err)
	}
	if err := dec.Decode(new(json.RawMessage)); err != io.EOF {
		return errors.New("reply has data after the JSON value")
	}
	return nil
}

// repairMessage asks the model to fix problems in its last reply.
func repairMessage(schemaName string, problems []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Your previous reply was not usable. Reply again with JSON matching the %s schema, fixing these problems:\n", schemaName)
	for _, p := range problems {
		fmt.Fprintf(&b, "- %s\n", p)
	}
	return b.String()
}
