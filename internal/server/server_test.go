package server

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/go-chi/chi/v5"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/aisettings"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/dinners"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/household"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/jobs"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/recipes"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/secretbox"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/migrations"
)

const (
	fixtureIndex = "<!doctype html><title>fixture index</title>"
	fixtureAsset = "console.log('fixture asset')"
	notFoundJSON = `{"error":{"code":"not_found","message":"not found","retryable":false}}`
)

func newTestRouter(t *testing.T, version string) http.Handler {
	t.Helper()
	return newTestRouterWith(t, version, newTestSettings(t), newTestRunner(t))
}

func newTestRouterWith(t *testing.T, version string, settings *household.SettingsService, runner *jobs.Runner) http.Handler {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	assets := fstest.MapFS{
		"index.html":             {Data: []byte(fixtureIndex)},
		"assets/index-AbC123.js": {Data: []byte(fixtureAsset)},
	}
	r, err := NewRouter(logger, version, nil, stubAuth{}, stubSessions{}, settings, newTestSearch(t), newTestAI(t), newTestDinners(t), runner, newTestPhotos(t), assets)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// stubAuth mounts a fixed GET /auth/login in place of the real auth flow.
type stubAuth struct{}

func (stubAuth) Mount(r chi.Router) {
	r.Get("/auth/login", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, "stub login")
	})
}

func TestAuthRoutesMounted(t *testing.T) {
	t.Parallel()

	router := newTestRouter(t, "dev")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/auth/login", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "stub login" {
		t.Errorf("GET /auth/login = %d %q, want the mounted handler", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/auth/login", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /auth/login status = %d, want 405", rec.Code)
	}
}

// newTestStore returns a migrated temp database, closed when the test ends.
func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := store.Migrate(ctx, st.Writer, migrations.FS); err != nil {
		t.Fatal(err)
	}
	return st
}

// newTestSettings returns a settings service over a migrated temp database
// seeded with zone America/New_York.
func newTestSettings(t *testing.T) *household.SettingsService {
	t.Helper()
	st := newTestStore(t)
	if err := household.SeedSettings(context.Background(), st, "America/New_York"); err != nil {
		t.Fatal(err)
	}
	return household.NewSettingsService(st)
}

// newTestRunner returns an unstarted job runner over a migrated temp
// database.
func newTestRunner(t *testing.T) *jobs.Runner {
	t.Helper()
	runner, err := jobs.NewRunner(newTestStore(t), slog.New(slog.NewTextHandler(io.Discard, nil)), 1)
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

// newTestPhotos returns a photo cache over a migrated temp database and an
// empty temp data directory.
func newTestPhotos(t *testing.T) *recipes.PhotoCache {
	t.Helper()
	photos, err := recipes.NewPhotoCache(recipes.NewFetcher(), newTestStore(t), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return photos
}

// newTestSearch returns a recipe search settings service, with nothing
// configured, over a migrated temp database and a random secret box.
func newTestSearch(t *testing.T) *recipes.SearchSettingsService {
	t.Helper()
	search, err := recipes.NewSearchSettingsService(newTestStore(t), newTestBox(t), "")
	if err != nil {
		t.Fatal(err)
	}
	return search
}

// newTestDinners returns a dinners service over a migrated temp database.
func newTestDinners(t *testing.T) *dinners.Service {
	t.Helper()
	return dinners.NewService(newTestStore(t))
}

// newTestBox returns a secret box under a random key.
func newTestBox(t *testing.T) *secretbox.Box {
	t.Helper()
	key := make([]byte, secretbox.KeySize)
	rand.Read(key)
	box, err := secretbox.New(base64.StdEncoding.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}
	return box
}

// newTestAI returns an AI settings service over a migrated temp database
// with the seed AI settings and a random secret box.
func newTestAI(t *testing.T) *aisettings.Service {
	t.Helper()
	st := newTestStore(t)
	if err := household.SeedAISettings(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	ai, err := aisettings.New(st, newTestBox(t), "https://mealquest.example")
	if err != nil {
		t.Fatal(err)
	}
	return ai
}

func TestNewRouterRequiresAISettings(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := NewRouter(logger, "dev", nil, stubAuth{}, stubSessions{}, newTestSettings(t), newTestSearch(t), nil, newTestDinners(t), newTestRunner(t), newTestPhotos(t), fstest.MapFS{"index.html": {}}); err == nil {
		t.Fatal("NewRouter accepted a nil AI settings service")
	}
}

func TestNewRouterRequiresSearchSettings(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := NewRouter(logger, "dev", nil, stubAuth{}, stubSessions{}, newTestSettings(t), nil, newTestAI(t), newTestDinners(t), newTestRunner(t), newTestPhotos(t), fstest.MapFS{"index.html": {}}); err == nil {
		t.Fatal("NewRouter accepted a nil recipe search settings service")
	}
}

func TestNewRouterRequiresPhotoFiles(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := NewRouter(logger, "dev", nil, stubAuth{}, stubSessions{}, newTestSettings(t), newTestSearch(t), newTestAI(t), newTestDinners(t), newTestRunner(t), nil, fstest.MapFS{"index.html": {}}); err == nil {
		t.Fatal("NewRouter accepted nil photo files")
	}
}

func TestNewRouterRequiresSettings(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := NewRouter(logger, "dev", nil, stubAuth{}, stubSessions{}, nil, newTestSearch(t), newTestAI(t), newTestDinners(t), newTestRunner(t), newTestPhotos(t), fstest.MapFS{"index.html": {}}); err == nil {
		t.Fatal("NewRouter accepted a nil settings service")
	}
}

func TestNewRouterRequiresDinners(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := NewRouter(logger, "dev", nil, stubAuth{}, stubSessions{}, newTestSettings(t), newTestSearch(t), newTestAI(t), nil, newTestRunner(t), newTestPhotos(t), fstest.MapFS{"index.html": {}}); err == nil {
		t.Fatal("NewRouter accepted a nil dinners service")
	}
}

func TestNewRouterRequiresJobRunner(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := NewRouter(logger, "dev", nil, stubAuth{}, stubSessions{}, newTestSettings(t), newTestSearch(t), newTestAI(t), newTestDinners(t), nil, newTestPhotos(t), fstest.MapFS{"index.html": {}}); err == nil {
		t.Fatal("NewRouter accepted a nil job runner")
	}
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
		{http.MethodGet, "/api/v1/unknown"},
		{http.MethodPost, "/api/v1/unknown"},
		{http.MethodPost, "/api/unknown"},
		{http.MethodGet, "/auth/unknown"},
		{http.MethodGet, "/media/photo.jpg"},
		{http.MethodGet, "/health/deep"},
		{http.MethodGet, "/auth/../quests/42"},
		{http.MethodGet, "/api%2F..%2Fquests/42"},
	}
	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			t.Parallel()

			// Signed in with the CSRF token, so /api/v1 and /media reach
			// their routers.
			req := httptest.NewRequest(tt.method, tt.path, nil)
			req.AddCookie(validSessionCookie)
			req.Header.Set(CSRFHeader, testSession.CSRFToken)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

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
		{"/quests/42/edit", fixtureIndex, "no-cache"},
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
