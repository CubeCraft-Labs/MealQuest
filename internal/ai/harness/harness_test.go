package harness

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/llm"
)

func reply(js string) llm.GenerateResult {
	return llm.GenerateResult{Response: llm.Response{Text: js, JSON: json.RawMessage(js)}}
}

// testConfig returns a Config whose provider is f, recording into a fresh
// migrated store.
func testConfig(t *testing.T, f *llm.Fake) Config {
	t.Helper()
	return Config{
		Settings:    llm.Settings{Provider: "anthropic", Model: "claude-test", Temperature: 0.7},
		NewProvider: func(llm.Settings, string) (llm.Provider, error) { return f, nil },
		Store:       emptyHousehold(t),
	}
}

func run(t *testing.T, f *llm.Fake) (echoOutput, error) {
	t.Helper()
	return Run(context.Background(), testConfig(t, f), echoSkill{}, echoInput{Word: "taco"})
}

func assertCalls(t *testing.T, f *llm.Fake, want int) []llm.Request {
	t.Helper()
	reqs := f.Requests()
	if len(reqs) != want {
		t.Fatalf("Generate called %d times, want %d", len(reqs), want)
	}
	if g, _, _ := f.Remaining(); g != 0 {
		t.Fatalf("%d scripted Generate results unused", g)
	}
	return reqs
}

func TestRunValidFirstTime(t *testing.T) {
	var f llm.Fake
	f.QueueGenerate(reply(`{"echo":" taco "}`))
	out, err := run(t, &f)
	if err != nil {
		t.Fatal(err)
	}
	if out.Echo != "taco" {
		t.Fatalf("Echo = %q, want post-processed %q", out.Echo, "taco")
	}
	req := assertCalls(t, &f, 1)[0]
	want, err := Render("", echoSkill{}, echoInput{Word: "taco"})
	if err != nil {
		t.Fatal(err)
	}
	if req.System != want.System || len(req.Messages) != 1 ||
		req.Messages[0] != (llm.Message{Role: llm.RoleUser, Content: "Repeat: taco"}) {
		t.Fatalf("request prompt = %+v", req)
	}
	if req.Temperature == nil || *req.Temperature != 0.7 {
		t.Fatalf("Temperature = %v, want 0.7", req.Temperature)
	}
	if req.Schema == nil || req.Schema.Name != "echo" || string(req.Schema.Schema) != string(echoSkill{}.Schema().Schema) {
		t.Fatalf("Schema = %+v", req.Schema)
	}
}

func TestRunInvalidThenValid(t *testing.T) {
	var f llm.Fake
	f.QueueGenerate(reply(`{"echo":"TACO"}`), reply(`{"echo":"taco"}`))
	out, err := run(t, &f)
	if err != nil {
		t.Fatal(err)
	}
	if out.Echo != "taco" {
		t.Fatalf("Echo = %q", out.Echo)
	}
	reqs := assertCalls(t, &f, 2)
	msgs := reqs[1].Messages
	if len(msgs) != 3 || msgs[0] != reqs[0].Messages[0] ||
		msgs[1] != (llm.Message{Role: llm.RoleAssistant, Content: `{"echo":"TACO"}`}) || msgs[2].Role != llm.RoleUser {
		t.Fatalf("repair messages = %+v", msgs)
	}
	if !strings.Contains(msgs[2].Content, "echo must be lower case") {
		t.Fatalf("repair message %q lacks the validator error", msgs[2].Content)
	}
	if reqs[1].System != reqs[0].System || reqs[1].Schema == nil || *reqs[1].Temperature != 0.7 {
		t.Fatalf("repair request lost system, schema or temperature: %+v", reqs[1])
	}
}

func TestRunPostProcessedOutputIsValidated(t *testing.T) {
	var f llm.Fake
	f.QueueGenerate(reply(`{"echo":"   "}`), reply(`{"echo":"taco"}`))
	out, err := run(t, &f)
	if err != nil {
		t.Fatal(err)
	}
	if out.Echo != "taco" {
		t.Fatalf("Echo = %q", out.Echo)
	}
	msgs := assertCalls(t, &f, 2)[1].Messages
	if len(msgs) != 3 || msgs[1] != (llm.Message{Role: llm.RoleAssistant, Content: `{"echo":"   "}`}) ||
		!strings.Contains(msgs[2].Content, "echo must not be empty") {
		t.Fatalf("repair messages = %+v", msgs)
	}
}

func TestRunWhitespaceTwiceIsBadOutput(t *testing.T) {
	var f llm.Fake
	f.QueueGenerate(reply(`{"echo":"   "}`), reply(`{"echo":" "}`))
	_, err := run(t, &f)
	var bad *BadOutputError
	if !errors.Is(err, llm.ErrBadOutput) || !errors.As(err, &bad) {
		t.Fatalf("err = %v, want *BadOutputError", err)
	}
	if !slices.Equal(bad.First, []string{"echo must not be empty"}) ||
		!slices.Equal(bad.Repair, []string{"echo must not be empty"}) {
		t.Fatalf("BadOutputError = %+v", bad)
	}
	assertCalls(t, &f, 2)
}

func TestRunRepairQuotesJSONOverText(t *testing.T) {
	var f llm.Fake
	f.QueueGenerate(
		llm.GenerateResult{Response: llm.Response{Text: "Here you go!", JSON: json.RawMessage(`{"echo":"TACO"}`)}},
		reply(`{"echo":"taco"}`),
	)
	if out, err := run(t, &f); err != nil || out.Echo != "taco" {
		t.Fatalf("Run = %+v, %v", out, err)
	}
	msgs := assertCalls(t, &f, 2)[1].Messages
	if len(msgs) != 3 || msgs[1] != (llm.Message{Role: llm.RoleAssistant, Content: `{"echo":"TACO"}`}) {
		t.Fatalf("repair messages = %+v, want the failed JSON as the assistant reply", msgs)
	}
}

func TestRunDecodeFailureIsRepaired(t *testing.T) {
	for name, first := range map[string]llm.GenerateResult{
		"unknown field":  reply(`{"echo":"taco","extra":1}`),
		"wrong type":     reply(`{"echo":5}`),
		"trailing data":  reply(`{"echo":"taco"}{}`),
		"no JSON":        {Response: llm.Response{Text: "taco"}},
		"adapter refuse": {Response: llm.Response{Text: "nope"}, Err: llm.ErrBadOutput},
	} {
		t.Run(name, func(t *testing.T) {
			var f llm.Fake
			f.QueueGenerate(first, reply(`{"echo":"taco"}`))
			if out, err := run(t, &f); err != nil || out.Echo != "taco" {
				t.Fatalf("Run = %+v, %v", out, err)
			}
			assertCalls(t, &f, 2)
		})
	}
}

func TestRunInvalidTwice(t *testing.T) {
	var f llm.Fake
	f.QueueGenerate(reply(`{"echo":"TACO"}`), reply(`{"echo":""}`))
	_, err := run(t, &f)
	if !errors.Is(err, llm.ErrBadOutput) {
		t.Fatalf("err = %v, want ErrBadOutput", err)
	}
	var bad *BadOutputError
	if !errors.As(err, &bad) {
		t.Fatalf("err = %T, want *BadOutputError", err)
	}
	if bad.Skill != "echo" || bad.Version != 1 ||
		!slices.Equal(bad.First, []string{"echo must be lower case"}) ||
		!slices.Equal(bad.Repair, []string{"echo must not be empty"}) {
		t.Fatalf("BadOutputError = %+v", bad)
	}
	for _, want := range []string{"echo must be lower case", "echo must not be empty"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q lacks %q", err, want)
		}
	}
	assertCalls(t, &f, 2)
}

func TestRunProviderErrorPassesThrough(t *testing.T) {
	for name, perr := range map[string]error{
		"auth":         llm.ErrAuth,
		"rate limited": &llm.RateLimitedError{},
		"timeout":      llm.ErrTimeout,
		"provider":     llm.NewProviderError(500, []byte("boom"), ""),
	} {
		t.Run(name, func(t *testing.T) {
			var f llm.Fake
			f.QueueGenerate(llm.GenerateResult{Err: perr})
			_, err := run(t, &f)
			if err != perr {
				t.Fatalf("err = %v, want the provider error unchanged", err)
			}
			assertCalls(t, &f, 1)
		})
	}
}

func TestRunProviderErrorOnRepairPassesThrough(t *testing.T) {
	var f llm.Fake
	f.QueueGenerate(reply(`{"echo":"TACO"}`), llm.GenerateResult{Err: llm.ErrTimeout})
	if _, err := run(t, &f); err != llm.ErrTimeout {
		t.Fatalf("err = %v, want ErrTimeout unchanged", err)
	}
	assertCalls(t, &f, 2)
}

func TestRunRenderError(t *testing.T) {
	var f llm.Fake
	cfg := testConfig(t, &f)
	_, err := Run(context.Background(), cfg, echoSkill{}, echoInput{})
	if err == nil || !strings.Contains(err.Error(), "render") {
		t.Fatalf("err = %v, want a render error", err)
	}
	assertCalls(t, &f, 0)
	rows := runRows(t, cfg.Store)
	if len(rows) != 1 {
		t.Fatalf("render error wrote %d ai_runs rows, want 1", len(rows))
	}
	r := rows[0]
	if r.Status != string(StatusBadOutput) || r.Error.String != err.Error() ||
		r.Request != "[]" || r.Response.String != "[]" || r.TokensIn.Valid || r.TokensOut.Valid {
		t.Fatalf("row = %+v, want bad_output with the render error and no turns", r)
	}
}

func TestRunProviderBuildError(t *testing.T) {
	var f llm.Fake
	cfg := testConfig(t, &f)
	cfg.NewProvider = func(llm.Settings, string) (llm.Provider, error) { return nil, llm.ErrMissingKey }
	_, err := Run(context.Background(), cfg, echoSkill{}, echoInput{Word: "taco"})
	if !errors.Is(err, llm.ErrMissingKey) {
		t.Fatalf("err = %v, want ErrMissingKey", err)
	}
	rows := runRows(t, cfg.Store)
	if len(rows) != 1 || rows[0].Status != string(StatusProviderError) ||
		rows[0].Error.String != err.Error() || rows[0].Request != "[]" {
		t.Fatalf("rows = %+v, want one provider_error row with no turns", rows)
	}
}

func TestRunConfigError(t *testing.T) {
	var f llm.Fake
	full := testConfig(t, &f)
	for name, edit := range map[string]func(*Config){
		"provider constructor": func(c *Config) { c.NewProvider = nil },
		"provider name":        func(c *Config) { c.Settings.Provider = "" },
		"model":                func(c *Config) { c.Settings.Model = "" },
		"store":                func(c *Config) { c.Store = nil },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := full
			edit(&cfg)
			_, err := Run(context.Background(), cfg, echoSkill{}, echoInput{Word: "taco"})
			if err == nil || !strings.Contains(err.Error(), "config has no "+name) {
				t.Fatalf("err = %v, want missing %s", err, name)
			}
		})
	}
	assertCalls(t, &f, 0)
	if rows := runRows(t, full.Store); len(rows) != 0 {
		t.Fatalf("config errors wrote %d ai_runs rows, want none", len(rows))
	}
}
