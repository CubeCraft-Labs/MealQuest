package presets

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/llm"
)

const testKey = "sk-test-0123456789"

// captured is the HTTP request an adapter sent.
type captured struct {
	url    string
	header http.Header
	body   map[string]json.RawMessage
}

var errStop = errors.New("recorded")

// recordingTransport records each request and fails it, so no request
// leaves the process.
type recordingTransport struct{ reqs []captured }

func (rt *recordingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c := captured{url: r.URL.String(), header: r.Header.Clone()}
	if r.Body != nil {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &c.body); err != nil {
			return nil, err
		}
	}
	rt.reqs = append(rt.reqs, c)
	return nil, errStop
}

// generate builds the provider s describes and returns the request its
// Generate sends for req. The adapters use http.DefaultTransport, which the
// test replaces, so the real preset base URLs are never contacted.
func generate(t *testing.T, s llm.Settings, key string, req llm.Request) captured {
	t.Helper()
	rt := &recordingTransport{}
	orig := http.DefaultTransport
	http.DefaultTransport = rt
	t.Cleanup(func() { http.DefaultTransport = orig })

	p, err := FromSettings(s, key)
	if err != nil {
		t.Fatalf("FromSettings: %v", err)
	}
	if _, err := p.Generate(context.Background(), req); err == nil {
		t.Fatal("Generate succeeded through the recording transport")
	}
	if len(rt.reqs) != 1 {
		t.Fatalf("sent %d requests, want 1", len(rt.reqs))
	}
	return rt.reqs[0]
}

func plainRequest() llm.Request {
	return llm.Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "dinner?"}}}
}

func schemaRequest() llm.Request {
	r := plainRequest()
	r.Schema = &llm.JSONSchema{Name: "side_quests", Schema: json.RawMessage(`{"type":"object"}`)}
	return r
}

func ptr(f float64) *float64 { return &f }

func responseFormatType(t *testing.T, c captured) string {
	t.Helper()
	raw, ok := c.body["response_format"]
	if !ok {
		return ""
	}
	var rf struct{ Type string }
	if err := json.Unmarshal(raw, &rf); err != nil {
		t.Fatalf("response_format: %v", err)
	}
	return rf.Type
}

func temperature(t *testing.T, c captured) (float64, bool) {
	t.Helper()
	raw, ok := c.body["temperature"]
	if !ok {
		return 0, false
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("temperature: %v", err)
	}
	return f, true
}

func TestAnthropicPreset(t *testing.T) {
	c := generate(t, llm.Settings{Provider: "anthropic", Model: "claude-sonnet-5", Temperature: 0.7}, testKey, plainRequest())
	if c.url != "https://api.anthropic.com/v1/messages" {
		t.Fatalf("URL = %q", c.url)
	}
	if got := c.header.Get("X-Api-Key"); got != testKey {
		t.Fatalf("x-api-key = %q", got)
	}
	if got := c.header.Get("Authorization"); got != "" {
		t.Fatalf("Authorization = %q, want none", got)
	}
	if got, ok := temperature(t, c); !ok || got != 0.7 {
		t.Fatalf("temperature = %v, %v; want 0.7", got, ok)
	}
	if string(c.body["max_tokens"]) != "4096" {
		t.Fatalf("max_tokens = %s", c.body["max_tokens"])
	}
}

func TestAnthropicPresetAcceptsItsOwnBaseURL(t *testing.T) {
	c := generate(t, llm.Settings{Provider: "anthropic", BaseURL: "https://api.anthropic.com", Model: "claude-sonnet-5"}, testKey, plainRequest())
	if c.url != "https://api.anthropic.com/v1/messages" {
		t.Fatalf("URL = %q", c.url)
	}
}

func TestOpenAIPreset(t *testing.T) {
	c := generate(t, llm.Settings{Provider: "openai", Model: "gpt-4o", Temperature: 0.4}, testKey, schemaRequest())
	if c.url != "https://api.openai.com/v1/chat/completions" {
		t.Fatalf("URL = %q", c.url)
	}
	if got := c.header.Get("Authorization"); got != "Bearer "+testKey {
		t.Fatalf("Authorization = %q", got)
	}
	if got := responseFormatType(t, c); got != "json_schema" {
		t.Fatalf("response_format type = %q, want json_schema", got)
	}
	if c.header.Get("HTTP-Referer") != "" || c.header.Get("X-Title") != "" {
		t.Fatal("openai sends OpenRouter headers")
	}
}

func TestOpenRouterPreset(t *testing.T) {
	s := llm.Settings{Provider: "openrouter", Model: "openai/gpt-4o", PublicURL: "https://meals.example.com/"}
	c := generate(t, s, testKey, schemaRequest())
	if c.url != "https://openrouter.ai/api/v1/chat/completions" {
		t.Fatalf("URL = %q", c.url)
	}
	if got := c.header.Get("Authorization"); got != "Bearer "+testKey {
		t.Fatalf("Authorization = %q", got)
	}
	if got := c.header.Get("HTTP-Referer"); got != "https://meals.example.com/" {
		t.Fatalf("HTTP-Referer = %q", got)
	}
	if got := c.header.Get("X-Title"); got != "MealQuest" {
		t.Fatalf("X-Title = %q", got)
	}
	if got := responseFormatType(t, c); got != "json_schema" {
		t.Fatalf("default response_format type = %q, want json_schema", got)
	}
}

func TestOpenRouterFallbackModeIsJSONObject(t *testing.T) {
	p, _ := Lookup("openrouter")
	s := llm.Settings{
		Provider:         "openrouter",
		Model:            "meta-llama/llama-3.3-70b-instruct",
		PublicURL:        "https://meals.example.com",
		StructuredOutput: string(p.FallbackStructuredOutput),
	}
	c := generate(t, s, testKey, schemaRequest())
	if got := responseFormatType(t, c); got != "json_object" {
		t.Fatalf("response_format type = %q, want json_object", got)
	}
}

func TestOllamaPreset(t *testing.T) {
	s := llm.Settings{Provider: "ollama", BaseURL: "http://ollama.lan:11434", Model: "llama3.2", Temperature: 0.5}
	c := generate(t, s, "", plainRequest())
	if c.url != "http://ollama.lan:11434/api/chat" {
		t.Fatalf("URL = %q", c.url)
	}
	if got := c.header.Get("Authorization"); got != "" {
		t.Fatalf("Authorization = %q, want none", got)
	}
	var opts struct{ Temperature *float64 }
	if err := json.Unmarshal(c.body["options"], &opts); err != nil || opts.Temperature == nil || *opts.Temperature != 0.5 {
		t.Fatalf("options = %s", c.body["options"])
	}
}

func TestOpenAICompatiblePreset(t *testing.T) {
	base := llm.Settings{Provider: "openai_compatible", BaseURL: "http://llm.lan:8000/v1", Model: "qwen2.5"}

	c := generate(t, base, "", schemaRequest())
	if c.url != "http://llm.lan:8000/v1/chat/completions" {
		t.Fatalf("URL = %q", c.url)
	}
	if got := c.header.Get("Authorization"); got != "" {
		t.Fatalf("Authorization without key = %q, want none", got)
	}
	if got := responseFormatType(t, c); got != "json_schema" {
		t.Fatalf("default response_format type = %q, want json_schema", got)
	}

	c = generate(t, base, testKey, plainRequest())
	if got := c.header.Get("Authorization"); got != "Bearer "+testKey {
		t.Fatalf("Authorization with key = %q", got)
	}

	for mode, want := range map[string]string{"json_schema": "json_schema", "json_object": "json_object", "prompt_only": ""} {
		s := base
		s.StructuredOutput = mode
		if got := responseFormatType(t, generate(t, s, "", schemaRequest())); got != want {
			t.Errorf("mode %s: response_format type = %q, want %q", mode, got, want)
		}
	}
}

func TestTemperatureOmittedForRejectingModels(t *testing.T) {
	for _, provider := range []string{"openai", "openai_compatible"} {
		for _, model := range []string{"o1", "o1-mini", "o3-mini"} {
			s := llm.Settings{Provider: provider, BaseURL: "http://llm.lan/v1", Model: model, Temperature: 0.8}
			if provider == "openai" {
				s.BaseURL = ""
			}
			for _, req := range []llm.Request{plainRequest(), func() llm.Request { r := plainRequest(); r.Temperature = ptr(0.2); return r }()} {
				if got, ok := temperature(t, generate(t, s, testKey, req)); ok {
					t.Errorf("%s %s: temperature %v sent, want omitted", provider, model, got)
				}
			}
		}
	}
}

func TestTemperatureSentForOtherModels(t *testing.T) {
	for _, model := range []string{"gpt-4o", "gpt-4o-mini", "chatgpt-o1-like"} {
		c := generate(t, llm.Settings{Provider: "openai", Model: model, Temperature: 0.8}, testKey, plainRequest())
		if got, ok := temperature(t, c); !ok || got != 0.8 {
			t.Errorf("%s: temperature = %v, %v; want 0.8", model, got, ok)
		}
	}
	// A request's own temperature wins over the household's.
	req := plainRequest()
	req.Temperature = ptr(0.1)
	c := generate(t, llm.Settings{Provider: "openai", Model: "gpt-4o", Temperature: 0.8}, testKey, req)
	if got, ok := temperature(t, c); !ok || got != 0.1 {
		t.Fatalf("temperature = %v, %v; want 0.1", got, ok)
	}
}

func TestTemperatureQuirkPresets(t *testing.T) {
	want := map[string]bool{"openai": true, "openai_compatible": true}
	for name, p := range all {
		if got := len(p.OmitTemperature) > 0; got != want[name] {
			t.Errorf("%s: lists temperature quirk = %v, want %v", name, got, want[name])
		}
	}
}

func TestPresetTable(t *testing.T) {
	cases := []struct {
		name    string
		adapter Adapter
		baseURL string
		key     KeyPolicy
	}{
		{"anthropic", AdapterAnthropic, "https://api.anthropic.com", KeyRequired},
		{"openai", AdapterOpenAI, "https://api.openai.com/v1", KeyRequired},
		{"openrouter", AdapterOpenAI, "https://openrouter.ai/api/v1", KeyRequired},
		{"ollama", AdapterOllama, "", KeyNone},
		{"openai_compatible", AdapterOpenAI, "", KeyOptional},
	}
	if len(all) != len(cases) {
		t.Fatalf("%d presets, want %d", len(all), len(cases))
	}
	for _, c := range cases {
		p, ok := Lookup(c.name)
		if !ok {
			t.Fatalf("no preset %q", c.name)
		}
		if p.Name != c.name || p.Adapter != c.adapter || p.BaseURL != c.baseURL || p.Key != c.key {
			t.Errorf("%s = {%s %s %q %d}", c.name, p.Name, p.Adapter, p.BaseURL, p.Key)
		}
	}
}

func TestConfigErrors(t *testing.T) {
	sentinels := []error{
		llm.ErrUnknownProvider, llm.ErrMissingKey, llm.ErrUnexpectedKey, llm.ErrMissingBaseURL,
		llm.ErrMissingModel, llm.ErrMissingPublicURL, llm.ErrInvalidSetting,
	}
	cases := []struct {
		name string
		s    llm.Settings
		key  string
		want error
	}{
		{"unknown provider", llm.Settings{Provider: "gemini", Model: "m"}, testKey, llm.ErrUnknownProvider},
		{"anthropic no key", llm.Settings{Provider: "anthropic", Model: "m"}, "", llm.ErrMissingKey},
		{"openai no key", llm.Settings{Provider: "openai", Model: "m"}, "", llm.ErrMissingKey},
		{"openrouter no key", llm.Settings{Provider: "openrouter", Model: "m", PublicURL: "https://x.example"}, "", llm.ErrMissingKey},
		{"ollama with key", llm.Settings{Provider: "ollama", BaseURL: "http://o:11434", Model: "m"}, testKey, llm.ErrUnexpectedKey},
		{"ollama no base URL", llm.Settings{Provider: "ollama", Model: "m"}, "", llm.ErrMissingBaseURL},
		{"openai_compatible no base URL", llm.Settings{Provider: "openai_compatible", Model: "m"}, "", llm.ErrMissingBaseURL},
		{"anthropic no model", llm.Settings{Provider: "anthropic"}, testKey, llm.ErrMissingModel},
		{"openai no model", llm.Settings{Provider: "openai"}, testKey, llm.ErrMissingModel},
		{"openrouter no model", llm.Settings{Provider: "openrouter", PublicURL: "https://x.example"}, testKey, llm.ErrMissingModel},
		{"ollama no model", llm.Settings{Provider: "ollama", BaseURL: "http://o:11434"}, "", llm.ErrMissingModel},
		{"openai_compatible no model", llm.Settings{Provider: "openai_compatible", BaseURL: "http://l/v1"}, "", llm.ErrMissingModel},
		{"openrouter no public URL", llm.Settings{Provider: "openrouter", Model: "m"}, testKey, llm.ErrMissingPublicURL},
		{"fixed base URL overridden", llm.Settings{Provider: "openai", BaseURL: "https://evil.example/v1", Model: "m"}, testKey, llm.ErrInvalidSetting},
		{"openai json_object", llm.Settings{Provider: "openai", Model: "m", StructuredOutput: "json_object"}, testKey, llm.ErrInvalidSetting},
		{"anthropic mode", llm.Settings{Provider: "anthropic", Model: "m", StructuredOutput: "json_schema"}, testKey, llm.ErrInvalidSetting},
		{"unknown mode", llm.Settings{Provider: "openai_compatible", BaseURL: "http://l/v1", Model: "m", StructuredOutput: "xml"}, "", llm.ErrInvalidSetting},
		{"negative temperature", llm.Settings{Provider: "openai", Model: "m", Temperature: -0.1}, testKey, llm.ErrInvalidSetting},
		{"bad base URL", llm.Settings{Provider: "ollama", BaseURL: "ftp://o", Model: "m"}, "", llm.ErrInvalidSetting},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, err := FromSettings(c.s, c.key)
			if p != nil || !errors.Is(err, c.want) {
				t.Fatalf("FromSettings = %v, %v; want %v", p, err, c.want)
			}
			for _, other := range sentinels {
				if other != c.want && errors.Is(err, other) {
					t.Fatalf("error %v also matches %v", err, other)
				}
			}
			if c.key != "" && strings.Contains(err.Error(), c.key) {
				t.Fatalf("error leaks the key: %v", err)
			}
		})
	}
}

func TestReadyProviderForEveryPreset(t *testing.T) {
	for _, s := range []struct {
		s   llm.Settings
		key string
	}{
		{llm.Settings{Provider: "anthropic", Model: "m"}, testKey},
		{llm.Settings{Provider: "openai", Model: "m"}, testKey},
		{llm.Settings{Provider: "openrouter", Model: "m", PublicURL: "https://x.example"}, testKey},
		{llm.Settings{Provider: "ollama", BaseURL: "http://o:11434", Model: "m"}, ""},
		{llm.Settings{Provider: "openai_compatible", BaseURL: "http://l/v1", Model: "m"}, ""},
	} {
		if p, err := FromSettings(s.s, s.key); err != nil || p == nil {
			t.Errorf("%s: FromSettings = %v, %v", s.s.Provider, p, err)
		}
	}
}
