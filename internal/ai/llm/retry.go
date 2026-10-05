package llm

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"
)

const (
	// DefaultTimeout bounds a whole Generate call, retries and waits
	// included, whose Request sets no Timeout.
	DefaultTimeout = 60 * time.Second
	// MaxRetries is how many times a failed Generate is retried, so a call
	// makes at most MaxRetries+1 attempts.
	MaxRetries = 2
	// MaxRetryAfter caps the wait a rate-limited vendor may ask for.
	MaxRetryAfter = 20 * time.Second
	// backoffBase is the first retry's backoff before jitter; each later
	// retry doubles it.
	backoffBase = time.Second
)

// Retrying is a Provider that retries transient Generate failures of the
// Provider it wraps. It retries ErrRateLimited, a transport ErrTimeout and
// a 5xx ErrProvider at most MaxRetries times, and nothing else: an error
// carrying a 4xx HTTP status other than a rate limit, such as an HTTP 408
// TimeoutStatusError, is not retried. A rate-limited retry waits for the
// vendor's RetryAfter, capped at MaxRetryAfter; every other retry, and a
// rate limit that names no wait, waits a jittered exponential backoff. The Request's Timeout, or DefaultTimeout, is one
// deadline for the whole call, retries and waits included; a caller's
// earlier context deadline wins. A retry whose wait would not end before
// that deadline is not made: the last error is returned at once. A wait
// cut short by cancellation or the deadline returns the context's error.
// ListModels and Verify pass straight through.
type Retrying struct {
	Provider
	// now is the clock; tests replace it.
	now func() time.Time
	// sleep waits d or until ctx is done; tests replace it.
	sleep func(ctx context.Context, d time.Duration) error
	// jitter returns a random duration in [0, n) for n > 0; tests replace
	// it.
	jitter func(n time.Duration) time.Duration
}

// NewRetrying returns inner wrapped with retries and the default Generate
// timeout.
func NewRetrying(inner Provider) *Retrying {
	return &Retrying{
		Provider: inner,
		now:      time.Now,
		sleep:    sleep,
		jitter:   rand.N[time.Duration],
	}
}

// Generate sends req to the wrapped Provider, retrying transient failures.
// req.Timeout, or DefaultTimeout when it is 0, sets one deadline for the
// whole call, carried by the context each attempt receives; attempts get
// req with Timeout 0 so none restarts the clock.
func (r *Retrying) Generate(ctx context.Context, req Request) (Response, error) {
	timeout := req.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	if timeout > 0 {
		// A negative Timeout stays on req for the wrapped Provider's
		// Validate to reject, with its key redaction.
		req.Timeout = 0
	}
	ctx, cancel := context.WithDeadline(ctx, r.now().Add(timeout))
	defer cancel()
	deadline, _ := ctx.Deadline()
	for retry := 0; ; retry++ {
		resp, err := r.Provider.Generate(ctx, req)
		if err == nil || retry == MaxRetries || ctx.Err() != nil {
			return resp, err
		}
		wait, ok := r.wait(err, retry)
		if !ok {
			return resp, err
		}
		if !r.now().Add(wait).Before(deadline) {
			return resp, err
		}
		if err := r.sleep(ctx, wait); err != nil {
			return Response{}, err
		}
	}
}

// wait returns how long to wait before retrying after err, the retry-th
// retry counting from 0, and false when err is not retried.
func (r *Retrying) wait(err error, retry int) (time.Duration, bool) {
	var st interface{ Status() int }
	if errors.As(err, &st) && st.Status() >= 400 && st.Status() <= 499 {
		return 0, false
	}
	var rl *RateLimitedError
	if errors.As(err, &rl) && rl.RetryAfter > 0 {
		return min(rl.RetryAfter, MaxRetryAfter), true
	}
	var pe *ProviderError
	switch {
	case errors.Is(err, ErrRateLimited), errors.Is(err, ErrTimeout):
	case errors.As(err, &pe) && pe.Status() >= 500 && pe.Status() <= 599:
	default:
		return 0, false
	}
	half := (backoffBase << retry) / 2
	return half + r.jitter(half), true
}

// sleep waits d or until ctx is done, returning ctx's error in that case.
func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
