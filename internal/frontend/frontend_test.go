package frontend

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

const (
	indexBody    = "<!doctype html><title>fixture index</title>"
	assetBody    = "console.log('fixture asset')"
	robotsBody   = "User-agent: *"
	swBody       = "self.addEventListener('fetch', () => {})"
	manifestBody = `{"name":"MealQuest"}`
	notFoundBody = "reserved not found"
)

func fixture() fstest.MapFS {
	return fstest.MapFS{
		"index.html":             {Data: []byte(indexBody)},
		"sw.js":                  {Data: []byte(swBody)},
		"manifest.webmanifest":   {Data: []byte(manifestBody)},
		"assets/index-AbC123.js": {Data: []byte(assetBody)},
		"robots.txt":             {Data: []byte(robotsBody)},
	}
}

func newTestHandler(t *testing.T) http.Handler {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	notFound := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, notFoundBody, http.StatusNotFound)
	})
	h, err := NewHandler(fixture(), logger, notFound, "/api", "/auth", "/media", "/health")
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestServe(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		method    string
		path      string
		wantBody  string
		wantCache string
	}{
		{"hashed asset", http.MethodGet, "/assets/index-AbC123.js", assetBody, cacheImmutable},
		{"unhashed asset", http.MethodGet, "/robots.txt", robotsBody, cacheImmutable},
		{"root", http.MethodGet, "/", indexBody, cacheNoCache},
		{"index.html", http.MethodGet, "/index.html", indexBody, cacheNoCache},
		{"service worker", http.MethodGet, "/sw.js", swBody, cacheNoCache},
		{"manifest", http.MethodGet, "/manifest.webmanifest", manifestBody, cacheNoCache},
		{"client route", http.MethodGet, "/quests/42", indexBody, cacheNoCache},
		{"missing asset", http.MethodGet, "/assets/missing.js", indexBody, cacheNoCache},
		{"directory", http.MethodGet, "/assets", indexBody, cacheNoCache},
		{"dot segments", http.MethodGet, "/x/../../sw.js", swBody, cacheNoCache},
		{"prefix lookalike", http.MethodGet, "/apiary", indexBody, cacheNoCache},
		{"head", http.MethodHead, "/assets/index-AbC123.js", "", cacheImmutable},
	}
	h := newTestHandler(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(tt.method, tt.path, nil))

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

func TestManifestContentType(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	newTestHandler(t).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/manifest.webmanifest", nil))

	if got := rec.Header().Get("Content-Type"); got != "application/manifest+json" {
		t.Errorf("Content-Type = %q, want application/manifest+json", got)
	}
}

func TestReservedPrefixesNotServed(t *testing.T) {
	t.Parallel()

	h := newTestHandler(t)
	for _, path := range []string{"/api", "/api/unknown", "/auth/login", "/media/x.jpg", "/health", "/health/deep", "/x/../api/unknown", "/auth/../quests/42", "/api%2F..%2Fquests/42"} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()

			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))

			if rec.Code != http.StatusNotFound {
				t.Errorf("status = %d, want 404", rec.Code)
			}
			if got := rec.Body.String(); got != notFoundBody+"\n" {
				t.Errorf("body = %q, want the notFound handler's body", got)
			}
		})
	}
}

func TestNewHandlerRequiresIndex(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	fsys := fstest.MapFS{"sw.js": {Data: []byte(swBody)}}
	if _, err := NewHandler(fsys, logger, http.NotFoundHandler()); err == nil {
		t.Fatal("NewHandler without index.html: want error, got nil")
	}
}

func TestAssetsContainsIndex(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := NewHandler(Assets(), logger, http.NotFoundHandler()); err != nil {
		t.Fatalf("embedded assets: %v", err)
	}
}
