package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
		if strings.Contains(err.Error(), secret) {
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
		"unknown mode":       {BaseURL: "https://example.com/v1", StructuredOutput: "json"},
		"no mode":            {BaseURL: "https://example.com/v1"},
		"authorization hdr":  {BaseURL: "https://example.com/v1", StructuredOutput: JSONSchema, Headers: map[string]string{"authorization": "Bearer x"}},
		"content-type hdr":   {BaseURL: "https://example.com/v1", StructuredOutput: JSONSchema, Headers: map[string]string{"Content-Type": "text/plain"}},
		"header newline":     {BaseURL: "https://example.com/v1", StructuredOutput: JSONSchema, Headers: map[string]string{"X-Title": "a\r\nb"}},
		"header bad name":    {BaseURL: "https://example.com/v1", StructuredOutput: JSONSchema, Headers: map[string]string{"X Title": "a"}},
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
