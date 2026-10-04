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

func run(t *testing.T, f *llm.Fake) (echoOutput, error) {
	t.Helper()
	return Run(context.Background(), Config{Provider: f, Temperature: 0.7}, echoSkill{}, echoInput{Word: "taco"})
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
	if req.System != "You repeat words." || len(req.Messages) != 1 ||
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
	_, err := Run(context.Background(), Config{Provider: &f}, echoSkill{}, echoInput{})
	if err == nil || errors.Is(err, llm.ErrBadOutput) {
		t.Fatalf("err = %v, want a render error", err)
	}
	assertCalls(t, &f, 0)
}
