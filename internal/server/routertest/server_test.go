package routertest

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

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/server"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/server/srvtest"
)

func TestAuthRoutesMounted(t *testing.T) {
	t.Parallel()

	router := srvtest.NewTestRouter(t, "dev")
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

func TestNewRouterRequiresAISettings(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := server.NewRouter(logger, "dev", nil, srvtest.StubAuth{}, srvtest.StubSessions{}, srvtest.NewTestSettings(t), srvtest.NewTestSearch(t), nil, srvtest.NewTestChat(t), srvtest.NewTestDinners(t), srvtest.NewTestSuggest(t), srvtest.NewTestPlan(t), srvtest.NewTestShopping(t), srvtest.NewTestGoogle(t), srvtest.NewTestPush(t), srvtest.NewTestIdeas(t), srvtest.NewTestHome(t), srvtest.NewTestRunner(t), srvtest.NewTestPhotos(t), fstest.MapFS{"index.html": {}}); err == nil {
		t.Fatal("NewRouter accepted a nil AI settings service")
	}
}

func TestNewRouterRequiresSearchSettings(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := server.NewRouter(logger, "dev", nil, srvtest.StubAuth{}, srvtest.StubSessions{}, srvtest.NewTestSettings(t), nil, srvtest.NewTestAI(t), srvtest.NewTestChat(t), srvtest.NewTestDinners(t), srvtest.NewTestSuggest(t), srvtest.NewTestPlan(t), srvtest.NewTestShopping(t), srvtest.NewTestGoogle(t), srvtest.NewTestPush(t), srvtest.NewTestIdeas(t), srvtest.NewTestHome(t), srvtest.NewTestRunner(t), srvtest.NewTestPhotos(t), fstest.MapFS{"index.html": {}}); err == nil {
		t.Fatal("NewRouter accepted a nil recipe search settings service")
	}
}

func TestNewRouterRequiresPhotoFiles(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := server.NewRouter(logger, "dev", nil, srvtest.StubAuth{}, srvtest.StubSessions{}, srvtest.NewTestSettings(t), srvtest.NewTestSearch(t), srvtest.NewTestAI(t), srvtest.NewTestChat(t), srvtest.NewTestDinners(t), srvtest.NewTestSuggest(t), srvtest.NewTestPlan(t), srvtest.NewTestShopping(t), srvtest.NewTestGoogle(t), srvtest.NewTestPush(t), srvtest.NewTestIdeas(t), srvtest.NewTestHome(t), srvtest.NewTestRunner(t), nil, fstest.MapFS{"index.html": {}}); err == nil {
		t.Fatal("NewRouter accepted nil photo files")
	}
}

func TestNewRouterRequiresSettings(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := server.NewRouter(logger, "dev", nil, srvtest.StubAuth{}, srvtest.StubSessions{}, nil, srvtest.NewTestSearch(t), srvtest.NewTestAI(t), srvtest.NewTestChat(t), srvtest.NewTestDinners(t), srvtest.NewTestSuggest(t), srvtest.NewTestPlan(t), srvtest.NewTestShopping(t), srvtest.NewTestGoogle(t), srvtest.NewTestPush(t), srvtest.NewTestIdeas(t), srvtest.NewTestHome(t), srvtest.NewTestRunner(t), srvtest.NewTestPhotos(t), fstest.MapFS{"index.html": {}}); err == nil {
		t.Fatal("NewRouter accepted a nil settings service")
	}
}

func TestNewRouterRequiresDinners(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := server.NewRouter(logger, "dev", nil, srvtest.StubAuth{}, srvtest.StubSessions{}, srvtest.NewTestSettings(t), srvtest.NewTestSearch(t), srvtest.NewTestAI(t), srvtest.NewTestChat(t), nil, srvtest.NewTestSuggest(t), srvtest.NewTestPlan(t), srvtest.NewTestShopping(t), srvtest.NewTestGoogle(t), srvtest.NewTestPush(t), srvtest.NewTestIdeas(t), srvtest.NewTestHome(t), srvtest.NewTestRunner(t), srvtest.NewTestPhotos(t), fstest.MapFS{"index.html": {}}); err == nil {
		t.Fatal("NewRouter accepted a nil dinners service")
	}
}

func TestNewRouterRequiresSuggestions(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := server.NewRouter(logger, "dev", nil, srvtest.StubAuth{}, srvtest.StubSessions{}, srvtest.NewTestSettings(t), srvtest.NewTestSearch(t), srvtest.NewTestAI(t), srvtest.NewTestChat(t), srvtest.NewTestDinners(t), nil, srvtest.NewTestPlan(t), srvtest.NewTestShopping(t), srvtest.NewTestGoogle(t), srvtest.NewTestPush(t), srvtest.NewTestIdeas(t), srvtest.NewTestHome(t), srvtest.NewTestRunner(t), srvtest.NewTestPhotos(t), fstest.MapFS{"index.html": {}}); err == nil {
		t.Fatal("NewRouter accepted a nil suggestions service")
	}
}

func TestNewRouterRequiresPlan(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := server.NewRouter(logger, "dev", nil, srvtest.StubAuth{}, srvtest.StubSessions{}, srvtest.NewTestSettings(t), srvtest.NewTestSearch(t), srvtest.NewTestAI(t), srvtest.NewTestChat(t), srvtest.NewTestDinners(t), srvtest.NewTestSuggest(t), nil, srvtest.NewTestShopping(t), srvtest.NewTestGoogle(t), srvtest.NewTestPush(t), srvtest.NewTestIdeas(t), srvtest.NewTestHome(t), srvtest.NewTestRunner(t), srvtest.NewTestPhotos(t), fstest.MapFS{"index.html": {}}); err == nil {
		t.Fatal("NewRouter accepted a nil plan service")
	}
}

func TestNewRouterRequiresShopping(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := server.NewRouter(logger, "dev", nil, srvtest.StubAuth{}, srvtest.StubSessions{}, srvtest.NewTestSettings(t), srvtest.NewTestSearch(t), srvtest.NewTestAI(t), srvtest.NewTestChat(t), srvtest.NewTestDinners(t), srvtest.NewTestSuggest(t), srvtest.NewTestPlan(t), nil, srvtest.NewTestGoogle(t), srvtest.NewTestPush(t), srvtest.NewTestIdeas(t), srvtest.NewTestHome(t), srvtest.NewTestRunner(t), srvtest.NewTestPhotos(t), fstest.MapFS{"index.html": {}}); err == nil {
		t.Fatal("NewRouter accepted a nil shopping service")
	}
}

func TestNewRouterRequiresIdeas(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := server.NewRouter(logger, "dev", nil, srvtest.StubAuth{}, srvtest.StubSessions{}, srvtest.NewTestSettings(t), srvtest.NewTestSearch(t), srvtest.NewTestAI(t), srvtest.NewTestChat(t), srvtest.NewTestDinners(t), srvtest.NewTestSuggest(t), srvtest.NewTestPlan(t), srvtest.NewTestShopping(t), srvtest.NewTestGoogle(t), srvtest.NewTestPush(t), nil, srvtest.NewTestHome(t), srvtest.NewTestRunner(t), srvtest.NewTestPhotos(t), fstest.MapFS{"index.html": {}}); err == nil {
		t.Fatal("NewRouter accepted a nil ideas service")
	}
}

func TestNewRouterRequiresHome(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := server.NewRouter(logger, "dev", nil, srvtest.StubAuth{}, srvtest.StubSessions{}, srvtest.NewTestSettings(t), srvtest.NewTestSearch(t), srvtest.NewTestAI(t), srvtest.NewTestChat(t), srvtest.NewTestDinners(t), srvtest.NewTestSuggest(t), srvtest.NewTestPlan(t), srvtest.NewTestShopping(t), srvtest.NewTestGoogle(t), srvtest.NewTestPush(t), srvtest.NewTestIdeas(t), nil, srvtest.NewTestRunner(t), srvtest.NewTestPhotos(t), fstest.MapFS{"index.html": {}}); err == nil {
		t.Fatal("NewRouter accepted a nil home service")
	}
}

func TestNewRouterRequiresGoogle(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := server.NewRouter(logger, "dev", nil, srvtest.StubAuth{}, srvtest.StubSessions{}, srvtest.NewTestSettings(t), srvtest.NewTestSearch(t), srvtest.NewTestAI(t), srvtest.NewTestChat(t), srvtest.NewTestDinners(t), srvtest.NewTestSuggest(t), srvtest.NewTestPlan(t), srvtest.NewTestShopping(t), nil, srvtest.NewTestPush(t), srvtest.NewTestIdeas(t), srvtest.NewTestHome(t), srvtest.NewTestRunner(t), srvtest.NewTestPhotos(t), fstest.MapFS{"index.html": {}}); err == nil {
		t.Fatal("NewRouter accepted a nil Google Calendar service")
	}
}

func TestNewRouterRequiresJobRunner(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := server.NewRouter(logger, "dev", nil, srvtest.StubAuth{}, srvtest.StubSessions{}, srvtest.NewTestSettings(t), srvtest.NewTestSearch(t), srvtest.NewTestAI(t), srvtest.NewTestChat(t), srvtest.NewTestDinners(t), srvtest.NewTestSuggest(t), srvtest.NewTestPlan(t), srvtest.NewTestShopping(t), srvtest.NewTestGoogle(t), srvtest.NewTestPush(t), srvtest.NewTestIdeas(t), srvtest.NewTestHome(t), nil, srvtest.NewTestPhotos(t), fstest.MapFS{"index.html": {}}); err == nil {
		t.Fatal("NewRouter accepted a nil job runner")
	}
}

func TestHealth(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(srvtest.NewTestRouter(t, "1.2.3"))
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
	srvtest.NewTestRouter(t, "dev").ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/health", nil))

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

	router := srvtest.NewTestRouter(t, "dev")
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
			req.AddCookie(srvtest.ValidSessionCookie)
			req.Header.Set(server.CSRFHeader, srvtest.TestSession.CSRFToken)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusNotFound {
				t.Errorf("status = %d, want 404", rec.Code)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", ct)
			}
			if got := rec.Body.String(); got != srvtest.NotFoundJSON {
				t.Errorf("body = %s, want %s", got, srvtest.NotFoundJSON)
			}
		})
	}
}

func TestFrontend(t *testing.T) {
	t.Parallel()

	router := srvtest.NewTestRouter(t, "dev")
	tests := []struct {
		path      string
		wantBody  string
		wantCache string
	}{
		{"/", srvtest.FixtureIndex, "no-cache"},
		{"/assets/index-AbC123.js", srvtest.FixtureAsset, "public, max-age=31536000, immutable"},
		{"/quests/42", srvtest.FixtureIndex, "no-cache"},
		{"/quests/42/edit", srvtest.FixtureIndex, "no-cache"},
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
	srvtest.NewTestRouter(t, "dev").ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	if body := rec.Body.String(); rec.Code != http.StatusOK || strings.Contains(body, srvtest.FixtureIndex) {
		t.Errorf("GET /health = %d %q, want 200 health JSON", rec.Code, body)
	}
}

func TestFrontendRejectsOtherMethods(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	srvtest.NewTestRouter(t, "dev").ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/quests/42", nil))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
	if strings.Contains(rec.Body.String(), srvtest.FixtureIndex) {
		t.Errorf("body = %q, want no index.html", rec.Body.String())
	}
}
