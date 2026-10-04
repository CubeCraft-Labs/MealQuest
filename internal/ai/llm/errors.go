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

// redactionMarker replaces each secret in redacted text, unless the marker
// would itself hold the secret.
const redactionMarker = "[REDACTED]"

// Redact returns s with every occurrence of a non-empty secret removed. Each
// occurrence becomes "[REDACTED]" when the result then holds no secret;
// otherwise, because the marker contains the secret or joins with its
// neighbours to form it, occurrences are deleted until none remains. The
// result never contains secret.
func Redact(s, secret string) string {
	if secret == "" {
		return s
	}
	if out := strings.ReplaceAll(s, secret, redactionMarker); !strings.Contains(out, secret) {
		return out
	}
	for strings.Contains(s, secret) {
		s = strings.ReplaceAll(s, secret, "")
	}
	return s
}

// ProviderError is an unexpected HTTP status from the vendor. It matches
// ErrProvider. Build it with NewProviderError, which redacts the body.
type ProviderError struct {
	status int
	body   string
}

// NewProviderError returns the error for an unexpected HTTP status. It
// always keeps a body excerpt of at most MaxBodyExcerpt bytes. secret is the
// credential sent with the request, or "" when the request sent none; every
// occurrence of a non-empty secret in body is removed by Redact before the
// body is cut, so no part of it survives, and again after the cut, so the
// truncation mark cannot complete it.
func NewProviderError(status int, body []byte, secret string) *ProviderError {
	s := strings.TrimSpace(Redact(string(body), secret))
	if len(s) > MaxBodyExcerpt {
		cut := MaxBodyExcerpt
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = Redact(s[:cut]+"…", secret)
	}
	return &ProviderError{status: status, body: s}
}

// Status returns the HTTP status code.
func (e *ProviderError) Status() int { return e.status }

// Body returns the redacted, truncated response-body excerpt; it is empty
// when the vendor sent no body.
func (e *ProviderError) Body() string { return e.body }

func (e *ProviderError) Error() string {
	if e.body == "" {
		return fmt.Sprintf("%s: HTTP %d", ErrProvider, e.status)
	}
	return fmt.Sprintf("%s: HTTP %d: %s", ErrProvider, e.status, e.body)
}

// Is reports whether target is ErrProvider.
func (e *ProviderError) Is(target error) bool { return target == ErrProvider }
