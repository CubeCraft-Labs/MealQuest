// Package openai is the llm.Provider adapter for OpenAI-compatible chat
// APIs: OpenAI itself, OpenRouter and Ollama's /v1 endpoint. It calls
// POST {base}/chat/completions and GET {base}/models over plain net/http.
//
// The adapter never logs, and no error it returns contains the API key or
// the Authorization header value: errors do not quote caller- or
// vendor-supplied text, and every exported method passes its error through
// redact as a backstop.
package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/llm"
)

// StructuredOutput is how the adapter asks the vendor for JSON when a
// Request carries a Schema. Which mode a vendor and model support comes
// from its preset.
type StructuredOutput string

const (
	// JSONSchema sends response_format {type: json_schema, strict: true}
	// with the Request's schema.
	JSONSchema StructuredOutput = "json_schema"
	// JSONObject sends response_format {type: json_object} and puts the
	// schema in the system prompt.
	JSONObject StructuredOutput = "json_object"
	// PromptOnly sends no response_format, puts the schema in the system
	// prompt and takes the first JSON object found in the reply.
	PromptOnly StructuredOutput = "prompt_only"
)

// Config configures a Provider.
type Config struct {
	// BaseURL is the API root the endpoint paths are appended to, e.g.
	// "https://api.openai.com/v1", "https://openrouter.ai/api/v1" or
	// "http://ollama:11434/v1".
	BaseURL string
	// APIKey is sent as "Authorization: Bearer <APIKey>". Empty sends no
	// Authorization header, for endpoints such as Ollama that need none.
	APIKey string
	// Model is the model Generate requests. ListModels and Verify do not
	// need it.
	Model string
	// Headers are extra request headers, e.g. OpenRouter's HTTP-Referer and
	// X-Title. An Accept header replaces the default "application/json".
	// Headers the adapter or net/http set themselves (see managedHeaders)
	// are rejected.
	Headers map[string]string
	// StructuredOutput is how JSON is requested when a Request has a Schema.
	StructuredOutput StructuredOutput
}

// Provider is an OpenAI-compatible llm.Provider.
type Provider struct {
	base    string
	apiKey  string
	model   string
	headers http.Header
	mode    StructuredOutput
	client  *http.Client
}

var _ llm.Provider = (*Provider)(nil)

// New validates cfg and returns a Provider.
func New(cfg Config) (*Provider, error) {
	p, err := newFromConfig(cfg)
	if err != nil {
		return nil, redact(err, cfg.APIKey)
	}
	return p, nil
}

func newFromConfig(cfg Config) (*Provider, error) {
	// Base URL errors never echo the URL: a rejected one may carry a
	// credential.
	u, err := url.Parse(cfg.BaseURL)
	if err != nil {
		return nil, errors.New("openai: base URL does not parse")
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, errors.New("openai: base URL must be an absolute http or https URL")
	}
	// A trailing "?" or "#" parses to an empty query or fragment, yet the
	// endpoint path appended to it would land in the query or fragment.
	if u.User != nil || strings.ContainsAny(cfg.BaseURL, "?#") {
		return nil, errors.New("openai: base URL must not carry credentials, a query or a fragment")
	}
	// No error from New quotes caller-supplied text or a name derived from
	// it: any such text may equal or contain the API key, and redaction
	// cannot catch a fragment or a case-changed copy of it.
	switch cfg.StructuredOutput {
	case JSONSchema, JSONObject, PromptOnly:
	default:
		return nil, errors.New("openai: unknown structured output mode")
	}
	headers := make(http.Header, len(cfg.Headers)+1)
	for name, value := range cfg.Headers {
		if !validHeaderName(name) {
			return nil, errors.New("openai: an extra header name is not a valid HTTP token")
		}
		canonical := textproto.CanonicalMIMEHeaderKey(name)
		if managedHeaders[canonical] {
			return nil, errors.New("openai: an extra header is set by the adapter")
		}
		if !validHeaderValue(value) {
			return nil, errors.New("openai: an extra header value has a control character")
		}
		headers.Set(canonical, value)
	}
	if headers.Get("Accept") == "" {
		headers.Set("Accept", "application/json")
	}
	return &Provider{
		base:    strings.TrimRight(cfg.BaseURL, "/"),
		apiKey:  cfg.APIKey,
		model:   cfg.Model,
		headers: headers,
		mode:    cfg.StructuredOutput,
		client:  &http.Client{},
	}, nil
}

// managedHeaders are the request headers the adapter or net/http set; a
// configured value for one would be overwritten or ignored.
var managedHeaders = map[string]bool{
	"Authorization":     true,
	"Content-Type":      true,
	"Content-Length":    true,
	"Transfer-Encoding": true,
	"Host":              true,
	"Connection":        true,
}

// validHeaderName reports whether name is an RFC 9110 field-name: a
// non-empty token of letters, digits and !#$%&'*+-.^_`|~.
func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
		case strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0:
		default:
			return false
		}
	}
	return true
}

// validHeaderValue reports whether value is an RFC 9110 field-value: no
// control characters other than horizontal tab, so no CR, LF or DEL.
func validHeaderValue(value string) bool {
	for i := 0; i < len(value); i++ {
		if c := value[i]; (c < 0x20 && c != '\t') || c == 0x7f {
			return false
		}
	}
	return true
}

// maxResponseBody caps a successful response body; a larger one is an error.
const maxResponseBody = 16 << 20

// maxErrorBody caps how much of an error response body is read. It is far
// above llm.MaxBodyExcerpt, so redaction still sees every secret that could
// reach the excerpt.
const maxErrorBody = 64 << 10

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type jsonSchemaFormat struct {
	Name   string          `json:"name"`
	Schema json.RawMessage `json:"schema"`
	Strict bool            `json:"strict"`
}

type responseFormat struct {
	Type       string            `json:"type"`
	JSONSchema *jsonSchemaFormat `json:"json_schema,omitempty"`
}

type chatRequest struct {
	Model          string          `json:"model"`
	Messages       []chatMessage   `json:"messages"`
	Temperature    *float64        `json:"temperature,omitempty"`
	MaxTokens      int             `json:"max_tokens,omitempty"`
	ResponseFormat *responseFormat `json:"response_format,omitempty"`
}

type chatResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content *string `json:"content"`
			Refusal *string `json:"refusal"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
	Error json.RawMessage `json:"error"`
}

// schemaInstruction is appended to the system prompt in the json_object and
// prompt_only modes, where the schema is not sent in response_format.
const schemaInstruction = "Reply with only a JSON object, and no other text, that matches this JSON Schema:\n"

// Generate sends req to {base}/chat/completions.
func (p *Provider) Generate(ctx context.Context, req llm.Request) (llm.Response, error) {
	resp, err := p.generate(ctx, req)
	return resp, redact(err, p.apiKey)
}

func (p *Provider) generate(ctx context.Context, req llm.Request) (llm.Response, error) {
	if err := req.Validate(); err != nil {
		return llm.Response{}, fmt.Errorf("openai: generate: %w", err)
	}
	if p.model == "" {
		return llm.Response{}, errors.New("openai: generate: no model configured")
	}
	body := chatRequest{
		Model:       p.model,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
	}
	system := req.System
	if req.Schema != nil {
		switch p.mode {
		case JSONSchema:
			body.ResponseFormat = &responseFormat{
				Type:       "json_schema",
				JSONSchema: &jsonSchemaFormat{Name: req.Schema.Name, Schema: req.Schema.Schema, Strict: true},
			}
		case JSONObject:
			body.ResponseFormat = &responseFormat{Type: "json_object"}
			system = appendInstruction(system, req.Schema.Schema)
		case PromptOnly:
			system = appendInstruction(system, req.Schema.Schema)
		}
	}
	if system != "" {
		body.Messages = append(body.Messages, chatMessage{Role: "system", Content: system})
	}
	for _, m := range req.Messages {
		body.Messages = append(body.Messages, chatMessage{Role: string(m.Role), Content: m.Content})
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return llm.Response{}, fmt.Errorf("openai: generate: encode request: %w", err)
	}

	if req.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}
	raw, status, err := p.do(ctx, http.MethodPost, "/chat/completions", payload)
	if err != nil {
		return llm.Response{}, fmt.Errorf("openai: generate: %w", err)
	}

	var cr chatResponse
	if err := json.Unmarshal(raw, &cr); err != nil {
		return llm.Response{}, fmt.Errorf("openai: generate: decode response: %w", err)
	}
	if len(cr.Error) > 0 && string(cr.Error) != "null" {
		// OpenRouter can report an upstream failure in a 2xx body.
		return llm.Response{}, fmt.Errorf("openai: generate: %w", llm.NewProviderError(status, raw, p.apiKey))
	}
	if len(cr.Choices) == 0 {
		return llm.Response{}, errors.New("openai: generate: response has no choices")
	}
	choice := cr.Choices[0]
	resp := llm.Response{
		Usage:        llm.Usage{InputTokens: cr.Usage.PromptTokens, OutputTokens: cr.Usage.CompletionTokens},
		Model:        cr.Model,
		FinishReason: choice.FinishReason,
	}
	if choice.Message.Content != nil {
		resp.Text = *choice.Message.Content
	}
	if req.Schema == nil {
		return resp, nil
	}
	if choice.Message.Refusal != nil && *choice.Message.Refusal != "" {
		return resp, fmt.Errorf("openai: generate: model refused the structured request: %w", llm.ErrBadOutput)
	}
	if p.mode == PromptOnly {
		resp.JSON = firstJSONObject(resp.Text)
	} else if text := strings.TrimSpace(resp.Text); isJSONObject(text) {
		resp.JSON = json.RawMessage(text)
	}
	if resp.JSON == nil {
		return resp, fmt.Errorf("openai: generate: reply holds no JSON object: %w", llm.ErrBadOutput)
	}
	return resp, nil
}

func appendInstruction(system string, schema json.RawMessage) string {
	instruction := schemaInstruction + string(schema)
	if system == "" {
		return instruction
	}
	return system + "\n\n" + instruction
}

// isJSONObject reports whether s is exactly one JSON object.
func isJSONObject(s string) bool {
	return strings.HasPrefix(s, "{") && json.Valid([]byte(s))
}

// firstJSONObject returns the first complete JSON object in s, trying each
// "{" in order, or nil when there is none. Text around it, such as prose or
// a Markdown code fence, is ignored.
func firstJSONObject(s string) json.RawMessage {
	for i := strings.IndexByte(s, '{'); i >= 0; {
		var obj json.RawMessage
		if err := json.NewDecoder(strings.NewReader(s[i:])).Decode(&obj); err == nil {
			return obj
		}
		next := strings.IndexByte(s[i+1:], '{')
		if next < 0 {
			break
		}
		i += 1 + next
	}
	return nil
}

type modelsResponse struct {
	Data []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"data"`
}

// ListModels returns the models {base}/models lists.
func (p *Provider) ListModels(ctx context.Context) ([]llm.Model, error) {
	models, err := p.listModels(ctx)
	return models, redact(err, p.apiKey)
}

func (p *Provider) listModels(ctx context.Context) ([]llm.Model, error) {
	raw, _, err := p.do(ctx, http.MethodGet, "/models", nil)
	if err != nil {
		return nil, fmt.Errorf("openai: list models: %w", err)
	}
	var mr modelsResponse
	if err := json.Unmarshal(raw, &mr); err != nil {
		return nil, fmt.Errorf("openai: list models: decode response: %w", err)
	}
	if mr.Data == nil {
		return nil, errors.New("openai: list models: response has no data list")
	}
	models := make([]llm.Model, 0, len(mr.Data))
	for i, m := range mr.Data {
		if m.ID == "" {
			return nil, fmt.Errorf("openai: list models: model %d has no id", i)
		}
		name := m.Name
		if name == "" {
			name = m.ID
		}
		models = append(models, llm.Model{ID: m.ID, DisplayName: name})
	}
	return models, nil
}

// Verify checks the credentials with an authenticated ListModels call.
func (p *Provider) Verify(ctx context.Context) error {
	_, err := p.listModels(ctx)
	return redact(err, p.apiKey)
}

// do sends one request and returns the body and status of a 2xx response.
// Any other status becomes the matching llm typed error.
func (p *Provider) do(ctx context.Context, method, path string, payload []byte) ([]byte, int, error) {
	var reqBody io.Reader
	if payload != nil {
		reqBody = bytes.NewReader(payload)
	}
	httpReq, err := http.NewRequestWithContext(ctx, method, p.base+path, reqBody)
	if err != nil {
		// The parse error names the URL, whose path may hold the API key.
		return nil, 0, errors.New("build request: request URL is invalid")
	}
	for name, values := range p.headers {
		httpReq.Header[name] = values
	}
	if payload != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	httpResp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, 0, p.transportError(err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode < 200 || httpResp.StatusCode > 299 {
		body, err := io.ReadAll(io.LimitReader(httpResp.Body, maxErrorBody))
		if err != nil {
			return nil, httpResp.StatusCode, p.transportError(err)
		}
		return nil, httpResp.StatusCode, p.statusError(httpResp, body)
	}
	body, err := io.ReadAll(io.LimitReader(httpResp.Body, maxResponseBody+1))
	if err != nil {
		return nil, httpResp.StatusCode, p.transportError(err)
	}
	if len(body) > maxResponseBody {
		return nil, httpResp.StatusCode, fmt.Errorf("response body exceeds %d bytes", maxResponseBody)
	}
	return body, httpResp.StatusCode, nil
}

// statusError maps a non-2xx response to an llm typed error.
func (p *Provider) statusError(resp *http.Response, body []byte) error {
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("HTTP %d: %w", resp.StatusCode, llm.ErrAuth)
	case http.StatusTooManyRequests:
		return &llm.RateLimitedError{RetryAfter: retryAfter(resp.Header.Get("Retry-After"), time.Now())}
	case http.StatusRequestTimeout:
		return fmt.Errorf("HTTP %d: %w", resp.StatusCode, llm.ErrTimeout)
	default:
		return llm.NewProviderError(resp.StatusCode, body, p.apiKey)
	}
}

// retryAfter parses a Retry-After header, in delay-seconds or HTTP-date
// form. A missing, malformed or past value means the vendor gave no usable
// wait, reported as 0.
func retryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if secs, err := strconv.Atoi(value); err == nil {
		if secs < 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if at, err := http.ParseTime(value); err == nil && at.After(now) {
		return at.Sub(now).Round(time.Second)
	}
	return 0
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout())
}

// transportFailure is a failed HTTP exchange. It carries the cause's text
// with the request URL removed and the API key redacted, and unwraps only to
// the cause's category: llm.ErrTimeout, context.Canceled or nothing.
type transportFailure struct {
	detail string
	kind   error
}

func (e *transportFailure) Error() string {
	if e.kind == llm.ErrTimeout {
		return fmt.Sprintf("%s: transport: %s", llm.ErrTimeout, e.detail)
	}
	return "transport: " + e.detail
}

func (e *transportFailure) Unwrap() error { return e.kind }

// transportError reports a failure to complete the HTTP exchange. A
// *url.Error names the request URL, and a base URL path may hold the API
// key, so only the wrapped cause is kept, rendered with the key redacted.
func (p *Provider) transportError(err error) error {
	var kind error
	switch {
	case isTimeout(err):
		kind = llm.ErrTimeout
	case errors.Is(err, context.Canceled):
		kind = context.Canceled
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	return &transportFailure{detail: redactKey(err.Error(), p.apiKey), kind: kind}
}

// redactKey replaces every occurrence of a non-empty key in s. Replacing
// the key also replaces the "Bearer "+key header value that contains it.
func redactKey(s, key string) string {
	if key == "" {
		return s
	}
	return strings.ReplaceAll(s, key, "[REDACTED]")
}

// withheldText replaces an error's text when redactKey cannot remove the
// key, because the key is part of the replacement marker.
const withheldText = "openai: error text withheld: it contains the API key"

// redactedError stands in for one layer of an error chain that holds the
// API key. Its text is the layer's text with the key redacted, and Unwrap
// returns the layer's causes passed through redact, so no error reached by
// unwrapping has text holding the key. Is and As consult the original
// layer and its chain, so sentinels and typed errors still match.
type redactedError struct {
	text string
	err  error
	key  string
}

func (e *redactedError) Error() string { return e.text }

func (e *redactedError) Unwrap() error { return redact(errors.Unwrap(e.err), e.key) }

func (e *redactedError) Is(target error) bool { return errors.Is(e.err, target) }

func (e *redactedError) As(target any) bool { return errors.As(e.err, target) }

// redactedJoin is redactedError for a layer with several causes, such as
// one built by errors.Join or by fmt.Errorf with more than one %w.
type redactedJoin struct {
	redactedError
}

func (e *redactedJoin) Unwrap() []error {
	causes := e.err.(interface{ Unwrap() []error }).Unwrap()
	safe := make([]error, len(causes))
	for i, c := range causes {
		safe[i] = redact(c, e.key)
	}
	return safe
}

// redact is the backstop every exported method applies to its error: when
// the text of err or of any error in its chain holds a non-empty key, it
// returns a stand-in whose text, and the text of every error it unwraps
// to, has the key redacted. Any other err is returned unchanged.
func redact(err error, key string) error {
	if err == nil || key == "" || !holdsKey(err, key) {
		return err
	}
	text := redactKey(err.Error(), key)
	if strings.Contains(text, key) {
		text = withheldText
	}
	re := redactedError{text: text, err: err, key: key}
	if _, ok := err.(interface{ Unwrap() []error }); ok {
		return &redactedJoin{re}
	}
	return &re
}

// holdsKey reports whether the text of err or of any error in its chain
// contains key.
func holdsKey(err error, key string) bool {
	if err == nil {
		return false
	}
	if strings.Contains(err.Error(), key) {
		return true
	}
	switch u := err.(type) {
	case interface{ Unwrap() error }:
		return holdsKey(u.Unwrap(), key)
	case interface{ Unwrap() []error }:
		for _, c := range u.Unwrap() {
			if holdsKey(c, key) {
				return true
			}
		}
	}
	return false
}
