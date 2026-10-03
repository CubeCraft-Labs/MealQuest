package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync"
)

// Fake is a scripted Provider for tests. Each method returns its queued
// results in order, one per call, and records every Request Generate
// receives. A call with nothing left in its queue fails, so an unexpected
// call is never silently answered. It is safe for concurrent use.
type Fake struct {
	mu       sync.Mutex
	generate []GenerateResult
	models   []ListModelsResult
	verify   []error
	requests []Request
}

var _ Provider = (*Fake)(nil)

// GenerateResult is one scripted Generate outcome.
type GenerateResult struct {
	Response Response
	Err      error
}

// ListModelsResult is one scripted ListModels outcome.
type ListModelsResult struct {
	Models []Model
	Err    error
}

// QueueGenerate appends results to the Generate script.
func (f *Fake) QueueGenerate(results ...GenerateResult) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.generate = append(f.generate, results...)
}

// QueueListModels appends results to the ListModels script.
func (f *Fake) QueueListModels(results ...ListModelsResult) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.models = append(f.models, results...)
}

// QueueVerify appends results to the Verify script; a nil entry is success.
func (f *Fake) QueueVerify(results ...error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.verify = append(f.verify, results...)
}

// Generate records req and returns the next scripted result.
func (f *Fake) Generate(_ context.Context, req Request) (Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, cloneRequest(req))
	if len(f.generate) == 0 {
		return Response{}, fmt.Errorf("llm: fake: unscripted Generate call %d", len(f.requests))
	}
	r := f.generate[0]
	f.generate = f.generate[1:]
	return r.Response, r.Err
}

// ListModels returns the next scripted result.
func (f *Fake) ListModels(context.Context) ([]Model, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.models) == 0 {
		return nil, fmt.Errorf("llm: fake: unscripted ListModels call")
	}
	r := f.models[0]
	f.models = f.models[1:]
	return slices.Clone(r.Models), r.Err
}

// Verify returns the next scripted result.
func (f *Fake) Verify(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.verify) == 0 {
		return fmt.Errorf("llm: fake: unscripted Verify call")
	}
	err := f.verify[0]
	f.verify = f.verify[1:]
	return err
}

// Requests returns a copy of every Request Generate received, in order.
func (f *Fake) Requests() []Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Request, len(f.requests))
	for i, r := range f.requests {
		out[i] = cloneRequest(r)
	}
	return out
}

// Remaining reports how many scripted results of each kind are unused, so a
// test can assert its script was consumed.
func (f *Fake) Remaining() (generate, listModels, verify int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.generate), len(f.models), len(f.verify)
}

// cloneRequest deep-copies r so a recorded Request is unaffected by later
// changes the caller makes to its slices and pointers.
func cloneRequest(r Request) Request {
	r.Messages = slices.Clone(r.Messages)
	if r.Temperature != nil {
		t := *r.Temperature
		r.Temperature = &t
	}
	if r.Schema != nil {
		s := JSONSchema{Name: r.Schema.Name, Schema: json.RawMessage(slices.Clone([]byte(r.Schema.Schema)))}
		r.Schema = &s
	}
	return r
}
