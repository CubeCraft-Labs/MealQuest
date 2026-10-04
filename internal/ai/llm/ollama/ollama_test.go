package ollama

import (
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
	fixture string // testdata file
	body    string // used when fixture is empty
	delay   time.Duration
}

// ollamaServer replays its replies in order, repeating the last, and
// records every request.
type ollamaServer struct {
	*httptest.Server
	mu  sync.Mutex
	got []recorded
}

func newOllamaServer(t *testing.T, replies ...reply) *ollamaServer {
	t.Helper()
	payloads := make([][]byte, len(replies))
	for i, r := range replies {
		payloads[i] = []byte(r.body)
		if r.fixture != "" {
			raw, err := os.ReadFile(filepath.Join("testdata", r.fixture))
			if err != nil {
				t.Fatal(err)
			}
			payloads[i] = raw
		}
	}
	s := &ollamaServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		s.mu.Lock()
		n := len(s.got)
		s.got = append(s.got, recorded{req.Method, req.URL.Path, req.URL.Query(), req.Header.Clone(), body})
		s.mu.Unlock()
		i := min(n, len(replies)-1)
		r := replies[i]
		if r.delay > 0 {
			select {
			case <-time.After(r.delay):
			case <-req.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(r.status)
		w.Write(payloads[i])
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *ollamaServer) requests() []recorded {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]recorded(nil), s.got...)
}

func (s *ollamaServer) only(t *testing.T) recorded {
	t.Helper()
	got := s.requests()
	if len(got) != 1 {
		t.Fatalf("server got %d requests, want 1", len(got))
	}
	return got[0]
}

func newProvider(t *testing.T, base string) *Provider {
	t.Helper()
	p, err := New(Config{BaseURL: base, Model: "llama3.2"})
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

func assertNoAuth(t *testing.T, r recorded) {
	t.Helper()
	if got := r.header.Values("Authorization"); len(got) != 0 {
		t.Errorf("Authorization = %q, want none", got)
	}
}

func TestGenerateText(t *testing.T) {
	s := newOllamaServer(t, reply{status: 200, fixture: "chat_text.json"})
	p := newProvider(t, s.URL+"/")

	resp, err := p.Generate(context.Background(), textRequest())
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "Try a crock pot chili: it is a Quick dinner." || resp.JSON != nil {
		t.Errorf("Text, JSON = %q, %s", resp.Text, resp.JSON)
	}
	if resp.Usage != (llm.Usage{InputTokens: 26, OutputTokens: 298}) || resp.Model != "llama3.2" || resp.FinishReason != "stop" {
		t.Errorf("Usage, Model, FinishReason = %+v, %q, %q", resp.Usage, resp.Model, resp.FinishReason)
	}

	got := s.only(t)
	if got.method != http.MethodPost || got.path != "/api/chat" || len(got.query) != 0 {
		t.Errorf("request = %s %s?%s", got.method, got.path, got.query.Encode())
	}
	assertNoAuth(t, got)
	body := sentBody(t, got)
	if stream, ok := body["stream"]; !ok || stream != false {
		t.Errorf("stream = %v (present %v), want false", stream, ok)
	}
	if body["model"] != "llama3.2" {
		t.Errorf("model = %v", body["model"])
	}
	for _, field := range []string{"format", "options"} {
		if v, ok := body[field]; ok {
			t.Errorf("%s = %v, want it omitted", field, v)
		}
	}
	if got, want := canonical(t, mustMarshal(t, body["messages"])), `[{"content":"What should we cook tonight?","role":"user"}]`; got != want {
		t.Errorf("messages = %s, want %s", got, want)
	}
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestGenerateSchemaOutput(t *testing.T) {
	s := newOllamaServer(t, reply{status: 200, fixture: "chat_schema.json"})
	p := newProvider(t, s.URL)

	resp, err := p.Generate(context.Background(), schemaRequest())
	if err != nil {
		t.Fatal(err)
	}
	want := `{"dinners":[{"name":"Sheet-pan gnocchi","why":"Like your Regular tray bake, but quicker."}]}`
	if got := canonical(t, resp.JSON); got != want {
		t.Errorf("Response.JSON = %s, want %s", got, want)
	}
	if canonical(t, []byte(resp.Text)) != want {
		t.Errorf("Text = %q, want the reply content", resp.Text)
	}
	if resp.Usage != (llm.Usage{InputTokens: 147, OutputTokens: 41}) || resp.FinishReason != "stop" {
		t.Errorf("Usage, FinishReason = %+v, %q", resp.Usage, resp.FinishReason)
	}

	got := s.only(t)
	assertNoAuth(t, got)
	body := sentBody(t, got)
	if body["stream"] != false {
		t.Errorf("stream = %v, want false", body["stream"])
	}
	if got, want := canonical(t, mustMarshal(t, body["format"])), canonical(t, dinnersSchema); got != want {
		t.Errorf("format = %s, want the schema %s", got, want)
	}
	opts, _ := body["options"].(map[string]any)
	if len(opts) != 2 || opts["temperature"] != 0.0 || opts["num_predict"] != 400.0 {
		t.Errorf("options = %v, want temperature 0 and num_predict 400", body["options"])
	}
	wantMessages := `[{"content":"You plan dinners for Our kitchen.","role":"system"},` +
		`{"content":"Suggest a Side quest.","role":"user"},` +
		`{"content":"Something quick?","role":"assistant"},` +
		`{"content":"Yes, a Quick dinner.","role":"user"}]`
	if got := canonical(t, mustMarshal(t, body["messages"])); got != wantMessages {
		t.Errorf("messages = %s, want %s", got, wantMessages)
	}
}

func TestGenerateTemperatureOnlyWhenSet(t *testing.T) {
	s := newOllamaServer(t, reply{status: 200, fixture: "chat_text.json"})
	req := textRequest()
	temp := 0.7
	req.Temperature = &temp
	if _, err := newProvider(t, s.URL).Generate(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	opts, _ := sentBody(t, s.only(t))["options"].(map[string]any)
	if len(opts) != 1 || opts["temperature"] != 0.7 {
		t.Errorf("options = %v, want only temperature 0.7", opts)
	}
}

func TestGenerateBadOutput(t *testing.T) {
	cases := map[string]string{
		"prose":        `{"model":"m","message":{"role":"assistant","content":"Sure! Try chili."},"done":true,"done_reason":"stop"}`,
		"array":        `{"model":"m","message":{"role":"assistant","content":"[1,2]"},"done":true,"done_reason":"stop"}`,
		"cut-off JSON": `{"model":"m","message":{"role":"assistant","content":"{\"dinners\": ["},"done":true,"done_reason":"length"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			s := newOllamaServer(t, reply{status: 200, body: body})
			resp, err := newProvider(t, s.URL).Generate(context.Background(), schemaRequest())
			if !errors.Is(err, llm.ErrBadOutput) {
				t.Fatalf("err = %v, want ErrBadOutput", err)
			}
			if resp.JSON != nil || resp.Model != "m" || resp.Text == "" {
				t.Fatalf("Response = %+v, want no JSON and the reply kept", resp)
			}
		})
	}
}

func TestGenerateRejectsMalformedResponses(t *testing.T) {
	for name, body := range map[string]string{
		"not JSON":     `not json`,
		"not finished": `{"model":"m","message":{"role":"assistant","content":"hi"},"done":false}`,
		"no message":   `{"model":"m","done":true}`,
	} {
		s := newOllamaServer(t, reply{status: 200, body: body})
		_, err := newProvider(t, s.URL).Generate(context.Background(), textRequest())
		if err == nil || errors.Is(err, llm.ErrBadOutput) || errors.Is(err, llm.ErrProvider) {
			t.Errorf("%s: err = %v, want an untyped malformed-response error", name, err)
		}
	}
}

func TestGenerateRejectsInvalidRequests(t *testing.T) {
	s := newOllamaServer(t, reply{status: 200, fixture: "chat_text.json"})
	if _, err := newProvider(t, s.URL).Generate(context.Background(), llm.Request{}); err == nil {
		t.Error("Generate accepted a request with no messages")
	}
	noModel, err := New(Config{BaseURL: s.URL})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := noModel.Generate(context.Background(), textRequest()); err == nil {
		t.Error("Generate ran with no model configured")
	}
	if n := len(s.requests()); n != 0 {
		t.Errorf("server got %d requests, want 0", n)
	}
}

func TestGenerateModelMissing(t *testing.T) {
	s := newOllamaServer(t, reply{status: 404, fixture: "error_model_not_found.json"})
	p, err := New(Config{BaseURL: s.URL, Model: "llama9"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Generate(context.Background(), textRequest())
	var pe *llm.ProviderError
	if !errors.Is(err, llm.ErrProvider) || !errors.As(err, &pe) || pe.Status() != http.StatusNotFound {
		t.Fatalf("err = %v, want a ProviderError for HTTP 404", err)
	}
	if errors.Is(err, llm.ErrTimeout) || errors.Is(err, llm.ErrBadOutput) {
		t.Fatalf("err = %v matches an unrelated sentinel", err)
	}
	if want := `ollama: generate: model "llama9" not found: llm: provider error: HTTP 404`; err.Error() != want {
		t.Fatalf("Error() = %q, want %q", err, want)
	}
	if pe.Body() != "" {
		t.Fatalf("Body() = %q, want the response body left out", pe.Body())
	}
}

func TestListModels(t *testing.T) {
	s := newOllamaServer(t, reply{status: 200, fixture: "tags.json"})
	models, err := newProvider(t, s.URL).ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []llm.Model{{ID: "llama3.2:latest", DisplayName: "llama3.2:latest"}, {ID: "qwen3:8b", DisplayName: "qwen3:8b"}}
	if len(models) != len(want) || models[0] != want[0] || models[1] != want[1] {
		t.Fatalf("ListModels() = %+v, want %+v", models, want)
	}
	got := s.only(t)
	if got.method != http.MethodGet || got.path != "/api/tags" || len(got.query) != 0 {
		t.Errorf("request = %s %s?%s", got.method, got.path, got.query.Encode())
	}
	assertNoAuth(t, got)

	empty := newOllamaServer(t, reply{status: 200, body: `{"models":[]}`})
	if models, err := newProvider(t, empty.URL).ListModels(context.Background()); err != nil || models == nil || len(models) != 0 {
		t.Fatalf("empty list: %v, %v", models, err)
	}
	for name, body := range map[string]string{
		"no models list": `{}`,
		"unnamed model":  `{"models":[{"model":"x"}]}`,
		"not JSON":       `<html>`,
	} {
		bad := newOllamaServer(t, reply{status: 200, body: body})
		if _, err := newProvider(t, bad.URL).ListModels(context.Background()); err == nil {
			t.Errorf("%s: ListModels accepted it", name)
		}
	}
}

func TestVerify(t *testing.T) {
	ok := newOllamaServer(t, reply{status: 200, fixture: "tags.json"})
	if err := newProvider(t, ok.URL).Verify(context.Background()); err != nil {
		t.Fatalf("Verify() = %v", err)
	}
	got := ok.only(t)
	if got.method != http.MethodGet || got.path != "/api/tags" {
		t.Errorf("Verify called %s %s", got.method, got.path)
	}
	assertNoAuth(t, got)

	bad := newOllamaServer(t, reply{status: 500, body: `{"error":"something went wrong"}`})
	err := newProvider(t, bad.URL).Verify(context.Background())
	var pe *llm.ProviderError
	if !errors.As(err, &pe) || pe.Status() != 500 || !strings.Contains(err.Error(), "something went wrong") {
		t.Fatalf("Verify() = %v, want a ProviderError for HTTP 500", err)
	}
	malformed := newOllamaServer(t, reply{status: 200, body: `{}`})
	if err := newProvider(t, malformed.URL).Verify(context.Background()); err == nil {
		t.Error("Verify accepted a body with no models list")
	}
}

func TestProviderErrors(t *testing.T) {
	cases := map[string]reply{
		"400":  {status: 400, body: `{"error":"invalid format"}`},
		"500":  {status: 500, body: `{"error":"llama runner process has terminated"}`},
		"503":  {status: 503, body: `{"error":"server busy, please try again"}`},
		"huge": {status: 500, body: strings.Repeat("x", 1<<20)},
	}
	for name, r := range cases {
		t.Run(name, func(t *testing.T) {
			s := newOllamaServer(t, r)
			p := newProvider(t, s.URL)
			_, genErr := p.Generate(context.Background(), schemaRequest())
			_, listErr := p.ListModels(context.Background())
			for _, err := range []error{genErr, listErr} {
				var pe *llm.ProviderError
				if !errors.As(err, &pe) || pe.Status() != r.status {
					t.Fatalf("err = %v, want a ProviderError for HTTP %d", err, r.status)
				}
				if errors.Is(err, llm.ErrTimeout) || strings.Contains(err.Error(), "not found") {
					t.Fatalf("err = %v, want only a provider error", err)
				}
				if len(err.Error()) > llm.MaxBodyExcerpt+200 {
					t.Fatalf("error text has %d bytes, want a bounded excerpt", len(err.Error()))
				}
			}
		})
	}
}

// assertUnreachable checks the error a request to an unreachable base
// returns: it matches llm.ErrTimeout and names the base.
func assertUnreachable(t *testing.T, err error, base string) {
	t.Helper()
	if !errors.Is(err, llm.ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
	if errors.Is(err, llm.ErrProvider) || errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v matches an unrelated sentinel", err)
	}
	if want := "Ollama not reachable at " + base; !strings.Contains(err.Error(), want) {
		t.Fatalf("Error() = %q, want it to contain %q", err, want)
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		t.Fatalf("error chain exposes the url.Error: %v", ue)
	}
}

func TestUnreachableHost(t *testing.T) {
	closed := httptest.NewServer(http.NotFoundHandler())
	base := closed.URL
	closed.Close()

	p := newProvider(t, base+"/")
	_, genErr := p.Generate(context.Background(), schemaRequest())
	_, listErr := p.ListModels(context.Background())
	verifyErr := p.Verify(context.Background())
	for _, err := range []error{genErr, listErr, verifyErr} {
		assertUnreachable(t, err, base)
		if !strings.Contains(err.Error(), "connection refused") {
			t.Errorf("Error() = %q, want the cause", err)
		}
	}

	dropped := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		conn.Close()
	}))
	t.Cleanup(dropped.Close)
	_, err := newProvider(t, dropped.URL).Generate(context.Background(), textRequest())
	assertUnreachable(t, err, dropped.URL)
}

func TestTimeouts(t *testing.T) {
	s := newOllamaServer(t, reply{status: 200, fixture: "chat_schema.json", delay: 5 * time.Second})
	p := newProvider(t, s.URL)

	req := schemaRequest()
	req.Timeout = 50 * time.Millisecond
	start := time.Now()
	_, err := p.Generate(context.Background(), req)
	assertUnreachable(t, err, s.URL)
	if !strings.Contains(err.Error(), llm.ErrTimeout.Error()) {
		t.Errorf("Error() = %q, want it to say the request timed out", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("Request.Timeout not honoured: took %v", elapsed)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = p.ListModels(ctx)
	assertUnreachable(t, err, s.URL)
	ctx, cancel = context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	assertUnreachable(t, p.Verify(ctx), s.URL)

	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	_, err = p.Generate(ctx, schemaRequest())
	if !errors.Is(err, context.Canceled) || errors.Is(err, llm.ErrTimeout) || strings.Contains(err.Error(), "not reachable") {
		t.Fatalf("cancelled context: err = %v, want context.Canceled only", err)
	}
}

func TestRedirectIsNotFollowed(t *testing.T) {
	var mu sync.Mutex
	var secondCalls int
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		secondCalls++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(second.Close)
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, second.URL+req.URL.Path, http.StatusFound)
	}))
	t.Cleanup(first.Close)
	p := newProvider(t, first.URL)

	_, genErr := p.Generate(context.Background(), textRequest())
	verifyErr := p.Verify(context.Background())
	for _, err := range []error{genErr, verifyErr} {
		var pe *llm.ProviderError
		if !errors.Is(err, llm.ErrProvider) || !errors.As(err, &pe) || pe.Status() != http.StatusFound {
			t.Fatalf("err = %v, want a ProviderError for HTTP 302", err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if secondCalls != 0 {
		t.Fatalf("redirect target got %d requests, want 0", secondCalls)
	}
}

func TestNewRejectsBadConfig(t *testing.T) {
	const secret = "hunter2-secret"
	cases := map[string]string{
		"empty base":         "",
		"relative base":      "ollama:11434",
		"host only":          "192.168.1.20",
		"ftp base":           "ftp://192.168.1.20:11434",
		"no host":            "http://",
		"credentials in URL": "http://user:" + secret + "@192.168.1.20:11434",
		"user in URL":        "http://" + secret + "@ollama:11434",
		"secret in query":    "http://ollama:11434/?token=" + secret,
		"secret in fragment": "http://ollama:11434/#" + secret,
		"unparseable":        "http://ollama:11434/%zz?token=" + secret,
		"empty query":        "http://ollama:11434?",
		"empty fragment":     "http://ollama:11434#",
		"empty both":         "http://ollama:11434/?#",
	}
	for name, base := range cases {
		_, err := New(Config{BaseURL: base, Model: "llama3.2"})
		if err == nil {
			t.Errorf("%s: New() = nil error", name)
			continue
		}
		if strings.Contains(err.Error(), secret) {
			t.Errorf("%s: error echoes the URL: %v", name, err)
		}
	}
	for _, base := range []string{
		"http://192.168.1.20:11434",
		"http://ollama:11434",
		"http://localhost:11434/",
		"https://ollama.lan/proxy/",
	} {
		if _, err := New(Config{BaseURL: base, Model: "llama3.2"}); err != nil {
			t.Errorf("New(%q) = %v", base, err)
		}
	}
}

func TestBaseWithPath(t *testing.T) {
	s := newOllamaServer(t, reply{status: 200, fixture: "tags.json"})
	if err := newProvider(t, s.URL+"/proxy/").Verify(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := s.only(t).path; got != "/proxy/api/tags" {
		t.Fatalf("path = %s, want /proxy/api/tags", got)
	}
}
