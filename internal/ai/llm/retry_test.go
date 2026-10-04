package llm

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"
)

// fakeClock is a clock whose sleeps return at once, advance it and are
// recorded.
type fakeClock struct {
	t      time.Time
	sleeps []time.Duration
}

func (c *fakeClock) now() time.Time { return c.t }

func (c *fakeClock) sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.sleeps = append(c.sleeps, d)
	c.t = c.t.Add(d)
	return nil
}

// newTestRetrying returns a Retrying over a fresh Fake with a fake clock
// at the real current time, so real context deadlines compare sensibly,
// and jitter that always returns its maximum.
func newTestRetrying() (*Retrying, *Fake, *fakeClock) {
	f := &Fake{}
	c := &fakeClock{t: time.Now()}
	r := NewRetrying(f)
	r.now = c.now
	r.sleep = c.sleep
	r.jitter = func(n time.Duration) time.Duration { return n - 1 }
	return r, f, c
}

var retryReq = Request{Messages: []Message{{Role: RoleUser, Content: "plan dinners"}}}

func ok(text string) GenerateResult { return GenerateResult{Response: Response{Text: text}} }

func fail(err error) GenerateResult { return GenerateResult{Err: err} }

func assertConsumed(t *testing.T, f *Fake, attempts int) {
	t.Helper()
	if g, _, _ := f.Remaining(); g != 0 {
		t.Errorf("%d scripted Generate results unused", g)
	}
	if n := len(f.Requests()); n != attempts {
		t.Errorf("made %d attempts, want %d", n, attempts)
	}
}

func TestRetryingRateLimitedThenSuccess(t *testing.T) {
	r, f, c := newTestRetrying()
	f.QueueGenerate(fail(NewRateLimitedError(3*time.Second, "")), ok("dinner"))

	resp, err := r.Generate(context.Background(), retryReq)
	if err != nil || resp.Text != "dinner" {
		t.Fatalf("Generate = %+v, %v", resp, err)
	}
	if want := []time.Duration{3 * time.Second}; !slices.Equal(c.sleeps, want) {
		t.Errorf("sleeps = %v, want %v", c.sleeps, want)
	}
	assertConsumed(t, f, 2)
}

func TestRetryingRateLimitedWaitIsCapped(t *testing.T) {
	r, f, c := newTestRetrying()
	f.QueueGenerate(fail(NewRateLimitedError(time.Hour, "")), ok("dinner"))

	if _, err := r.Generate(context.Background(), retryReq); err != nil {
		t.Fatal(err)
	}
	if want := []time.Duration{MaxRetryAfter}; !slices.Equal(c.sleeps, want) {
		t.Errorf("sleeps = %v, want %v", c.sleeps, want)
	}
	assertConsumed(t, f, 2)
}

func TestRetryingRateLimitedWithoutRetryAfterBacksOff(t *testing.T) {
	r, f, c := newTestRetrying()
	f.QueueGenerate(fail(NewRateLimitedError(0, "")), ok("dinner"))

	if _, err := r.Generate(context.Background(), retryReq); err != nil {
		t.Fatal(err)
	}
	if want := []time.Duration{time.Second - 1}; !slices.Equal(c.sleeps, want) {
		t.Errorf("sleeps = %v, want %v", c.sleeps, want)
	}
}

func TestRetrying5xxTwiceThenSuccess(t *testing.T) {
	r, f, c := newTestRetrying()
	f.QueueGenerate(
		fail(NewProviderError(503, []byte("busy"), "")),
		// Adapters wrap the error they return.
		fail(fmt.Errorf("openai: generate: %w", NewProviderError(503, []byte("busy"), ""))),
		ok("dinner"),
	)

	resp, err := r.Generate(context.Background(), retryReq)
	if err != nil || resp.Text != "dinner" {
		t.Fatalf("Generate = %+v, %v", resp, err)
	}
	// Max jitter: [0.5 s, 1 s) then [1 s, 2 s).
	if want := []time.Duration{time.Second - 1, 2*time.Second - 1}; !slices.Equal(c.sleeps, want) {
		t.Errorf("sleeps = %v, want %v", c.sleeps, want)
	}
	assertConsumed(t, f, 3)
}

func TestRetryingBackoffJitterLowerBound(t *testing.T) {
	r, f, c := newTestRetrying()
	r.jitter = func(time.Duration) time.Duration { return 0 }
	f.QueueGenerate(fail(ErrTimeout), fail(ErrTimeout), ok("dinner"))

	if _, err := r.Generate(context.Background(), retryReq); err != nil {
		t.Fatal(err)
	}
	if want := []time.Duration{500 * time.Millisecond, time.Second}; !slices.Equal(c.sleeps, want) {
		t.Errorf("sleeps = %v, want %v", c.sleeps, want)
	}
}

func TestRetrying5xxThreeTimesFails(t *testing.T) {
	r, f, c := newTestRetrying()
	last := NewProviderError(503, []byte("third"), "")
	f.QueueGenerate(
		fail(NewProviderError(503, []byte("first"), "")),
		fail(NewProviderError(503, []byte("second"), "")),
		fail(last),
	)

	_, err := r.Generate(context.Background(), retryReq)
	if err != last {
		t.Fatalf("err = %v, want the last attempt's error", err)
	}
	if len(c.sleeps) != MaxRetries {
		t.Errorf("slept %d times, want %d", len(c.sleeps), MaxRetries)
	}
	assertConsumed(t, f, 3)
}

func TestRetryingDoesNotRetryPermanentErrors(t *testing.T) {
	for name, err := range map[string]error{
		"401 auth":   ErrAuth,
		"400":        NewProviderError(400, []byte("bad request"), ""),
		"404":        NewProviderError(404, []byte("no such model"), ""),
		"bad output": ErrBadOutput,
		"other":      errors.New("llm: request has no messages"),
	} {
		t.Run(name, func(t *testing.T) {
			r, f, c := newTestRetrying()
			f.QueueGenerate(fail(err))

			if _, got := r.Generate(context.Background(), retryReq); got != err {
				t.Fatalf("err = %v, want %v", got, err)
			}
			if len(c.sleeps) != 0 {
				t.Errorf("slept %v", c.sleeps)
			}
			assertConsumed(t, f, 1)
		})
	}
}

func TestRetryingDeadlineShorterThanRetryAfter(t *testing.T) {
	r, f, c := newTestRetrying()
	rl := NewRateLimitedError(10*time.Second, "")
	f.QueueGenerate(fail(rl))
	ctx, cancel := context.WithDeadline(context.Background(), c.t.Add(5*time.Second))
	defer cancel()

	if _, err := r.Generate(ctx, retryReq); err != rl {
		t.Fatalf("err = %v, want the rate-limited error", err)
	}
	if len(c.sleeps) != 0 {
		t.Errorf("slept %v", c.sleeps)
	}
	assertConsumed(t, f, 1)
}

func TestRetryingDeadlineShorterThanBackoff(t *testing.T) {
	r, f, c := newTestRetrying()
	f.QueueGenerate(fail(ErrTimeout))
	// The first backoff with maximum jitter is just under 1 s.
	ctx, cancel := context.WithDeadline(context.Background(), c.t.Add(time.Second-1))
	defer cancel()

	if _, err := r.Generate(ctx, retryReq); !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
	if len(c.sleeps) != 0 {
		t.Errorf("slept %v", c.sleeps)
	}
	assertConsumed(t, f, 1)
}

func TestRetryingRetriesWithinDeadline(t *testing.T) {
	r, f, c := newTestRetrying()
	f.QueueGenerate(fail(NewRateLimitedError(3*time.Second, "")), ok("dinner"))
	ctx, cancel := context.WithDeadline(context.Background(), c.t.Add(time.Minute))
	defer cancel()

	if _, err := r.Generate(ctx, retryReq); err != nil {
		t.Fatal(err)
	}
	assertConsumed(t, f, 2)
}

func TestRetryingStopsWhenContextDone(t *testing.T) {
	r, f, _ := newTestRetrying()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f.QueueGenerate(fail(ErrTimeout))

	if _, err := r.Generate(ctx, retryReq); !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
	assertConsumed(t, f, 1)
}

func TestRetryingDefaultTimeout(t *testing.T) {
	r, f, _ := newTestRetrying()
	f.QueueGenerate(ok("a"), ok("b"))
	custom := retryReq
	custom.Timeout = 5 * time.Second

	for _, req := range []Request{retryReq, custom} {
		if _, err := r.Generate(context.Background(), req); err != nil {
			t.Fatal(err)
		}
	}
	got := f.Requests()
	if got[0].Timeout != DefaultTimeout || got[1].Timeout != 5*time.Second {
		t.Fatalf("timeouts = %v, %v; want %v, 5s", got[0].Timeout, got[1].Timeout, DefaultTimeout)
	}
}

func TestRetryingPassesThroughListModelsAndVerify(t *testing.T) {
	r, f, _ := newTestRetrying()
	f.QueueListModels(ListModelsResult{Err: ErrTimeout})
	f.QueueVerify(ErrTimeout)

	if _, err := r.ListModels(context.Background()); !errors.Is(err, ErrTimeout) {
		t.Fatalf("ListModels err = %v", err)
	}
	if err := r.Verify(context.Background()); !errors.Is(err, ErrTimeout) {
		t.Fatalf("Verify err = %v", err)
	}
	if _, l, v := f.Remaining(); l != 0 || v != 0 {
		t.Fatalf("Remaining = %d, %d", l, v)
	}
}

func TestSleepStopsWhenContextDone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleep(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("sleep err = %v", err)
	}
	if err := sleep(context.Background(), time.Millisecond); err != nil {
		t.Fatalf("sleep err = %v", err)
	}
}
