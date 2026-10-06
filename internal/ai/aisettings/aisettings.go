// Package aisettings reads and changes the household's AI settings — the
// ai_settings row seeded by household.SeedAISettings — and builds the
// provider and harness.Config they describe. The API key is sealed with the
// secret box and never leaves the package except to the provider.
package aisettings

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strings"
	"unicode/utf8"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/harness"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/llm"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/llm/presets"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/household"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/secretbox"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/secureurl"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

const (
	// MaxSystemPromptChars is the longest system prompt accepted, in
	// characters.
	MaxSystemPromptChars = 8000
	// MinKeyLength is the shortest API key accepted, so its last four
	// characters never give away most of it.
	MinKeyLength = 8
)

var (
	// ErrNotConfigured is returned when the stored AI settings cannot
	// build a provider, e.g. there is no API key yet. It is joined with
	// the llm configuration error that says why.
	ErrNotConfigured = errors.New("aisettings: AI provider is not configured")
	// ErrProviderFailed is returned when a call to the provider fails. It
	// is joined with the provider's error, so the llm typed error it
	// matches says why.
	ErrProviderFailed = errors.New("aisettings: AI provider call failed")
	// ErrChanged is returned when the AI settings changed while
	// their key was being verified, so the result applies to old settings.
	ErrChanged = errors.New("aisettings: AI settings changed during verification")
)

// Settings is the household's AI configuration as the API shows it. The
// API key is never part of it: only its last four characters.
type Settings struct {
	Provider    string  `json:"provider"`
	BaseURL     string  `json:"base_url"`
	Model       string  `json:"model"`
	Temperature float64 `json:"temperature"`
	// SystemPrompt is the admin persona, nil when none is stored.
	SystemPrompt *string `json:"system_prompt"`
	// APIKeyLast4 is nil when no key is stored.
	APIKeyLast4 *string `json:"api_key_last4"`
	// KeyVerifiedAt is when the stored key was last verified with these
	// provider settings, nil when it has not been.
	KeyVerifiedAt *string `json:"key_verified_at"`
	UpdatedAt     string  `json:"updated_at"`
}

// Patch changes the settings. A nil field is left as it is.
type Patch struct {
	Provider     *string  `json:"provider"`
	BaseURL      *string  `json:"base_url"`
	Model        *string  `json:"model"`
	Temperature  *float64 `json:"temperature"`
	SystemPrompt *string  `json:"system_prompt"`
}

// Model is one model the provider offers.
type Model struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

// Problem is one invalid field, named by its JSON key.
type Problem struct {
	Field  string
	Reason string
}

// ValidationError lists every problem with an Patch or API
// key, or with the settings it would leave. No problem quotes the key.
type ValidationError struct {
	Problems []Problem
}

func (e *ValidationError) Error() string {
	parts := make([]string, len(e.Problems))
	for i, p := range e.Problems {
		parts[i] = p.Field + ": " + p.Reason
	}
	return "invalid AI settings: " + strings.Join(parts, "; ")
}

// Service reads and changes the ai_settings row and builds the provider it
// names. API keys are sealed with its secret box and never leave it except
// to the provider.
type Service struct {
	st        *store.Store
	box       *secretbox.Box
	publicURL string
	// newProvider and newAccount are presets.FromSettings and
	// presets.ForAccount; tests substitute a fake provider.
	newProvider harness.NewProvider
	newAccount  func(llm.Settings, string) (presets.Account, error)
}

// New returns the service over st, sealing keys with box.
// publicURL is MEALQUEST_PUBLIC_URL, which some presets send.
func New(st *store.Store, box *secretbox.Box, publicURL string) (*Service, error) {
	switch {
	case st == nil:
		return nil, errors.New("aisettings: nil store")
	case box == nil:
		return nil, errors.New("aisettings: nil secret box")
	case publicURL == "":
		return nil, errors.New("aisettings: empty public URL")
	}
	return &Service{st: st, box: box, publicURL: publicURL, newProvider: presets.FromSettings, newAccount: presets.ForAccount}, nil
}

// Get returns the settings.
func (s *Service) Get(ctx context.Context) (Settings, error) {
	row, err := s.read(ctx, db.New(s.st.Reader))
	if err != nil {
		return Settings{}, err
	}
	return settingsFrom(row), nil
}

// Update applies patch and returns the settings it leaves. The provider
// and base URL cannot change while an API key is stored, so the key never
// reaches an endpoint it was not stored for. Every problem is reported in
// a *ValidationError and nothing changes.
func (s *Service) Update(ctx context.Context, patch Patch) (Settings, error) {
	return s.change(ctx, func(row db.AiSetting) (db.AiSetting, error) {
		return applyPatch(row, patch)
	})
}

// SetAPIKey seals key and records its last four characters, clearing
// key_verified_at. An empty key removes the stored one. A preset without
// a fixed base URL takes a key only when its base URL is secure (see
// secureurl.Check).
func (s *Service) SetAPIKey(ctx context.Context, key string) (Settings, error) {
	return s.change(ctx, func(row db.AiSetting) (db.AiSetting, error) {
		row.ApiKeyCt, row.ApiKeyLast4, row.KeyVerifiedAt = nil, sql.NullString{}, sql.NullString{}
		if key == "" {
			return row, nil
		}
		p, _ := presets.Lookup(row.Provider)
		var reason string
		switch {
		case p.Key == presets.KeyNone:
			reason = row.Provider + " takes no API key"
		case p.BaseURL == "" && !secureBaseURL(row.BaseUrl):
			reason = "is sent only over https://, or http:// to localhost: set such a base_url first"
		case !validHeaderValue(key):
			reason = "has a character not allowed in a header"
		case len(key) < MinKeyLength:
			reason = fmt.Sprintf("must be at least %d characters", MinKeyLength)
		}
		if reason != "" {
			return db.AiSetting{}, &ValidationError{Problems: []Problem{{Field: "api_key", Reason: reason}}}
		}
		ct, err := s.box.Encrypt([]byte(key))
		if err != nil {
			return db.AiSetting{}, fmt.Errorf("aisettings: seal AI API key: %w", err)
		}
		row.ApiKeyCt = ct
		row.ApiKeyLast4 = sql.NullString{String: key[len(key)-4:], Valid: true}
		return row, nil
	})
}

// ResetPrompt restores household.DefaultPersonaPrompt as the system prompt.
func (s *Service) ResetPrompt(ctx context.Context) (Settings, error) {
	return s.change(ctx, func(row db.AiSetting) (db.AiSetting, error) {
		row.SystemPrompt = sql.NullString{String: household.DefaultPersonaPrompt, Valid: true}
		return row, nil
	})
}

// Verify checks the stored key with the provider and, when it is
// accepted, records key_verified_at. Settings that cannot build a
// provider fail with ErrNotConfigured; a provider failure matches
// ErrProviderFailed and the llm typed error the provider returned. If the
// settings change while the provider is asked, nothing is recorded and the
// error is ErrChanged.
func (s *Service) Verify(ctx context.Context) (Settings, error) {
	row, err := s.read(ctx, db.New(s.st.Reader))
	if err != nil {
		return Settings{}, err
	}
	acct, key, err := s.account(row)
	if err != nil {
		return Settings{}, err
	}
	if err := acct.Verify(ctx); err != nil {
		return Settings{}, redact(fmt.Errorf("%w: verify: %w", ErrProviderFailed, err), key)
	}
	verified := store.Now().String()
	return s.change(ctx, func(now db.AiSetting) (db.AiSetting, error) {
		if now.Provider != row.Provider || now.BaseUrl != row.BaseUrl || !bytes.Equal(now.ApiKeyCt, row.ApiKeyCt) {
			return db.AiSetting{}, ErrChanged
		}
		now.KeyVerifiedAt = sql.NullString{String: verified, Valid: true}
		return now, nil
	})
}

// Models returns the provider's live model list. Its errors are Verify's.
func (s *Service) Models(ctx context.Context) ([]Model, error) {
	row, err := s.read(ctx, db.New(s.st.Reader))
	if err != nil {
		return nil, err
	}
	acct, key, err := s.account(row)
	if err != nil {
		return nil, err
	}
	models, err := acct.ListModels(ctx)
	if err != nil {
		return nil, redact(fmt.Errorf("%w: list models: %w", ErrProviderFailed, err), key)
	}
	out := make([]Model, len(models))
	for i, m := range models {
		out[i] = Model{ID: m.ID, DisplayName: m.DisplayName}
	}
	return out, nil
}

// HarnessConfig returns the harness.Config for a skill run with the
// current settings, sharing guard. Read it for each run, so a run always
// uses the latest saved provider, model, temperature and persona.
func (s *Service) HarnessConfig(ctx context.Context, guard *harness.Guard) (harness.Config, error) {
	row, err := s.read(ctx, db.New(s.st.Reader))
	if err != nil {
		return harness.Config{}, err
	}
	key, err := s.key(row)
	if err != nil {
		return harness.Config{}, err
	}
	return harness.Config{
		Settings:    s.llmSettings(row),
		APIKey:      key,
		NewProvider: s.newProvider,
		Persona:     row.SystemPrompt.String,
		Store:       s.st,
		Guard:       guard,
	}, nil
}

// account builds the provider's Account from row, returning the key too
// so the caller can redact it.
func (s *Service) account(row db.AiSetting) (presets.Account, string, error) {
	key, err := s.key(row)
	if err != nil {
		return nil, "", err
	}
	acct, err := s.newAccount(s.llmSettings(row), key)
	if err != nil {
		return nil, "", redact(fmt.Errorf("%w: %w", ErrNotConfigured, err), key)
	}
	return acct, key, nil
}

func (s *Service) llmSettings(row db.AiSetting) llm.Settings {
	return llm.Settings{
		Provider:    row.Provider,
		BaseURL:     row.BaseUrl,
		Model:       row.Model,
		Temperature: row.Temperature,
		PublicURL:   s.publicURL,
	}
}

// key opens row's API key; "" when none is stored.
func (s *Service) key(row db.AiSetting) (string, error) {
	if len(row.ApiKeyCt) == 0 {
		return "", nil
	}
	key, err := s.box.Decrypt(row.ApiKeyCt)
	if err != nil {
		return "", fmt.Errorf("aisettings: open AI API key: %w", err)
	}
	return string(key), nil
}

func (s *Service) read(ctx context.Context, q *db.Queries) (db.AiSetting, error) {
	row, err := q.GetAISettings(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return db.AiSetting{}, errors.New("aisettings: AI settings are not seeded")
	}
	if err != nil {
		return db.AiSetting{}, fmt.Errorf("aisettings: read AI settings: %w", err)
	}
	return row, nil
}

// change saves edit's result for the current row in one transaction.
func (s *Service) change(ctx context.Context, edit func(db.AiSetting) (db.AiSetting, error)) (Settings, error) {
	var saved db.AiSetting
	err := s.st.WithTx(ctx, func(tx *sql.Tx) error {
		q := db.New(tx)
		row, err := s.read(ctx, q)
		if err != nil {
			return err
		}
		next, err := edit(row)
		if err != nil {
			return err
		}
		saved, err = q.UpsertAISettings(ctx, db.UpsertAISettingsParams{
			Provider:      next.Provider,
			BaseUrl:       next.BaseUrl,
			Model:         next.Model,
			Temperature:   next.Temperature,
			SystemPrompt:  next.SystemPrompt,
			ApiKeyCt:      next.ApiKeyCt,
			ApiKeyLast4:   next.ApiKeyLast4,
			KeyVerifiedAt: next.KeyVerifiedAt,
			CreatedAt:     row.CreatedAt,
			UpdatedAt:     store.Now().String(),
		})
		if err != nil {
			return fmt.Errorf("aisettings: save AI settings: %w", err)
		}
		return nil
	})
	if err != nil {
		return Settings{}, err
	}
	return settingsFrom(saved), nil
}

// applyPatch returns row with patch applied, validated.
func applyPatch(row db.AiSetting, patch Patch) (db.AiSetting, error) {
	var problems []Problem
	fail := func(field, reason string) {
		problems = append(problems, Problem{Field: field, Reason: reason})
	}
	if patch == (Patch{}) {
		fail("settings", "no settings to change")
	}
	before := row
	if patch.Provider != nil {
		if _, ok := presets.Lookup(*patch.Provider); ok {
			row.Provider = *patch.Provider
		} else {
			fail("provider", "must be one of "+strings.Join(presets.Names(), ", "))
		}
	}
	if patch.BaseURL != nil {
		row.BaseUrl = *patch.BaseURL
	}
	if patch.Model != nil {
		switch m := *patch.Model; {
		case strings.TrimSpace(m) == "":
			fail("model", "must not be empty")
		case !validHeaderValue(m):
			fail("model", "must not contain control characters")
		default:
			row.Model = m
		}
	}
	if patch.Temperature != nil {
		t := *patch.Temperature
		tenths := math.Round(t * 10)
		if t < 0 || t > 1 || math.Abs(t*10-tenths) > 1e-9 {
			fail("temperature", "must be between 0 and 1 in steps of 0.1")
		} else {
			row.Temperature = tenths / 10
		}
	}
	if patch.SystemPrompt != nil {
		if n := utf8.RuneCountInString(*patch.SystemPrompt); n > MaxSystemPromptChars {
			fail("system_prompt", fmt.Sprintf("must be at most %d characters, got %d", MaxSystemPromptChars, n))
		} else {
			row.SystemPrompt = sql.NullString{String: *patch.SystemPrompt, Valid: true}
		}
	}
	if p, ok := presets.Lookup(row.Provider); ok && len(problems) == 0 {
		switch {
		case p.BaseURL != "" && row.BaseUrl != "" && row.BaseUrl != p.BaseURL:
			fail("base_url", fmt.Sprintf("is fixed for %s: must be empty or %s", p.Name, p.BaseURL))
		case p.BaseURL == "" && row.BaseUrl != "":
			// The preset's own check of the URL; its errors never quote it.
			if _, err := presets.ForAccount(llm.Settings{Provider: p.Name, BaseURL: row.BaseUrl}, ""); err != nil {
				fail("base_url", err.Error())
			}
		}
	}
	// The stored key is only ever sent where it was stored for: moving it
	// to another provider or server would let a changed base URL receive
	// it. Remove the key, change the endpoint, then store the new key.
	if len(problems) == 0 && len(row.ApiKeyCt) > 0 {
		if row.Provider != before.Provider {
			fail("provider", "cannot change while an API key is stored: remove the key first")
		}
		if row.BaseUrl != before.BaseUrl {
			fail("base_url", "cannot change while an API key is stored: remove the key first")
		}
	}
	if len(problems) > 0 {
		return db.AiSetting{}, &ValidationError{Problems: problems}
	}
	return row, nil
}

func settingsFrom(row db.AiSetting) Settings {
	return Settings{
		Provider:      row.Provider,
		BaseURL:       row.BaseUrl,
		Model:         row.Model,
		Temperature:   row.Temperature,
		SystemPrompt:  nullString(row.SystemPrompt),
		APIKeyLast4:   nullString(row.ApiKeyLast4),
		KeyVerifiedAt: nullString(row.KeyVerifiedAt),
		UpdatedAt:     row.UpdatedAt,
	}
}

func nullString(s sql.NullString) *string {
	if !s.Valid {
		return nil
	}
	return &s.String
}

// secureBaseURL reports whether base is a URL a key may be sent to.
func secureBaseURL(base string) bool {
	u, err := url.Parse(base)
	return base != "" && err == nil && secureurl.Check(u) == nil
}

// validHeaderValue reports whether value has no control character other
// than tab.
func validHeaderValue(value string) bool {
	for i := 0; i < len(value); i++ {
		if c := value[i]; (c < 0x20 && c != '\t') || c == 0x7f {
			return false
		}
	}
	return true
}

// redactedError is an error with the API key removed from its text. It
// does not unwrap, because the original may hold the key; Is still matches
// the sentinels the original wraps.
type redactedError struct {
	text string
	err  error
}

func (e *redactedError) Error() string { return e.text }

// Is reports whether the original error matches target.
func (e *redactedError) Is(target error) bool { return errors.Is(e.err, target) }

// redact returns err with key removed from its text; an empty key returns
// err unchanged.
func redact(err error, key string) error {
	if key == "" {
		return err
	}
	return &redactedError{text: llm.Redact(err.Error(), key), err: err}
}
