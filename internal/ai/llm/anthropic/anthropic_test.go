package anthropic

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

const testKey = "sk-ant-test-9f8e7d6c5b4a39281706f5e4d3c2b1a0"

// recorded is one request the test server received.
type recorded struct {
	method string
	path   string
	query  url.Values
	header http.Header
	body   []byte
}

// reply is a response the test server replays.
type reply struct {
	status  int
	header  map[string]string
	fixture string // testdata file; {{KEY}} is replaced with the key
	body    string // used when fixture is empty
	delay   time.Duration
}

// vendorServer replays its replies in order, repeating the last, and
// records every request.
type vendorServer struct {
	*httptest.Server
	mu  sync.Mutex
	got []recorded
}

func newVendorServer(t *testing.T, replies ...reply) *vendorServer {
	return newVendorServerWithKey(t, testKey, replies...)
}

func newVendorServerWithKey(t *testing.T, key string, replies ...reply) *vendorServer {
	t.Helper()
	payloads := make([][]byte, len(replies))
	for i, r := range replies {
		payloads[i] = []byte(r.body)
		if r.fixture != "" {
			raw, err := os.ReadFile(filepath.Join("testdata", r.fixture))
			if err != nil {
				t.Fatal(err)
			}
			payloads[i] = bytes.ReplaceAll(raw, []byte("{{KEY}}"), []byte(key))
		}
	}
	vs := &vendorServer{}
	vs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		vs.mu.Lock()
		n := len(vs.got)
		vs.got = append(vs.got, recorded{req.Method, req.URL.Path, req.URL.Query(), req.Header.Clone(), body})
		vs.mu.Unlock()
		i := min(n, len(replies)-1)
		r := replies[i]
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
		w.Write(payloads[i])
	}))
	t.Cleanup(vs.Close)
	return vs
}

func (vs *vendorServer) requests() []recorded {
	vs.mu.Lock()
	defer vs.mu.Unlock()
	return append([]recorded(nil), vs.got...)
}

func (vs *vendorServer) only(t *testing.T) recorded {
	t.Helper()
	got := vs.requests()
	if len(got) != 1 {
		t.Fatalf("server got %d requests, want 1", len(got))
	}
	return got[0]
}

func newProvider(t *testing.T, base string) *Provider {
	return newProviderWithKey(t, base, testKey)
}

func newProviderWithKey(t *testing.T, base, key string) *Provider {
	t.Helper()
	p, err := New(Config{BaseURL: base, APIKey: key, Model: "test-model", MaxTokens: 1024})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

var dinnersSchema = json.RawMessage(`{"type":"object","properties":{"dinners":{"type":"array"}},"required":["dinners"]}`)

func schemaRequest() llm.Request {
	temp := 0.0
	return llm.Request{
		System: "You plan dinners for Our kitchen.",
		Messages: []llm.Message{
			{Role: llm.RoleUser, Content: "Suggest a Side quest."},
			{Role: llm.RoleAssistant, Content: "Something quick?"},
			{Role: llm.RoleUser, Content: "Yes, a Quick dinner."},
		},
		Temperature: &temp,
		MaxTokens:   400,
		Schema:      &llm.JSONSchema{Name: "side_quests", Schema: dinnersSchema},
	}
}

func textRequest() llm.Request {
	return llm.Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "What should we cook tonight?"}}}
}

// sentBody decodes a request body into a generic map.
func sentBody(t *testing.T, r recorded) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(r.body, &m); err != nil {
		t.Fatalf("request body %q: %v", r.body, err)
	}
	return m
}

// canonical re-encodes JSON so two encodings of one value compare equal.
func canonical(t *testing.T, raw []byte) string {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("JSON %q: %v", raw, err)
	}
	out, _ := json.Marshal(v)
	return string(out)
}

func assertHeaders(t *testing.T, r recorded, key string) {
	t.Helper()
	if got := r.header.Values("X-Api-Key"); len(got) != 1 || got[0] != key {
		t.Errorf("x-api-key = %q", got)
	}
	if got := r.header.Values("Anthropic-Version"); len(got) != 1 || got[0] != "2023-06-01" {
		t.Errorf("anthropic-version = %q", got)
	}
	if got := r.header.Get("Authorization"); got != "" {
		t.Errorf("Authorization = %q, want none", got)
	}
}

func TestGenerateToolForcedJSON(t *testing.T) {
	vs := newVendorServer(t, reply{status: 200, fixture: "messages_tool_use.json"})
	p := newProvider(t, vs.URL+"/")

	resp, err := p.Generate(context.Background(), schemaRequest())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := canonical(t, resp.JSON), `{"dinners":[{"name":"Sheet-pan gnocchi","why":"Like your Regular tray bake, but quicker."}]}`; got != want {
		t.Errorf("Response.JSON = %s, want %s", got, want)
	}
	if resp.Text != "" {
		t.Errorf("Text = %q, want none", resp.Text)
	}
	if resp.Usage != (llm.Usage{InputTokens: 472, OutputTokens: 61}) || resp.Model != "claude-sonnet-5" || resp.FinishReason != "tool_use" {
		t.Errorf("Usage, Model, FinishReason = %+v, %q, %q", resp.Usage, resp.Model, resp.FinishReason)
	}

	got := vs.only(t)
	if got.method != http.MethodPost || got.path != "/v1/messages" || len(got.query) != 0 {
		t.Errorf("request = %s %s?%s", got.method, got.path, got.query.Encode())
	}
	assertHeaders(t, got, testKey)
	if h := got.header.Get("Content-Type"); h != "application/json" {
		t.Errorf("Content-Type = %q", h)
	}
	body := sentBody(t, got)
	if body["model"] != "test-model" || body["temperature"] != 0.0 || body["max_tokens"] != 400.0 {
		t.Errorf("model, temperature, max_tokens = %v, %v, %v", body["model"], body["temperature"], body["max_tokens"])
	}
	if body["system"] != "You plan dinners for Our kitchen." {
		t.Errorf("system = %v", body["system"])
	}
	msgs := body["messages"].([]any)
	wantMsgs := []string{"user:Suggest a Side quest.", "assistant:Something quick?", "user:Yes, a Quick dinner."}
	if len(msgs) != len(wantMsgs) {
		t.Fatalf("messages = %v", msgs)
	}
	for i, m := range msgs {
		mm := m.(map[string]any)
		if got := fmt.Sprintf("%v:%v", mm["role"], mm["content"]); got != wantMsgs[i] {
			t.Errorf("message %d = %s, want %s", i, got, wantMsgs[i])
		}
	}
	tools := body["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %v", tools)
	}
	tool := tools[0].(map[string]any)
	schema, _ := json.Marshal(tool["input_schema"])
	if tool["name"] != "side_quests" || string(schema) != canonical(t, dinnersSchema) {
		t.Errorf("tool = %v", tool)
	}
	choice := body["tool_choice"].(map[string]any)
	if len(choice) != 2 || choice["type"] != "tool" || choice["name"] != "side_quests" {
		t.Errorf("tool_choice = %v", choice)
	}
}

func TestGenerateText(t *testing.T) {
	vs := newVendorServer(t, reply{status: 200, fixture: "messages_text.json"})
	p := newProvider(t, vs.URL)

	resp, err := p.Generate(context.Background(), textRequest())
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "Try a crock pot chili: it is a Quick dinner." || resp.JSON != nil {
		t.Errorf("Text, JSON = %q, %s", resp.Text, resp.JSON)
	}
	if resp.Usage != (llm.Usage{InputTokens: 2095, OutputTokens: 503}) || resp.Model != "claude-sonnet-5" || resp.FinishReason != "end_turn" {
		t.Errorf("Usage, Model, FinishReason = %+v, %q, %q", resp.Usage, resp.Model, resp.FinishReason)
	}

	got := vs.only(t)
	if got.path != "/v1/messages" {
		t.Errorf("path = %s", got.path)
	}
	assertHeaders(t, got, testKey)
	body := sentBody(t, got)
	if body["max_tokens"] != 1024.0 {
		t.Errorf("max_tokens = %v, want the configured 1024", body["max_tokens"])
	}
	for _, field := range []string{"system", "temperature", "tools", "tool_choice"} {
		if v, ok := body[field]; ok {
			t.Errorf("%s = %v, want it omitted", field, v)
		}
	}
}

func TestGenerateTextKeepsStopReason(t *testing.T) {
	vs := newVendorServer(t, reply{status: 200, fixture: "messages_max_tokens.json"})
	resp, err := newProvider(t, vs.URL).Generate(context.Background(), textRequest())
	if err != nil {
		t.Fatal(err)
	}
	if resp.FinishReason != "max_tokens" || resp.Usage.OutputTokens != 400 {
		t.Errorf("FinishReason, Usage = %q, %+v", resp.FinishReason, resp.Usage)
	}
}

func TestGenerateBadOutput(t *testing.T) {
	cases := map[string]string{
		"text instead of tool": `{"type":"message","model":"m","content":[{"type":"text","text":"Sure!"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":2}}`,
		"other tool":           `{"type":"message","model":"m","content":[{"type":"tool_use","id":"t","name":"other","input":{"a":1}}],"stop_reason":"tool_use","usage":{}}`,
		"input not an object":  `{"type":"message","model":"m","content":[{"type":"tool_use","id":"t","name":"side_quests","input":[1]}],"stop_reason":"tool_use","usage":{}}`,
		"no content":           `{"type":"message","model":"m","content":[],"stop_reason":"max_tokens","usage":{}}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			vs := newVendorServer(t, reply{status: 200, body: body})
			resp, err := newProvider(t, vs.URL).Generate(context.Background(), schemaRequest())
			if !errors.Is(err, llm.ErrBadOutput) {
				t.Fatalf("err = %v, want ErrBadOutput", err)
			}
			if resp.JSON != nil || resp.Model != "m" {
				t.Fatalf("Response = %+v, want no JSON and the model kept", resp)
			}
		})
	}
	vs := newVendorServer(t, reply{status: 200, fixture: "messages_max_tokens.json"})
	resp, err := newProvider(t, vs.URL).Generate(context.Background(), schemaRequest())
	if !errors.Is(err, llm.ErrBadOutput) || resp.FinishReason != "max_tokens" {
		t.Fatalf("truncated: resp, err = %+v, %v", resp, err)
	}
}

func TestGenerateRejectsNonMessage(t *testing.T) {
	vs := newVendorServer(t, reply{status: 200, fixture: "error_overloaded.json"})
	_, err := newProvider(t, vs.URL).Generate(context.Background(), textRequest())
	if err == nil || errors.Is(err, llm.ErrBadOutput) {
		t.Fatalf("err = %v, want an untyped malformed-response error", err)
	}
}

func TestGenerateRejectsInvalidRequests(t *testing.T) {
	vs := newVendorServer(t, reply{status: 200, fixture: "messages_text.json"})
	p := newProvider(t, vs.URL)
	if _, err := p.Generate(context.Background(), llm.Request{}); err == nil {
		t.Error("Generate accepted a request with no messages")
	}
	for _, name := range []string{"side quests", "", strings.Repeat("a", 65), "plan.v2"} {
		req := schemaRequest()
		req.Schema.Name = name
		if _, err := p.Generate(context.Background(), req); err == nil {
			t.Errorf("Generate accepted tool name %q", name)
		}
	}
	noModel, err := New(Config{BaseURL: vs.URL, APIKey: testKey, MaxTokens: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := noModel.Generate(context.Background(), textRequest()); err == nil {
		t.Error("Generate succeeded with no model configured")
	}
	if n := len(vs.requests()); n != 0 {
		t.Errorf("invalid requests reached the vendor %d times", n)
	}
}

func TestListModelsFollowsPages(t *testing.T) {
	vs := newVendorServer(t,
		reply{status: 200, fixture: "models_page1.json"},
		reply{status: 200, fixture: "models_page2.json"},
	)
	models, err := newProvider(t, vs.URL).ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []llm.Model{
		{ID: "claude-sonnet-5", DisplayName: "Claude Sonnet 5"},
		{ID: "claude-haiku-4-5-20251001", DisplayName: "Claude Haiku 4.5"},
		{ID: "claude-opus-5-5", DisplayName: "claude-opus-5-5"},
	}
	if len(models) != len(want) {
		t.Fatalf("models = %+v", models)
	}
	for i := range want {
		if models[i] != want[i] {
			t.Errorf("model %d = %+v, want %+v", i, models[i], want[i])
		}
	}
	got := vs.requests()
	if len(got) != 2 {
		t.Fatalf("server got %d requests, want 2", len(got))
	}
	for i, wantAfter := range []string{"", "claude-sonnet-5"} {
		r := got[i]
		if r.method != http.MethodGet || r.path != "/v1/models" || r.query.Get("limit") != "1000" || r.query.Get("after_id") != wantAfter {
			t.Errorf("request %d = %s %s?%s", i, r.method, r.path, r.query.Encode())
		}
		assertHeaders(t, r, testKey)
		if len(r.body) != 0 || r.header.Get("Content-Type") != "" {
			t.Errorf("request %d sent a body", i)
		}
	}
}

func TestListModelsRejectsBadPages(t *testing.T) {
	cases := map[string]string{
		"no data":      `{"has_more":false}`,
		"no id":        `{"data":[{"type":"model","display_name":"x"}],"has_more":false}`,
		"no cursor":    `{"data":[{"id":"a"}],"has_more":true}`,
		"stuck cursor": `{"data":[{"id":"a"}],"has_more":true,"last_id":"a"}`,
		"invalid":      `{"data":`,
	}
	for name, body := range cases {
		vs := newVendorServer(t, reply{status: 200, body: body})
		if models, err := newProvider(t, vs.URL).ListModels(context.Background()); err == nil {
			t.Errorf("%s: ListModels() = %+v, nil error", name, models)
		}
	}
}

func TestListModelsEmpty(t *testing.T) {
	vs := newVendorServer(t, reply{status: 200, body: `{"data":[],"has_more":false}`})
	models, err := newProvider(t, vs.URL).ListModels(context.Background())
	if err != nil || models == nil || len(models) != 0 {
		t.Fatalf("ListModels() = %#v, %v", models, err)
	}
}

func TestVerify(t *testing.T) {
	ok := newVendorServer(t, reply{status: 200, fixture: "models_page1.json"})
	if err := newProvider(t, ok.URL).Verify(context.Background()); err != nil {
		t.Fatalf("Verify() = %v", err)
	}
	got := ok.only(t)
	if got.method != http.MethodGet || got.path != "/v1/models" || got.query.Get("limit") != "1" || got.query.Has("after_id") {
		t.Errorf("Verify called %s %s?%s", got.method, got.path, got.query.Encode())
	}
	assertHeaders(t, got, testKey)

	for status, want := range map[int]error{401: llm.ErrAuth, 403: llm.ErrAuth, 529: llm.ErrRateLimited, 500: llm.ErrProvider} {
		bad := newVendorServer(t, reply{status: status, fixture: "error_api.json"})
		err := newProvider(t, bad.URL).Verify(context.Background())
		if !errors.Is(err, want) {
			t.Errorf("HTTP %d: Verify() = %v, want %v", status, err, want)
		}
		assertNoSecret(t, err)
	}
	malformed := newVendorServer(t, reply{status: 200, body: `{}`})
	if err := newProvider(t, malformed.URL).Verify(context.Background()); err == nil {
		t.Error("Verify accepted a body with no data list")
	}
}

// assertNoSecret fails if err's text holds the key or a prefix of it.
func assertNoSecret(t *testing.T, err error) {
	t.Helper()
	for _, secret := range []string{testKey, testKey[:16]} {
		if strings.Contains(strings.ToLower(err.Error()), strings.ToLower(secret)) {
			t.Fatalf("error leaks %q: %v", secret, err)
		}
	}
}

func TestErrorMapping(t *testing.T) {
	rateLimited := func(want time.Duration) func(*testing.T, error) {
		return func(t *testing.T, err error) {
			var rl *llm.RateLimitedError
			if !errors.As(err, &rl) || rl.RetryAfter != want {
				t.Fatalf("RetryAfter from %v, want %v", err, want)
			}
		}
	}
	providerError := func(status int, excerpt string) func(*testing.T, error) {
		return func(t *testing.T, err error) {
			var pe *llm.ProviderError
			if !errors.As(err, &pe) || pe.Status() != status || !strings.Contains(pe.Body(), excerpt) {
				t.Fatalf("ProviderError from %v, want HTTP %d with %q", err, status, excerpt)
			}
			if !strings.Contains(err.Error(), pe.Body()) {
				t.Fatalf("Error() = %q, want it to carry the excerpt", err)
			}
		}
	}
	cases := []struct {
		name  string
		reply reply
		want  error
		check func(*testing.T, error)
	}{
		{name: "401", reply: reply{status: 401, fixture: "error_authentication.json"}, want: llm.ErrAuth},
		{name: "403", reply: reply{status: 403, fixture: "error_permission.json"}, want: llm.ErrAuth},
		{name: "429 seconds", reply: reply{status: 429, header: map[string]string{"Retry-After": "17"}, fixture: "error_rate_limit.json"}, want: llm.ErrRateLimited, check: rateLimited(17 * time.Second)},
		{name: "429 no Retry-After", reply: reply{status: 429, fixture: "error_rate_limit.json"}, want: llm.ErrRateLimited, check: rateLimited(0)},
		{name: "429 HTTP-date", reply: reply{status: 429, header: map[string]string{"Retry-After": time.Now().Add(90 * time.Second).UTC().Format(http.TimeFormat)}, fixture: "error_rate_limit.json"}, want: llm.ErrRateLimited,
			check: func(t *testing.T, err error) {
				var rl *llm.RateLimitedError
				if !errors.As(err, &rl) || rl.RetryAfter < 85*time.Second || rl.RetryAfter > 91*time.Second {
					t.Fatalf("RetryAfter from %v", err)
				}
			}},
		{name: "529 seconds", reply: reply{status: 529, header: map[string]string{"Retry-After": "4"}, fixture: "error_overloaded.json"}, want: llm.ErrRateLimited, check: rateLimited(4 * time.Second)},
		{name: "529 no Retry-After", reply: reply{status: 529, fixture: "error_overloaded.json"}, want: llm.ErrRateLimited, check: rateLimited(0)},
		{name: "408", reply: reply{status: 408}, want: llm.ErrTimeout},
		{name: "400", reply: reply{status: 400, fixture: "error_invalid_request.json"}, want: llm.ErrProvider, check: providerError(400, "max_tokens: Field required")},
		{name: "404", reply: reply{status: 404, fixture: "error_not_found.json"}, want: llm.ErrProvider, check: providerError(404, "not_found_error")},
		{name: "500 echoing the key", reply: reply{status: 500, fixture: "error_api.json"}, want: llm.ErrProvider, check: providerError(500, "x-api-key: [REDACTED]")},
		{name: "503", reply: reply{status: 503, body: "upstream connect error"}, want: llm.ErrProvider, check: providerError(503, "upstream connect error")},
	}
	all := []error{llm.ErrAuth, llm.ErrRateLimited, llm.ErrTimeout, llm.ErrBadOutput, llm.ErrProvider}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			vs := newVendorServer(t, c.reply)
			p := newProvider(t, vs.URL)
			_, genErr := p.Generate(context.Background(), schemaRequest())
			_, listErr := p.ListModels(context.Background())
			for _, err := range []error{genErr, listErr} {
				for _, sentinel := range all {
					if errors.Is(err, sentinel) != (sentinel == c.want) {
						t.Fatalf("errors.Is(%v, %v) = %v", err, sentinel, sentinel != c.want)
					}
				}
				assertNoSecret(t, err)
				assertChainNoExactKey(t, err, testKey)
				if c.check != nil {
					c.check(t, err)
				}
			}
		})
	}
}

func TestTimeouts(t *testing.T) {
	vs := newVendorServer(t, reply{status: 200, fixture: "messages_tool_use.json", delay: 5 * time.Second})
	p := newProvider(t, vs.URL)

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
	ctx, cancel = context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := p.Verify(ctx); !errors.Is(err, llm.ErrTimeout) {
		t.Fatalf("Verify context deadline: err = %v, want ErrTimeout", err)
	}

	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	if _, err := p.Generate(ctx, schemaRequest()); !errors.Is(err, context.Canceled) || errors.Is(err, llm.ErrTimeout) {
		t.Fatalf("cancelled context: err = %v, want context.Canceled only", err)
	}
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
		"connection refused": refusedBase + "/" + testKey,
		"connection dropped": dropped.URL + "/" + testKey,
	} {
		p := newProvider(t, base)
		_, genErr := p.Generate(context.Background(), schemaRequest())
		_, listErr := p.ListModels(context.Background())
		verifyErr := p.Verify(context.Background())
		for _, err := range []error{genErr, listErr, verifyErr} {
			if err == nil {
				t.Fatalf("%s: nil error", name)
			}
			assertNoSecret(t, err)
			assertChainNoExactKey(t, err, testKey)
			var ue *url.Error
			if errors.As(err, &ue) {
				t.Fatalf("%s: error chain exposes the request URL: %v", name, ue)
			}
			if errors.Is(err, llm.ErrTimeout) || errors.Is(err, llm.ErrProvider) {
				t.Fatalf("%s: err = %v, want an untyped transport failure", name, err)
			}
		}
	}

	// The request URL escapes a key with spaces, so redacting the key
	// itself would miss that copy.
	const spacedKey = "sk ant key"
	p := newProviderWithKey(t, refusedBase+"/"+spacedKey, spacedKey)
	_, genErr := p.Generate(context.Background(), textRequest())
	_, listErr := p.ListModels(context.Background())
	verifyErr := p.Verify(context.Background())
	for _, err := range []error{genErr, listErr, verifyErr} {
		if err == nil {
			t.Fatal("escaped key: nil error")
		}
		assertChainNoExactKey(t, err, spacedKey)
		assertChainNoExactKey(t, err, url.PathEscape(spacedKey))
	}
}

func TestNewRejectsBadConfig(t *testing.T) {
	cases := map[string]Config{
		"empty base":         {},
		"relative base":      {BaseURL: "api.anthropic.com"},
		"ftp base":           {BaseURL: "ftp://example.com"},
		"credentials in URL": {BaseURL: "https://user:" + testKey + "@example.com"},
		"key in query":       {BaseURL: "https://example.com/?key=" + testKey},
		"key in fragment":    {BaseURL: "https://example.com/#" + testKey},
		"key, relative base": {BaseURL: "example.com?key=" + testKey},
		"key, ftp base":      {BaseURL: "ftp://example.com/?key=" + testKey},
		"key, unparseable":   {BaseURL: "https://example.com/%zz?key=" + testKey},
		"empty query":        {BaseURL: "https://example.com?"},
		"empty fragment":     {BaseURL: "https://example.com#"},
		"empty both":         {BaseURL: "https://example.com/?#"},
		"empty key":          {BaseURL: "https://example.com", APIKey: "-"},
		"key with newline":   {BaseURL: "https://example.com", APIKey: testKey + "\n"},
		"zero max tokens":    {BaseURL: "https://example.com", MaxTokens: -1},
		"negative max":       {BaseURL: "https://example.com", MaxTokens: -5},
	}
	for name, cfg := range cases {
		switch cfg.APIKey {
		case "":
			cfg.APIKey = testKey
		case "-":
			cfg.APIKey = ""
		}
		switch cfg.MaxTokens {
		case 0:
			cfg.MaxTokens = 1024
		case -1:
			cfg.MaxTokens = 0
		}
		_, err := New(cfg)
		if err == nil {
			t.Errorf("%s: New() = nil error", name)
			continue
		}
		assertNoSecret(t, err)
		assertChainNoExactKey(t, err, testKey)
	}
	if _, err := New(Config{BaseURL: "http://anthropic:8080/proxy/", APIKey: testKey, MaxTokens: 1}); err != nil {
		t.Errorf("New() rejected a valid config: %v", err)
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
		"9223372036854775807":           time.Duration(maxRetryAfterSeconds) * time.Second,
		"99999999999999999999":          time.Duration(maxRetryAfterSeconds) * time.Second,
		"-99999999999999999999":         0,
	}
	for in, want := range cases {
		if got := retryAfter(in, now); got != want {
			t.Errorf("retryAfter(%q) = %v, want %v", in, got, want)
		}
	}
}

// assertChainNoExactKey fails if key appears, case-sensitively, in the text
// of err, of any error reached from it by unwrapping, or of a typed error
// errors.As finds in it, including a ProviderError's Body.
func assertChainNoExactKey(t *testing.T, err error, key string) {
	t.Helper()
	var walk func(error)
	walk = func(e error) {
		if e == nil {
			return
		}
		if strings.Contains(e.Error(), key) {
			t.Fatalf("layer leaks %q: %q", key, e.Error())
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
	for layer := err; layer != nil; layer = errors.Unwrap(layer) {
		if strings.Contains(layer.Error(), key) {
			t.Fatalf("errors.Unwrap layer leaks %q: %q", key, layer.Error())
		}
	}
	var pe *llm.ProviderError
	if errors.As(err, &pe) && (strings.Contains(pe.Body(), key) || strings.Contains(pe.Error(), key)) {
		t.Fatalf("ProviderError leaks %q: Error %q, Body %q", key, pe.Error(), pe.Body())
	}
	var rl *llm.RateLimitedError
	if errors.As(err, &rl) && strings.Contains(rl.Error(), key) {
		t.Fatalf("RateLimitedError leaks %q: %q", key, rl.Error())
	}
	var syntax *json.SyntaxError
	if errors.As(err, &syntax) && strings.Contains(syntax.Error(), key) {
		t.Fatalf("errors.As reached a *json.SyntaxError leaking %q: %q", key, syntax.Error())
	}
	if got := fmt.Sprintf("%v %+v %#v", err, err, err); strings.Contains(got, key) {
		t.Fatalf("formatting leaks %q: %q", key, got)
	}
}

// TestHeaderNameKeyNeverLeaks uses the header name "x-api-key" as the API
// key, a word an error naming the header would print, and drives every
// construction and request error path with it.
func TestHeaderNameKeyNeverLeaks(t *testing.T) {
	const key = "x-api-key"
	construction := map[string]Config{
		"unparseable base": {BaseURL: "https://example.com/%zz/" + key, APIKey: key, MaxTokens: 1},
		"relative base":    {BaseURL: key, APIKey: key, MaxTokens: 1},
		"query in base":    {BaseURL: "https://example.com/?" + key, APIKey: key, MaxTokens: 1},
		"userinfo in base": {BaseURL: "https://" + key + "@example.com", APIKey: key, MaxTokens: 1},
		"no max tokens":    {BaseURL: "https://example.com", APIKey: key},
		"key with newline": {BaseURL: "https://example.com", APIKey: key + "\r\n", MaxTokens: 1},
	}
	for name, cfg := range construction {
		_, err := New(cfg)
		if err == nil {
			t.Fatalf("%s: New() = nil error", name)
		}
		assertChainNoExactKey(t, err, key)
	}

	body := `{"type":"error","error":{"type":"x","message":"bad x-api-key header: x-api-key"}}`
	statuses := []int{400, 401, 403, 404, 408, 429, 500, 529}
	for _, status := range statuses {
		vs := newVendorServerWithKey(t, key, reply{status: status, body: body, header: map[string]string{"Retry-After": "3"}})
		p := newProviderWithKey(t, vs.URL, key)
		_, genErr := p.Generate(context.Background(), schemaRequest())
		_, listErr := p.ListModels(context.Background())
		verifyErr := p.Verify(context.Background())
		for _, err := range []error{genErr, listErr, verifyErr} {
			if err == nil {
				t.Fatalf("HTTP %d: nil error", status)
			}
			assertChainNoExactKey(t, err, key)
		}
	}

	ok := newVendorServerWithKey(t, key, reply{status: 200, fixture: "messages_text.json"})
	p := newProviderWithKey(t, ok.URL, key)
	requests := map[string]llm.Request{
		"unknown role":  {Messages: []llm.Message{{Role: llm.Role(key), Content: "hi"}}},
		"bad tool name": {Messages: textRequest().Messages, Schema: &llm.JSONSchema{Name: key + "!", Schema: dinnersSchema}},
		"schema array":  {Messages: textRequest().Messages, Schema: &llm.JSONSchema{Name: key, Schema: json.RawMessage(`[]`)}},
		"bad output":    {Messages: textRequest().Messages, Schema: &llm.JSONSchema{Name: key, Schema: dinnersSchema}},
	}
	for name, req := range requests {
		_, err := p.Generate(context.Background(), req)
		if err == nil {
			t.Fatalf("%s: nil error", name)
		}
		assertChainNoExactKey(t, err, key)
	}
	noModel, err := New(Config{BaseURL: ok.URL, APIKey: key, MaxTokens: 1})
	if err != nil {
		t.Fatal(err)
	}
	_, err = noModel.Generate(context.Background(), textRequest())
	if err == nil {
		t.Fatal("Generate succeeded with no model")
	}
	assertChainNoExactKey(t, err, key)

	refused := newVendorServer(t, reply{status: 200})
	refusedBase := refused.URL
	refused.Close()
	_, err = newProviderWithKey(t, refusedBase+"/"+key, key).Generate(context.Background(), textRequest())
	if err == nil {
		t.Fatal("Generate against a closed server succeeded")
	}
	assertChainNoExactKey(t, err, key)
}

// TestAdapterNameKeyOn401 uses the adapter's own error prefix "anthropic" as
// the API key.
func TestAdapterNameKeyOn401(t *testing.T) {
	const key = "anthropic"
	vs := newVendorServerWithKey(t, key, reply{status: 401, fixture: "error_authentication.json"})
	p := newProviderWithKey(t, vs.URL, key)
	_, genErr := p.Generate(context.Background(), schemaRequest())
	_, listErr := p.ListModels(context.Background())
	verifyErr := p.Verify(context.Background())
	for _, err := range []error{genErr, listErr, verifyErr} {
		if !errors.Is(err, llm.ErrAuth) {
			t.Fatalf("err = %v, want ErrAuth", err)
		}
		assertChainNoExactKey(t, err, key)
	}
}

// TestFixedWordKeyOn500 uses the fixed word "HTTP" of ProviderError's text
// as the API key: errors.As must still reach the ProviderError, whose Body
// and Error must drop it.
func TestFixedWordKeyOn500(t *testing.T) {
	const key = "HTTP"
	vs := newVendorServerWithKey(t, key, reply{status: 500, body: `{"type":"error","error":{"type":"api_error","message":"HTTP backend down"}}`})
	_, err := newProviderWithKey(t, vs.URL, key).Generate(context.Background(), schemaRequest())
	var pe *llm.ProviderError
	if !errors.Is(err, llm.ErrProvider) || !errors.As(err, &pe) || pe.Status() != 500 {
		t.Fatalf("err = %v, want a 500 ProviderError", err)
	}
	if strings.Contains(pe.Error(), key) || strings.Contains(pe.Body(), key) {
		t.Fatalf("ProviderError leaks the key: Error %q, Body %q", pe.Error(), pe.Body())
	}
	if !strings.Contains(pe.Body(), "backend down") {
		t.Fatalf("Body() = %q, want the redacted excerpt", pe.Body())
	}
	assertChainNoExactKey(t, err, key)
}

// TestRateLimitedFixedWordKey uses the fixed word "rate" of
// RateLimitedError's text as the API key, on both rate-limit statuses.
func TestRateLimitedFixedWordKey(t *testing.T) {
	const key = "rate"
	for _, status := range []int{429, 529} {
		vs := newVendorServerWithKey(t, key, reply{status: status, header: map[string]string{"Retry-After": "17"}, fixture: "error_rate_limit.json"})
		_, err := newProviderWithKey(t, vs.URL, key).Generate(context.Background(), schemaRequest())
		var rl *llm.RateLimitedError
		if !errors.Is(err, llm.ErrRateLimited) || !errors.As(err, &rl) || rl.RetryAfter != 17*time.Second {
			t.Fatalf("HTTP %d: err = %v, want a RateLimitedError with RetryAfter 17s", status, err)
		}
		assertChainNoExactKey(t, err, key)
	}
}

// TestResponseBodyWithKeyNeverLeaks has the vendor echo the key in error
// and success bodies, including ones that fail to decode, and checks
// Error(), every errors.Unwrap layer and every errors.As target.
func TestResponseBodyWithKeyNeverLeaks(t *testing.T) {
	bodies := []reply{
		{status: 500, fixture: "error_api.json"},
		{status: 400, body: `{"type":"error","error":{"type":"invalid_request_error","message":"key ` + testKey + ` rejected"}}`},
		{status: 401, fixture: "error_authentication.json"},
		{status: 429, fixture: "error_rate_limit.json"},
		{status: 200, body: `{"type": ` + testKey + `}`},
		{status: 200, body: testKey},
		{status: 200, body: `{"type":"message","content":[{"type":"text","text":"` + testKey + `"}],"stop_reason":"` + testKey + `"}`},
		{status: 200, body: `{"data": [` + testKey + `]}`},
	}
	for i, r := range bodies {
		vs := newVendorServer(t, r)
		p := newProvider(t, vs.URL)
		_, genErr := p.Generate(context.Background(), schemaRequest())
		_, listErr := p.ListModels(context.Background())
		verifyErr := p.Verify(context.Background())
		for _, err := range []error{genErr, listErr, verifyErr} {
			if err == nil {
				t.Fatalf("body %d: nil error", i)
			}
			assertNoSecret(t, err)
			assertChainNoExactKey(t, err, testKey)
		}
	}
	// A short key that decode errors quote from a malformed body.
	const key = "Z"
	vs := newVendorServerWithKey(t, key, reply{status: 200, body: `{"type": Z}`})
	_, err := newProviderWithKey(t, vs.URL, key).Generate(context.Background(), textRequest())
	var syntax *json.SyntaxError
	if err == nil || errors.As(err, &syntax) {
		t.Fatalf("err = %v, want no reachable *json.SyntaxError quoting the key", err)
	}
	assertChainNoExactKey(t, err, key)
}

// TestRedactKeepsChainMatches checks that the redaction backstop keeps
// errors.Is and errors.As working through a chain that holds the key.
func TestRedactKeepsChainMatches(t *testing.T) {
	pe := llm.NewProviderError(500, []byte(`{"error":"down"}`), testKey)
	rl := llm.NewRateLimitedError(time.Second, testKey)
	inner := fmt.Errorf("decode %s: %w", testKey, llm.ErrBadOutput)
	joined := errors.Join(inner, pe, rl, context.Canceled, &transportFailure{detail: "slow", kind: llm.ErrTimeout}, fmt.Errorf("HTTP 401: %w", llm.ErrAuth))
	err := redact(fmt.Errorf("anthropic %s: %w", testKey, joined), testKey)
	assertChainNoExactKey(t, err, testKey)
	for _, target := range []error{llm.ErrBadOutput, llm.ErrProvider, llm.ErrRateLimited, llm.ErrTimeout, llm.ErrAuth, context.Canceled} {
		if !errors.Is(err, target) {
			t.Fatalf("errors.Is(%v, %v) = false", err, target)
		}
	}
	var gotPE *llm.ProviderError
	var gotRL *llm.RateLimitedError
	if !errors.As(err, &gotPE) || gotPE != pe || !errors.As(err, &gotRL) || gotRL != rl {
		t.Fatalf("errors.As lost a typed error in %v", err)
	}
	clean := errors.New("anthropic: generate: no model configured")
	if redact(clean, testKey) != clean {
		t.Fatal("redact wrapped an error that holds no key")
	}
}

// TestRedactedErrorsFormatWithoutKey formats every error type that holds the
// key, or a cause holding it, with %v, %+v and %#v; %#v prints GoString.
func TestRedactedErrorsFormatWithoutKey(t *testing.T) {
	const key = "sk-distinctive-anthropic-format-7c1e"
	single := redact(fmt.Errorf("anthropic %s: %w", key, llm.ErrBadOutput), key)
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

func TestRateLimitedHugeRetryAfterIsCapped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "9223372036854775807")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(srv.Close)
	p, err := New(Config{BaseURL: srv.URL, APIKey: testKey, Model: "test-model", MaxTokens: 1024})
	if err != nil {
		t.Fatal(err)
	}
	err = p.Verify(context.Background())
	var rl *llm.RateLimitedError
	if !errors.Is(err, llm.ErrRateLimited) || !errors.As(err, &rl) || rl.RetryAfter != time.Duration(maxRetryAfterSeconds)*time.Second {
		t.Fatalf("err = %v, want a RateLimitedError with RetryAfter capped to %v", err, time.Duration(maxRetryAfterSeconds)*time.Second)
	}
}

func TestRedirectIsNotFollowed(t *testing.T) {
	var mu sync.Mutex
	var secondCalls int
	var secondHeader http.Header
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		secondCalls++
		secondHeader = req.Header.Clone()
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(second.Close)
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, second.URL+req.URL.Path, http.StatusFound)
	}))
	t.Cleanup(first.Close)
	p, err := New(Config{BaseURL: first.URL, APIKey: testKey, Model: "test-model", MaxTokens: 1024})
	if err != nil {
		t.Fatal(err)
	}

	err = p.Verify(context.Background())
	var pe *llm.ProviderError
	if !errors.Is(err, llm.ErrProvider) || !errors.As(err, &pe) || pe.Status() != http.StatusFound {
		t.Fatalf("err = %v, want a ProviderError for HTTP 302", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if secondCalls != 0 {
		for name, values := range secondHeader {
			for _, v := range values {
				if strings.Contains(v, testKey) {
					t.Errorf("redirect target saw the key in %s", name)
				}
			}
		}
		t.Fatalf("redirect target got %d requests, want 0", secondCalls)
	}
}
