// Package srvtest is the shared fixture for the MealQuest HTTP tests.
// The feature packages under internal/server each run as their own test
// binary, so go test executes them concurrently; every package builds
// routers through this fixture instead of copying it.
package srvtest

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/go-chi/chi/v5"
	"golang.org/x/oauth2"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/aisettings"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/llm"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/skills"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/auth"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/chat"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/dinners"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/gcal"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/home"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/household"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ideas"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/jobs"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/notify/notifytest"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/plan"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/push"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/recipes"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/recipes/recipestest"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/secretbox"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/server"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/shopping"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/suggest"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/vapid"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/migrations"
)

const (
	FixtureIndex = "<!doctype html><title>fixture index</title>"
	FixtureAsset = "console.log('fixture asset')"
	NotFoundJSON = `{"error":{"code":"not_found","message":"not found","retryable":false}}`
)

func NewTestRouter(t *testing.T, version string) http.Handler {
	t.Helper()
	return NewTestRouterWith(t, version, NewTestSettings(t), NewTestRunner(t))
}

func NewTestRouterWith(t *testing.T, version string, settings *household.SettingsService, runner *jobs.Runner) http.Handler {
	t.Helper()
	return NewTestRouterWithHome(t, version, settings, runner, NewTestHome(t))
}

// NewTestRouterWithHome is NewTestRouterWith serving Home from homeSvc.
func NewTestRouterWithHome(t *testing.T, version string, settings *household.SettingsService, runner *jobs.Runner, homeSvc *home.Service) http.Handler {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	assets := fstest.MapFS{
		"index.html":             {Data: []byte(FixtureIndex)},
		"assets/index-AbC123.js": {Data: []byte(FixtureAsset)},
	}
	r, err := server.NewRouter(logger, version, nil, StubAuth{}, StubSessions{}, settings, NewTestSearch(t), NewTestAI(t), NewTestChat(t), NewTestDinners(t), NewTestSuggest(t), NewTestPlan(t), NewTestShopping(t), NewTestGoogle(t), NewTestPush(t), NewTestIdeas(t), homeSvc, runner, NewTestPhotos(t), assets)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// StubAuth mounts a fixed GET /auth/login in place of the real auth flow.
type StubAuth struct{}

func (StubAuth) Mount(r chi.Router) {
	r.Get("/auth/login", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, "stub login")
	})
}

// NewTestStore returns a migrated temp database, closed when the test ends.
func NewTestStore(t *testing.T) *store.Store {
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

// NewTestSettings returns a settings service over a migrated temp database
// seeded with zone America/New_York and the seed notification preferences.
func NewTestSettings(t *testing.T) *household.SettingsService {
	t.Helper()
	st := NewTestStore(t)
	if err := household.SeedSettings(context.Background(), st, "America/New_York"); err != nil {
		t.Fatal(err)
	}
	if err := household.SeedNotificationPrefs(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	return household.NewSettingsService(st)
}

// NewTestRunner returns an unstarted job runner over a migrated temp
// database.
func NewTestRunner(t *testing.T) *jobs.Runner {
	t.Helper()
	runner, err := jobs.NewRunner(NewTestStore(t), slog.New(slog.NewTextHandler(io.Discard, nil)), 1)
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

// NewTestPhotos returns a photo cache over a migrated temp database and an
// empty temp data directory.
func NewTestPhotos(t *testing.T) *recipes.PhotoCache {
	t.Helper()
	photos, err := recipes.NewPhotoCache(recipes.NewFetcher(), NewTestStore(t), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return photos
}

// NewTestSearch returns a recipe search settings service, with nothing
// configured, over a migrated temp database and a random secret box.
func NewTestSearch(t *testing.T) *recipes.SearchSettingsService {
	t.Helper()
	search, err := recipes.NewSearchSettingsService(NewTestStore(t), NewTestBox(t), "")
	if err != nil {
		t.Fatal(err)
	}
	return search
}

// NewTestDinners returns a dinners service over a migrated temp database,
// with its own unstarted job runner and a grounder that finds nothing.
func NewTestDinners(t *testing.T) *dinners.Service {
	t.Helper()
	return NewTestDinnersOn(t, NewTestStore(t))
}

// NewTestDinnersOn is NewTestDinners over st.
func NewTestDinnersOn(t *testing.T, st *store.Store) *dinners.Service {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	runner, err := jobs.NewRunner(st, logger, 1)
	if err != nil {
		t.Fatal(err)
	}
	g := recipestest.NewGrounder(t, st, t.TempDir(), &recipestest.Search{}, &recipestest.Pages{}, &recipestest.Images{}, &llm.Fake{})
	svc, err := dinners.NewService(st, g, runner, func() {}, logger)
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

// NewTestSuggest returns a suggestions service over a migrated temp
// database, with its own unstarted job runner, a grounder that finds
// nothing and a suggester that always fails.
func NewTestSuggest(t *testing.T) *suggest.Service {
	t.Helper()
	st := NewTestStore(t)
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

// NewTestIdeas returns an ideas service with its own unstarted job runner
// and generators that always fail.
func NewTestIdeas(t *testing.T) *ideas.Service {
	t.Helper()
	st := NewTestStore(t)
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

// NewTestPlan returns a plan service over a migrated temp database, with
// its own unstarted job runner and a drafter that always fails.
func NewTestPlan(t *testing.T) *plan.Service {
	t.Helper()
	st := NewTestStore(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	runner, err := jobs.NewRunner(st, logger, 1)
	if err != nil {
		t.Fatal(err)
	}
	fail := func(context.Context, string, skills.WeekRequest) (skills.Week, error) {
		return skills.Week{}, errors.New("no model in this test")
	}
	svc, err := plan.NewService(st, fail, runner, NewTestGoogle(t), notifytest.New(t, st), func() {}, &url.URL{Scheme: "https", Host: "mealquest.example"}, time.Now, logger)
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

// NewTestShopping returns a shopping service over a migrated temp
// database, whose Supply runs stay untitled and whose posts to Google Chat
// all fail.
func NewTestShopping(t *testing.T) *shopping.Service {
	t.Helper()
	st := NewTestStore(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	runner, err := jobs.NewRunner(st, logger, 1)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := shopping.NewService(st, Untitled, runner, notifytest.New(t, st), NewTestChat(t), logger)
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

// NewTestHome returns a home service whose services all share one
// migrated temp database with zone America/New_York, with an unstarted job
// runner, no model, and Google and Google Chat endpoints that refuse every
// connection. One database keeps the routers that never ask for Home cheap.
func NewTestHome(t *testing.T) *home.Service {
	t.Helper()
	return NewTestHomeOn(t, NewTestStore(t))
}

// NewTestHomeOn returns a home service over st, a migrated database, which
// it seeds with zone America/New_York and the seed notification preferences.
func NewTestHomeOn(t *testing.T, st *store.Store) *home.Service {
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
	googleSvc, err := gcal.NewService(st, NewTestBox(t), gcal.Config{
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
	planSvc, err := plan.NewService(st, draft, runner, googleSvc, notifier, func() {}, pub, time.Now, logger)
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
	chatSvc, err := chat.NewService(st, NewTestBox(t), pub, RefuseTransport{})
	if err != nil {
		t.Fatal(err)
	}
	shop, err := shopping.NewService(st, Untitled, runner, notifier, chatSvc, logger)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := home.NewService(household.NewSettingsService(st), planSvc, suggestSvc, shop, notifier)
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

// Untitled is a shopping.Titler with no model: every Supply run it is
// asked to title stays untitled.
func Untitled(context.Context, string, skills.ArchiveTitleRequest) (skills.ArchiveTitle, error) {
	return skills.ArchiveTitle{}, errors.New("no model in this test")
}

// NewTestBox returns a secret box under a random key.
func NewTestBox(t *testing.T) *secretbox.Box {
	t.Helper()
	key := make([]byte, secretbox.KeySize)
	rand.Read(key)
	box, err := secretbox.New(base64.StdEncoding.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}
	return box
}

// NewTestGoogle returns a Google Calendar service over a migrated temp
// database whose Google endpoints refuse every connection.
func NewTestGoogle(t *testing.T) *gcal.Service {
	t.Helper()
	pub, err := url.Parse("https://mealquest.example")
	if err != nil {
		t.Fatal(err)
	}
	svc, err := gcal.NewService(NewTestStore(t), NewTestBox(t), gcal.Config{
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

// NewTestAI returns an AI settings service over a migrated temp database
// with the seed AI settings and a random secret box.
func NewTestAI(t *testing.T) *aisettings.Service {
	t.Helper()
	st := NewTestStore(t)
	if err := household.SeedAISettings(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	ai, err := aisettings.New(st, NewTestBox(t), "https://mealquest.example")
	if err != nil {
		t.Fatal(err)
	}
	return ai
}

var (
	ValidSessionCookie  = &http.Cookie{Name: auth.SessionCookieName, Value: "valid"}
	BrokenSessionCookie = &http.Cookie{Name: auth.SessionCookieName, Value: "broken"}
	TestSession         = auth.Session{
		Subject:   "user-123",
		Email:     "cook@example.com",
		Name:      "Home Cook",
		Groups:    []string{"mealquest-admin"},
		CSRFToken: "csrf-token-123",
	}
)

// StubSessions knows one valid session cookie; the broken cookie fails the
// lookup as a database error would.
type StubSessions struct{}

func (StubSessions) SessionFor(r *http.Request) (auth.Session, error) {
	c, err := r.Cookie(auth.SessionCookieName)
	switch {
	case err != nil:
		return auth.Session{}, auth.ErrNoSession
	case c.Value == ValidSessionCookie.Value:
		return TestSession, nil
	case c.Value == BrokenSessionCookie.Value:
		return auth.Session{}, errors.New("database down")
	default:
		return auth.Session{}, auth.ErrNoSession
	}
}

// SignedIn returns a request carrying StubSessions' valid session cookie and
// its CSRF token.
func SignedIn(method, target string, body io.Reader) *http.Request {
	req := httptest.NewRequest(method, target, body)
	req.AddCookie(ValidSessionCookie)
	req.Header.Set(server.CSRFHeader, TestSession.CSRFToken)
	return req
}

func Serve(t *testing.T, h http.Handler, req *http.Request) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	return rec, body
}

const DinnersPath = "/api/v1/dinners"

// CallDinners sends a signed-in request with body (none when empty).
func CallDinners(t *testing.T, h http.Handler, method, target, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	return Serve(t, h, SignedIn(method, target, r))
}

// CreateDinner adds a Regular and returns its id.
func CreateDinner(t *testing.T, h http.Handler, body string) string {
	t.Helper()
	rec, got := CallDinners(t, h, http.MethodPost, DinnersPath, body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST %s = %d: %s", body, rec.Code, rec.Body)
	}
	return got["id"].(string)
}

// ListNames returns the names GET /dinners?status=status lists, in order.
func ListNames(t *testing.T, h http.Handler, status string) []string {
	t.Helper()
	rec, body := CallDinners(t, h, http.MethodGet, DinnersPath+"?status="+status, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status=%s = %d: %s", status, rec.Code, rec.Body)
	}
	list, ok := body["dinners"].([]any)
	if !ok {
		t.Fatalf("GET status=%s has no dinners list: %v", status, body)
	}
	names := make([]string, len(list))
	for i, d := range list {
		names[i] = d.(map[string]any)["name"].(string)
	}
	return names
}

func Ptr[T any](v T) *T { return &v }

// siteNameMigration is migration 0022, which clears the host-shaped site
// names stored before extraction stopped storing hosts.
const siteNameMigration = "0022_site_name_from_page.sql"

// ApplySiteNameMigration runs siteNameMigration again over rows a test
// stored as the code before it did, as upgrading a database holding them
// does.
func ApplySiteNameMigration(t *testing.T, st *store.Store) {
	t.Helper()
	body, err := fs.ReadFile(migrations.FS, siteNameMigration)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Writer.ExecContext(t.Context(), string(body)); err != nil {
		t.Fatalf("%s: %v", siteNameMigration, err)
	}
}

// jobWait is how long WaitJob polls before a job that has not reached
// succeeded or failed fails the test. Grounding (search, fetch, photo and
// the model) can outlast a few seconds when the CI runner is loaded; the
// bound stays finite so a stuck job still fails.
const jobWait = 90 * time.Second

// WaitJob polls GET /jobs/{id} until the job ends and returns its body.
func WaitJob(t *testing.T, h http.Handler, id string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(jobWait)
	for {
		rec, body := CallDinners(t, h, http.MethodGet, "/api/v1/jobs/"+id, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET job = %d: %s", rec.Code, rec.Body)
		}
		if s := body["status"]; s == "succeeded" || s == "failed" {
			return body
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s never finished; last %v", id, body)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// RefuseTransport fails every request, as a Chat endpoint that is down.
type RefuseTransport struct{}

func (RefuseTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("connection refused")
}

// NewTestChat returns a Google Chat service over a migrated temp database
// whose requests all fail.
func NewTestChat(t *testing.T) *chat.Service {
	t.Helper()
	return NewTestChatVia(t, RefuseTransport{})
}

func NewTestChatVia(t *testing.T, transport http.RoundTripper) *chat.Service {
	t.Helper()
	pub, err := url.Parse("https://mealquest.example")
	if err != nil {
		t.Fatal(err)
	}
	svc, err := chat.NewService(NewTestStore(t), NewTestBox(t), pub, transport)
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

// NewTestPush returns a push service over a migrated temp database with a
// new VAPID key pair.
func NewTestPush(t *testing.T) *push.Service {
	t.Helper()
	svc, _, _ := NewTestPushWith(t)
	return svc
}

// NewTestPushWith is NewTestPush also returning its store and key pair.
func NewTestPushWith(t *testing.T) (*push.Service, *store.Store, vapid.KeyPair) {
	t.Helper()
	pair, err := vapid.Generate()
	if err != nil {
		t.Fatal(err)
	}
	st := NewTestStore(t)
	svc, err := push.NewService(st, pair.PublicKey, notifytest.New(t, st))
	if err != nil {
		t.Fatal(err)
	}
	return svc, st, pair
}

func ErrorCode(body map[string]any) any {
	e, _ := body["error"].(map[string]any)
	return e["code"]
}
