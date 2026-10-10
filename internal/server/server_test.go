package server

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/go-chi/chi/v5"
	"golang.org/x/oauth2"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/aisettings"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/llm"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/skills"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/chat"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/dinners"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/gcal"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/home"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/household"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ideas"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/jobs"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/notify/notifytest"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/plan"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/recipes"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/recipes/recipestest"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/secretbox"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/shopping"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/suggest"
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
	return newTestRouterWithHome(t, version, settings, runner, newTestHome(t))
}

// newTestRouterWithHome is newTestRouterWith serving Home from homeSvc.
func newTestRouterWithHome(t *testing.T, version string, settings *household.SettingsService, runner *jobs.Runner, homeSvc *home.Service) http.Handler {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	assets := fstest.MapFS{
		"index.html":             {Data: []byte(fixtureIndex)},
		"assets/index-AbC123.js": {Data: []byte(fixtureAsset)},
	}
	r, err := NewRouter(logger, version, nil, stubAuth{}, stubSessions{}, settings, newTestSearch(t), newTestAI(t), newTestChat(t), newTestDinners(t), newTestSuggest(t), newTestPlan(t), newTestShopping(t), newTestGoogle(t), newTestPush(t), newTestIdeas(t), homeSvc, runner, newTestPhotos(t), assets)
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
// seeded with zone America/New_York and the seed notification preferences.
func newTestSettings(t *testing.T) *household.SettingsService {
	t.Helper()
	st := newTestStore(t)
	if err := household.SeedSettings(context.Background(), st, "America/New_York"); err != nil {
		t.Fatal(err)
	}
	if err := household.SeedNotificationPrefs(context.Background(), st); err != nil {
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

// newTestDinners returns a dinners service over a migrated temp database,
// with its own unstarted job runner and a grounder that finds nothing.
func newTestDinners(t *testing.T) *dinners.Service {
	t.Helper()
	st := newTestStore(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	runner, err := jobs.NewRunner(st, logger, 1)
	if err != nil {
		t.Fatal(err)
	}
	g := recipestest.NewGrounder(t, st, t.TempDir(), &recipestest.Search{}, &recipestest.Pages{}, &recipestest.Images{}, &llm.Fake{})
	svc, err := dinners.NewService(st, g, runner, logger)
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

// newTestSuggest returns a suggestions service over a migrated temp
// database, with its own unstarted job runner, a grounder that finds
// nothing and a suggester that always fails.
func newTestSuggest(t *testing.T) *suggest.Service {
	t.Helper()
	st := newTestStore(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	runner, err := jobs.NewRunner(st, logger, 1)
	if err != nil {
		t.Fatal(err)
	}
	g := recipestest.NewGrounder(t, st, t.TempDir(), &recipestest.Search{}, &recipestest.Pages{}, &recipestest.Images{}, &llm.Fake{})
	fail := func(context.Context, string, skills.SideQuestsRequest) (skills.SideQuests, error) {
		return skills.SideQuests{}, errors.New("no model in this test")
	}
	svc, err := suggest.NewService(st, g, fail, runner, notifytest.New(t, st), logger)
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

// newTestIdeas returns an ideas service with its own unstarted job runner
// and generators that always fail.
func newTestIdeas(t *testing.T) *ideas.Service {
	t.Helper()
	st := newTestStore(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	runner, err := jobs.NewRunner(st, logger, 1)
	if err != nil {
		t.Fatal(err)
	}
	g := recipestest.NewGrounder(t, st, t.TempDir(), &recipestest.Search{}, &recipestest.Pages{}, &recipestest.Images{}, &llm.Fake{})
	fail := func(context.Context, string, skills.IdeasRequest) (skills.Ideas, error) {
		return skills.Ideas{}, errors.New("no model in this test")
	}
	failQuick := func(context.Context, string, skills.QuickIdeasRequest) (skills.QuickIdeaList, error) {
		return skills.QuickIdeaList{}, errors.New("no model in this test")
	}
	svc, err := ideas.NewService(st, g, fail, failQuick, runner, logger)
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

// newTestPlan returns a plan service over a migrated temp database, with
// its own unstarted job runner and a drafter that always fails.
func newTestPlan(t *testing.T) *plan.Service {
	t.Helper()
	st := newTestStore(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	runner, err := jobs.NewRunner(st, logger, 1)
	if err != nil {
		t.Fatal(err)
	}
	fail := func(context.Context, string, skills.WeekRequest) (skills.Week, error) {
		return skills.Week{}, errors.New("no model in this test")
	}
	svc, err := plan.NewService(st, fail, runner, newTestGoogle(t), notifytest.New(t, st), &url.URL{Scheme: "https", Host: "mealquest.example"}, logger)
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

// newTestShopping returns a shopping service over a migrated temp
// database, whose Supply runs stay untitled and whose posts to Google Chat
// all fail.
func newTestShopping(t *testing.T) *shopping.Service {
	t.Helper()
	st := newTestStore(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	runner, err := jobs.NewRunner(st, logger, 1)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := shopping.NewService(st, untitled, runner, notifytest.New(t, st), newTestChat(t), logger)
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

// newTestHome returns a home service whose services all share one
// migrated temp database with zone America/New_York, with an unstarted job
// runner, no model, and Google and Google Chat endpoints that refuse every
// connection. One database keeps the routers that never ask for Home cheap.
func newTestHome(t *testing.T) *home.Service {
	t.Helper()
	return newTestHomeOn(t, newTestStore(t))
}

// newTestHomeOn returns a home service over st, a migrated database, which
// it seeds with zone America/New_York and the seed notification preferences.
func newTestHomeOn(t *testing.T, st *store.Store) *home.Service {
	t.Helper()
	if err := household.SeedSettings(context.Background(), st, "America/New_York"); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	runner, err := jobs.NewRunner(st, logger, 1)
	if err != nil {
		t.Fatal(err)
	}
	notifier := notifytest.New(t, st)
	draft := func(context.Context, string, skills.WeekRequest) (skills.Week, error) {
		return skills.Week{}, errors.New("no model in this test")
	}
	pub := &url.URL{Scheme: "https", Host: "mealquest.example"}
	googleSvc, err := gcal.NewService(st, newTestBox(t), gcal.Config{
		ClientID:     "client-id",
		ClientSecret: "client-secret",
		PublicURL:    pub,
		SecretKey:    []byte("server-secret-key"),
		Endpoint:     oauth2.Endpoint{AuthURL: "http://127.0.0.1:1/auth", TokenURL: "http://127.0.0.1:1/token"},
		RevokeURL:    "http://127.0.0.1:1/revoke",
		CalendarAPI:  "http://127.0.0.1:1/calendar/v3",
	})
	if err != nil {
		t.Fatal(err)
	}
	planSvc, err := plan.NewService(st, draft, runner, googleSvc, notifier, pub, logger)
	if err != nil {
		t.Fatal(err)
	}
	g := recipestest.NewGrounder(t, st, t.TempDir(), &recipestest.Search{}, &recipestest.Pages{}, &recipestest.Images{}, &llm.Fake{})
	sideQuests := func(context.Context, string, skills.SideQuestsRequest) (skills.SideQuests, error) {
		return skills.SideQuests{}, errors.New("no model in this test")
	}
	suggestSvc, err := suggest.NewService(st, g, sideQuests, runner, notifier, logger)
	if err != nil {
		t.Fatal(err)
	}
	chatSvc, err := chat.NewService(st, newTestBox(t), pub, refuseTransport{})
	if err != nil {
		t.Fatal(err)
	}
	shop, err := shopping.NewService(st, untitled, runner, notifier, chatSvc, logger)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := home.NewService(household.NewSettingsService(st), planSvc, suggestSvc, shop, notifier)
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

// untitled is a shopping.Titler with no model: every Supply run it is
// asked to title stays untitled.
func untitled(context.Context, string, skills.ArchiveTitleRequest) (skills.ArchiveTitle, error) {
	return skills.ArchiveTitle{}, errors.New("no model in this test")
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

// newTestGoogle returns a Google Calendar service over a migrated temp
// database whose Google endpoints refuse every connection.
func newTestGoogle(t *testing.T) *gcal.Service {
	t.Helper()
	pub, err := url.Parse("https://mealquest.example")
	if err != nil {
		t.Fatal(err)
	}
	svc, err := gcal.NewService(newTestStore(t), newTestBox(t), gcal.Config{
		ClientID:     "client-id",
		ClientSecret: "client-secret",
		PublicURL:    pub,
		SecretKey:    []byte("server-secret-key"),
		Endpoint:     oauth2.Endpoint{AuthURL: "http://127.0.0.1:1/auth", TokenURL: "http://127.0.0.1:1/token"},
		RevokeURL:    "http://127.0.0.1:1/revoke",
		CalendarAPI:  "http://127.0.0.1:1/calendar/v3",
	})
	if err != nil {
		t.Fatal(err)
	}
	return svc
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
	if _, err := NewRouter(logger, "dev", nil, stubAuth{}, stubSessions{}, newTestSettings(t), newTestSearch(t), nil, newTestChat(t), newTestDinners(t), newTestSuggest(t), newTestPlan(t), newTestShopping(t), newTestGoogle(t), newTestPush(t), newTestIdeas(t), newTestHome(t), newTestRunner(t), newTestPhotos(t), fstest.MapFS{"index.html": {}}); err == nil {
		t.Fatal("NewRouter accepted a nil AI settings service")
	}
}

func TestNewRouterRequiresSearchSettings(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := NewRouter(logger, "dev", nil, stubAuth{}, stubSessions{}, newTestSettings(t), nil, newTestAI(t), newTestChat(t), newTestDinners(t), newTestSuggest(t), newTestPlan(t), newTestShopping(t), newTestGoogle(t), newTestPush(t), newTestIdeas(t), newTestHome(t), newTestRunner(t), newTestPhotos(t), fstest.MapFS{"index.html": {}}); err == nil {
		t.Fatal("NewRouter accepted a nil recipe search settings service")
	}
}

func TestNewRouterRequiresPhotoFiles(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := NewRouter(logger, "dev", nil, stubAuth{}, stubSessions{}, newTestSettings(t), newTestSearch(t), newTestAI(t), newTestChat(t), newTestDinners(t), newTestSuggest(t), newTestPlan(t), newTestShopping(t), newTestGoogle(t), newTestPush(t), newTestIdeas(t), newTestHome(t), newTestRunner(t), nil, fstest.MapFS{"index.html": {}}); err == nil {
		t.Fatal("NewRouter accepted nil photo files")
	}
}

func TestNewRouterRequiresSettings(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := NewRouter(logger, "dev", nil, stubAuth{}, stubSessions{}, nil, newTestSearch(t), newTestAI(t), newTestChat(t), newTestDinners(t), newTestSuggest(t), newTestPlan(t), newTestShopping(t), newTestGoogle(t), newTestPush(t), newTestIdeas(t), newTestHome(t), newTestRunner(t), newTestPhotos(t), fstest.MapFS{"index.html": {}}); err == nil {
		t.Fatal("NewRouter accepted a nil settings service")
	}
}

func TestNewRouterRequiresDinners(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := NewRouter(logger, "dev", nil, stubAuth{}, stubSessions{}, newTestSettings(t), newTestSearch(t), newTestAI(t), newTestChat(t), nil, newTestSuggest(t), newTestPlan(t), newTestShopping(t), newTestGoogle(t), newTestPush(t), newTestIdeas(t), newTestHome(t), newTestRunner(t), newTestPhotos(t), fstest.MapFS{"index.html": {}}); err == nil {
		t.Fatal("NewRouter accepted a nil dinners service")
	}
}

func TestNewRouterRequiresSuggestions(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := NewRouter(logger, "dev", nil, stubAuth{}, stubSessions{}, newTestSettings(t), newTestSearch(t), newTestAI(t), newTestChat(t), newTestDinners(t), nil, newTestPlan(t), newTestShopping(t), newTestGoogle(t), newTestPush(t), newTestIdeas(t), newTestHome(t), newTestRunner(t), newTestPhotos(t), fstest.MapFS{"index.html": {}}); err == nil {
		t.Fatal("NewRouter accepted a nil suggestions service")
	}
}

func TestNewRouterRequiresPlan(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := NewRouter(logger, "dev", nil, stubAuth{}, stubSessions{}, newTestSettings(t), newTestSearch(t), newTestAI(t), newTestChat(t), newTestDinners(t), newTestSuggest(t), nil, newTestShopping(t), newTestGoogle(t), newTestPush(t), newTestIdeas(t), newTestHome(t), newTestRunner(t), newTestPhotos(t), fstest.MapFS{"index.html": {}}); err == nil {
		t.Fatal("NewRouter accepted a nil plan service")
	}
}

func TestNewRouterRequiresShopping(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := NewRouter(logger, "dev", nil, stubAuth{}, stubSessions{}, newTestSettings(t), newTestSearch(t), newTestAI(t), newTestChat(t), newTestDinners(t), newTestSuggest(t), newTestPlan(t), nil, newTestGoogle(t), newTestPush(t), newTestIdeas(t), newTestHome(t), newTestRunner(t), newTestPhotos(t), fstest.MapFS{"index.html": {}}); err == nil {
		t.Fatal("NewRouter accepted a nil shopping service")
	}
}

func TestNewRouterRequiresIdeas(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := NewRouter(logger, "dev", nil, stubAuth{}, stubSessions{}, newTestSettings(t), newTestSearch(t), newTestAI(t), newTestChat(t), newTestDinners(t), newTestSuggest(t), newTestPlan(t), newTestShopping(t), newTestGoogle(t), newTestPush(t), nil, newTestHome(t), newTestRunner(t), newTestPhotos(t), fstest.MapFS{"index.html": {}}); err == nil {
		t.Fatal("NewRouter accepted a nil ideas service")
	}
}

func TestNewRouterRequiresHome(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := NewRouter(logger, "dev", nil, stubAuth{}, stubSessions{}, newTestSettings(t), newTestSearch(t), newTestAI(t), newTestChat(t), newTestDinners(t), newTestSuggest(t), newTestPlan(t), newTestShopping(t), newTestGoogle(t), newTestPush(t), newTestIdeas(t), nil, newTestRunner(t), newTestPhotos(t), fstest.MapFS{"index.html": {}}); err == nil {
		t.Fatal("NewRouter accepted a nil home service")
	}
}

func TestNewRouterRequiresGoogle(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := NewRouter(logger, "dev", nil, stubAuth{}, stubSessions{}, newTestSettings(t), newTestSearch(t), newTestAI(t), newTestChat(t), newTestDinners(t), newTestSuggest(t), newTestPlan(t), newTestShopping(t), nil, newTestPush(t), newTestIdeas(t), newTestHome(t), newTestRunner(t), newTestPhotos(t), fstest.MapFS{"index.html": {}}); err == nil {
		t.Fatal("NewRouter accepted a nil Google Calendar service")
	}
}

func TestNewRouterRequiresJobRunner(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := NewRouter(logger, "dev", nil, stubAuth{}, stubSessions{}, newTestSettings(t), newTestSearch(t), newTestAI(t), newTestChat(t), newTestDinners(t), newTestSuggest(t), newTestPlan(t), newTestShopping(t), newTestGoogle(t), newTestPush(t), newTestIdeas(t), newTestHome(t), nil, newTestPhotos(t), fstest.MapFS{"index.html": {}}); err == nil {
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
