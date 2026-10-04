// Package ollama is the llm.Provider adapter for Ollama's native API. It
// calls POST {base}/api/chat and GET {base}/api/tags over plain net/http.
//
// Ollama takes no credentials: the adapter sends no Authorization header
// and never logs. The base URL may be a LAN address, and errors for an
// unreachable server name it, so New refuses a base URL that could carry a
// credential.
package ollama

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
	"strings"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/llm"
)

// Config configures a Provider.
type Config struct {
	// BaseURL is the Ollama server root the /api endpoint paths are
	// appended to, e.g. "http://192.168.1.20:11434" or
	// "http://ollama:11434". It is required.
	BaseURL string
	// Model is the model Generate requests, e.g. "llama3.2". ListModels
	// and Verify do not need it.
	Model string
}

// Provider is an Ollama native API llm.Provider.
type Provider struct {
	base   string
	model  string
	client *http.Client
}

var _ llm.Provider = (*Provider)(nil)

// New validates cfg and returns a Provider.
func New(cfg Config) (*Provider, error) {
	// Base URL errors never echo the URL: a rejected one may hold a
	// credential.
	u, err := url.Parse(cfg.BaseURL)
	if err != nil {
		return nil, errors.New("ollama: base URL does not parse")
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, errors.New("ollama: base URL must be an absolute http or https URL")
	}
	// A trailing "?" or "#" parses to an empty query or fragment, yet the
	// endpoint path appended to it would land in the query or fragment.
	if u.User != nil || strings.ContainsAny(cfg.BaseURL, "?#") {
		return nil, errors.New("ollama: base URL must not carry credentials, a query or a fragment")
	}
	return &Provider{
		base:   strings.TrimRight(cfg.BaseURL, "/"),
		model:  cfg.Model,
		client: noRedirectClient(),
	}, nil
}

// maxResponseBody caps a successful response body; a larger one is an error.
const maxResponseBody = 16 << 20

// maxErrorBody caps how much of an error response body is read; only an
// llm.MaxBodyExcerpt of it is kept.
const maxErrorBody = 64 << 10

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type options struct {
	Temperature *float64 `json:"temperature,omitempty"`
	NumPredict  int      `json:"num_predict,omitempty"`
}

type chatRequest struct {
	Model    string          `json:"model"`
	Messages []message       `json:"messages"`
	Stream   bool            `json:"stream"`
	Format   json.RawMessage `json:"format,omitempty"`
	Options  *options        `json:"options,omitempty"`
}

type chatResponse struct {
	Model   string `json:"model"`
	Message *struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"message"`
	Done            bool   `json:"done"`
	DoneReason      string `json:"done_reason"`
	PromptEvalCount int    `json:"prompt_eval_count"`
	EvalCount       int    `json:"eval_count"`
}

// Generate sends req to {base}/api/chat without streaming. A Request with a
// Schema sends it as "format", so Ollama constrains the reply to it;
// Response.JSON is then the reply text, which must be a JSON object.
func (p *Provider) Generate(ctx context.Context, req llm.Request) (llm.Response, error) {
	if err := req.Validate(); err != nil {
		return llm.Response{}, fmt.Errorf("ollama: generate: %w", err)
	}
	if p.model == "" {
		return llm.Response{}, errors.New("ollama: generate: no model configured")
	}
	body := chatRequest{Model: p.model, Stream: false}
	if req.System != "" {
		body.Messages = append(body.Messages, message{Role: "system", Content: req.System})
	}
	for _, m := range req.Messages {
		body.Messages = append(body.Messages, message{Role: string(m.Role), Content: m.Content})
	}
	if req.Schema != nil {
		body.Format = req.Schema.Schema
	}
	if req.Temperature != nil || req.MaxTokens > 0 {
		body.Options = &options{Temperature: req.Temperature, NumPredict: req.MaxTokens}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return llm.Response{}, fmt.Errorf("ollama: generate: encode request: %w", err)
	}

	if req.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}
	raw, err := p.do(ctx, http.MethodPost, "/api/chat", payload)
	if err != nil {
		var pe *llm.ProviderError
		if errors.As(err, &pe) && pe.Status() == http.StatusNotFound {
			// Ollama answers 404 when the model is not pulled. The body is
			// not quoted: the model name is all the caller needs.
			return llm.Response{}, fmt.Errorf("ollama: generate: model %q not found: %w", p.model, llm.NewProviderError(pe.Status(), nil, ""))
		}
		return llm.Response{}, fmt.Errorf("ollama: generate: %w", err)
	}

	var cr chatResponse
	if err := json.Unmarshal(raw, &cr); err != nil {
		return llm.Response{}, fmt.Errorf("ollama: generate: decode response: %w", err)
	}
	if !cr.Done || cr.Message == nil {
		return llm.Response{}, errors.New("ollama: generate: response is not a finished message")
	}
	resp := llm.Response{
		Text:         cr.Message.Content,
		Usage:        llm.Usage{InputTokens: cr.PromptEvalCount, OutputTokens: cr.EvalCount},
		Model:        cr.Model,
		FinishReason: cr.DoneReason,
	}
	if req.Schema == nil {
		return resp, nil
	}
	if text := strings.TrimSpace(resp.Text); isJSONObject(text) {
		resp.JSON = json.RawMessage(text)
		return resp, nil
	}
	return resp, fmt.Errorf("ollama: generate: reply is not a JSON object: %w", llm.ErrBadOutput)
}

// isJSONObject reports whether s is exactly one JSON object.
func isJSONObject(s string) bool {
	return strings.HasPrefix(s, "{") && json.Valid([]byte(s))
}

type tagsResponse struct {
	Models []struct {
		Name string `json:"name"`
	} `json:"models"`
}

// ListModels returns every model {base}/api/tags lists.
func (p *Provider) ListModels(ctx context.Context) ([]llm.Model, error) {
	models, err := p.tags(ctx)
	if err != nil {
		return nil, fmt.Errorf("ollama: list models: %w", err)
	}
	return models, nil
}

// Verify checks that the server answers GET {base}/api/tags with a model
// list.
func (p *Provider) Verify(ctx context.Context) error {
	if _, err := p.tags(ctx); err != nil {
		return fmt.Errorf("ollama: verify: %w", err)
	}
	return nil
}

func (p *Provider) tags(ctx context.Context) ([]llm.Model, error) {
	raw, err := p.do(ctx, http.MethodGet, "/api/tags", nil)
	if err != nil {
		return nil, err
	}
	var tr tagsResponse
	if err := json.Unmarshal(raw, &tr); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if tr.Models == nil {
		return nil, errors.New("response has no models list")
	}
	models := make([]llm.Model, 0, len(tr.Models))
	for i, m := range tr.Models {
		if m.Name == "" {
			return nil, fmt.Errorf("model %d has no name", i)
		}
		models = append(models, llm.Model{ID: m.Name, DisplayName: m.Name})
	}
	return models, nil
}

// do sends one request and returns the body of a 2xx response. Any other
// status, a 3xx included, is an *llm.ProviderError.
func (p *Provider) do(ctx context.Context, method, path string, payload []byte) ([]byte, error) {
	var reqBody io.Reader
	if payload != nil {
		reqBody = bytes.NewReader(payload)
	}
	httpReq, err := http.NewRequestWithContext(ctx, method, p.base+path, reqBody)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
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
		return nil, llm.NewProviderError(httpResp.StatusCode, body, "")
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

// noRedirectClient returns an HTTP client that never follows a redirect:
// the 3xx response is returned and becomes a provider error, so a request
// is only ever sent to the configured server.
func noRedirectClient() *http.Client {
	return &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout())
}

// transportFailure is a failed HTTP exchange. Its text names the base URL
// and the cause; it unwraps only to the cause's category, llm.ErrTimeout or
// context.Canceled.
type transportFailure struct {
	text string
	kind error
}

func (e *transportFailure) Error() string { return e.text }

func (e *transportFailure) Unwrap() error { return e.kind }

// transportError reports a failure to complete the HTTP exchange. A
// cancelled context is the caller's choice and stays context.Canceled.
// Every other failure, a refused connection or a timeout alike, means the
// server could not be reached in time: it matches llm.ErrTimeout and says
// "Ollama not reachable at {base}".
func (p *Provider) transportError(err error) error {
	timedOut := isTimeout(err)
	// The *url.Error repeats the request URL; the base is named once below.
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	if !timedOut && errors.Is(err, context.Canceled) {
		return &transportFailure{text: "request canceled: " + err.Error(), kind: context.Canceled}
	}
	text := fmt.Sprintf("Ollama not reachable at %s: %s", p.base, err)
	if timedOut {
		text = fmt.Sprintf("Ollama not reachable at %s: %s: %s", p.base, llm.ErrTimeout, err)
	}
	return &transportFailure{text: text, kind: llm.ErrTimeout}
}
