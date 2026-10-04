// Package presets holds what MealQuest knows about each language-model
// vendor — base URL, adapter, credentials, headers, structured output mode
// and model quirks — and FromSettings, which turns the household's
// llm.Settings into a ready llm.Provider.
//
// FromSettings lives here rather than in package llm because it builds the
// adapters, and every adapter imports llm.
//
// Nothing in this package logs, and no error it returns contains the API
// key or quotes a setting's value.
package presets

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"slices"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/llm"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/llm/anthropic"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/llm/ollama"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/llm/openai"
)

// Adapter names the llm adapter package a preset builds.
type Adapter string

// The adapters.
const (
	AdapterAnthropic Adapter = "anthropic"
	AdapterOpenAI    Adapter = "openai"
	AdapterOllama    Adapter = "ollama"
)

// KeyPolicy is whether a preset sends an API key.
type KeyPolicy int

// The key policies.
const (
	// KeyRequired presets fail without a key.
	KeyRequired KeyPolicy = iota
	// KeyOptional presets send a key when one is given.
	KeyOptional
	// KeyNone presets send no key and refuse one.
	KeyNone
)

// TemperatureRejectingModels is the one list of model-id patterns that
// reject the temperature field: OpenAI's o1 and o3 reasoning models. A
// preset whose OmitTemperature holds it never sends a temperature to a
// matching model.
var TemperatureRejectingModels = []*regexp.Regexp{
	regexp.MustCompile(`^o1`),
	regexp.MustCompile(`^o3`),
}

// Preset is one vendor's fixed configuration.
type Preset struct {
	// Name is the Settings.Provider value that selects the preset.
	Name string
	// Adapter is the adapter the preset builds.
	Adapter Adapter
	// BaseURL is the API root the adapter appends its endpoint paths to.
	// Empty means the household must supply it in Settings.BaseURL.
	BaseURL string
	// Key is whether the preset sends an API key.
	Key KeyPolicy
	// RequiresPublicURL means the preset needs Settings.PublicURL for its
	// headers.
	RequiresPublicURL bool
	// Headers returns the extra request headers an openai-adapter preset
	// sends; nil means none.
	Headers func(llm.Settings) map[string]string
	// StructuredOutput is the default mode of an openai-adapter preset.
	StructuredOutput openai.StructuredOutput
	// FallbackStructuredOutput is the mode the household can select when
	// the default fails for its model; empty means none. FromSettings
	// builds one mode and never retries a request in another.
	FallbackStructuredOutput openai.StructuredOutput
	// StructuredOutputs are the modes Settings.StructuredOutput may select.
	StructuredOutputs []openai.StructuredOutput
	// MaxTokens is the max_tokens the anthropic adapter sends when a
	// Request sets none.
	MaxTokens int
	// OmitTemperature are model-id patterns the preset never sends a
	// temperature to.
	OmitTemperature []*regexp.Regexp
}

// openRouterHeaders identify MealQuest to OpenRouter: HTTP-Referer is
// MEALQUEST_PUBLIC_URL and X-Title is the app name.
func openRouterHeaders(s llm.Settings) map[string]string {
	return map[string]string{
		"HTTP-Referer": s.PublicURL,
		"X-Title":      "MealQuest",
	}
}

// omitsTemperature reports whether model matches one of the preset's
// OmitTemperature patterns.
func (p Preset) omitsTemperature(model string) bool {
	return slices.ContainsFunc(p.OmitTemperature, func(re *regexp.Regexp) bool {
		return re.MatchString(model)
	})
}

// Lookup returns the preset Settings.Provider name selects.
func Lookup(name string) (Preset, bool) {
	p, ok := all[name]
	return p, ok
}

// all is every preset, by Settings.Provider name.
var all = map[string]Preset{
	"anthropic": {
		Name:      "anthropic",
		Adapter:   AdapterAnthropic,
		BaseURL:   "https://api.anthropic.com",
		Key:       KeyRequired,
		MaxTokens: 4096,
	},
	"openai": {
		Name:              "openai",
		Adapter:           AdapterOpenAI,
		BaseURL:           "https://api.openai.com/v1",
		Key:               KeyRequired,
		StructuredOutput:  openai.JSONSchema,
		StructuredOutputs: []openai.StructuredOutput{openai.JSONSchema},
		OmitTemperature:   TemperatureRejectingModels,
	},
	"openrouter": {
		Name:                     "openrouter",
		Adapter:                  AdapterOpenAI,
		BaseURL:                  "https://openrouter.ai/api/v1",
		Key:                      KeyRequired,
		RequiresPublicURL:        true,
		Headers:                  openRouterHeaders,
		StructuredOutput:         openai.JSONSchema,
		FallbackStructuredOutput: openai.JSONObject,
		StructuredOutputs:        []openai.StructuredOutput{openai.JSONSchema, openai.JSONObject},
	},
	"ollama": {
		Name:    "ollama",
		Adapter: AdapterOllama,
		Key:     KeyNone,
	},
	"openai_compatible": {
		Name:              "openai_compatible",
		Adapter:           AdapterOpenAI,
		Key:               KeyOptional,
		StructuredOutput:  openai.JSONSchema,
		StructuredOutputs: []openai.StructuredOutput{openai.JSONSchema, openai.JSONObject, openai.PromptOnly},
		OmitTemperature:   TemperatureRejectingModels,
	},
}

// FromSettings returns the Provider s describes, using key as its API key.
// It fails with a configuration error from package llm — ErrUnknownProvider,
// ErrMissingKey, ErrUnexpectedKey, ErrMissingBaseURL, ErrMissingModel,
// ErrMissingPublicURL or ErrInvalidSetting — before contacting any vendor.
//
// The Provider sends s.Temperature with every request whose Temperature is
// nil; a request that sets one keeps it. Neither is sent to a model the
// preset's OmitTemperature matches.
func FromSettings(s llm.Settings, key string) (llm.Provider, error) {
	p, ok := Lookup(s.Provider)
	if !ok {
		return nil, llm.ErrUnknownProvider
	}
	if err := check(p, s, key); err != nil {
		return nil, fmt.Errorf("%s: %w", p.Name, err)
	}
	base := p.BaseURL
	if base == "" {
		base = s.BaseURL
	}

	var inner llm.Provider
	var err error
	switch p.Adapter {
	case AdapterAnthropic:
		inner, err = anthropic.New(anthropic.Config{BaseURL: base, APIKey: key, Model: s.Model, MaxTokens: p.MaxTokens})
	case AdapterOllama:
		inner, err = ollama.New(ollama.Config{BaseURL: base, Model: s.Model})
	case AdapterOpenAI:
		mode := p.StructuredOutput
		if s.StructuredOutput != "" {
			mode = openai.StructuredOutput(s.StructuredOutput)
		}
		cfg := openai.Config{BaseURL: base, APIKey: key, Model: s.Model, StructuredOutput: mode}
		if p.Headers != nil {
			cfg.Headers = p.Headers(s)
		}
		inner, err = openai.New(cfg)
	default:
		panic("presets: preset " + p.Name + " has unknown adapter " + string(p.Adapter))
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w: %w", p.Name, llm.ErrInvalidSetting, err)
	}

	cp := &configured{Provider: inner}
	if !p.omitsTemperature(s.Model) {
		t := s.Temperature
		cp.temperature = &t
	}
	return cp, nil
}

// check validates s and key against p. Its errors never quote a value.
func check(p Preset, s llm.Settings, key string) error {
	switch {
	case p.Key == KeyRequired && key == "":
		return llm.ErrMissingKey
	case p.Key == KeyNone && key != "":
		return llm.ErrUnexpectedKey
	}
	switch {
	case p.BaseURL == "" && s.BaseURL == "":
		return llm.ErrMissingBaseURL
	case p.BaseURL != "" && s.BaseURL != "" && s.BaseURL != p.BaseURL:
		return fmt.Errorf("%w: base URL is fixed by the preset", llm.ErrInvalidSetting)
	}
	if s.Model == "" {
		return llm.ErrMissingModel
	}
	if p.RequiresPublicURL && s.PublicURL == "" {
		return llm.ErrMissingPublicURL
	}
	if s.StructuredOutput != "" && !slices.Contains(p.StructuredOutputs, openai.StructuredOutput(s.StructuredOutput)) {
		return fmt.Errorf("%w: structured output mode is not supported by the preset", llm.ErrInvalidSetting)
	}
	if math.IsNaN(s.Temperature) || math.IsInf(s.Temperature, 0) || s.Temperature < 0 {
		return fmt.Errorf("%w: temperature must be a non-negative number", llm.ErrInvalidSetting)
	}
	return nil
}

// configured applies the household temperature and the preset's
// temperature quirk to every Generate.
type configured struct {
	llm.Provider
	// temperature is sent when a request sets none; nil means the model
	// rejects temperature, so none is ever sent.
	temperature *float64
}

func (c *configured) Generate(ctx context.Context, req llm.Request) (llm.Response, error) {
	if c.temperature == nil {
		req.Temperature = nil
	} else if req.Temperature == nil {
		t := *c.temperature
		req.Temperature = &t
	}
	return c.Provider.Generate(ctx, req)
}
