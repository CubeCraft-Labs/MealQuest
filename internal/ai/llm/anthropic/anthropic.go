// Package anthropic is the llm.Provider adapter for Anthropic's Messages
// API. It calls POST {base}/v1/messages and GET {base}/v1/models over plain
// net/http.
//
// The adapter never logs, and no error it returns contains the API key:
// errors do not quote caller- or vendor-supplied text, and every exported
// method passes its error through redact as a backstop.
package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/llm"
)

// APIVersion is the Messages API version sent as the anthropic-version
// header.
const APIVersion = "2023-06-01"

// Config configures a Provider.
type Config struct {
	// BaseURL is the API root the /v1 endpoint paths are appended to, e.g.
	// "https://api.anthropic.com".
	BaseURL string
	// APIKey is sent as the x-api-key header. It must not be empty.
	APIKey string
	// Model is the model Generate requests. ListModels and Verify do not
	// need it.
	Model string
	// MaxTokens is the max_tokens Generate sends when a Request sets none:
	// the Messages API requires one. It must be positive.
	MaxTokens int
}

// Provider is an Anthropic Messages API llm.Provider.
type Provider struct {
	base      string
	apiKey    string
	model     string
	maxTokens int
	client    *http.Client
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
	// No error from New quotes caller-supplied text: any of it may equal or
	// contain the API key. Base URL errors never echo the URL.
	u, err := url.Parse(cfg.BaseURL)
	if err != nil {
		return nil, errors.New("anthropic: base URL does not parse")
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, errors.New("anthropic: base URL must be an absolute http or https URL")
	}
	// A trailing "?" or "#" parses to an empty query or fragment, yet the
	// endpoint path appended to it would land in the query or fragment.
	if u.User != nil || strings.ContainsAny(cfg.BaseURL, "?#") {
		return nil, errors.New("anthropic: base URL must not carry credentials, a query or a fragment")
	}
	if cfg.APIKey == "" {
		return nil, errors.New("anthropic: no API key configured")
	}
	if !validHeaderValue(cfg.APIKey) {
		return nil, errors.New("anthropic: API key has a control character")
	}
	if cfg.MaxTokens <= 0 {
		return nil, errors.New("anthropic: max tokens must be positive")
	}
	return &Provider{
		base:      strings.TrimRight(cfg.BaseURL, "/"),
		apiKey:    cfg.APIKey,
		model:     cfg.Model,
		maxTokens: cfg.MaxTokens,
		client:    &http.Client{},
	}, nil
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

// toolName is the pattern the Messages API requires of a tool name.
var toolName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// maxResponseBody caps a successful response body; a larger one is an error.
const maxResponseBody = 16 << 20

// maxErrorBody caps how much of an error response body is read. It is far
// above llm.MaxBodyExcerpt, so redaction still sees every secret that could
// reach the excerpt.
const maxErrorBody = 64 << 10

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type tool struct {
	Name        string          `json:"name"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type toolChoice struct {
	Type string `json:"type"`
	Name string `json:"name"`
}

type messagesRequest struct {
	Model       string      `json:"model"`
	MaxTokens   int         `json:"max_tokens"`
	System      string      `json:"system,omitempty"`
	Messages    []message   `json:"messages"`
	Temperature *float64    `json:"temperature,omitempty"`
	Tools       []tool      `json:"tools,omitempty"`
	ToolChoice  *toolChoice `json:"tool_choice,omitempty"`
}

type messagesResponse struct {
	Type    string `json:"type"`
	Model   string `json:"model"`
	Content []struct {
		Type  string          `json:"type"`
		Text  string          `json:"text"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	} `json:"content"`
	StopReason string `json:"stop_reason"`
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// Generate sends req to {base}/v1/messages. A Request with a Schema is sent
// as one tool whose input_schema is the schema, with tool_choice forcing
// it; Response.JSON is then that tool's input.
func (p *Provider) Generate(ctx context.Context, req llm.Request) (llm.Response, error) {
	resp, err := p.generate(ctx, req)
	return resp, redact(err, p.apiKey)
}

func (p *Provider) generate(ctx context.Context, req llm.Request) (llm.Response, error) {
	if err := req.Validate(); err != nil {
		return llm.Response{}, fmt.Errorf("anthropic: generate: %w", err)
	}
	if p.model == "" {
		return llm.Response{}, errors.New("anthropic: generate: no model configured")
	}
	body := messagesRequest{
		Model:       p.model,
		MaxTokens:   p.maxTokens,
		System:      req.System,
		Temperature: req.Temperature,
	}
	if req.MaxTokens > 0 {
		body.MaxTokens = req.MaxTokens
	}
	if req.Schema != nil {
		if !toolName.MatchString(req.Schema.Name) {
			return llm.Response{}, errors.New("anthropic: generate: schema name is not a valid tool name")
		}
		body.Tools = []tool{{Name: req.Schema.Name, InputSchema: req.Schema.Schema}}
		body.ToolChoice = &toolChoice{Type: "tool", Name: req.Schema.Name}
	}
	for _, m := range req.Messages {
		body.Messages = append(body.Messages, message{Role: string(m.Role), Content: m.Content})
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return llm.Response{}, fmt.Errorf("anthropic: generate: encode request: %w", err)
	}

	if req.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}
	raw, err := p.do(ctx, http.MethodPost, "/v1/messages", payload)
	if err != nil {
		return llm.Response{}, fmt.Errorf("anthropic: generate: %w", err)
	}

	var mr messagesResponse
	if err := json.Unmarshal(raw, &mr); err != nil {
		return llm.Response{}, fmt.Errorf("anthropic: generate: decode response: %w", err)
	}
	if mr.Type != "message" {
		return llm.Response{}, errors.New("anthropic: generate: response is not a message")
	}
	resp := llm.Response{
		Usage:        llm.Usage{InputTokens: mr.Usage.InputTokens, OutputTokens: mr.Usage.OutputTokens},
		Model:        mr.Model,
		FinishReason: mr.StopReason,
	}
	var text strings.Builder
	for _, block := range mr.Content {
		switch block.Type {
		case "text":
			text.WriteString(block.Text)
		case "tool_use":
			if req.Schema != nil && block.Name == req.Schema.Name && resp.JSON == nil {
				resp.JSON = block.Input
			}
		}
	}
	resp.Text = text.String()
	if req.Schema == nil {
		return resp, nil
	}
	if !isJSONObject(resp.JSON) {
		resp.JSON = nil
		return resp, fmt.Errorf("anthropic: generate: reply holds no input for the forced tool: %w", llm.ErrBadOutput)
	}
	return resp, nil
}

// isJSONObject reports whether raw is exactly one JSON object.
func isJSONObject(raw json.RawMessage) bool {
	s := bytes.TrimSpace(raw)
	return bytes.HasPrefix(s, []byte("{")) && json.Valid(s)
}

type modelsResponse struct {
	Data []struct {
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
	} `json:"data"`
	HasMore bool   `json:"has_more"`
	LastID  string `json:"last_id"`
}

// modelsPageLimit is the largest page GET /v1/models serves.
const modelsPageLimit = 1000

// ListModels returns every model {base}/v1/models lists, following pages.
func (p *Provider) ListModels(ctx context.Context) ([]llm.Model, error) {
	models, err := p.listModels(ctx)
	return models, redact(err, p.apiKey)
}

func (p *Provider) listModels(ctx context.Context) ([]llm.Model, error) {
	models := []llm.Model{}
	after := ""
	for {
		page, err := p.modelsPage(ctx, modelsPageLimit, after)
		if err != nil {
			return nil, fmt.Errorf("anthropic: list models: %w", err)
		}
		for _, m := range page.Data {
			if m.ID == "" {
				return nil, fmt.Errorf("anthropic: list models: model %d has no id", len(models))
			}
			name := m.DisplayName
			if name == "" {
				name = m.ID
			}
			models = append(models, llm.Model{ID: m.ID, DisplayName: name})
		}
		if !page.HasMore {
			return models, nil
		}
		if page.LastID == "" || page.LastID == after {
			return nil, errors.New("anthropic: list models: response has more pages but no new cursor")
		}
		after = page.LastID
	}
}

// modelsPage fetches one page of GET {base}/v1/models.
func (p *Provider) modelsPage(ctx context.Context, limit int, after string) (modelsResponse, error) {
	q := url.Values{"limit": {strconv.Itoa(limit)}}
	if after != "" {
		q.Set("after_id", after)
	}
	raw, err := p.do(ctx, http.MethodGet, "/v1/models?"+q.Encode(), nil)
	if err != nil {
		return modelsResponse{}, err
	}
	var mr modelsResponse
	if err := json.Unmarshal(raw, &mr); err != nil {
		return modelsResponse{}, fmt.Errorf("decode response: %w", err)
	}
	if mr.Data == nil {
		return modelsResponse{}, errors.New("response has no data list")
	}
	return mr, nil
}

// Verify checks the credentials with the cheapest authenticated call: one
// page of GET {base}/v1/models holding at most one model.
func (p *Provider) Verify(ctx context.Context) error {
	_, err := p.modelsPage(ctx, 1, "")
	if err != nil {
		err = fmt.Errorf("anthropic: verify: %w", err)
	}
	return redact(err, p.apiKey)
}

// do sends one request and returns the body of a 2xx response. Any other
// status becomes the matching llm typed error.
func (p *Provider) do(ctx context.Context, method, path string, payload []byte) ([]byte, error) {
	var reqBody io.Reader
	if payload != nil {
		reqBody = bytes.NewReader(payload)
	}
	httpReq, err := http.NewRequestWithContext(ctx, method, p.base+path, reqBody)
	if err != nil {
		// The parse error names the URL, whose path may hold the API key.
		return nil, errors.New("build request: request URL is invalid")
	}
	httpReq.Header.Set("x-api-key", p.apiKey)
	httpReq.Header.Set("anthropic-version", APIVersion)
	httpReq.Header.Set("Accept", "application/json")
	if payload != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}

	httpResp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, p.transportError(err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode < 200 || httpResp.StatusCode > 299 {
		body, err := io.ReadAll(io.LimitReader(httpResp.Body, maxErrorBody))
		if err != nil {
			return nil, p.transportError(err)
		}
		return nil, p.statusError(httpResp, body)
	}
	body, err := io.ReadAll(io.LimitReader(httpResp.Body, maxResponseBody+1))
	if err != nil {
		return nil, p.transportError(err)
	}
	if len(body) > maxResponseBody {
		return nil, fmt.Errorf("response body exceeds %d bytes", maxResponseBody)
	}
	return body, nil
}

// statusOverloaded is the status the Messages API returns when it is
// temporarily overloaded.
const statusOverloaded = 529

// statusError maps a non-2xx response to an llm typed error.
func (p *Provider) statusError(resp *http.Response, body []byte) error {
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("HTTP %d: %w", resp.StatusCode, llm.ErrAuth)
	case http.StatusTooManyRequests, statusOverloaded:
		return llm.NewRateLimitedError(retryAfter(resp.Header.Get("Retry-After"), time.Now()), p.apiKey)
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
	return &transportFailure{detail: llm.Redact(err.Error(), p.apiKey), kind: kind}
}

// redactedError stands in for one layer of an error chain that holds the
// API key. Its text is the layer's text with the key redacted, and Unwrap
// returns the layer's causes passed through redact, so no error reached by
// unwrapping has text holding the key. Is consults the original layer and
// its chain, so sentinels still match. As does too. The llm typed errors
// remove the key from their own complete text, fixed words included, so
// they always match; As refuses only a match from outside llm whose text
// holds the key, so it never hands back an unredacted error.
type redactedError struct {
	text string
	err  error
	key  string
}

func (e *redactedError) Error() string { return e.text }

// GoString keeps %#v from printing the fields, which hold the key.
func (e *redactedError) GoString() string { return e.text }

func (e *redactedError) Unwrap() error { return redact(errors.Unwrap(e.err), e.key) }

func (e *redactedError) Is(target error) bool { return errors.Is(e.err, target) }

func (e *redactedError) As(target any) bool {
	if !errors.As(e.err, target) {
		return false
	}
	found := reflect.ValueOf(target).Elem()
	if matched, ok := found.Interface().(error); ok && holdsKey(matched, e.key) {
		found.SetZero()
		return false
	}
	return true
}

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
	re := redactedError{text: llm.Redact(err.Error(), key), err: err, key: key}
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
