package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/go-chi/chi/v5"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/forwarded"
)

// traefik is the production proxy's address.
const traefik = "10.60.1.33"

// probeAuth mounts GET /auth/probe, which reports the request's resolved
// origin, in place of the real auth flow.
type probeAuth struct{}

func (probeAuth) Mount(r chi.Router) {
	r.Get("/auth/probe", func(w http.ResponseWriter, r *http.Request) {
		ip, ipOK := forwarded.ClientIP(r.Context())
		scheme, schemeOK := forwarded.Scheme(r.Context())
		if !ipOK || !schemeOK {
			http.Error(w, "origin not resolved", http.StatusInternalServerError)
			return
		}
		fmt.Fprintf(w, "%s %s", ip, scheme)
	})
}

// newOriginRouter builds the production router trusting Traefik, logging
// through forwarded.LogHandler as cmd/server does.
func newOriginRouter(t *testing.T, logs io.Writer) http.Handler {
	t.Helper()
	trusted, err := forwarded.ParseTrusted(traefik)
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(forwarded.LogHandler(slog.NewJSONHandler(logs, nil)))
	r, err := NewRouter(logger, "dev", trusted, probeAuth{}, stubSessions{}, newTestSettings(t),
		newTestSearch(t), newTestRunner(t), newTestPhotos(t), fstest.MapFS{"index.html": {Data: []byte(fixtureIndex)}})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func originRequest(target, peer string, headers map[string]string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.RemoteAddr = peer
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return req
}

func TestRouterResolvesOrigin(t *testing.T) {
	t.Parallel()
	spoofed := map[string]string{forwarded.HeaderFor: "203.0.113.7", forwarded.HeaderProto: "https"}
	tests := []struct {
		name string
		peer string
		want string
	}{
		{"trusted proxy's forwarded headers are honoured", traefik + ":41000", "203.0.113.7 https"},
		{"untrusted peer spoofing them is ignored", "198.51.100.9:5000", "198.51.100.9 http"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			newOriginRouter(t, io.Discard).ServeHTTP(rec, originRequest("/auth/probe", tt.peer, spoofed))
			if rec.Code != http.StatusOK || rec.Body.String() != tt.want {
				t.Errorf("probe = %d %q, want 200 %q", rec.Code, rec.Body.String(), tt.want)
			}
		})
	}
}

func TestRouterLogsResolvedClientIP(t *testing.T) {
	t.Parallel()
	spoofed := map[string]string{forwarded.HeaderFor: "203.0.113.7", forwarded.HeaderProto: "https"}
	tests := []struct {
		name string
		peer string
		want string
	}{
		{"trusted proxy", traefik + ":41000", "203.0.113.7"},
		{"untrusted peer spoofing", "198.51.100.9:5000", "198.51.100.9"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var logs bytes.Buffer
			// A failed session lookup is logged with the request's context.
			req := originRequest("/api/v1/me", tt.peer, spoofed)
			req.AddCookie(brokenSessionCookie)
			rec := httptest.NewRecorder()
			newOriginRouter(t, &logs).ServeHTTP(rec, req)
			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500", rec.Code)
			}
			var line map[string]any
			if err := json.Unmarshal(logs.Bytes(), &line); err != nil {
				t.Fatalf("log %q: %v", logs.String(), err)
			}
			if line["msg"] != "look up session" || line[forwarded.LogAttr] != tt.want {
				t.Errorf("log = %v, want the session error with %s=%s", line, forwarded.LogAttr, tt.want)
			}
		})
	}
}

func TestRouterRejectsMalformedForwardedHeaders(t *testing.T) {
	t.Parallel()
	const badRequestJSON = `{"error":{"code":"bad_request","message":"invalid forwarded headers","retryable":false}}`
	for _, headers := range []map[string]string{
		{forwarded.HeaderFor: "not-an-ip"},
		{forwarded.HeaderProto: "ftp"},
	} {
		var logs bytes.Buffer
		rec := httptest.NewRecorder()
		newOriginRouter(t, &logs).ServeHTTP(rec, originRequest("/health", traefik+":41000", headers))
		if rec.Code != http.StatusBadRequest || rec.Body.String() != badRequestJSON {
			t.Errorf("%v: response = %d %s, want 400 %s", headers, rec.Code, rec.Body.String(), badRequestJSON)
		}
		if !bytes.Contains(logs.Bytes(), []byte("rejected forwarded headers")) {
			t.Errorf("%v: logs = %q, want the rejection logged", headers, logs.String())
		}

		// The same headers from an untrusted peer are not examined at all.
		rec = httptest.NewRecorder()
		newOriginRouter(t, io.Discard).ServeHTTP(rec, originRequest("/health", "198.51.100.9:5000", headers))
		if rec.Code != http.StatusOK {
			t.Errorf("%v from an untrusted peer: status = %d, want 200", headers, rec.Code)
		}
	}
}
