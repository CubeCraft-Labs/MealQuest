package harness

import (
	"context"
	"fmt"
	"sync"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/llm"
)

// DefaultMaxCalls is how many LLM calls a Guard lets run at once unless it
// is built with another limit.
const DefaultMaxCalls = 2

// DefaultTimeout is the run timeout a skill declares unless it needs
// another.
const DefaultTimeout = 60 * time.Second

// Guard bounds the process's runs. Runs of one skill with one scope share a
// single in-flight execution and its result (single-flight), and at most
// its limit of LLM calls run at once across every run (the semaphore).
// Build one with NewGuard and share it between every run of the process.
type Guard struct {
	slots chan struct{}

	mu      sync.Mutex
	flights map[flightKey]*flight
}

// flightKey identifies the runs that share one execution.
type flightKey struct {
	skill string
	scope string
}

// flight is one in-flight execution and the callers waiting for it.
type flight struct {
	done chan struct{}
	val  any
	err  error
	// waiters counts the callers still waiting; when the last one gives
	// up, cancel stops the execution.
	waiters int
	cancel  context.CancelFunc
}

// NewGuard returns a Guard that lets maxCalls LLM calls run at once.
func NewGuard(maxCalls int) (*Guard, error) {
	if maxCalls < 1 {
		return nil, fmt.Errorf("harness: guard: max concurrent LLM calls %d is not positive", maxCalls)
	}
	return &Guard{slots: make(chan struct{}, maxCalls), flights: map[flightKey]*flight{}}, nil
}

// do returns the result of fn for key, starting fn unless a call for key is
// already in flight, in which case it waits for that call's result
// instead. fn runs on a context that keeps the first caller's values but
// not its cancellation, so one caller giving up does not fail the others.
// When ctx is done first, do returns its error; when no caller is left
// waiting, fn's context is cancelled and do returns only once fn has
// ended, so the abandoned execution has released its slot by then.
func (g *Guard) do(ctx context.Context, key flightKey, fn func(context.Context) (any, error)) (any, error) {
	g.mu.Lock()
	f, ok := g.flights[key]
	if !ok {
		fctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		f = &flight{done: make(chan struct{}), cancel: cancel}
		g.flights[key] = f
		go func() {
			defer cancel()
			f.val, f.err = fn(fctx)
			g.forget(key, f)
			close(f.done)
		}()
	}
	f.waiters++
	g.mu.Unlock()

	select {
	case <-f.done:
		return f.val, f.err
	case <-ctx.Done():
	}
	g.mu.Lock()
	f.waiters--
	last := f.waiters == 0
	if last {
		// No caller may join an execution that is being abandoned.
		g.forgetLocked(key, f)
		f.cancel()
	}
	g.mu.Unlock()
	if last {
		<-f.done
	}
	return nil, ctx.Err()
}

// forget removes f from the in-flight executions if it is still there.
func (g *Guard) forget(key flightKey, f *flight) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.forgetLocked(key, f)
}

func (g *Guard) forgetLocked(key flightKey, f *flight) {
	if g.flights[key] == f {
		delete(g.flights, key)
	}
}

// limited is a Provider whose Generate calls hold one of its Guard's slots.
type limited struct {
	llm.Provider
	slots chan struct{}
}

// Generate waits for a free slot, or for ctx to be done, then sends req
// and frees the slot when the call returns.
func (p limited) Generate(ctx context.Context, req llm.Request) (llm.Response, error) {
	// A done context never takes a slot, even when one is free.
	if err := ctx.Err(); err != nil {
		return llm.Response{}, err
	}
	select {
	case p.slots <- struct{}{}:
	case <-ctx.Done():
		return llm.Response{}, ctx.Err()
	}
	defer func() { <-p.slots }()
	return p.Provider.Generate(ctx, req)
}
