// Package presets holds what MealQuest knows about each language-model
// vendor — base URL, adapter, credentials, headers, structured output mode
// and model quirks — and FromSettings, which turns the household's
// llm.Settings into a ready llm.Provider.
//
// FromSettings lives here rather than in package llm because it builds the
// adapters, and every adapter imports llm.
//
// Nothing in this package logs, and no error it returns or unwraps to
// contains the API key, even where the key equals a setting's or request's
// value or text the error spells anyway.
package presets

import (
	"context"
	"errors"
	"fmt"
	"maps"
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

// The reasoning models that reject the temperature field are OpenAI's o1
// and o3 families: the bare family name or the name followed by a "-"
// variant (o1, o1-mini, o3-2025-04-16), never a longer word such as o123.
// OpenRouter names them with an "openai/" prefix and may add a ":" variant
// (openai/o1:online). A preset whose OmitTemperature holds a pattern never
// sends a temperature to a matching model.
var (
	openAIReasoningModels     = regexp.MustCompile(`^o[13](-.*)?$`)
	openRouterReasoningModels = regexp.MustCompile(`^openai/o[13]([-:].*)?$`)
)

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

// Names returns every preset's name, sorted.
func Names() []string {
	return slices.Sorted(maps.Keys(all))
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
		OmitTemperature:   []*regexp.Regexp{openAIReasoningModels},
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
		OmitTemperature:          []*regexp.Regexp{openRouterReasoningModels},
	},
	"xai": {
		Name:                     "xai",
		Adapter:                  AdapterOpenAI,
		BaseURL:                  "https://api.x.ai/v1",
		Key:                      KeyRequired,
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
		// The server may be OpenAI itself or a gateway that names models
		// the OpenRouter way.
		OmitTemperature: []*regexp.Regexp{openAIReasoningModels, openRouterReasoningModels},
	},
}

// FromSettings returns the Provider s describes, using key as its API key.
// It fails with a configuration error from package llm — ErrUnknownProvider,
// ErrMissingKey, ErrUnexpectedKey, ErrInvalidKey, ErrMissingBaseURL, ErrMissingModel,
// ErrMissingPublicURL or ErrInvalidSetting — before contacting any vendor.
//
// The Provider sends s.Temperature with every request whose Temperature is
// nil; a request that sets one keeps it. Neither is sent to a model the
// preset's OmitTemperature matches. It is wrapped in llm.NewRetrying, so
// Generate retries transient vendor failures and has the default timeout.
func FromSettings(s llm.Settings, key string) (llm.Provider, error) {
	cp, err := fromSettings(s, key, true)
	if err != nil {
		return nil, redact(err, key)
	}
	return llm.NewRetrying(cp), nil
}

// Account is what a provider offers before a model is chosen: the live
// model list and a check of the credentials.
type Account interface {
	ListModels(ctx context.Context) ([]llm.Model, error)
	Verify(ctx context.Context) error
}

// ForAccount returns the Account s describes, using key as its API key. It
// checks s and key as FromSettings does except that s.Model may be empty,
// so the household can verify its key and pick from the live model list
// before it has chosen a model. Neither call is retried.
func ForAccount(s llm.Settings, key string) (Account, error) {
	cp, err := fromSettings(s, key, false)
	if err != nil {
		return nil, redact(err, key)
	}
	return cp, nil
}

// fromSettings builds the provider s describes; needModel is whether
// s.Model is required.
func fromSettings(s llm.Settings, key string, needModel bool) (*configured, error) {
	p, ok := Lookup(s.Provider)
	if !ok {
		return nil, llm.ErrUnknownProvider
	}
	if err := check(p, s, key, needModel); err != nil {
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

	cp := &configured{Provider: inner, key: key}
	if !p.omitsTemperature(s.Model) {
		t := s.Temperature
		cp.temperature = &t
	}
	return cp, nil
}

// check validates s and key against p, and that s names a model when
// needModel is set. Its errors never quote a value.
func check(p Preset, s llm.Settings, key string, needModel bool) error {
	switch {
	case p.Key == KeyRequired && key == "":
		return llm.ErrMissingKey
	case p.Key == KeyNone && key != "":
		return llm.ErrUnexpectedKey
	case !validHeaderValue(key):
		return llm.ErrInvalidKey
	}
	switch {
	case p.BaseURL == "" && s.BaseURL == "":
		return llm.ErrMissingBaseURL
	case p.BaseURL != "" && s.BaseURL != "" && s.BaseURL != p.BaseURL:
		return fmt.Errorf("%w: base URL is fixed by the preset", llm.ErrInvalidSetting)
	}
	if needModel && s.Model == "" {
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

// validHeaderValue reports whether value has no control character other
// than tab, so it can be sent as an HTTP header value.
func validHeaderValue(value string) bool {
	for i := 0; i < len(value); i++ {
		if c := value[i]; (c < 0x20 && c != '\t') || c == 0x7f {
			return false
		}
	}
	return true
}

// configured applies the household temperature and the preset's
// temperature quirk to every Generate.
type configured struct {
	llm.Provider
	// key is the API key, redacted from validation errors; the adapter
	// redacts its own.
	key string
	// temperature is sent when a request sets none; nil means the model
	// rejects temperature, so none is ever sent.
	temperature *float64
}

// Generate validates req as sent by the caller before changing its
// temperature, so clearing it for a model that rejects temperature never
// hides an invalid one.
func (c *configured) Generate(ctx context.Context, req llm.Request) (llm.Response, error) {
	if err := req.Validate(); err != nil {
		return llm.Response{}, redact(fmt.Errorf("presets: generate: %w", err), c.key)
	}
	if c.temperature == nil {
		req.Temperature = nil
	} else if req.Temperature == nil {
		t := *c.temperature
		req.Temperature = &t
	}
	return c.Provider.Generate(ctx, req)
}

// redactedError is an error with the API key removed from its text. It
// does not unwrap, because every layer below may hold the key; Is still
// matches the sentinels the original error wraps.
type redactedError struct {
	text string
	err  error
}

func (e *redactedError) Error() string { return e.text }

// GoString keeps %#v to the redacted Error text instead of the fields.
func (e *redactedError) GoString() string { return e.text }

// Is reports whether the original error matches target.
func (e *redactedError) Is(target error) bool { return errors.Is(e.err, target) }

// redact returns err with key removed from its text; a nil err or an empty
// key returns err unchanged.
func redact(err error, key string) error {
	if err == nil || key == "" {
		return err
	}
	return &redactedError{text: llm.Redact(err.Error(), key), err: err}
}
