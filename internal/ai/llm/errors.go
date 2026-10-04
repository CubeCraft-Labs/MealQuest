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
// Adapters build it with NewRateLimitedError so its text never holds their
// credential.
type RateLimitedError struct {
	// RetryAfter is how long the vendor asked callers to wait; 0 means it
	// did not say.
	RetryAfter time.Duration
	// text is the redacted Error text set by NewRateLimitedError; "" means
	// the error was built without a credential.
	text string
}

// NewRateLimitedError returns the error for a throttled request. secret is
// the credential sent with the request, or "" when the request sent none.
// Every occurrence of a non-empty secret is removed from the error text
// when it is built, even where the fixed text happens to spell it, and the
// secret itself is not kept.
func NewRateLimitedError(retryAfter time.Duration, secret string) *RateLimitedError {
	e := &RateLimitedError{RetryAfter: retryAfter}
	e.text = Redact(e.Error(), secret)
	return e
}

func (e *RateLimitedError) Error() string {
	if e.text != "" {
		return e.text
	}
	if e.RetryAfter == 0 {
		return ErrRateLimited.Error()
	}
	return fmt.Sprintf("%s: retry after %v", ErrRateLimited, e.RetryAfter)
}

// GoString keeps %#v to the redacted Error text instead of the fields.
func (e *RateLimitedError) GoString() string { return e.Error() }

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
	text   string
}

// NewProviderError returns the error for an unexpected HTTP status. It
// always keeps a body excerpt of at most MaxBodyExcerpt bytes. secret is the
// credential sent with the request, or "" when the request sent none; every
// occurrence of a non-empty secret in body is removed by Redact before the
// body is cut, so no part of it survives, and again after the cut, so the
// truncation mark cannot complete it. It is removed from the complete error
// text too, even where the fixed text happens to spell it, and the secret
// itself is not kept.
func NewProviderError(status int, body []byte, secret string) *ProviderError {
	s := strings.TrimSpace(Redact(string(body), secret))
	if len(s) > MaxBodyExcerpt {
		cut := MaxBodyExcerpt
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = Redact(s[:cut]+"…", secret)
	}
	text := fmt.Sprintf("%s: HTTP %d", ErrProvider, status)
	if s != "" {
		text += ": " + s
	}
	return &ProviderError{status: status, body: s, text: Redact(text, secret)}
}

// Status returns the HTTP status code.
func (e *ProviderError) Status() int { return e.status }

// Body returns the redacted, truncated response-body excerpt; it is empty
// when the vendor sent no body.
func (e *ProviderError) Body() string { return e.body }

func (e *ProviderError) Error() string { return e.text }

// GoString keeps %#v to the redacted Error text instead of the fields,
// whose names could spell the credential.
func (e *ProviderError) GoString() string { return e.text }

// Is reports whether target is ErrProvider.
func (e *ProviderError) Is(target error) bool { return target == ErrProvider }

// TimeoutStatusError is an HTTP status, such as 408, by which the vendor
// said the request timed out. It matches ErrTimeout, and Status keeps it
// apart from a transport timeout, so a caller can tell the vendor answered.
// Build it with NewTimeoutStatusError, which redacts its text.
type TimeoutStatusError struct {
	status int
	text   string
}

// NewTimeoutStatusError returns the error for a timeout HTTP status.
// secret is the credential sent with the request, or "" when the request
// sent none; every occurrence of a non-empty secret is removed from the
// error text, and the secret itself is not kept.
func NewTimeoutStatusError(status int, secret string) *TimeoutStatusError {
	text := fmt.Sprintf("HTTP %d: %s", status, ErrTimeout)
	return &TimeoutStatusError{status: status, text: Redact(text, secret)}
}

// Status returns the HTTP status code.
func (e *TimeoutStatusError) Status() int { return e.status }

func (e *TimeoutStatusError) Error() string { return e.text }

// GoString keeps %#v to the redacted Error text instead of the fields.
func (e *TimeoutStatusError) GoString() string { return e.text }

// Unwrap returns ErrTimeout.
func (e *TimeoutStatusError) Unwrap() error { return ErrTimeout }
