package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestFakeGenerateReturnsScriptInOrder(t *testing.T) {
	ctx := context.Background()
	var f Fake
	f.QueueGenerate(
		GenerateResult{Response: Response{Text: "first", Model: "m1"}},
		GenerateResult{Err: &RateLimitedError{RetryAfter: time.Second}},
	)
	f.QueueGenerate(GenerateResult{Response: Response{JSON: json.RawMessage(`{"ok":true}`), FinishReason: "stop"}})

	req := Request{Messages: []Message{{Role: RoleUser, Content: "one"}}}
	resp, err := f.Generate(ctx, req)
	if err != nil || resp.Text != "first" || resp.Model != "m1" {
		t.Fatalf("call 1 = %+v, %v", resp, err)
	}
	if _, err := f.Generate(ctx, req); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("call 2 err = %v, want ErrRateLimited", err)
	}
	resp, err = f.Generate(ctx, req)
	if err != nil || string(resp.JSON) != `{"ok":true}` || resp.FinishReason != "stop" {
		t.Fatalf("call 3 = %+v, %v", resp, err)
	}
	if _, err := f.Generate(ctx, req); err == nil {
		t.Fatal("unscripted call 4 succeeded")
	}
	if g, l, v := f.Remaining(); g != 0 || l != 0 || v != 0 {
		t.Fatalf("Remaining() = %d, %d, %d", g, l, v)
	}
	if n := len(f.Requests()); n != 4 {
		t.Fatalf("recorded %d requests, want 4 (unscripted calls are recorded too)", n)
	}
}

func TestFakeRecordsRequests(t *testing.T) {
	var f Fake
	f.QueueGenerate(GenerateResult{}, GenerateResult{})
	temp := 0.2
	first := Request{
		System:      "be brief",
		Messages:    []Message{{Role: RoleUser, Content: "plan dinners"}},
		Temperature: &temp,
		MaxTokens:   100,
		Schema:      &JSONSchema{Name: "plan", Schema: json.RawMessage(`{"type":"object"}`)},
		Timeout:     5 * time.Second,
	}
	if _, err := f.Generate(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Generate(context.Background(), Request{System: "second"}); err != nil {
		t.Fatal(err)
	}

	// Later changes by the caller must not alter what was recorded.
	first.Messages[0].Content = "changed"
	temp = 1
	first.Schema.Schema[2] = 'X'

	got := f.Requests()
	if len(got) != 2 || got[1].System != "second" {
		t.Fatalf("Requests() = %+v", got)
	}
	r := got[0]
	if r.System != "be brief" || r.Messages[0].Content != "plan dinners" || *r.Temperature != 0.2 ||
		r.MaxTokens != 100 || r.Schema.Name != "plan" || string(r.Schema.Schema) != `{"type":"object"}` ||
		r.Timeout != 5*time.Second {
		t.Fatalf("recorded request = %+v", r)
	}

	// Changing a returned copy must not alter the record either.
	got[0].Messages[0].Content = "mutated"
	if f.Requests()[0].Messages[0].Content != "plan dinners" {
		t.Fatal("Requests() returned shared state")
	}
}

func TestFakeListModelsAndVerify(t *testing.T) {
	ctx := context.Background()
	var f Fake
	f.QueueListModels(
		ListModelsResult{Models: []Model{{ID: "a", DisplayName: "A"}, {ID: "b", DisplayName: "b"}}},
		ListModelsResult{Err: ErrAuth},
	)
	f.QueueVerify(nil, fmt.Errorf("wrapped: %w", ErrAuth))

	models, err := f.ListModels(ctx)
	if err != nil || len(models) != 2 || models[0].ID != "a" {
		t.Fatalf("ListModels 1 = %+v, %v", models, err)
	}
	if _, err := f.ListModels(ctx); !errors.Is(err, ErrAuth) {
		t.Fatalf("ListModels 2 err = %v, want ErrAuth", err)
	}
	if _, err := f.ListModels(ctx); err == nil {
		t.Fatal("unscripted ListModels succeeded")
	}
	if err := f.Verify(ctx); err != nil {
		t.Fatalf("Verify 1 = %v", err)
	}
	if err := f.Verify(ctx); !errors.Is(err, ErrAuth) {
		t.Fatalf("Verify 2 = %v, want ErrAuth", err)
	}
	if err := f.Verify(ctx); err == nil {
		t.Fatal("unscripted Verify succeeded")
	}
}

func TestFakeConcurrentUse(t *testing.T) {
	const n = 50
	var f Fake
	for i := range n {
		f.QueueGenerate(GenerateResult{Response: Response{Text: fmt.Sprint(i)}})
	}
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() {
			if _, err := f.Generate(context.Background(), Request{}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if len(f.Requests()) != n {
		t.Fatalf("recorded %d requests, want %d", len(f.Requests()), n)
	}
	if g, _, _ := f.Remaining(); g != 0 {
		t.Fatalf("%d Generate results unused", g)
	}
}

func TestFakeGenerateBlocksUntilReleased(t *testing.T) {
	var f Fake
	started, release := make(chan struct{}), make(chan struct{})
	f.QueueGenerate(GenerateResult{Response: Response{Text: "done"}, Started: started, Release: release})
	got := make(chan string)
	go func() {
		resp, err := f.Generate(context.Background(), Request{})
		if err != nil {
			t.Error(err)
		}
		got <- resp.Text
	}()
	<-started
	select {
	case text := <-got:
		t.Fatalf("Generate returned %q before release", text)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if text := <-got; text != "done" {
		t.Fatalf("Generate = %q, want the scripted reply", text)
	}
}

func TestFakeBlockedGenerateHonoursContext(t *testing.T) {
	var f Fake
	f.QueueGenerate(GenerateResult{Response: Response{Text: "never"}, Release: make(chan struct{})})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := f.Generate(ctx, Request{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
}
