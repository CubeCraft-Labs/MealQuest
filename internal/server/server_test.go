package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

const (
	fixtureIndex = "<!doctype html><title>fixture index</title>"
	fixtureAsset = "console.log('fixture asset')"
	notFoundJSON = `{"error":{"code":"not_found","message":"not found","retryable":false}}`
)

func newTestRouter(t *testing.T, version string) http.Handler {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	assets := fstest.MapFS{
		"index.html":             {Data: []byte(fixtureIndex)},
		"assets/index-AbC123.js": {Data: []byte(fixtureAsset)},
	}
	r, err := NewRouter(logger, version, assets)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestHealth(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(newTestRouter(t, "1.2.3"))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var got map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"status": "ok", "version": "1.2.3"}
	if len(got) != len(want) || got["status"] != want["status"] || got["version"] != want["version"] {
		t.Errorf("body = %v, want %v", got, want)
	}
}

func TestHealthRejectsOtherMethods(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	newTestRouter(t, "dev").ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/health", nil))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var got struct {
		Error map[string]any `json:"error"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"code":      "method_not_allowed",
		"message":   "method not allowed",
		"retryable": false,
	}
	if !maps.Equal(got.Error, want) {
		t.Errorf("error body = %v, want %v", got.Error, want)
	}
}

func TestReservedPathsNotFound(t *testing.T) {
	t.Parallel()

	router := newTestRouter(t, "dev")
	tests := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api"},
		{http.MethodGet, "/api/unknown"},
		{http.MethodPost, "/api/unknown"},
		{http.MethodGet, "/auth/callback"},
		{http.MethodGet, "/media/photo.jpg"},
		{http.MethodGet, "/health/deep"},
	}
	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			t.Parallel()

			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(tt.method, tt.path, nil))

			if rec.Code != http.StatusNotFound {
				t.Errorf("status = %d, want 404", rec.Code)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", ct)
			}
			if got := rec.Body.String(); got != notFoundJSON {
				t.Errorf("body = %s, want %s", got, notFoundJSON)
			}
		})
	}
}

func TestFrontend(t *testing.T) {
	t.Parallel()

	router := newTestRouter(t, "dev")
	tests := []struct {
		path      string
		wantBody  string
		wantCache string
	}{
		{"/", fixtureIndex, "no-cache"},
		{"/assets/index-AbC123.js", fixtureAsset, "public, max-age=31536000, immutable"},
		{"/quests/42", fixtureIndex, "no-cache"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			t.Parallel()

			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))

			if rec.Code != http.StatusOK {
				t.Errorf("status = %d, want 200", rec.Code)
			}
			if got := rec.Body.String(); got != tt.wantBody {
				t.Errorf("body = %q, want %q", got, tt.wantBody)
			}
			if got := rec.Header().Get("Cache-Control"); got != tt.wantCache {
				t.Errorf("Cache-Control = %q, want %q", got, tt.wantCache)
			}
		})
	}
}

func TestHealthNotShadowed(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	newTestRouter(t, "dev").ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	if body := rec.Body.String(); rec.Code != http.StatusOK || strings.Contains(body, fixtureIndex) {
		t.Errorf("GET /health = %d %q, want 200 health JSON", rec.Code, body)
	}
}

func TestFrontendRejectsOtherMethods(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	newTestRouter(t, "dev").ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/quests/42", nil))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
	if strings.Contains(rec.Body.String(), fixtureIndex) {
		t.Errorf("body = %q, want no index.html", rec.Body.String())
	}
}
