// Package harness runs skills: versioned, typed prompts whose structured
// output Go validates. A run renders the skill's layered prompt from its
// embedded template (see Render and package prompts), asks the provider
// for output matching the skill's schema, decodes and validates the reply,
// and on failure sends exactly one repair turn quoting the problems. If the
// repair fails too the run fails with llm.ErrBadOutput; there is no silent
// fallback (D-007). Every run that reaches the provider is recorded in
// ai_runs (see Run and Purge).
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
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

// Skill is one versioned AI task with input In and structured output Out.
type Skill[In, Out any] interface {
	// Name identifies the skill, e.g. "side_quests".
	Name() string
	// Version is bumped whenever the prompt, schema or validation changes.
	// The prompt template is prompts/<Name>.v<Version>.tmpl.
	Version() int
	// PromptInput derives the template data and household context for in.
	PromptInput(in In) (PromptInput, error)
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
	// Persona is ai_settings.system_prompt, the first layer of every
	// system prompt; empty means none.
	Persona string
	// Temperature is the household's sampling temperature, sent with
	// every request.
	Temperature float64
	// ProviderName is ai_settings.provider and Model is ai_settings.model,
	// both recorded in ai_runs. Neither may be empty.
	ProviderName string
	Model        string
	// APIKey is the decrypted API key Provider sends, or "" when it sends
	// none. It is used only to remove the key from the ai_runs row.
	APIKey string
	// Store receives the run's ai_runs row. It must not be nil.
	Store *store.Store
}

// validate reports a Config missing what every run needs.
func (c Config) validate() error {
	switch {
	case c.Provider == nil:
		return errors.New("config has no provider")
	case c.ProviderName == "":
		return errors.New("config has no provider name")
	case c.Model == "":
		return errors.New("config has no model")
	case c.Store == nil:
		return errors.New("config has no store")
	}
	return nil
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
//
// Every run that sends a request writes one ai_runs row when it ends,
// covering both turns, with its RunStatus and the request and response
// bodies redacted (see redact). A Config or render error fails the run
// before any request, and writes no row. When the row cannot be written
// the run fails with that error too, even if its output was usable.
func Run[In, Out any](ctx context.Context, cfg Config, s Skill[In, Out], in In) (Out, error) {
	var zero Out
	if err := cfg.validate(); err != nil {
		return zero, fmt.Errorf("harness: skill %s v%d: %w", s.Name(), s.Version(), err)
	}
	prompt, err := Render(cfg.Persona, s, in)
	if err != nil {
		return zero, fmt.Errorf("harness: skill %s v%d: render: %w", s.Name(), s.Version(), err)
	}
	schema := s.Schema()
	temperature := cfg.Temperature
	req := llm.Request{
		System:      prompt.System,
		Messages:    []llm.Message{{Role: llm.RoleUser, Content: prompt.User}},
		Temperature: &temperature,
		Schema:      &schema,
	}

	log := &runLog{start: store.Now()}
	out, status, err := turns(ctx, cfg, s, req, log)
	if saveErr := log.save(ctx, cfg, s.Name(), s.Version(), status, err); saveErr != nil {
		return zero, errors.Join(err, fmt.Errorf("harness: skill %s v%d: record ai_runs row: %w", s.Name(), s.Version(), saveErr))
	}
	if err != nil {
		return zero, err
	}
	return out, nil
}

// turns sends req and, when its reply is unusable, the repair turn,
// recording both in log. It returns the run's status alongside its result.
func turns[In, Out any](ctx context.Context, cfg Config, s Skill[In, Out], req llm.Request, log *runLog) (Out, RunStatus, error) {
	var zero Out
	out, reply, first, status, err := attempt(ctx, cfg, s, req, log)
	if err != nil {
		return zero, status, err
	}
	if len(first) == 0 {
		return out, StatusOK, nil
	}
	log.problems = first
	if reply != "" {
		req.Messages = append(req.Messages, llm.Message{Role: llm.RoleAssistant, Content: reply})
	}
	req.Messages = append(req.Messages, llm.Message{Role: llm.RoleUser, Content: repairMessage(req.Schema.Name, first)})
	out, _, second, status, err := attempt(ctx, cfg, s, req, log)
	if err != nil {
		return zero, status, err
	}
	if len(second) > 0 {
		return zero, StatusBadOutput, &BadOutputError{Skill: s.Name(), Version: s.Version(), First: first, Repair: second}
	}
	return out, StatusRepaired, nil
}

// attempt sends req once, records the turn in log, then decodes,
// validates, post-processes and validates the reply again. It returns the
// reply for the repair turn and the problems found; err is set only for a
// provider or post-process failure, neither of which is repairable, and
// status then says which.
func attempt[In, Out any](ctx context.Context, cfg Config, s Skill[In, Out], req llm.Request, log *runLog) (out Out, reply string, problems []string, status RunStatus, err error) {
	resp, err := cfg.Provider.Generate(ctx, req)
	log.add(cfg.APIKey, req, resp, err)
	// The repair turn must quote what failed: the structured output when
	// there is any, otherwise the text.
	reply = string(resp.JSON)
	if reply == "" {
		reply = resp.Text
	}
	if err != nil {
		if errors.Is(err, llm.ErrBadOutput) {
			return out, reply, []string{err.Error()}, "", nil
		}
		return out, "", nil, StatusProviderError, err
	}
	if err := decode(resp.JSON, &out); err != nil {
		return out, reply, []string{err.Error()}, "", nil
	}
	if problems := s.Validate(out); len(problems) > 0 {
		return out, reply, problems, "", nil
	}
	out, err = s.PostProcess(out)
	if err != nil {
		return out, "", nil, StatusBadOutput, fmt.Errorf("harness: skill %s v%d: post-process: %w", s.Name(), s.Version(), err)
	}
	return out, reply, s.Validate(out), "", nil
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
