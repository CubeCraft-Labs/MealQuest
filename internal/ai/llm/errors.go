package llm

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	// ErrAuth means the vendor rejected the credentials.
	ErrAuth = errors.New("llm: authentication failed")
	// ErrRateLimited means the vendor throttled the request. The concrete
	// error is a *RateLimitedError carrying the requested wait.
	ErrRateLimited = errors.New("llm: rate limited")
	// ErrTimeout means the request did not finish within its timeout.
	ErrTimeout = errors.New("llm: request timed out")
	// ErrBadOutput means the model replied but the reply is unusable, e.g.
	// structured output that does not parse.
	ErrBadOutput = errors.New("llm: unusable model output")
	// ErrProvider means the vendor returned an unexpected HTTP status. The
	// concrete error is a *ProviderError carrying the status and a redacted
	// body excerpt.
	ErrProvider = errors.New("llm: provider error")
)

// RateLimitedError is a throttled request. It matches ErrRateLimited.
type RateLimitedError struct {
	// RetryAfter is how long the vendor asked callers to wait; 0 means it
	// did not say.
	RetryAfter time.Duration
}

func (e *RateLimitedError) Error() string {
	if e.RetryAfter == 0 {
		return ErrRateLimited.Error()
	}
	return fmt.Sprintf("%s: retry after %v", ErrRateLimited, e.RetryAfter)
}

// Is reports whether target is ErrRateLimited.
func (e *RateLimitedError) Is(target error) bool { return target == ErrRateLimited }

// MaxBodyExcerpt is the longest response-body excerpt, in bytes, a
// ProviderError keeps.
const MaxBodyExcerpt = 512

// redacted replaces each secret in a ProviderError body.
const redacted = "[REDACTED]"

// ProviderError is an unexpected HTTP status from the vendor. It matches
// ErrProvider. Build it with NewProviderError, which redacts the body.
type ProviderError struct {
	status int
	body   string
}

// NewProviderError returns the error for an unexpected HTTP status.
// secrets must hold the API key (and any other credential sent with the
// request). Every occurrence of each non-empty secret in body is replaced
// before the body is cut to MaxBodyExcerpt bytes, so no part of a secret
// survives. If secrets holds no non-empty value the body cannot be
// redacted, so no excerpt is kept at all.
func NewProviderError(status int, body []byte, secrets []string) *ProviderError {
	s := string(body)
	redactedAny := false
	for _, secret := range secrets {
		if secret != "" {
			s = strings.ReplaceAll(s, secret, redacted)
			redactedAny = true
		}
	}
	if !redactedAny {
		return &ProviderError{status: status}
	}
	s = strings.TrimSpace(s)
	if len(s) > MaxBodyExcerpt {
		cut := MaxBodyExcerpt
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut] + "…"
	}
	return &ProviderError{status: status, body: s}
}

// Status returns the HTTP status code.
func (e *ProviderError) Status() int { return e.status }

// Body returns the redacted, truncated response-body excerpt; it is empty
// when the vendor sent no body or no secret was given to redact it with.
func (e *ProviderError) Body() string { return e.body }

func (e *ProviderError) Error() string {
	if e.body == "" {
		return fmt.Sprintf("%s: HTTP %d", ErrProvider, e.status)
	}
	return fmt.Sprintf("%s: HTTP %d: %s", ErrProvider, e.status, e.body)
}

// Is reports whether target is ErrProvider.
func (e *ProviderError) Is(target error) bool { return target == ErrProvider }
