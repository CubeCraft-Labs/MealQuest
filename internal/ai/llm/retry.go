package llm

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"
)

const (
	// DefaultTimeout bounds each Generate attempt whose Request sets no
	// Timeout.
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
// Provider it wraps. It retries ErrRateLimited, ErrTimeout and a 5xx
// ErrProvider at most MaxRetries times, and nothing else. A rate-limited
// retry waits for the vendor's RetryAfter, capped at MaxRetryAfter; every
// other retry, and a rate limit that names no wait, waits a jittered
// exponential backoff. A retry whose wait would not end before the
// caller's context deadline is not made: the last error is returned at
// once. ListModels and Verify pass straight through.
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
// A req.Timeout of 0 becomes DefaultTimeout; either bounds each attempt,
// not the whole call, which ctx bounds.
func (r *Retrying) Generate(ctx context.Context, req Request) (Response, error) {
	if req.Timeout == 0 {
		req.Timeout = DefaultTimeout
	}
	for retry := 0; ; retry++ {
		resp, err := r.Provider.Generate(ctx, req)
		if err == nil || retry == MaxRetries || ctx.Err() != nil {
			return resp, err
		}
		wait, ok := r.wait(err, retry)
		if !ok {
			return resp, err
		}
		if deadline, has := ctx.Deadline(); has && !r.now().Add(wait).Before(deadline) {
			return resp, err
		}
		if serr := r.sleep(ctx, wait); serr != nil {
			return resp, err
		}
	}
}

// wait returns how long to wait before retrying after err, the retry-th
// retry counting from 0, and false when err is not retried.
func (r *Retrying) wait(err error, retry int) (time.Duration, bool) {
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
