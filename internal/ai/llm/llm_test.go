package llm

import (
	"encoding/json"
	"testing"
)

func TestRequestValidate(t *testing.T) {
	neg := -0.1
	valid := func() Request {
		return Request{
			Messages: []Message{{Role: RoleUser, Content: "hi"}, {Role: RoleAssistant, Content: "hello"}},
			Schema:   &JSONSchema{Name: "plan", Schema: json.RawMessage(`{"type":"object"}`)},
		}
	}
	if err := valid().Validate(); err != nil {
		t.Fatalf("valid request: %v", err)
	}
	cases := map[string]func(*Request){
		"no messages":    func(r *Request) { r.Messages = nil },
		"unknown role":   func(r *Request) { r.Messages[0].Role = "system" },
		"negative temp":  func(r *Request) { r.Temperature = &neg },
		"negative max":   func(r *Request) { r.MaxTokens = -1 },
		"negative time":  func(r *Request) { r.Timeout = -1 },
		"unnamed schema": func(r *Request) { r.Schema.Name = "" },
		"schema array":   func(r *Request) { r.Schema.Schema = json.RawMessage(`[]`) },
		"schema null":    func(r *Request) { r.Schema.Schema = json.RawMessage(`null`) },
		"schema invalid": func(r *Request) { r.Schema.Schema = json.RawMessage(`{`) },
		"schema empty":   func(r *Request) { r.Schema.Schema = nil },
	}
	for name, mutate := range cases {
		r := valid()
		mutate(&r)
		if err := r.Validate(); err == nil {
			t.Errorf("%s: Validate() = nil", name)
		}
	}
}

func TestUsageTotal(t *testing.T) {
	if got := (Usage{InputTokens: 12, OutputTokens: 30}).Total(); got != 42 {
		t.Fatalf("Total() = %d, want 42", got)
	}
}
