package llm

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestSentinelErrorsMatchWhenWrapped(t *testing.T) {
	for _, sentinel := range []error{ErrAuth, ErrTimeout, ErrBadOutput} {
		wrapped := fmt.Errorf("openai: generate: %w", sentinel)
		if !errors.Is(wrapped, sentinel) {
			t.Errorf("errors.Is(%v, %v) = false", wrapped, sentinel)
		}
		for _, other := range []error{ErrAuth, ErrTimeout, ErrBadOutput, ErrRateLimited, ErrProvider} {
			if other != sentinel && errors.Is(wrapped, other) {
				t.Errorf("errors.Is(%v, %v) = true", wrapped, other)
			}
		}
	}
}

func TestRateLimitedError(t *testing.T) {
	err := fmt.Errorf("anthropic: %w", &RateLimitedError{RetryAfter: 30 * time.Second})
	if !errors.Is(err, ErrRateLimited) {
		t.Fatal("errors.Is(err, ErrRateLimited) = false")
	}
	if errors.Is(err, ErrProvider) || errors.Is(err, ErrAuth) {
		t.Fatal("rate-limited error matches an unrelated sentinel")
	}
	var rl *RateLimitedError
	if !errors.As(err, &rl) {
		t.Fatal("errors.As(err, *RateLimitedError) = false")
	}
	if rl.RetryAfter != 30*time.Second {
		t.Fatalf("RetryAfter = %v, want 30s", rl.RetryAfter)
	}
	if got := err.Error(); got != "anthropic: llm: rate limited: retry after 30s" {
		t.Fatalf("Error() = %q", got)
	}
	if got := (&RateLimitedError{}).Error(); got != "llm: rate limited" {
		t.Fatalf("Error() without RetryAfter = %q", got)
	}
}

func TestProviderError(t *testing.T) {
	err := fmt.Errorf("gemini: %w", NewProviderError(503, []byte(` {"error":"overloaded"} `), "sk-test"))
	if !errors.Is(err, ErrProvider) {
		t.Fatal("errors.Is(err, ErrProvider) = false")
	}
	if errors.Is(err, ErrRateLimited) || errors.Is(err, ErrAuth) {
		t.Fatal("provider error matches an unrelated sentinel")
	}
	var pe *ProviderError
	if !errors.As(err, &pe) {
		t.Fatal("errors.As(err, *ProviderError) = false")
	}
	if pe.Status() != 503 || pe.Body() != `{"error":"overloaded"}` {
		t.Fatalf("Status, Body = %d, %q", pe.Status(), pe.Body())
	}
	if got := err.Error(); got != `gemini: llm: provider error: HTTP 503: {"error":"overloaded"}` {
		t.Fatalf("Error() = %q", got)
	}
	if got := NewProviderError(500, nil, "sk-test").Error(); got != "llm: provider error: HTTP 500" {
		t.Fatalf("Error() without body = %q", got)
	}
}

func TestProviderErrorRedactsSecrets(t *testing.T) {
	const key = "sk-live-0123456789abcdef"
	body := []byte(`{"error":"invalid key sk-live-0123456789abcdef","echo":"Bearer sk-live-0123456789abcdef"}`)
	pe := NewProviderError(400, body, key)
	for _, s := range []string{pe.Error(), pe.Body()} {
		if strings.Contains(s, key) {
			t.Fatalf("secret leaked: %q", s)
		}
	}
	want := `{"error":"invalid key [REDACTED]","echo":"Bearer [REDACTED]"}`
	if pe.Body() != want {
		t.Fatalf("Body() = %q, want %q", pe.Body(), want)
	}
}

func TestProviderErrorRedactsBeforeTruncating(t *testing.T) {
	const key = "sk-secret-straddling-the-cut"
	// The key starts just before the excerpt limit, so truncating first
	// would leave its prefix in the excerpt.
	body := strings.Repeat("x", MaxBodyExcerpt-5) + key
	pe := NewProviderError(500, []byte(body), key)
	if strings.Contains(pe.Body(), "sk-se") {
		t.Fatalf("secret prefix leaked: %q", pe.Body())
	}
}

func TestProviderErrorTruncatesOnRuneBoundary(t *testing.T) {
	body := strings.Repeat("a", MaxBodyExcerpt-1) + "é" + strings.Repeat("b", 100)
	pe := NewProviderError(500, []byte(body), "sk-test")
	want := strings.Repeat("a", MaxBodyExcerpt-1) + "…"
	if pe.Body() != want {
		t.Fatalf("Body() has %d bytes, want %d", len(pe.Body()), len(want))
	}
}

func TestProviderErrorKeepsBoundedBodyWithoutSecret(t *testing.T) {
	body := []byte(` {"error":"model \"llama9\" not found"} `)
	pe := NewProviderError(404, body, "")
	if pe.Body() != `{"error":"model \"llama9\" not found"}` {
		t.Fatalf("Body() = %q", pe.Body())
	}
	if got := pe.Error(); got != `llm: provider error: HTTP 404: {"error":"model \"llama9\" not found"}` {
		t.Fatalf("Error() = %q", got)
	}
	long := NewProviderError(500, []byte(strings.Repeat("x", 4*MaxBodyExcerpt)), "")
	if want := strings.Repeat("x", MaxBodyExcerpt) + "…"; long.Body() != want {
		t.Fatalf("Body() has %d bytes, want %d", len(long.Body()), len(want))
	}
}
