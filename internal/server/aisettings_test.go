package server

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"unicode/utf8"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/household"
)

const (
	aiPath            = "/api/v1/settings/ai"
	aiKeyPath         = "/api/v1/settings/ai/api-key"
	aiVerifyPath      = "/api/v1/settings/ai/verify"
	aiModelsPath      = "/api/v1/settings/ai/models"
	aiResetPromptPath = "/api/v1/settings/ai/reset-prompt"
	testAIKey         = "sk-test-secret-ai-key-7d21"
	testWrongAIKey    = "sk-test-wrong-ai-key-0b9e"
)

// newAIRouter returns a router with a fresh, seeded AI settings service.
func newAIRouter(t *testing.T) http.Handler {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	r, err := NewRouter(logger, "dev", nil, stubAuth{}, stubSessions{}, newTestSettings(t), newTestSearch(t), newTestAI(t), newTestDinners(t), newTestSuggest(t),
		newTestPlan(t), newTestShopping(t), newTestGoogle(t), newTestIdeas(t), newTestRunner(t), newTestPhotos(t), fstest.MapFS{"index.html": {Data: []byte(fixtureIndex)}})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// newModelsServer is an OpenAI-compatible server whose GET /models accepts
// only testAIKey. While echo is set it answers 500 with a body quoting the
// Authorization header it received, so a response that passed the vendor
// body through unredacted would leak the key. Its model list holds a model
// whose ID and name quote the key.
func newModelsServer(t *testing.T, echo *atomic.Bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		switch {
		case r.URL.Path != "/v1/models":
			http.NotFound(w, r)
		case echo.Load():
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, `{"error":{"message":"upstream failed for `+auth+`"}}`)
		case auth != "Bearer "+testAIKey:
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"error":{"message":"invalid key `+auth+`"}}`)
		default:
			io.WriteString(w, `{"data":[{"id":"gpt-test"},{"id":"gpt-test-mini","name":"GPT Test Mini"},`+
				`{"id":"gpt-`+testAIKey+`","name":"Leaky `+testAIKey+`"}]}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func errorCode(body map[string]any) any {
	e, _ := body["error"].(map[string]any)
	return e["code"]
}

// TestAISettingsAPI drives every /settings/ai route and scans every
// response body for the API keys.
func TestAISettingsAPI(t *testing.T) {
	t.Parallel()
	router := newAIRouter(t)
	var echo atomic.Bool
	vendor := newModelsServer(t, &echo)
	var bodies []string
	call := func(method, target, body string) (*httptest.ResponseRecorder, map[string]any) {
		t.Helper()
		var r io.Reader
		if body != "" {
			r = strings.NewReader(body)
		}
		rec, decoded := serve(t, router, signedIn(method, target, r))
		bodies = append(bodies, rec.Body.String())
		return rec, decoded
	}

	// The seed: anthropic, the default persona, no key.
	rec, body := call(http.MethodGet, aiPath, "")
	if rec.Code != http.StatusOK || body["provider"] != "anthropic" || body["system_prompt"] != household.DefaultPersonaPrompt ||
		body["temperature"] != 0.7 || body["api_key_last4"] != nil || body["key_verified_at"] != nil {
		t.Errorf("GET seed = %d %v", rec.Code, body)
	}
	for _, field := range []string{"provider", "base_url", "model", "temperature", "system_prompt", "api_key_last4", "key_verified_at"} {
		if _, ok := body[field]; !ok {
			t.Errorf("GET has no %s field: %v", field, body)
		}
	}
	for _, path := range []string{aiVerifyPath, aiModelsPath} {
		method := http.MethodPost
		if path == aiModelsPath {
			method = http.MethodGet
		}
		if rec, body := call(method, path, ""); rec.Code != http.StatusConflict || errorCode(body) != "not_configured" {
			t.Errorf("%s %s without a key = %d %v, want 409 not_configured", method, path, rec.Code, body)
		}
	}

	for _, bad := range []string{
		`{}`,
		`{"provider":"google"}`,
		`{"provider":"openai_compatible","base_url":"not a url"}`,
		`{"base_url":"https://evil.example"}`,
		`{"temperature":1.1}`,
		`{"temperature":-0.1}`,
		`{"temperature":0.25}`,
		`{"temperature":"0.5"}`,
		`{"model":""}`,
		`{"model":null}`,
		`{"system_prompt":"` + strings.Repeat("é", 8001) + `"}`,
		`{"system_prompt":"` + strings.Repeat("€", 8001) + `"}`,
		`{"system_prompt":"` + strings.Repeat("a", maxAISettingsBody) + `"}`,
		`{"api_key":"` + testAIKey + `"}`,
	} {
		if rec, body := call(http.MethodPatch, aiPath, bad); rec.Code != http.StatusBadRequest {
			t.Errorf("PATCH %.80s = %d %v, want 400", bad, rec.Code, body)
		}
	}

	// The longest prompt fits the body limit however its 8,000 characters
	// are written: three-byte, four-byte, or as \u-escaped surrogate pairs.
	for _, char := range []string{"€", "🍲", `\ud83c\udf72`} {
		rec, body := call(http.MethodPatch, aiPath, `{"system_prompt":"`+strings.Repeat(char, 8000)+`"}`)
		if prompt, _ := body["system_prompt"].(string); rec.Code != http.StatusOK || utf8.RuneCountInString(prompt) != 8000 {
			t.Errorf("PATCH 8,000 × %s = %d, %d characters", char, rec.Code, utf8.RuneCountInString(prompt))
		}
	}

	rec, body = call(http.MethodPatch, aiPath, `{"provider":"openai_compatible","base_url":"`+vendor.URL+`/v1","model":"gpt-test","temperature":0.3,"system_prompt":"`+strings.Repeat("é", 8000)+`"}`)
	if rec.Code != http.StatusOK || body["provider"] != "openai_compatible" || body["base_url"] != vendor.URL+"/v1" ||
		body["model"] != "gpt-test" || body["temperature"] != 0.3 {
		t.Errorf("PATCH openai_compatible = %d %v", rec.Code, body)
	}

	for _, bad := range []string{`{}`, `{"api_key":"short"}`, `{"api_key":"sk-line\nbreak"}`, `{"api_key":null}`, `{"key":"` + testAIKey + `"}`} {
		if rec, body := call(http.MethodPut, aiKeyPath, bad); rec.Code != http.StatusBadRequest {
			t.Errorf("PUT api-key %s = %d %v, want 400", bad, rec.Code, body)
		}
	}

	// A rejected key is a typed auth failure and records nothing.
	rec, body = call(http.MethodPut, aiKeyPath, `{"api_key":"`+testWrongAIKey+`"}`)
	if rec.Code != http.StatusOK || body["api_key_last4"] != "0b9e" || body["key_verified_at"] != nil {
		t.Errorf("PUT wrong key = %d %v", rec.Code, body)
	}
	if rec, body := call(http.MethodPost, aiVerifyPath, ""); rec.Code != http.StatusBadGateway || errorCode(body) != "auth_failed" {
		t.Errorf("POST verify with the wrong key = %d %v, want 502 auth_failed", rec.Code, body)
	}
	if rec, body := call(http.MethodGet, aiModelsPath, ""); rec.Code != http.StatusBadGateway || errorCode(body) != "auth_failed" {
		t.Errorf("GET models with the wrong key = %d %v, want 502 auth_failed", rec.Code, body)
	}

	rec, body = call(http.MethodPut, aiKeyPath, `{"api_key":"`+testAIKey+`"}`)
	if rec.Code != http.StatusOK || body["api_key_last4"] != "7d21" {
		t.Errorf("PUT key = %d %v", rec.Code, body)
	}
	if _, ok := body["api_key"]; ok {
		t.Errorf("PUT response has an api_key field: %v", body)
	}

	// The vendor quoting the key in an error body: the body excerpt
	// reaches the response redacted.
	echo.Store(true)
	for _, c := range []struct{ method, path string }{{http.MethodPost, aiVerifyPath}, {http.MethodGet, aiModelsPath}} {
		rec, body := call(c.method, c.path, "")
		if rec.Code != http.StatusBadGateway || errorCode(body) != "provider_error" || !strings.Contains(rec.Body.String(), "upstream failed for Bearer [REDACTED]") {
			t.Errorf("%s %s against an echoing vendor = %d %v, want 502 provider_error", c.method, c.path, rec.Code, body)
		}
	}
	echo.Store(false)

	rec, body = call(http.MethodPost, aiVerifyPath, "")
	if rec.Code != http.StatusOK || body["key_verified_at"] == nil || body["api_key_last4"] != "7d21" {
		t.Errorf("POST verify = %d %v", rec.Code, body)
	}
	rec, body = call(http.MethodGet, aiPath, "")
	if rec.Code != http.StatusOK || body["key_verified_at"] == nil || body["model"] != "gpt-test" || body["temperature"] != 0.3 {
		t.Errorf("GET after verify = %d %v", rec.Code, body)
	}

	rec, body = call(http.MethodGet, aiModelsPath, "")
	models, _ := body["models"].([]any)
	if rec.Code != http.StatusOK || len(models) != 3 {
		t.Fatalf("GET models = %d %v", rec.Code, body)
	}
	if m := models[1].(map[string]any); m["id"] != "gpt-test-mini" || m["display_name"] != "GPT Test Mini" {
		t.Errorf("GET models[1] = %v", m)
	}
	if m := models[2].(map[string]any); m["id"] != "gpt-[REDACTED]" || m["display_name"] != "Leaky [REDACTED]" {
		t.Errorf("GET models[2] = %v, want the key redacted", m)
	}

	rec, body = call(http.MethodPatch, aiPath, `{"system_prompt":"Be brief."}`)
	if rec.Code != http.StatusOK || body["system_prompt"] != "Be brief." || body["key_verified_at"] == nil {
		t.Errorf("PATCH system_prompt = %d %v", rec.Code, body)
	}
	rec, body = call(http.MethodPost, aiResetPromptPath, "")
	if rec.Code != http.StatusOK || body["system_prompt"] != household.DefaultPersonaPrompt {
		t.Errorf("POST reset-prompt = %d %v", rec.Code, body)
	}

	// The stored key is never pointed at another server or provider.
	for _, patch := range []string{`{"base_url":"https://attacker.example/v1"}`, `{"provider":"openai","base_url":""}`} {
		if rec, body := call(http.MethodPatch, aiPath, patch); rec.Code != http.StatusBadRequest || errorCode(body) != "invalid_settings" {
			t.Errorf("PATCH %s with a key stored = %d %v, want 400 invalid_settings", patch, rec.Code, body)
		}
	}
	if rec, body := call(http.MethodPut, aiKeyPath, `{"api_key":""}`); rec.Code != http.StatusOK || body["api_key_last4"] != nil || body["key_verified_at"] != nil {
		t.Errorf("PUT empty key = %d %v", rec.Code, body)
	}
	rec, body = call(http.MethodPatch, aiPath, `{"base_url":"https://llm.example/v1"}`)
	if rec.Code != http.StatusOK || body["base_url"] != "https://llm.example/v1" {
		t.Errorf("PATCH base_url without a key = %d %v", rec.Code, body)
	}

	if len(bodies) < 30 {
		t.Fatalf("scanned only %d response bodies", len(bodies))
	}
	for _, b := range bodies {
		for _, key := range []string{testAIKey, testWrongAIKey} {
			if strings.Contains(b, key) {
				t.Errorf("response holds an API key: %s", b)
			}
		}
	}
}

func TestAISettingsWritesNeedCSRF(t *testing.T) {
	t.Parallel()
	router := newAIRouter(t)
	for _, c := range []struct{ method, path, body string }{
		{http.MethodPatch, aiPath, `{"temperature":0.5}`},
		{http.MethodPut, aiKeyPath, `{"api_key":"` + testAIKey + `"}`},
		{http.MethodPost, aiVerifyPath, ""},
		{http.MethodPost, aiResetPromptPath, ""},
	} {
		req := httptest.NewRequest(c.method, c.path, strings.NewReader(c.body))
		req.AddCookie(validSessionCookie)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s without the CSRF header = %d, want 403", c.method, c.path, rec.Code)
		}
	}
	rec, body := serve(t, router, signedIn(http.MethodGet, aiPath, nil))
	if rec.Code != http.StatusOK || body["api_key_last4"] != nil || body["temperature"] != 0.7 {
		t.Errorf("GET after refused writes = %d %v, want the seed", rec.Code, body)
	}
}
