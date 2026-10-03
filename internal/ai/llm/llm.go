// Package llm is the single interface MealQuest uses to talk to a language
// model. Every vendor adapter implements Provider over plain net/http (no
// vendor SDKs) and reports failures with this package's typed errors, so
// callers decide what to do from errors.Is and errors.As alone.
//
// Errors from this package and its adapters never contain an API key.
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Provider is a language-model vendor.
type Provider interface {
	// Generate sends one request and returns the model's reply.
	Generate(ctx context.Context, req Request) (Response, error)
	// ListModels returns the models the configured credentials can use.
	ListModels(ctx context.Context) ([]Model, error)
	// Verify checks that the configured credentials are accepted.
	Verify(ctx context.Context) error
}

// Role is the author of a Message.
type Role string

// The roles a Message may have. The system prompt is Request.System, not a
// message.
const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Message is one turn of a conversation.
type Message struct {
	Role    Role
	Content string
}

// JSONSchema is a named structured-output contract: when a Request carries
// one, the model must reply with JSON matching Schema.
type JSONSchema struct {
	// Name identifies the contract to the vendor, e.g. "side_quests".
	Name string
	// Schema is a JSON Schema object.
	Schema json.RawMessage
}

// Request is one generation request.
type Request struct {
	// System is the system prompt; empty means none.
	System string
	// Messages is the conversation, oldest first. It must not be empty.
	Messages []Message
	// Temperature is the sampling temperature; nil means the vendor default.
	Temperature *float64
	// MaxTokens caps the reply length; 0 means the vendor default.
	MaxTokens int
	// Schema, when set, requests structured output; the reply is then in
	// Response.JSON.
	Schema *JSONSchema
	// Timeout bounds the request; 0 means only ctx bounds it.
	Timeout time.Duration
}

// Validate reports whether r is well formed. Adapters call it before
// contacting the vendor.
func (r Request) Validate() error {
	if len(r.Messages) == 0 {
		return errors.New("llm: request has no messages")
	}
	for i, m := range r.Messages {
		if m.Role != RoleUser && m.Role != RoleAssistant {
			return fmt.Errorf("llm: message %d has unknown role %q", i, m.Role)
		}
	}
	if r.Temperature != nil && *r.Temperature < 0 {
		return fmt.Errorf("llm: temperature %v is negative", *r.Temperature)
	}
	if r.MaxTokens < 0 {
		return fmt.Errorf("llm: max tokens %d is negative", r.MaxTokens)
	}
	if r.Timeout < 0 {
		return fmt.Errorf("llm: timeout %v is negative", r.Timeout)
	}
	if r.Schema != nil {
		if r.Schema.Name == "" {
			return errors.New("llm: schema has no name")
		}
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(r.Schema.Schema, &obj); err != nil || obj == nil {
			return fmt.Errorf("llm: schema %q is not a JSON object", r.Schema.Name)
		}
	}
	return nil
}

// Usage is the token count of one Generate call.
type Usage struct {
	InputTokens  int
	OutputTokens int
}

// Total returns InputTokens + OutputTokens.
func (u Usage) Total() int { return u.InputTokens + u.OutputTokens }

// Response is the model's reply to a Request.
type Response struct {
	// Text is the reply text.
	Text string
	// JSON is the structured reply when the Request carried a Schema, and
	// nil otherwise.
	JSON json.RawMessage
	// Usage is the tokens the call consumed.
	Usage Usage
	// Model is the model that answered, as the vendor reports it.
	Model string
	// FinishReason is why generation stopped, as the vendor reports it.
	FinishReason string
}

// Model is a model a Provider offers.
type Model struct {
	// ID is the vendor's model identifier, used in requests.
	ID string
	// DisplayName is a human-readable name; it equals ID when the vendor
	// gives none.
	DisplayName string
}
