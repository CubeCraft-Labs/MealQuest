package aisettings

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/harness"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/llm"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/llm/presets"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/household"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/secretbox"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/migrations"
)

const testKey = "sk-ant-test-secret-key-41ab"

// newService returns a Service over a migrated store with the seed AI
// settings.
func newService(t *testing.T) (*Service, *store.Store) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := store.Migrate(ctx, st.Writer, migrations.FS); err != nil {
		t.Fatal(err)
	}
	if err := household.SeedAISettings(ctx, st); err != nil {
		t.Fatal(err)
	}
	raw := make([]byte, secretbox.KeySize)
	rand.Read(raw)
	box, err := secretbox.New(base64.StdEncoding.EncodeToString(raw))
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(st, box, "https://mealquest.example")
	if err != nil {
		t.Fatal(err)
	}
	return s, st
}

// useFake makes s build f for every run, verification and model list, and
// returns a func reporting the settings and key each build was given.
func useFake(s *Service, f *llm.Fake) func() ([]llm.Settings, []string) {
	var mu sync.Mutex
	var settings []llm.Settings
	var keys []string
	record := func(set llm.Settings, key string) {
		mu.Lock()
		defer mu.Unlock()
		settings = append(settings, set)
		keys = append(keys, key)
	}
	s.newProvider = func(set llm.Settings, key string) (llm.Provider, error) {
		record(set, key)
		return f, nil
	}
	s.newAccount = func(set llm.Settings, key string) (presets.Account, error) {
		record(set, key)
		return f, nil
	}
	return func() ([]llm.Settings, []string) {
		mu.Lock()
		defer mu.Unlock()
		return settings, keys
	}
}

func ptr[T any](v T) *T { return &v }

// echoSkill asks the model to repeat a word; its prompt is the harness's
// test template prompts/echo.v1.tmpl.
type echoSkill struct{}

type echoInput struct{ Word string }

type echoOutput struct {
	Echo string `json:"echo"`
}

func (echoSkill) Name() string { return "echo" }
func (echoSkill) Version() int { return 1 }
func (echoSkill) PromptInput(in echoInput) (harness.PromptInput, error) {
	return harness.PromptInput{Data: in}, nil
}
func (echoSkill) Schema() llm.JSONSchema {
	return llm.JSONSchema{Name: "echo", Schema: json.RawMessage(
		`{"type":"object","properties":{"echo":{"type":"string"}},"required":["echo"],"additionalProperties":false}`)}
}
func (echoSkill) Validate(echoOutput) []string                   { return nil }
func (echoSkill) PostProcess(out echoOutput) (echoOutput, error) { return out, nil }
func (echoSkill) Timeout() time.Duration                         { return harness.DefaultTimeout }

// A PATCHed model and temperature are what the next skill run uses: the
// provider is built with them, the request carries the temperature and the
// ai_runs row records both. A second PATCH changes the run after it.
func TestPatchedModelAndTemperatureUsedByNextRun(t *testing.T) {
	ctx := context.Background()
	s, st := newService(t)
	f := &llm.Fake{}
	built := useFake(s, f)
	guard, err := harness.NewGuard(harness.DefaultMaxCalls)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetAPIKey(ctx, testKey); err != nil {
		t.Fatal(err)
	}

	for i, want := range []struct {
		model string
		temp  float64
	}{{"claude-patched", 0.3}, {"claude-repatched", 1}} {
		if _, err := s.Update(ctx, Patch{Model: ptr(want.model), Temperature: ptr(want.temp)}); err != nil {
			t.Fatal(err)
		}
		cfg, err := s.HarnessConfig(ctx, guard)
		if err != nil {
			t.Fatal(err)
		}
		f.QueueGenerate(llm.GenerateResult{Response: llm.Response{JSON: json.RawMessage(`{"echo":"hi"}`)}})
		if _, err := harness.Run(ctx, cfg, echoSkill{}, "test", echoInput{Word: "hi"}); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}

		settings, keys := built()
		if got := settings[i]; got.Provider != "anthropic" || got.Model != want.model || got.Temperature != want.temp || keys[i] != testKey {
			t.Errorf("run %d provider built with %+v, key ok %v; want model %s at %v", i, got, keys[i] == testKey, want.model, want.temp)
		}
		req := f.Requests()[i]
		if req.Temperature == nil || *req.Temperature != want.temp {
			t.Errorf("run %d request temperature = %v, want %v", i, req.Temperature, want.temp)
		}
		if !strings.HasPrefix(req.System, household.DefaultPersonaPrompt[:40]) {
			t.Errorf("run %d system prompt does not start with the persona: %.80q", i, req.System)
		}
		var model string
		var temp float64
		err = st.Reader.QueryRowContext(ctx, `SELECT model, temperature FROM ai_runs ORDER BY created_at DESC, rowid DESC LIMIT 1`).Scan(&model, &temp)
		if err != nil {
			t.Fatal(err)
		}
		if model != want.model || temp != want.temp {
			t.Errorf("run %d ai_runs row = %s at %v, want %s at %v", i, model, temp, want.model, want.temp)
		}
	}
}

func TestUpdateValidates(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name  string
		patch Patch
		field string
	}{
		{"empty", Patch{}, "settings"},
		{"unknown provider", Patch{Provider: ptr("gemini")}, "provider"},
		{"temperature above 1", Patch{Temperature: ptr(1.1)}, "temperature"},
		{"negative temperature", Patch{Temperature: ptr(-0.1)}, "temperature"},
		{"temperature between steps", Patch{Temperature: ptr(0.35)}, "temperature"},
		{"empty model", Patch{Model: ptr(" ")}, "model"},
		{"long system prompt", Patch{SystemPrompt: ptr(strings.Repeat("ü", MaxSystemPromptChars+1))}, "system_prompt"},
		{"other base URL for a fixed preset", Patch{BaseURL: ptr("https://api.example/v1")}, "base_url"},
		{"bad base URL", Patch{Provider: ptr("ollama"), BaseURL: ptr("ftp://ollama:11434")}, "base_url"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, _ := newService(t)
			before, err := s.Get(ctx)
			if err != nil {
				t.Fatal(err)
			}
			_, err = s.Update(ctx, c.patch)
			var invalid *ValidationError
			if !errors.As(err, &invalid) || len(invalid.Problems) != 1 || invalid.Problems[0].Field != c.field {
				t.Fatalf("Update = %v, want one %s problem", err, c.field)
			}
			if after, err := s.Get(ctx); err != nil || !reflect.DeepEqual(after, before) {
				t.Errorf("settings changed by a refused patch: %+v, %v", after, err)
			}
		})
	}
}

func TestUpdateAccepts(t *testing.T) {
	ctx := context.Background()
	s, _ := newService(t)
	for _, temp := range []float64{0, 0.1, 0.2, 0.3, 0.6, 0.7, 0.9, 1} {
		got, err := s.Update(ctx, Patch{Temperature: ptr(temp)})
		if err != nil || got.Temperature != temp {
			t.Errorf("temperature %v = %v, %v", temp, got.Temperature, err)
		}
	}
	prompt := strings.Repeat("ü", MaxSystemPromptChars)
	if got, err := s.Update(ctx, Patch{SystemPrompt: &prompt}); err != nil || *got.SystemPrompt != prompt {
		t.Errorf("system prompt of %d characters: %v", MaxSystemPromptChars, err)
	}
	for _, p := range presets.Names() {
		patch := Patch{Provider: ptr(p), BaseURL: ptr("")}
		if pr, _ := presets.Lookup(p); pr.BaseURL == "" {
			patch.BaseURL = ptr("http://llm.local:11434")
		}
		if got, err := s.Update(ctx, patch); err != nil || got.Provider != p {
			t.Errorf("provider %s: %+v, %v", p, got, err)
		}
	}
}

// While a key is stored the provider and base URL cannot change, so the
// key never reaches an endpoint it was not stored for; the model,
// temperature and system prompt can, keeping the verification.
func TestEndpointLockedWhileKeyStored(t *testing.T) {
	ctx := context.Background()
	s, _ := newService(t)
	f := &llm.Fake{}
	useFake(s, f)
	if _, err := s.Update(ctx, Patch{Provider: ptr("openai_compatible"), BaseURL: ptr("https://llm.example/v1")}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetAPIKey(ctx, testKey); err != nil {
		t.Fatal(err)
	}
	f.QueueVerify(nil)
	if got, err := s.Verify(ctx); err != nil || got.KeyVerifiedAt == nil {
		t.Fatalf("Verify = %+v, %v", got, err)
	}
	got, err := s.Update(ctx, Patch{Model: ptr("m"), Temperature: ptr(0.5), SystemPrompt: ptr("p")})
	if err != nil || got.KeyVerifiedAt == nil {
		t.Errorf("model change = %+v, %v; want verification kept", got, err)
	}
	before, err := s.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		patch Patch
		field string
	}{
		{Patch{BaseURL: ptr("https://attacker.example/v1")}, "base_url"},
		{Patch{Provider: ptr("openai"), BaseURL: ptr("")}, "provider"},
		{Patch{Provider: ptr("ollama"), BaseURL: ptr("http://localhost:11434")}, "provider"},
	} {
		var invalid *ValidationError
		_, err := s.Update(ctx, c.patch)
		if !errors.As(err, &invalid) || !slices.ContainsFunc(invalid.Problems, func(p Problem) bool { return p.Field == c.field }) {
			t.Errorf("Update(%+v) with a key stored = %v, want a %s problem", c.patch, err, c.field)
		}
	}
	if after, err := s.Get(ctx); err != nil || !reflect.DeepEqual(after, before) {
		t.Errorf("refused endpoint change altered the settings: %+v, %v", after, err)
	}

	// Removing the key frees the endpoint; a new key is unverified.
	if _, err := s.SetAPIKey(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update(ctx, Patch{Provider: ptr("openai"), BaseURL: ptr("")}); err != nil {
		t.Fatal(err)
	}
	got, err = s.SetAPIKey(ctx, "sk-another-key-9999")
	if err != nil || got.KeyVerifiedAt != nil || *got.APIKeyLast4 != "9999" {
		t.Errorf("new key = %+v, %v; want it unverified", got, err)
	}
}

// A key is never stored for a base URL it would cross the network to in
// plain text.
func TestSetAPIKeyNeedsSecureBaseURL(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		base string
		ok   bool
	}{
		{"", false},
		{"http://llm.lan:8080/v1", false},
		{"http://127.0.0.1:8080/v1", true},
		{"https://llm.example/v1", true},
	} {
		s, _ := newService(t)
		patch := Patch{Provider: ptr("openai_compatible")}
		if c.base != "" {
			patch.BaseURL = ptr(c.base)
		}
		if _, err := s.Update(ctx, patch); err != nil {
			t.Fatal(err)
		}
		_, err := s.SetAPIKey(ctx, testKey)
		var invalid *ValidationError
		if c.ok != (err == nil) || (!c.ok && !errors.As(err, &invalid)) {
			t.Errorf("SetAPIKey with base URL %q = %v, want ok %v", c.base, err, c.ok)
		}
	}
}

func TestVerifyReturnsProviderTypedError(t *testing.T) {
	ctx := context.Background()
	s, _ := newService(t)
	f := &llm.Fake{}
	useFake(s, f)
	if _, err := s.SetAPIKey(ctx, testKey); err != nil {
		t.Fatal(err)
	}
	for _, want := range []error{llm.ErrAuth, llm.NewRateLimitedError(time.Second, testKey), llm.NewProviderError(500, []byte("bad "+testKey), testKey)} {
		f.QueueVerify(want)
		_, err := s.Verify(ctx)
		if !errors.Is(err, ErrProviderFailed) || !errors.Is(err, want) {
			t.Errorf("Verify = %v, want ErrProviderFailed and %v", err, want)
		}
		if err != nil && strings.Contains(err.Error(), testKey) {
			t.Errorf("Verify error holds the key: %v", err)
		}
	}
	if got, err := s.Get(ctx); err != nil || got.KeyVerifiedAt != nil {
		t.Errorf("failed verification recorded: %+v, %v", got, err)
	}
}

// A key error that quotes the key, as a misbehaving adapter might, is
// redacted by the service.
func TestVerifyRedactsKey(t *testing.T) {
	ctx := context.Background()
	s, _ := newService(t)
	f := &llm.Fake{}
	useFake(s, f)
	if _, err := s.SetAPIKey(ctx, testKey); err != nil {
		t.Fatal(err)
	}
	f.QueueVerify(errors.Join(llm.ErrAuth, errors.New("rejected "+testKey)))
	_, err := s.Verify(ctx)
	if !errors.Is(err, llm.ErrAuth) || strings.Contains(err.Error(), testKey) {
		t.Errorf("Verify = %v, want ErrAuth without the key", err)
	}
	f.QueueListModels(llm.ListModelsResult{Err: errors.New("rejected " + testKey)})
	if _, err := s.Models(ctx); !errors.Is(err, ErrProviderFailed) || strings.Contains(err.Error(), testKey) {
		t.Errorf("Models = %v, want ErrProviderFailed without the key", err)
	}
	f.QueueListModels(llm.ListModelsResult{Models: []llm.Model{{ID: "m-" + testKey, DisplayName: "Model " + testKey}}})
	models, err := s.Models(ctx)
	if err != nil || len(models) != 1 || models[0] != (Model{ID: "m-[REDACTED]", DisplayName: "Model [REDACTED]"}) {
		t.Errorf("Models = %+v, %v, want the key redacted", models, err)
	}
}

// Settings changed while the provider verifies the key are not marked
// verified.
func TestVerifyRefusesChangedSettings(t *testing.T) {
	ctx := context.Background()
	s, _ := newService(t)
	if _, err := s.SetAPIKey(ctx, testKey); err != nil {
		t.Fatal(err)
	}
	s.newAccount = func(llm.Settings, string) (presets.Account, error) {
		return changingAccount{s: s}, nil
	}
	if _, err := s.Verify(ctx); !errors.Is(err, ErrChanged) {
		t.Fatalf("Verify = %v, want ErrChanged", err)
	}
	if got, err := s.Get(ctx); err != nil || got.KeyVerifiedAt != nil || *got.APIKeyLast4 != "5678" {
		t.Errorf("settings after a changed verification = %+v, %v", got, err)
	}
}

// changingAccount replaces the stored key while it verifies.
type changingAccount struct{ s *Service }

func (a changingAccount) Verify(ctx context.Context) error {
	_, err := a.s.SetAPIKey(ctx, "sk-replaced-5678")
	return err
}

func (changingAccount) ListModels(context.Context) ([]llm.Model, error) { return nil, nil }

func TestNotConfigured(t *testing.T) {
	ctx := context.Background()
	s, _ := newService(t)
	// The seed is anthropic with no key; the real presets refuse it.
	if _, err := s.Verify(ctx); !errors.Is(err, ErrNotConfigured) || !errors.Is(err, llm.ErrMissingKey) {
		t.Errorf("Verify = %v, want ErrNotConfigured and ErrMissingKey", err)
	}
	if _, err := s.Models(ctx); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("Models = %v, want ErrNotConfigured", err)
	}
}

func TestSetAPIKey(t *testing.T) {
	ctx := context.Background()
	s, st := newService(t)
	got, err := s.SetAPIKey(ctx, testKey)
	if err != nil || got.APIKeyLast4 == nil || *got.APIKeyLast4 != "41ab" {
		t.Fatalf("SetAPIKey = %+v, %v", got, err)
	}
	var ct []byte
	if err := st.Reader.QueryRowContext(ctx, `SELECT api_key_ct FROM ai_settings`).Scan(&ct); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(ct), testKey) {
		t.Error("the key is stored in plain text")
	}
	if key, err := s.key(mustRow(t, s)); err != nil || key != testKey {
		t.Errorf("stored key opens to %q, %v", key, err)
	}

	for _, bad := range []string{"short", "sk-bad\r\nkey"} {
		var invalid *ValidationError
		if _, err := s.SetAPIKey(ctx, bad); !errors.As(err, &invalid) {
			t.Errorf("SetAPIKey(%q) = %v, want a ValidationError", bad, err)
		}
	}

	// Ollama takes no key: neither storing one nor switching to it with one.
	var invalid *ValidationError
	if _, err := s.Update(ctx, Patch{Provider: ptr("ollama"), BaseURL: ptr("http://ollama:11434")}); !errors.As(err, &invalid) {
		t.Errorf("switch to ollama with a key = %v, want a ValidationError", err)
	}
	if got, err := s.SetAPIKey(ctx, ""); err != nil || got.APIKeyLast4 != nil {
		t.Fatalf("remove key = %+v, %v", got, err)
	}
	if _, err := s.Update(ctx, Patch{Provider: ptr("ollama"), BaseURL: ptr("http://ollama:11434")}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetAPIKey(ctx, testKey); !errors.As(err, &invalid) {
		t.Errorf("SetAPIKey for ollama = %v, want a ValidationError", err)
	}
}

func TestResetPrompt(t *testing.T) {
	ctx := context.Background()
	s, _ := newService(t)
	if _, err := s.Update(ctx, Patch{SystemPrompt: ptr("")}); err != nil {
		t.Fatal(err)
	}
	got, err := s.ResetPrompt(ctx)
	if err != nil || got.SystemPrompt == nil || *got.SystemPrompt != household.DefaultPersonaPrompt {
		t.Errorf("ResetPrompt = %+v, %v", got, err)
	}
}

func mustRow(t *testing.T, s *Service) db.AiSetting {
	t.Helper()
	row, err := s.read(context.Background(), db.New(s.st.Reader))
	if err != nil {
		t.Fatal(err)
	}
	return row
}
