// Package harness runs skills: versioned, typed prompts whose structured
// output Go validates. A run renders the skill's layered prompt from its
// embedded template (see Render and package prompts), asks the provider
// for output matching the skill's schema, decodes and validates the reply,
// and on failure sends exactly one repair turn quoting the problems. If the
// repair fails too the run fails with llm.ErrBadOutput; there is no silent
// fallback (D-007). Every run is recorded in ai_runs (see Run and Purge).
// A Guard shares in-flight runs of one skill and scope, bounds concurrent
// LLM calls, and each run ends at its skill's timeout.
package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

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
	// Timeout bounds a whole run, waiting for LLM call slots, the repair
	// turn and writing a successful run's ai_runs row included (see Run). It is DefaultTimeout unless the skill needs
	// another, and must be positive.
	Timeout() time.Duration
}

// NewProvider builds the provider a run sends its requests to from the
// household's settings and decrypted API key. In production it is
// presets.FromSettings.
type NewProvider func(s llm.Settings, key string) (llm.Provider, error)

// Config is what a run needs from the household's AI settings.
type Config struct {
	// Settings is the household's AI configuration. Settings.Provider and
	// Settings.Model are recorded in ai_runs and may not be empty;
	// Settings.Temperature is sent with every request.
	Settings llm.Settings
	// APIKey is the decrypted API key, or "" for a provider that takes
	// none. It reaches the provider only through NewProvider, and every
	// occurrence of it is removed from the ai_runs row.
	APIKey string
	// NewProvider builds the run's provider from Settings and APIKey.
	NewProvider NewProvider
	// Persona is ai_settings.system_prompt, the first layer of every
	// system prompt; empty means none.
	Persona string
	// Store receives the run's ai_runs row. It must not be nil.
	Store *store.Store
	// Guard is the process's Guard, shared by every run. It must not be
	// nil.
	Guard *Guard
}

// validate reports a Config missing what every run needs.
func (c Config) validate() error {
	switch {
	case c.NewProvider == nil:
		return errors.New("config has no provider constructor")
	case c.Settings.Provider == "":
		return errors.New("config has no provider name")
	case c.Settings.Model == "":
		return errors.New("config has no model")
	case c.Store == nil:
		return errors.New("config has no store")
	case c.Guard == nil:
		return errors.New("config has no guard")
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
// scope names what the run is for, e.g. a week, and must not be empty.
// While a run of s with scope is in flight on cfg.Guard, another Run of s
// with the same scope does not start a run of its own: it waits for that
// run and returns its output or error, whatever its own in and cfg. So a
// scope must identify the input. Runs with different scopes, or of
// different skills, run independently. Every Generate call holds one of
// cfg.Guard's slots while it runs.
//
// A run that has not ended within s.Timeout() fails with an error matching
// llm.ErrTimeout, wrapping the error its work returned if any, and its row
// records StatusProviderError whatever that work's outcome. When ctx is done first, Run returns ctx.Err(); the run
// goes on for the callers still waiting for it, and is cancelled when none
// is left.
//
// Every run with a valid Config writes one ai_runs row when it ends,
// covering both turns, with its RunStatus and the request and response
// bodies redacted (see redact). A render failure is recorded as
// StatusBadOutput and a provider that cannot be built as
// StatusProviderError, both with no turns. A Config error fails the run
// before anything else, and writes no row since the row cannot be
// written without one. When the row cannot be written the run fails with
// that error too, even if its output was usable.
//
// Writing the row is bounded too. A run whose work ended in time must
// write its row before s.Timeout() as well; if the store's writer is not
// free by then, the run fails with llm.ErrTimeout and its row records the
// timeout instead. The row of a run that failed is written within a
// further fixed bound (runSaveTimeout); if it cannot be, the run fails with
// the write error alongside its own, and no row is stored. So Run returns
// at most that bound after s.Timeout(), and a stored row's status always
// matches what the caller received.
func Run[In, Out any](ctx context.Context, cfg Config, s Skill[In, Out], scope string, in In) (Out, error) {
	var zero Out
	if err := cfg.validate(); err != nil {
		return zero, fmt.Errorf("harness: skill %s v%d: %w", s.Name(), s.Version(), err)
	}
	if scope == "" {
		return zero, fmt.Errorf("harness: skill %s v%d: no scope", s.Name(), s.Version())
	}
	timeout := s.Timeout()
	if timeout <= 0 {
		return zero, fmt.Errorf("harness: skill %s v%d: timeout %v is not positive", s.Name(), s.Version(), timeout)
	}
	val, err := cfg.Guard.do(ctx, flightKey{skill: s.Name(), scope: scope}, func(ctx context.Context) (any, error) {
		return runOnce(ctx, cfg, s, timeout, in)
	})
	if err != nil {
		return zero, err
	}
	out, ok := val.(Out)
	if !ok {
		return zero, fmt.Errorf("harness: skill %s v%d: scope %q is shared with a run whose output is %T", s.Name(), s.Version(), scope, val)
	}
	return out, nil
}

// runOnce is one execution of s on in, ended at timeout, with its ai_runs row.
func runOnce[In, Out any](ctx context.Context, cfg Config, s Skill[In, Out], timeout time.Duration, in In) (Out, error) {
	var zero Out
	expired := fmt.Errorf("harness: skill %s v%d: %w: no result within %v", s.Name(), s.Version(), llm.ErrTimeout, timeout)
	deadline := time.Now().Add(timeout)
	ctx, cancel := context.WithDeadlineCause(ctx, deadline, expired)
	defer cancel()
	log := &runLog{id: store.NewID(), start: store.Now()}
	out, status, err := execute(ctx, cfg, s, in, log)
	// Work that only ends after the deadline, such as a slow PostProcess,
	// is a timeout whatever it returned, so its row says provider_error
	// like the caller's error, which keeps any underlying error. The clock
	// is checked as well as ctx because ctx's timer may not have fired yet.
	if context.Cause(ctx) == expired || !time.Now().Before(deadline) {
		if err == nil {
			err = expired
		} else {
			err = fmt.Errorf("%w: %w", expired, err)
		}
		status = StatusProviderError
	}
	// The saves ignore the callers' cancellation, so a cancelled run is
	// recorded too, but each is bounded. Work that ended in time must also
	// be recorded in time: its row is written within the deadline or not at
	// all, so a stored ok or repaired row always means the caller got the
	// output.
	if err == nil {
		sctx, cancel := context.WithDeadline(context.WithoutCancel(ctx), deadline)
		saveErr := log.save(sctx, cfg, s.Name(), s.Version(), status, nil)
		late := sctx.Err() != nil
		cancel()
		if saveErr == nil {
			return out, nil
		}
		if !late {
			return zero, fmt.Errorf("harness: skill %s v%d: record ai_runs row: %w", s.Name(), s.Version(), saveErr)
		}
		// The store's writer was not free before the deadline, so the run
		// has not ended in time.
		err, status = expired, StatusProviderError
	}
	// A run that failed is recorded within runSaveTimeout; when it cannot
	// be, the run fails with both errors.
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), runSaveTimeout)
	defer cancel()
	if saveErr := log.save(sctx, cfg, s.Name(), s.Version(), status, err); saveErr != nil {
		return zero, errors.Join(err, fmt.Errorf("harness: skill %s v%d: record ai_runs row: %w", s.Name(), s.Version(), saveErr))
	}
	return zero, err
}

// execute renders the prompt, builds the provider and sends the turns,
// recording them in log. It returns the run's status alongside its result.
func execute[In, Out any](ctx context.Context, cfg Config, s Skill[In, Out], in In, log *runLog) (Out, RunStatus, error) {
	var zero Out
	prompt, err := Render(cfg.Persona, s, in)
	if err != nil {
		return zero, StatusBadOutput, fmt.Errorf("harness: skill %s v%d: render: %w", s.Name(), s.Version(), err)
	}
	provider, err := cfg.NewProvider(cfg.Settings, cfg.APIKey)
	if err != nil {
		return zero, StatusProviderError, fmt.Errorf("harness: skill %s v%d: provider: %w", s.Name(), s.Version(), err)
	}
	provider = limited{Provider: provider, slots: cfg.Guard.slots}
	schema := s.Schema()
	temperature := cfg.Settings.Temperature
	req := llm.Request{
		System:      prompt.System,
		Messages:    []llm.Message{{Role: llm.RoleUser, Content: prompt.User}},
		Temperature: &temperature,
		Schema:      &schema,
	}
	return turns(ctx, cfg.APIKey, provider, s, req, log)
}

// turns sends req and, when its reply is unusable, the repair turn,
// recording both in log. It returns the run's status alongside its result.
func turns[In, Out any](ctx context.Context, apiKey string, provider llm.Provider, s Skill[In, Out], req llm.Request, log *runLog) (Out, RunStatus, error) {
	var zero Out
	out, reply, first, status, err := attempt(ctx, apiKey, provider, s, req, log)
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
	out, _, second, status, err := attempt(ctx, apiKey, provider, s, req, log)
	if err != nil {
		return zero, status, err
	}
	if len(second) > 0 {
		return zero, StatusBadOutput, &BadOutputError{Skill: s.Name(), Version: s.Version(), First: first, Repair: second}
	}
	return out, StatusRepaired, nil
}

// attempt sends req to provider once, records the turn in log, then decodes,
// validates, post-processes and validates the reply again. It returns the
// reply for the repair turn and the problems found; err is set only for a
// provider or post-process failure, neither of which is repairable, and
// status then says which.
func attempt[In, Out any](ctx context.Context, apiKey string, provider llm.Provider, s Skill[In, Out], req llm.Request, log *runLog) (out Out, reply string, problems []string, status RunStatus, err error) {
	resp, err := provider.Generate(ctx, req)
	log.add(apiKey, req, resp, err)
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
