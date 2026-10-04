package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/llm"
)

const testKey = "sk-test-9f8e7d6c5b4a39281706f5e4d3c2b1a0"

// recorded is one request the test server received.
type recorded struct {
	method string
	path   string
	header http.Header
	body   []byte
}

// reply is the response the test server replays.
type reply struct {
	status  int
	header  map[string]string
	fixture string // testdata file; {{KEY}} is replaced with testKey
	delay   time.Duration
}

// vendorServer replays one recorded payload and records the request.
type vendorServer struct {
	*httptest.Server
	mu  sync.Mutex
	got []recorded
}

func newVendorServer(t *testing.T, r reply) *vendorServer {
	t.Helper()
	var payload []byte
	if r.fixture != "" {
		raw, err := os.ReadFile(filepath.Join("testdata", r.fixture))
		if err != nil {
			t.Fatal(err)
		}
		payload = bytes.ReplaceAll(raw, []byte("{{KEY}}"), []byte(testKey))
	}
	vs := &vendorServer{}
	vs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		vs.mu.Lock()
		vs.got = append(vs.got, recorded{req.Method, req.URL.Path, req.Header.Clone(), body})
		vs.mu.Unlock()
		if r.delay > 0 {
			select {
			case <-time.After(r.delay):
			case <-req.Context().Done():
				return
			}
		}
		for k, v := range r.header {
			w.Header().Set(k, v)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(r.status)
		w.Write(payload)
	}))
	t.Cleanup(vs.Close)
	return vs
}

func (vs *vendorServer) only(t *testing.T) recorded {
	t.Helper()
	vs.mu.Lock()
	defer vs.mu.Unlock()
	if len(vs.got) != 1 {
		t.Fatalf("server got %d requests, want 1", len(vs.got))
	}
	return vs.got[0]
}

func newProvider(t *testing.T, base string, mode StructuredOutput, headers map[string]string) *Provider {
	t.Helper()
	p, err := New(Config{BaseURL: base, APIKey: testKey, Model: "test-model", Headers: headers, StructuredOutput: mode})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

var dinnersSchema = json.RawMessage(`{"type":"object","properties":{"dinners":{"type":"array"}},"required":["dinners"],"additionalProperties":false}`)

func schemaRequest() llm.Request {
	temp := 0.0
	return llm.Request{
		System:      "You plan dinners for Our kitchen.",
		Messages:    []llm.Message{{Role: llm.RoleUser, Content: "Suggest a Side quest."}},
		Temperature: &temp,
		MaxTokens:   400,
		Schema:      &llm.JSONSchema{Name: "side_quests", Schema: dinnersSchema},
	}
}

// sentBody decodes the chat request body into a generic map.
func sentBody(t *testing.T, r recorded) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(r.body, &m); err != nil {
		t.Fatalf("request body %q: %v", r.body, err)
	}
	return m
}

func assertJSON(t *testing.T, got json.RawMessage, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("Response.JSON %q: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatal(err)
	}
	gb, _ := json.Marshal(g)
	wb, _ := json.Marshal(w)
	if !bytes.Equal(gb, wb) {
		t.Fatalf("Response.JSON = %s, want %s", gb, wb)
	}
}

func TestGenerateJSONSchemaOpenAI(t *testing.T) {
	vs := newVendorServer(t, reply{status: 200, fixture: "openai_chat_json_schema.json"})
	p := newProvider(t, vs.URL+"/v1/", JSONSchema, nil)

	resp, err := p.Generate(context.Background(), schemaRequest())
	if err != nil {
		t.Fatal(err)
	}
	assertJSON(t, resp.JSON, `{"dinners":[{"name":"Sheet-pan gnocchi","why":"Like your Regular tray bake, but quicker."}]}`)
	if resp.Text != string(resp.JSON) {
		t.Errorf("Text = %q, want the raw reply", resp.Text)
	}
	if resp.Usage != (llm.Usage{InputTokens: 1117, OutputTokens: 46}) || resp.Model != "gpt-4o-2024-08-06" || resp.FinishReason != "stop" {
		t.Errorf("Usage, Model, FinishReason = %+v, %q, %q", resp.Usage, resp.Model, resp.FinishReason)
	}

	got := vs.only(t)
	if got.method != http.MethodPost || got.path != "/v1/chat/completions" {
		t.Errorf("request = %s %s", got.method, got.path)
	}
	if h := got.header.Get("Authorization"); h != "Bearer "+testKey {
		t.Errorf("Authorization = %q", h)
	}
	if h := got.header.Get("Content-Type"); h != "application/json" {
		t.Errorf("Content-Type = %q", h)
	}
	body := sentBody(t, got)
	if body["model"] != "test-model" || body["temperature"] != 0.0 || body["max_tokens"] != 400.0 {
		t.Errorf("model, temperature, max_tokens = %v, %v, %v", body["model"], body["temperature"], body["max_tokens"])
	}
	msgs := body["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("messages = %v", msgs)
	}
	system := msgs[0].(map[string]any)
	if system["role"] != "system" || system["content"] != "You plan dinners for Our kitchen." {
		t.Errorf("system message = %v (the schema must not be in the prompt in json_schema mode)", system)
	}
	if user := msgs[1].(map[string]any); user["role"] != "user" || user["content"] != "Suggest a Side quest." {
		t.Errorf("user message = %v", user)
	}
	rf := body["response_format"].(map[string]any)
	js := rf["json_schema"].(map[string]any)
	if rf["type"] != "json_schema" || js["name"] != "side_quests" || js["strict"] != true {
		t.Errorf("response_format = %v", rf)
	}
	schema, _ := json.Marshal(js["schema"])
	var want any
	json.Unmarshal(dinnersSchema, &want)
	wantB, _ := json.Marshal(want)
	if !bytes.Equal(schema, wantB) {
		t.Errorf("response_format schema = %s", schema)
	}
}

func TestGenerateJSONObjectOpenRouter(t *testing.T) {
	vs := newVendorServer(t, reply{status: 200, fixture: "openrouter_chat_json_object.json"})
	p := newProvider(t, vs.URL+"/api/v1", JSONObject, map[string]string{"http-referer": "https://mealquest.example", "X-Title": "MealQuest"})

	req := schemaRequest()
	req.Temperature = nil
	req.MaxTokens = 0
	resp, err := p.Generate(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	assertJSON(t, resp.JSON, `{"dinners":[{"name":"Chicken tinga tacos","why":"Close to your taco-night Regular."}]}`)
	if resp.Usage != (llm.Usage{InputTokens: 842, OutputTokens: 31}) || resp.Model != "openai/gpt-4o-mini" || resp.FinishReason != "stop" {
		t.Errorf("Usage, Model, FinishReason = %+v, %q, %q", resp.Usage, resp.Model, resp.FinishReason)
	}

	got := vs.only(t)
	if got.path != "/api/v1/chat/completions" {
		t.Errorf("path = %s", got.path)
	}
	if got.header.Get("HTTP-Referer") != "https://mealquest.example" || got.header.Get("X-Title") != "MealQuest" {
		t.Errorf("extra headers = %v", got.header)
	}
	body := sentBody(t, got)
	if _, ok := body["temperature"]; ok {
		t.Error("temperature sent although Request.Temperature is nil")
	}
	if _, ok := body["max_tokens"]; ok {
		t.Error("max_tokens sent although Request.MaxTokens is 0")
	}
	if rf := body["response_format"].(map[string]any); rf["type"] != "json_object" || rf["json_schema"] != nil {
		t.Errorf("response_format = %v", rf)
	}
	system := body["messages"].([]any)[0].(map[string]any)["content"].(string)
	if !strings.HasPrefix(system, "You plan dinners for Our kitchen.\n\n"+schemaInstruction) || !strings.Contains(system, `"required":["dinners"]`) {
		t.Errorf("system prompt lacks the schema instruction: %q", system)
	}
}

func TestGeneratePromptOnlyOllama(t *testing.T) {
	vs := newVendorServer(t, reply{status: 200, fixture: "ollama_chat_prompt_only.json"})
	p, err := New(Config{BaseURL: vs.URL + "/v1", Model: "llama3.2", StructuredOutput: PromptOnly})
	if err != nil {
		t.Fatal(err)
	}
	req := schemaRequest()
	req.System = ""
	resp, err := p.Generate(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	// The reply has a non-JSON "{idea}" first and a second object after the
	// fenced one; only the first complete object is taken.
	assertJSON(t, resp.JSON, `{"dinners":[{"name":"Crock pot chili","why":"A Quick dinner like your {Regular} stew."}]}`)
	if !strings.HasPrefix(resp.Text, "Sure! Here is") {
		t.Errorf("Text = %q, want the whole reply", resp.Text)
	}
	if resp.Usage != (llm.Usage{InputTokens: 512, OutputTokens: 58}) || resp.Model != "llama3.2" || resp.FinishReason != "stop" {
		t.Errorf("Usage, Model, FinishReason = %+v, %q, %q", resp.Usage, resp.Model, resp.FinishReason)
	}

	got := vs.only(t)
	if _, ok := got.header["Authorization"]; ok {
		t.Error("Authorization sent without an API key")
	}
	body := sentBody(t, got)
	if _, ok := body["response_format"]; ok {
		t.Error("response_format sent in prompt_only mode")
	}
	system := body["messages"].([]any)[0].(map[string]any)
	if system["role"] != "system" || !strings.HasPrefix(system["content"].(string), schemaInstruction) {
		t.Errorf("system message = %v", system)
	}
}

func TestGenerateWithoutSchemaReturnsText(t *testing.T) {
	vs := newVendorServer(t, reply{status: 200, fixture: "ollama_chat_text.json"})
	p := newProvider(t, vs.URL+"/v1", PromptOnly, nil)
	resp, err := p.Generate(context.Background(), llm.Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "Dinner?"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "Try a lemon orzo bake tonight." || resp.JSON != nil {
		t.Errorf("Text, JSON = %q, %q", resp.Text, resp.JSON)
	}
	body := sentBody(t, vs.only(t))
	if msgs := body["messages"].([]any); len(msgs) != 1 {
		t.Errorf("messages = %v, want only the user message", msgs)
	}
	if _, ok := body["response_format"]; ok {
		t.Error("response_format sent without a schema")
	}
}

func TestGenerateBadOutput(t *testing.T) {
	cases := []struct {
		name    string
		fixture string
		mode    StructuredOutput
	}{
		{"refusal", "openai_chat_refusal.json", JSONSchema},
		{"truncated prompt_only", "ollama_chat_truncated.json", PromptOnly},
		{"truncated json_object", "ollama_chat_truncated.json", JSONObject},
		{"prose in json_object", "ollama_chat_text.json", JSONObject},
		{"prose in prompt_only", "ollama_chat_text.json", PromptOnly},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			vs := newVendorServer(t, reply{status: 200, fixture: c.fixture})
			p := newProvider(t, vs.URL, c.mode, nil)
			_, err := p.Generate(context.Background(), schemaRequest())
			if !errors.Is(err, llm.ErrBadOutput) {
				t.Fatalf("err = %v, want ErrBadOutput", err)
			}
		})
	}
}

func TestListModels(t *testing.T) {
	cases := []struct {
		fixture string
		base    string
		want    []llm.Model
	}{
		{"openai_models.json", "/v1", []llm.Model{
			{ID: "gpt-4o-2024-08-06", DisplayName: "gpt-4o-2024-08-06"},
			{ID: "gpt-4o-mini", DisplayName: "gpt-4o-mini"},
		}},
		{"openrouter_models.json", "/api/v1", []llm.Model{
			{ID: "openai/gpt-4o-mini", DisplayName: "OpenAI: GPT-4o-mini"},
			{ID: "anthropic/claude-sonnet-5", DisplayName: "Anthropic: Claude Sonnet 5"},
		}},
		{"ollama_models.json", "/v1", []llm.Model{
			{ID: "llama3.2:latest", DisplayName: "llama3.2:latest"},
			{ID: "qwen2.5:7b", DisplayName: "qwen2.5:7b"},
		}},
	}
	for _, c := range cases {
		t.Run(c.fixture, func(t *testing.T) {
			vs := newVendorServer(t, reply{status: 200, fixture: c.fixture})
			p := newProvider(t, vs.URL+c.base, JSONSchema, map[string]string{"X-Title": "MealQuest"})
			models, err := p.ListModels(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(models) != len(c.want) {
				t.Fatalf("models = %+v", models)
			}
			for i := range c.want {
				if models[i] != c.want[i] {
					t.Errorf("model %d = %+v, want %+v", i, models[i], c.want[i])
				}
			}
			got := vs.only(t)
			if got.method != http.MethodGet || got.path != c.base+"/models" {
				t.Errorf("request = %s %s", got.method, got.path)
			}
			if got.header.Get("Authorization") != "Bearer "+testKey || got.header.Get("X-Title") != "MealQuest" {
				t.Errorf("headers = %v", got.header)
			}
		})
	}
}

func TestVerify(t *testing.T) {
	ok := newVendorServer(t, reply{status: 200, fixture: "openai_models.json"})
	if err := newProvider(t, ok.URL, JSONSchema, nil).Verify(context.Background()); err != nil {
		t.Fatalf("Verify() = %v", err)
	}
	if got := ok.only(t); got.path != "/models" {
		t.Errorf("Verify called %s", got.path)
	}
	bad := newVendorServer(t, reply{status: 401, fixture: "openai_error_invalid_key.json"})
	if err := newProvider(t, bad.URL, JSONSchema, nil).Verify(context.Background()); !errors.Is(err, llm.ErrAuth) {
		t.Fatalf("Verify() = %v, want ErrAuth", err)
	}
}

// assertNoSecret fails if err's text holds the key or Authorization value.
func assertNoSecret(t *testing.T, err error) {
	t.Helper()
	for _, secret := range []string{testKey, "Bearer " + testKey, testKey[:12]} {
		if strings.Contains(strings.ToLower(err.Error()), strings.ToLower(secret)) {
			t.Fatalf("error leaks %q: %v", secret, err)
		}
	}
}

func TestErrorMapping(t *testing.T) {
	cases := []struct {
		name  string
		reply reply
		want  error
		check func(t *testing.T, err error)
	}{
		{name: "401 OpenAI", reply: reply{status: 401, fixture: "openai_error_invalid_key.json"}, want: llm.ErrAuth},
		{name: "403", reply: reply{status: 403, fixture: "openai_error_invalid_key.json"}, want: llm.ErrAuth},
		{name: "429 seconds OpenRouter", reply: reply{status: 429, header: map[string]string{"Retry-After": "17"}, fixture: "openrouter_error_rate_limited.json"}, want: llm.ErrRateLimited,
			check: func(t *testing.T, err error) {
				var rl *llm.RateLimitedError
				if !errors.As(err, &rl) || rl.RetryAfter != 17*time.Second {
					t.Fatalf("RetryAfter from %v", err)
				}
			}},
		{name: "429 HTTP-date", reply: reply{status: 429, header: map[string]string{"Retry-After": time.Now().Add(90 * time.Second).UTC().Format(http.TimeFormat)}, fixture: "openrouter_error_rate_limited.json"}, want: llm.ErrRateLimited,
			check: func(t *testing.T, err error) {
				var rl *llm.RateLimitedError
				if !errors.As(err, &rl) || rl.RetryAfter < 85*time.Second || rl.RetryAfter > 91*time.Second {
					t.Fatalf("RetryAfter from %v", err)
				}
			}},
		{name: "429 no Retry-After", reply: reply{status: 429, fixture: "openrouter_error_rate_limited.json"}, want: llm.ErrRateLimited,
			check: func(t *testing.T, err error) {
				var rl *llm.RateLimitedError
				if !errors.As(err, &rl) || rl.RetryAfter != 0 {
					t.Fatalf("RetryAfter from %v", err)
				}
			}},
		{name: "408", reply: reply{status: 408}, want: llm.ErrTimeout},
		{name: "404 Ollama", reply: reply{status: 404, fixture: "ollama_error_not_found.json"}, want: llm.ErrProvider,
			check: func(t *testing.T, err error) {
				var pe *llm.ProviderError
				if !errors.As(err, &pe) || pe.Status() != 404 || !strings.Contains(pe.Body(), `model \"llama9\" not found`) {
					t.Fatalf("ProviderError from %v", err)
				}
			}},
		{name: "500 echoing the key", reply: reply{status: 500, fixture: "openai_error_server.json"}, want: llm.ErrProvider,
			check: func(t *testing.T, err error) {
				var pe *llm.ProviderError
				if !errors.As(err, &pe) || pe.Status() != 500 || !strings.Contains(pe.Body(), "Authorization: Bearer [REDACTED]") {
					t.Fatalf("ProviderError from %v", err)
				}
			}},
		{name: "400 echoing the key", reply: reply{status: 400, fixture: "openai_error_invalid_key.json"}, want: llm.ErrProvider},
		{name: "200 OpenRouter upstream error", reply: reply{status: 200, fixture: "openrouter_chat_upstream_error.json"}, want: llm.ErrProvider,
			check: func(t *testing.T, err error) {
				var pe *llm.ProviderError
				if !errors.As(err, &pe) || !strings.Contains(pe.Body(), "Upstream error from provider") {
					t.Fatalf("ProviderError from %v", err)
				}
			}},
	}
	all := []error{llm.ErrAuth, llm.ErrRateLimited, llm.ErrTimeout, llm.ErrBadOutput, llm.ErrProvider}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			vs := newVendorServer(t, c.reply)
			p := newProvider(t, vs.URL+"/v1", JSONSchema, nil)
			_, genErr := p.Generate(context.Background(), schemaRequest())
			errs := []error{genErr}
			if c.reply.status != 200 {
				_, listErr := p.ListModels(context.Background())
				errs = append(errs, listErr)
			}
			for _, err := range errs {
				for _, sentinel := range all {
					if errors.Is(err, sentinel) != (sentinel == c.want) {
						t.Fatalf("errors.Is(%v, %v) = %v", err, sentinel, !(sentinel == c.want))
					}
				}
				assertNoSecret(t, err)
				if c.check != nil {
					c.check(t, err)
				}
			}
		})
	}
}

func TestTimeouts(t *testing.T) {
	vs := newVendorServer(t, reply{status: 200, fixture: "openai_chat_json_schema.json", delay: 5 * time.Second})
	p := newProvider(t, vs.URL, JSONSchema, nil)

	req := schemaRequest()
	req.Timeout = 50 * time.Millisecond
	start := time.Now()
	_, err := p.Generate(context.Background(), req)
	if !errors.Is(err, llm.ErrTimeout) {
		t.Fatalf("Request.Timeout: err = %v, want ErrTimeout", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("Request.Timeout not honoured: took %v", elapsed)
	}
	assertNoSecret(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := p.ListModels(ctx); !errors.Is(err, llm.ErrTimeout) {
		t.Fatalf("context deadline: err = %v, want ErrTimeout", err)
	}

	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	if _, err := p.Generate(ctx, schemaRequest()); !errors.Is(err, context.Canceled) || errors.Is(err, llm.ErrTimeout) {
		t.Fatalf("cancelled context: err = %v, want context.Canceled only", err)
	}
}

func TestTransportErrorHasNoSecret(t *testing.T) {
	vs := newVendorServer(t, reply{status: 200})
	base := vs.URL
	vs.Close()
	_, err := newProvider(t, base, JSONSchema, nil).Generate(context.Background(), schemaRequest())
	if err == nil {
		t.Fatal("Generate against a closed server succeeded")
	}
	assertNoSecret(t, err)
}

// TestTransportErrorWithKeyInBaseURLPath fails the exchange against a base
// URL whose path holds the key, which New accepts; net/http names the full
// request URL in its transport errors.
func TestTransportErrorWithKeyInBaseURLPath(t *testing.T) {
	refused := newVendorServer(t, reply{status: 200})
	refusedBase := refused.URL
	refused.Close()

	dropped := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		conn.Close()
	}))
	t.Cleanup(dropped.Close)

	for name, base := range map[string]string{
		"connection refused": refusedBase + "/v1/" + testKey,
		"connection dropped": dropped.URL + "/v1/" + testKey,
	} {
		p := newProvider(t, base, JSONSchema, nil)
		_, genErr := p.Generate(context.Background(), schemaRequest())
		_, listErr := p.ListModels(context.Background())
		verifyErr := p.Verify(context.Background())
		for _, err := range []error{genErr, listErr, verifyErr} {
			if err == nil {
				t.Fatalf("%s: nil error", name)
			}
			assertNoSecret(t, err)
			var ue *url.Error
			if errors.As(err, &ue) {
				t.Fatalf("%s: error chain exposes the request URL: %v", name, ue)
			}
			if errors.Is(err, llm.ErrTimeout) || errors.Is(err, llm.ErrProvider) {
				t.Fatalf("%s: err = %v, want an untyped transport failure", name, err)
			}
		}
	}
}

// TestKeyedErrorsNeverLeakKey drives every error path of the keyed OpenAI
// and OpenRouter shapes with vendor bodies that echo the key.
func TestKeyedErrorsNeverLeakKey(t *testing.T) {
	shapes := map[string]struct {
		path    string
		headers map[string]string
	}{
		"OpenAI":     {path: "/v1"},
		"OpenRouter": {path: "/api/v1", headers: map[string]string{"HTTP-Referer": "https://mealquest.example", "X-Title": "MealQuest"}},
	}
	fixtures := []string{"openai_error_invalid_key.json", "openai_error_server.json", "openrouter_error_rate_limited.json"}
	statuses := []int{400, 401, 403, 404, 408, 429, 500, 502, 503}
	for name, shape := range shapes {
		for _, fixture := range fixtures {
			for _, status := range statuses {
				vs := newVendorServer(t, reply{status: status, fixture: fixture})
				p := newProvider(t, vs.URL+shape.path, JSONSchema, shape.headers)
				_, genErr := p.Generate(context.Background(), schemaRequest())
				_, listErr := p.ListModels(context.Background())
				verifyErr := p.Verify(context.Background())
				for _, err := range []error{genErr, listErr, verifyErr} {
					if err == nil {
						t.Fatalf("%s %s HTTP %d: nil error", name, fixture, status)
					}
					assertNoSecret(t, err)
					var pe *llm.ProviderError
					if errors.As(err, &pe) {
						assertNoSecret(t, errors.New(pe.Body()))
					}
				}
			}
		}
	}
}

func TestKeylessOllamaErrorKeepsExcerpt(t *testing.T) {
	vs := newVendorServer(t, reply{status: 404, fixture: "ollama_error_not_found.json"})
	p, err := New(Config{BaseURL: vs.URL + "/v1", Model: "llama9", StructuredOutput: PromptOnly})
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Generate(context.Background(), schemaRequest())
	var pe *llm.ProviderError
	if !errors.As(err, &pe) || pe.Status() != 404 {
		t.Fatalf("err = %v, want a 404 ProviderError", err)
	}
	if !strings.Contains(pe.Body(), `model \"llama9\" not found`) || len(pe.Body()) > llm.MaxBodyExcerpt {
		t.Fatalf("Body() = %q, want the bounded not-found explanation", pe.Body())
	}
	if !strings.Contains(err.Error(), pe.Body()) {
		t.Fatalf("Error() = %q, want it to carry the excerpt", err)
	}
	assertNoSecret(t, err)
	if got := vs.only(t).header.Get("Authorization"); got != "" {
		t.Fatalf("keyless request sent Authorization %q", got)
	}
}

func TestGenerateRejectsInvalidRequests(t *testing.T) {
	vs := newVendorServer(t, reply{status: 200, fixture: "ollama_chat_text.json"})
	p := newProvider(t, vs.URL, JSONSchema, nil)
	if _, err := p.Generate(context.Background(), llm.Request{}); err == nil {
		t.Error("Generate accepted a request with no messages")
	}
	noModel, err := New(Config{BaseURL: vs.URL, APIKey: testKey, StructuredOutput: JSONSchema})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := noModel.Generate(context.Background(), schemaRequest()); err == nil {
		t.Error("Generate succeeded with no model configured")
	}
	vs.mu.Lock()
	defer vs.mu.Unlock()
	if len(vs.got) != 0 {
		t.Errorf("invalid requests reached the vendor %d times", len(vs.got))
	}
}

func TestNewRejectsBadHeaderWithoutEchoingValue(t *testing.T) {
	_, err := New(Config{BaseURL: "https://example.com/v1", StructuredOutput: JSONSchema, Headers: map[string]string{"X-Title": testKey + "\u0019"}})
	if err == nil {
		t.Fatal("New() = nil error")
	}
	assertNoSecret(t, err)
}

// TestNewConfigErrorsNeverLeakKey covers every New error that names
// caller-supplied text, each with the API key embedded in that text.
func TestNewConfigErrorsNeverLeakKey(t *testing.T) {
	cases := map[string]Config{
		"key in invalid header name": {Headers: map[string]string{"Bearer " + testKey: "a"}},
		"key as invalid header name": {Headers: map[string]string{testKey + ":": "a"}},
		"key in name, bad value":     {Headers: map[string]string{"x-" + testKey: "a\nb"}},
		"key in structured output":   {StructuredOutput: StructuredOutput("Bearer " + testKey)},
		"key with quote in mode":     {StructuredOutput: StructuredOutput(testKey + "\"\x00")},
	}
	for name, cfg := range cases {
		cfg.BaseURL = "https://example.com/v1"
		cfg.APIKey = testKey
		if cfg.StructuredOutput == "" {
			cfg.StructuredOutput = JSONSchema
		}
		_, err := New(cfg)
		if err == nil {
			t.Errorf("%s: New() = nil error", name)
			continue
		}
		assertNoSecret(t, err)
	}
}

// TestNewManagedHeaderErrorNeverLeaksKey uses an API key that is itself a
// managed header name, so naming the rejected header would print the key.
func TestNewManagedHeaderErrorNeverLeaksKey(t *testing.T) {
	for _, key := range []string{"Authorization", "Content-Type", "Host"} {
		_, err := New(Config{
			BaseURL:          "https://example.com/v1",
			APIKey:           key,
			StructuredOutput: JSONSchema,
			Headers:          map[string]string{key: "x"},
		})
		if err == nil {
			t.Errorf("key %q: New() = nil error", key)
			continue
		}
		for _, secret := range []string{key, "Bearer " + key} {
			if strings.Contains(strings.ToLower(err.Error()), strings.ToLower(secret)) {
				t.Errorf("key %q: error leaks %q: %v", key, secret, err)
			}
		}
	}
}

// TestNewModeErrorNeverLeaksKeyFragment uses a structured output mode that is
// a fragment of the API key, which redacting the whole key would not catch.
func TestNewModeErrorNeverLeaksKeyFragment(t *testing.T) {
	_, err := New(Config{BaseURL: "https://example.com/v1", APIKey: testKey, StructuredOutput: StructuredOutput(testKey[:20])})
	if err == nil {
		t.Fatal("New() = nil error")
	}
	assertNoSecret(t, err)
}

func TestAcceptHeader(t *testing.T) {
	for name, tc := range map[string]struct {
		headers map[string]string
		want    string
	}{
		"default":    {nil, "application/json"},
		"configured": {map[string]string{"accept": "application/json, text/event-stream"}, "application/json, text/event-stream"},
	} {
		vs := newVendorServer(t, reply{status: 200, fixture: "openai_models.json"})
		p := newProvider(t, vs.URL+"/v1", JSONSchema, tc.headers)
		if _, err := p.ListModels(context.Background()); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := vs.only(t).header.Values("Accept"); len(got) != 1 || got[0] != tc.want {
			t.Errorf("%s: Accept = %q, want %q", name, got, tc.want)
		}
	}
}

func TestNewAcceptsValidHeaders(t *testing.T) {
	headers := map[string]string{"X-Trace_ID.v2!#$%&'*+^`|~": "tab\tand obs-text \xe2\x9c\x93", "X-Empty": ""}
	if _, err := New(Config{BaseURL: "https://example.com/v1", StructuredOutput: JSONSchema, Headers: headers}); err != nil {
		t.Fatal(err)
	}
}

func TestNewRejectsBadConfig(t *testing.T) {
	cases := map[string]Config{
		"empty base":         {StructuredOutput: JSONSchema},
		"relative base":      {BaseURL: "api.openai.com/v1", StructuredOutput: JSONSchema},
		"ftp base":           {BaseURL: "ftp://example.com", StructuredOutput: JSONSchema},
		"credentials in URL": {BaseURL: "https://user:" + testKey + "@example.com/v1", StructuredOutput: JSONSchema},
		"key in query":       {BaseURL: "https://example.com/v1?key=" + testKey, StructuredOutput: JSONSchema},
		"key in fragment":    {BaseURL: "https://example.com/v1#" + testKey, StructuredOutput: JSONSchema},
		"key, relative base": {BaseURL: "example.com/v1?key=" + testKey, StructuredOutput: JSONSchema},
		"key, ftp base":      {BaseURL: "ftp://example.com/v1?key=" + testKey, StructuredOutput: JSONSchema},
		"key, unparseable":   {BaseURL: "https://example.com/%zz?key=" + testKey, StructuredOutput: JSONSchema},
		"empty query":        {BaseURL: "https://example.com/v1?", StructuredOutput: JSONSchema},
		"empty fragment":     {BaseURL: "https://example.com/v1#", StructuredOutput: JSONSchema},
		"empty both":         {BaseURL: "https://example.com/v1?#", StructuredOutput: JSONSchema},
		"unknown mode":       {BaseURL: "https://example.com/v1", StructuredOutput: "json"},
		"no mode":            {BaseURL: "https://example.com/v1"},
		"authorization hdr":  {BaseURL: "https://example.com/v1", StructuredOutput: JSONSchema, Headers: map[string]string{"authorization": "Bearer x"}},
		"content-type hdr":   {BaseURL: "https://example.com/v1", StructuredOutput: JSONSchema, Headers: map[string]string{"Content-Type": "text/plain"}},
		"header newline":     {BaseURL: "https://example.com/v1", StructuredOutput: JSONSchema, Headers: map[string]string{"X-Title": "a\r\nb"}},
		"header bad name":    {BaseURL: "https://example.com/v1", StructuredOutput: JSONSchema, Headers: map[string]string{"X Title": "a"}},
		"header empty name":  {BaseURL: "https://example.com/v1", StructuredOutput: JSONSchema, Headers: map[string]string{"": "a"}},
		"header bracket":     {BaseURL: "https://example.com/v1", StructuredOutput: JSONSchema, Headers: map[string]string{"X[Trace]": "a"}},
		"header colon":       {BaseURL: "https://example.com/v1", StructuredOutput: JSONSchema, Headers: map[string]string{"X-Title:": "a"}},
		"header non-ASCII":   {BaseURL: "https://example.com/v1", StructuredOutput: JSONSchema, Headers: map[string]string{"X-Tïtle": "a"}},
		"header U+0019":      {BaseURL: "https://example.com/v1", StructuredOutput: JSONSchema, Headers: map[string]string{"X-Title": "a\u0019b"}},
		"header NUL":         {BaseURL: "https://example.com/v1", StructuredOutput: JSONSchema, Headers: map[string]string{"X-Title": "a\x00b"}},
		"header DEL":         {BaseURL: "https://example.com/v1", StructuredOutput: JSONSchema, Headers: map[string]string{"X-Title": "a\x7fb"}},
		"host hdr":           {BaseURL: "https://example.com/v1", StructuredOutput: JSONSchema, Headers: map[string]string{"host": "other.example"}},
		"content-length hdr": {BaseURL: "https://example.com/v1", StructuredOutput: JSONSchema, Headers: map[string]string{"Content-Length": "1"}},
	}
	for name, cfg := range cases {
		cfg.APIKey = testKey
		_, err := New(cfg)
		if err == nil {
			t.Errorf("%s: New() = nil error", name)
			continue
		}
		assertNoSecret(t, err)
	}
}

func TestFirstJSONObject(t *testing.T) {
	cases := map[string]string{
		`{"a":1}`:                         `{"a":1}`,
		"text {not json} then {\"b\":2}":  `{"b":2}`,
		"```json\n{\"c\":{\"d\":3}}\n```": `{"c":{"d":3}}`,
		`{"a":1} {"b":2}`:                 `{"a":1}`,
		"no object":                       ``,
		`{"open":`:                        ``,
		`[{"in":"array"}]`:                `{"in":"array"}`,
	}
	for in, want := range cases {
		if got := string(firstJSONObject(in)); got != want {
			t.Errorf("firstJSONObject(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRetryAfter(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	cases := map[string]time.Duration{
		"":                              0,
		"30":                            30 * time.Second,
		" 5 ":                           5 * time.Second,
		"-3":                            0,
		"soon":                          0,
		"Sat, 03 Oct 2026 12:02:00 GMT": 2 * time.Minute,
		"Sat, 03 Oct 2026 11:59:00 GMT": 0,
	}
	for in, want := range cases {
		if got := retryAfter(in, now); got != want {
			t.Errorf("retryAfter(%q) = %v, want %v", in, got, want)
		}
	}
}

// headerNameKey is an API key that is also a word callers and vendors put in
// text: an error that quotes such text prints the key.
const headerNameKey = "Authorization"

// assertNoKey fails if err's text holds key or "Bearer "+key in any case.
func assertNoKey(t *testing.T, err error, key string) {
	t.Helper()
	for _, secret := range []string{key, "Bearer " + key} {
		if strings.Contains(err.Error(), secret) || strings.Contains(strings.ToLower(err.Error()), strings.ToLower(secret)) {
			t.Fatalf("error leaks %q: %v", secret, err)
		}
	}
}

// keyServer replies to every request with status and body.
func keyServer(t *testing.T, status int, body string) *Provider {
	t.Helper()
	return keyServerWithKey(t, headerNameKey, status, body)
}

// keyServerWithKey is keyServer for a provider configured with key.
func keyServerWithKey(t *testing.T, key string, status int, body string) *Provider {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	p, err := New(Config{BaseURL: srv.URL + "/v1", APIKey: key, Model: "test-model", StructuredOutput: PromptOnly})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestGenerateInvalidRoleNeverLeaksKey(t *testing.T) {
	p := keyServer(t, 200, `{}`)
	_, err := p.Generate(context.Background(), llm.Request{Messages: []llm.Message{{Role: llm.Role(headerNameKey), Content: "hi"}}})
	if err == nil {
		t.Fatal("Generate accepted an unknown role")
	}
	assertNoKey(t, err, headerNameKey)
}

func TestGenerateInvalidSchemaNeverLeaksKey(t *testing.T) {
	p := keyServer(t, 200, `{}`)
	req := schemaRequest()
	req.Schema = &llm.JSONSchema{Name: headerNameKey, Schema: json.RawMessage(`[]`)}
	_, err := p.Generate(context.Background(), req)
	if err == nil {
		t.Fatal("Generate accepted a schema that is not a JSON object")
	}
	assertNoKey(t, err, headerNameKey)
}

func TestGenerateBadOutputNeverLeaksKey(t *testing.T) {
	p := keyServer(t, 200, `{"model":"m","choices":[{"message":{"content":"no JSON here"},"finish_reason":"`+headerNameKey+`"}]}`)
	resp, err := p.Generate(context.Background(), schemaRequest())
	if !errors.Is(err, llm.ErrBadOutput) {
		t.Fatalf("err = %v, want ErrBadOutput", err)
	}
	assertNoKey(t, err, headerNameKey)
	if resp.FinishReason != headerNameKey {
		t.Fatalf("FinishReason = %q, want it kept on the response", resp.FinishReason)
	}
}

func TestProviderErrorBodyNeverLeaksKey(t *testing.T) {
	p := keyServer(t, 500, `{"error":{"message":"rejected `+headerNameKey+`: Bearer `+headerNameKey+`"}}`)
	_, genErr := p.Generate(context.Background(), schemaRequest())
	_, listErr := p.ListModels(context.Background())
	verifyErr := p.Verify(context.Background())
	for _, err := range []error{genErr, listErr, verifyErr} {
		var pe *llm.ProviderError
		if !errors.Is(err, llm.ErrProvider) || !errors.As(err, &pe) || pe.Status() != 500 {
			t.Fatalf("err = %v, want a 500 ProviderError", err)
		}
		assertNoKey(t, err, headerNameKey)
	}
}

// TestRedactKeepsChainAndHidesKey checks the backstop every exported method
// applies: the key and its Bearer value leave the text, the chain stays.
func TestRedactKeepsChainAndHidesKey(t *testing.T) {
	inner := fmt.Errorf("decode: body %q: %w", "Bearer "+testKey, llm.ErrBadOutput)
	err := redact(inner, testKey)
	assertChainNoKey(t, err, testKey)
	if !errors.Is(err, llm.ErrBadOutput) {
		t.Fatalf("redact broke the chain of %v", err)
	}
	if redact(inner, "") != inner {
		t.Fatal("redact changed an error for the empty key")
	}
	clean := errors.New("openai: generate: no model configured")
	if redact(clean, testKey) != clean {
		t.Fatal("redact wrapped an error that holds no key")
	}
	// A key inside the redaction marker would survive replacement.
	if got := redact(errors.New("x E y"), "E").Error(); strings.Contains(got, "E") {
		t.Fatalf("redact kept the key: %q", got)
	}
}

// assertChainNoKey fails if err, or any error reached from it by unwrapping,
// has text holding key or "Bearer "+key.
func assertChainNoKey(t *testing.T, err error, key string) {
	t.Helper()
	if err == nil {
		return
	}
	assertNoKey(t, err, key)
	switch u := err.(type) {
	case interface{ Unwrap() error }:
		assertChainNoKey(t, u.Unwrap(), key)
	case interface{ Unwrap() []error }:
		for _, c := range u.Unwrap() {
			assertChainNoKey(t, c, key)
		}
	}
}

// TestGenerateInvalidJSONWithKeyNeverLeaksKeyByUnwrapping sends an invalid
// JSON body holding the key and walks the returned chain with errors.Unwrap.
func TestGenerateInvalidJSONWithKeyNeverLeaksKeyByUnwrapping(t *testing.T) {
	p := keyServer(t, 200, `{"choices": Bearer `+headerNameKey+` `+headerNameKey+`}`)
	_, err := p.Generate(context.Background(), schemaRequest())
	if err == nil {
		t.Fatal("Generate accepted an invalid JSON body")
	}
	var syntax *json.SyntaxError
	if !errors.As(err, &syntax) {
		t.Fatalf("err = %v, want a *json.SyntaxError in the chain", err)
	}
	for layer := err; layer != nil; layer = errors.Unwrap(layer) {
		assertNoKey(t, layer, headerNameKey)
	}
}

// TestRedactMakesEveryUnwrappedLayerSafe covers chains whose inner layers
// hold the key, including a layer whose own text does not show it and a
// layer with several causes, and checks Is and As still match.
func TestRedactMakesEveryUnwrappedLayerSafe(t *testing.T) {
	pe := llm.NewProviderError(500, []byte(`{"error":"down"}`), testKey)
	hidden := &opaqueError{err: fmt.Errorf("body Bearer %s: %w", testKey, llm.ErrAuth)}
	inner := fmt.Errorf("decode %s: %w", testKey, llm.ErrBadOutput)
	joined := errors.Join(inner, pe, context.Canceled, fmt.Errorf("limit: %w", llm.ErrRateLimited))
	cases := []error{
		fmt.Errorf("openai: generate: %w", inner),
		fmt.Errorf("openai: generate: %w", hidden),
		fmt.Errorf("openai: generate: %w", joined),
		fmt.Errorf("openai: %w and %w", inner, &transportFailure{detail: "slow", kind: llm.ErrTimeout}),
	}
	for _, c := range cases {
		err := redact(c, testKey)
		if err == c {
			t.Fatalf("redact left %v unchanged", c)
		}
		assertChainNoKey(t, err, testKey)
		for layer := err; layer != nil; layer = errors.Unwrap(layer) {
			assertNoKey(t, layer, testKey)
		}
	}
	err := redact(fmt.Errorf("openai: %w", joined), testKey)
	var got *llm.ProviderError
	if !errors.As(err, &got) || got != pe {
		t.Fatalf("errors.As lost the ProviderError in %v", err)
	}
	for _, target := range []error{llm.ErrBadOutput, llm.ErrProvider, llm.ErrRateLimited, context.Canceled} {
		if !errors.Is(err, target) {
			t.Fatalf("errors.Is(%v, %v) = false", err, target)
		}
	}
	err = redact(fmt.Errorf("openai: %w", hidden), testKey)
	if !errors.Is(err, llm.ErrAuth) {
		t.Fatalf("errors.Is lost ErrAuth in %v", err)
	}
	err = redact(fmt.Errorf("openai %s: %w", testKey, &transportFailure{detail: "slow", kind: llm.ErrTimeout}), testKey)
	if !errors.Is(err, llm.ErrTimeout) {
		t.Fatalf("errors.Is lost ErrTimeout in %v", err)
	}
}

// opaqueError hides its cause's text but unwraps to it.
type opaqueError struct{ err error }

func (e *opaqueError) Error() string { return "opaque" }

func (e *opaqueError) Unwrap() error { return e.err }

// assertChainNoExactKey fails if the text of err, of any error reached from
// it by unwrapping, or of a *llm.ProviderError found by errors.As holds key.
// It matches case-sensitively, so it suits keys too short for assertNoKey.
func assertChainNoExactKey(t *testing.T, err error, key string) {
	t.Helper()
	var walk func(error)
	walk = func(e error) {
		if e == nil {
			return
		}
		if strings.Contains(e.Error(), key) {
			t.Fatalf("error leaks %q: %q", key, e.Error())
		}
		switch u := e.(type) {
		case interface{ Unwrap() error }:
			walk(u.Unwrap())
		case interface{ Unwrap() []error }:
			for _, c := range u.Unwrap() {
				walk(c)
			}
		}
	}
	walk(err)
	var pe *llm.ProviderError
	if errors.As(err, &pe) && (strings.Contains(pe.Body(), key) || strings.Contains(pe.Error(), key)) {
		t.Fatalf("ProviderError leaks %q: body %q", key, pe.Body())
	}
	var syntax *json.SyntaxError
	if errors.As(err, &syntax) && strings.Contains(syntax.Error(), key) {
		t.Fatalf("errors.As reached a *json.SyntaxError leaking %q: %q", key, syntax.Error())
	}
}

// TestProviderErrorBodyWithMarkerKeyNeverLeaksKey uses the redaction marker
// text as the API key, so replacing the key with the marker would keep it.
func TestProviderErrorBodyWithMarkerKeyNeverLeaksKey(t *testing.T) {
	const key = "REDACTED"
	p := keyServerWithKey(t, key, 500, `{"error":{"message":"rejected `+key+`: Bearer `+key+`"}}`)
	_, err := p.Generate(context.Background(), schemaRequest())
	var pe *llm.ProviderError
	if !errors.Is(err, llm.ErrProvider) || !errors.As(err, &pe) || pe.Status() != 500 {
		t.Fatalf("err = %v, want a 500 ProviderError", err)
	}
	if strings.Contains(pe.Body(), key) || strings.Contains(pe.Error(), key) {
		t.Fatalf("ProviderError leaks the key: %q", pe.Error())
	}
	assertChainNoExactKey(t, err, key)
}

// TestGenerateMalformedJSONWithShortKeyNeverLeaksKey uses a one-letter key
// found in the redaction marker and in ordinary error prose.
func TestGenerateMalformedJSONWithShortKeyNeverLeaksKey(t *testing.T) {
	const key = "A"
	p := keyServerWithKey(t, key, 200, `{"choices": A Bearer A}`)
	_, err := p.Generate(context.Background(), schemaRequest())
	if err == nil {
		t.Fatal("Generate accepted a malformed JSON body")
	}
	assertChainNoExactKey(t, err, key)
	for layer := err; layer != nil; layer = errors.Unwrap(layer) {
		if strings.Contains(layer.Error(), key) {
			t.Fatalf("layer leaks %q: %q", key, layer.Error())
		}
	}
}

// assertLayersNoExactKey fails if the text of err or of any error reached
// from it by errors.Unwrap holds key, matched case-sensitively.
func assertLayersNoExactKey(t *testing.T, err error, key string) {
	t.Helper()
	for layer := err; layer != nil; layer = errors.Unwrap(layer) {
		if strings.Contains(layer.Error(), key) {
			t.Fatalf("layer leaks %q: %q", key, layer.Error())
		}
	}
}

// TestProviderErrorWithFixedWordKeyKeepsTypedMatch uses the fixed word
// "HTTP" of ProviderError's text as the API key: the text must drop it and
// errors.As must still reach the ProviderError.
func TestProviderErrorWithFixedWordKeyKeepsTypedMatch(t *testing.T) {
	const key = "HTTP"
	p := keyServerWithKey(t, key, 500, `{"error":"down"}`)
	_, err := p.Generate(context.Background(), schemaRequest())
	var pe *llm.ProviderError
	if !errors.Is(err, llm.ErrProvider) || !errors.As(err, &pe) || pe.Status() != 500 {
		t.Fatalf("err = %v, want a 500 ProviderError", err)
	}
	if strings.Contains(pe.Error(), key) || strings.Contains(pe.Body(), key) {
		t.Fatalf("ProviderError leaks the key: Error %q, Body %q", pe.Error(), pe.Body())
	}
	assertLayersNoExactKey(t, err, key)
	assertChainNoExactKey(t, err, key)
}

// TestRateLimitedErrorWithFixedWordKeyKeepsTypedMatch uses the fixed word
// "rate" of RateLimitedError's text as the API key.
func TestRateLimitedErrorWithFixedWordKeyKeepsTypedMatch(t *testing.T) {
	const key = "rate"
	srv := newVendorServer(t, reply{status: 429, header: map[string]string{"Retry-After": "17"}})
	p, err := New(Config{BaseURL: srv.URL + "/v1", APIKey: key, Model: "test-model", StructuredOutput: PromptOnly})
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Generate(context.Background(), schemaRequest())
	var rl *llm.RateLimitedError
	if !errors.Is(err, llm.ErrRateLimited) || !errors.As(err, &rl) || rl.RetryAfter != 17*time.Second {
		t.Fatalf("err = %v, want a RateLimitedError with RetryAfter 17s", err)
	}
	if strings.Contains(rl.Error(), key) {
		t.Fatalf("RateLimitedError leaks the key: %q", rl.Error())
	}
	assertLayersNoExactKey(t, err, key)
	assertChainNoExactKey(t, err, key)
}

// TestGenerateWithAdapterNameKeyNeverLeaksKey uses the fixed prefix
// "openai" of the adapter's errors as the API key.
func TestGenerateWithAdapterNameKeyNeverLeaksKey(t *testing.T) {
	const key = "openai"
	t.Run("401", func(t *testing.T) {
		p := keyServerWithKey(t, key, 401, `{"error":{"message":"bad key"}}`)
		_, err := p.Generate(context.Background(), schemaRequest())
		if !errors.Is(err, llm.ErrAuth) {
			t.Fatalf("err = %v, want ErrAuth", err)
		}
		assertLayersNoExactKey(t, err, key)
		assertChainNoExactKey(t, err, key)
	})
	t.Run("no model", func(t *testing.T) {
		p, err := New(Config{BaseURL: "http://127.0.0.1:1/v1", APIKey: key, StructuredOutput: PromptOnly})
		if err != nil {
			t.Fatal(err)
		}
		_, err = p.Generate(context.Background(), schemaRequest())
		if err == nil {
			t.Fatal("Generate succeeded without a model")
		}
		assertLayersNoExactKey(t, err, key)
		assertChainNoExactKey(t, err, key)
	})
}

// TestRedactedErrorsFormatWithoutKey formats every error type that holds the
// key, or a cause holding it, with %v, %+v and %#v; %#v prints GoString.
func TestRedactedErrorsFormatWithoutKey(t *testing.T) {
	const key = "sk-distinctive-openai-format-7c1e"
	single := redact(fmt.Errorf("openai %s: %w", key, llm.ErrBadOutput), key)
	joined := redact(errors.Join(fmt.Errorf("decode %s: %w", key, llm.ErrAuth), llm.NewProviderError(500, []byte("down "+key), key)), key)
	if _, ok := single.(*redactedError); !ok {
		t.Fatalf("single = %T, want *redactedError", single)
	}
	if _, ok := joined.(*redactedJoin); !ok {
		t.Fatalf("joined = %T, want *redactedJoin", joined)
	}
	p := &Provider{apiKey: key}
	transport := p.transportError(fmt.Errorf("dial %s: %w", key, context.DeadlineExceeded))
	for _, err := range []error{single, joined, transport} {
		for _, verb := range []string{"%v", "%+v", "%#v"} {
			if got := fmt.Sprintf(verb, err); strings.Contains(got, key) {
				t.Fatalf("%s of %T leaks the key: %q", verb, err, got)
			}
		}
	}
}
