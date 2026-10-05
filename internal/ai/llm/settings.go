package llm

import "errors"

// Settings is the household's AI configuration, the input to
// presets.FromSettings. The caller fills it from the db.AiSetting row and
// the service config; this package does not import internal/db.
//
//	llm.Settings{
//		Provider:    row.Provider,
//		BaseURL:     row.BaseUrl,
//		Model:       row.Model,
//		Temperature: row.Temperature,
//		PublicURL:   cfg.PublicURL.String(), // MEALQUEST_PUBLIC_URL
//	}
//
// Settings never holds the API key: the caller decrypts it and passes it
// to presets.FromSettings separately.
type Settings struct {
	// Provider names the preset: "anthropic", "openai", "openrouter",
	// "xai", "ollama" or "openai_compatible".
	Provider string
	// BaseURL is the API root. It is required for ollama and
	// openai_compatible; the other presets fix their own, and a non-empty
	// value must equal it.
	BaseURL string
	// Model is the model id Generate requests. It is required.
	Model string
	// Temperature is the household's sampling temperature. It is sent with
	// every request that sets none, except to models that reject it.
	Temperature float64
	// PublicURL is MEALQUEST_PUBLIC_URL. OpenRouter requires it and
	// receives it as the HTTP-Referer header; other presets ignore it.
	PublicURL string
	// StructuredOutput selects how an OpenAI-compatible preset asks for
	// JSON: "json_schema", "json_object" or "prompt_only". Empty selects
	// the preset's default, json_schema. Each preset allows only the modes
	// it lists; anthropic and ollama allow none, so it must be empty.
	StructuredOutput string
}

// Configuration errors from presets.FromSettings. Each is distinct and
// matches with errors.Is.
var (
	// ErrUnknownProvider means Settings.Provider names no preset.
	ErrUnknownProvider = errors.New("llm: unknown provider")
	// ErrMissingKey means the preset requires an API key and none was
	// given.
	ErrMissingKey = errors.New("llm: API key is required")
	// ErrUnexpectedKey means an API key was given to a preset that sends
	// none.
	ErrUnexpectedKey = errors.New("llm: provider takes no API key")
	// ErrInvalidKey means the API key is not a valid HTTP header value:
	// it has a control character such as CR or LF.
	ErrInvalidKey = errors.New("llm: API key is not a valid header value")
	// ErrMissingBaseURL means the preset requires a base URL and none was
	// given.
	ErrMissingBaseURL = errors.New("llm: base URL is required")
	// ErrMissingModel means Settings.Model is empty.
	ErrMissingModel = errors.New("llm: model is required")
	// ErrMissingPublicURL means the preset requires Settings.PublicURL and
	// it is empty.
	ErrMissingPublicURL = errors.New("llm: public URL is required")
	// ErrInvalidSetting means a setting has a value the preset does not
	// accept, such as a base URL other than the preset's, an unsupported
	// structured output mode or a negative temperature.
	ErrInvalidSetting = errors.New("llm: invalid setting")
)
