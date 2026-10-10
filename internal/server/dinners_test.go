package server

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/llm"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/dinners"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/household"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/jobs"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/recipes"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/recipes/recipestest"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

const dinnersPath = "/api/v1/dinners"

// newDinnersRouter returns a router whose dinners service runs over the
// returned store. Its job runner is not started, so grounding jobs stay
// queued.
func newDinnersRouter(t *testing.T) (http.Handler, *store.Store) {
	t.Helper()
	e := newDinnersEnv(t)
	return e.h, e.st
}

// dinnersEnv is a router whose dinners service, job runner and grounder
// share one store, grounding with fake search, pages, images and model.
type dinnersEnv struct {
	h      http.Handler
	st     *store.Store
	runner *jobs.Runner
	search *recipestest.Search
	pages  *recipestest.Pages
	images *recipestest.Images
	model  *llm.Fake
	// logs holds everything the router and services log.
	logs *logBuffer
}

// logBuffer is a bytes.Buffer safe for concurrent log writes.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func newDinnersEnv(t *testing.T) *dinnersEnv {
	t.Helper()
	st := newTestStore(t)
	// The Quest log is read against the household's current week.
	if err := household.SeedSettings(context.Background(), st, "America/New_York"); err != nil {
		t.Fatal(err)
	}
	e := &dinnersEnv{
		st:     st,
		search: &recipestest.Search{Results: map[string][]recipes.Candidate{}},
		pages:  &recipestest.Pages{Bodies: map[string][]byte{}},
		images: &recipestest.Images{JPEGs: map[string][]byte{}},
		model:  &llm.Fake{},
		logs:   &logBuffer{},
	}
	logger := slog.New(slog.NewTextHandler(e.logs, nil))
	runner, err := jobs.NewRunner(st, logger, 1)
	if err != nil {
		t.Fatal(err)
	}
	e.runner = runner
	// The router serves the photos the grounder caches.
	dataDir := t.TempDir()
	g := recipestest.NewGrounder(t, st, dataDir, e.search, e.pages, e.images, e.model)
	photos, err := recipes.NewPhotoCache(e.images, st, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := dinners.NewService(st, g, runner, func() {}, logger)
	if err != nil {
		t.Fatal(err)
	}
	e.h, err = NewRouter(logger, "dev", nil, stubAuth{}, stubSessions{}, newTestSettings(t), newTestSearch(t), newTestAI(t), newTestChat(t),
		svc, newTestSuggest(t), newTestPlan(t), newTestShopping(t), newTestGoogle(t), newTestPush(t), newTestIdeas(t), runner, photos, fstest.MapFS{"index.html": {Data: []byte(fixtureIndex)}})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// start starts the env's job runner, stopping it when the test ends.
func (e *dinnersEnv) start(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	if err := e.runner.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		e.runner.Wait()
	})
}

// callDinners sends a signed-in request with body (none when empty).
func callDinners(t *testing.T, h http.Handler, method, target, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	return serve(t, h, signedIn(method, target, r))
}

// createDinner adds a Regular and returns its id.
func createDinner(t *testing.T, h http.Handler, body string) string {
	t.Helper()
	rec, got := callDinners(t, h, http.MethodPost, dinnersPath, body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST %s = %d: %s", body, rec.Code, rec.Body)
	}
	return got["id"].(string)
}

// listNames returns the names GET /dinners?status=status lists, in order.
func listNames(t *testing.T, h http.Handler, status string) []string {
	t.Helper()
	rec, body := callDinners(t, h, http.MethodGet, dinnersPath+"?status="+status, "")
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

func TestCreateDinnerByName(t *testing.T) {
	t.Parallel()
	router, st := newDinnersRouter(t)

	rec, body := callDinners(t, router, http.MethodPost, dinnersPath, `{"name":"  Chicken Tikka Masala "}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body)
	}
	for k, v := range map[string]any{
		"name":             "Chicken Tikka Masala",
		"status":           "regular",
		"origin":           "regular",
		"grounding_status": "pending",
		"is_favorite":      false,
		"photo_url":        nil,
		"method":           nil,
		"minutes_total":    nil,
		"difficulty":       nil,
		"updated_by":       testSession.Subject,
	} {
		if got, ok := body[k]; !ok || got != v {
			t.Errorf("%s = %#v (present %v), want %#v", k, got, ok, v)
		}
	}
	if got, ok := body["recipe_url"]; ok {
		t.Errorf("recipe_url = %#v, want no recipe_url in a dinner response", got)
	}
	var normalized, updatedBy string
	if err := st.Reader.QueryRow(`SELECT normalized_name, updated_by FROM dinners WHERE id = ?`, body["id"]).Scan(&normalized, &updatedBy); err != nil {
		t.Fatal(err)
	}
	if normalized != "chicken tikka masala" || updatedBy != testSession.Subject {
		t.Errorf("stored normalized_name, updated_by = %q, %q", normalized, updatedBy)
	}
}

func TestCreateDinnerByURL(t *testing.T) {
	t.Parallel()
	router, _ := newDinnersRouter(t)

	link := "https://www.example.com/recipes/12345/beef-and-broccoli-stir-fry/"
	rec, body := callDinners(t, router, http.MethodPost, dinnersPath, `{"name":"Stir fry night","recipe_url":"`+link+`"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body)
	}
	for k, v := range map[string]any{
		"name":             "Stir fry night",
		"status":           "regular",
		"origin":           "regular",
		"grounding_status": "pending",
		"updated_by":       testSession.Subject,
	} {
		if body[k] != v {
			t.Errorf("%s = %#v, want %#v", k, body[k], v)
		}
	}
	// The link stays on the server for grounding; responses never carry it.
	if got, ok := body["recipe_url"]; ok {
		t.Errorf("recipe_url = %#v, want no recipe_url in a dinner response", got)
	}

	// The same link again is a duplicate even though its name would differ.
	rec, body = callDinners(t, router, http.MethodPost, dinnersPath, `{"name":"Something else entirely"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("unrelated name = %d: %s", rec.Code, rec.Body)
	}
	rec, body = callDinners(t, router, http.MethodPost, dinnersPath, `{"name":"Beef and broccoli","recipe_url":"`+link+`"}`)
	if rec.Code != http.StatusConflict || errorCode(body) != "duplicate_dinner" {
		t.Errorf("same link again = %d %v, want 409 duplicate_dinner", rec.Code, body)
	}

	for _, tc := range []struct{ name, body string }{
		{"plain http", `{"name":"Chili","recipe_url":"http://example.com/recipes/chili"}`},
		{"not a URL", `{"name":"Chili","recipe_url":"chili"}`},
		{"credentials", `{"name":"Chili","recipe_url":"https://me:pw@example.com/recipes/chili"}`},
		{"link without a name", `{"recipe_url":"https://example.com/recipes/chili"}`},
		{"filler name with a link", `{"name":"The Best Easy Recipe","recipe_url":"https://example.com/recipes/chili"}`},
		{"neither field", `{}`},
	} {
		rec, body := callDinners(t, router, http.MethodPost, dinnersPath, tc.body)
		if rec.Code != http.StatusBadRequest || errorCode(body) != "invalid_dinner" {
			t.Errorf("%s: %d %v, want 400 invalid_dinner", tc.name, rec.Code, body)
		}
	}
}

// TestDinnerResponsesHideRecipeURL: a pasted link may carry a personal
// token, so neither the create response nor a later list read shows any
// part of it, while the stored link still awaits grounding.
func TestDinnerResponsesHideRecipeURL(t *testing.T) {
	t.Parallel()
	router, st := newDinnersRouter(t)

	const link = "https://c.example/recipes/stew?access_token=personal-token-abc123"
	rec, body := callDinners(t, router, http.MethodPost, dinnersPath, `{"name":"Stew","recipe_url":"`+link+`"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body)
	}
	created := rec.Body.String()
	rec, _ = callDinners(t, router, http.MethodGet, dinnersPath+"?status=regular", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status=regular = %d: %s", rec.Code, rec.Body)
	}
	for _, resp := range []struct{ name, body string }{{"create", created}, {"list", rec.Body.String()}} {
		for _, secret := range []string{link, "c.example", "access_token", "personal-token-abc123", "recipe_url"} {
			if strings.Contains(resp.body, secret) {
				t.Errorf("%s response contains %q: %s", resp.name, secret, resp.body)
			}
		}
	}

	var stored string
	if err := st.Reader.QueryRow(`SELECT recipe_url FROM dinners WHERE id = ?`, body["id"]).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != link {
		t.Errorf("stored recipe_url = %q, want %q kept for grounding", stored, link)
	}
}

// TestDinnerResponsesHideTokenInLinkPath: a personal token can sit
// anywhere in a link's path, even as plain letters, so a dinner added from
// a link is named only by the household and neither the create response, a
// later list read nor the duplicate error for the same link shows any of
// the link.
func TestDinnerResponsesHideTokenInLinkPath(t *testing.T) {
	t.Parallel()
	router, _ := newDinnersRouter(t)

	const link = "https://c.example/recipes/stew-key-abcdefghijklmno"
	rec, body := callDinners(t, router, http.MethodPost, dinnersPath, `{"name":"Sunday stew","recipe_url":"`+link+`"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body)
	}
	if body["name"] != "Sunday stew" {
		t.Errorf("name = %v, want Sunday stew", body["name"])
	}
	created := rec.Body.String()
	rec, _ = callDinners(t, router, http.MethodGet, dinnersPath+"?status=regular", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status=regular = %d: %s", rec.Code, rec.Body)
	}
	listed := rec.Body.String()
	rec, _ = callDinners(t, router, http.MethodPost, dinnersPath, `{"name":"Beef stew","recipe_url":"`+link+`"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("same link again = %d, want 409: %s", rec.Code, rec.Body)
	}
	// Each secret holds a character no ULID has (Crockford base32 has no I,
	// L, O, U, "-", "." or "/"), so the dinner's id never matches one.
	for _, resp := range []struct{ name, body string }{{"create", created}, {"list", listed}, {"duplicate", rec.Body.String()}} {
		lower := strings.ToLower(resp.body)
		for _, secret := range []string{link, "c.example", "recipes/stew", "stew-key", "abcdefghijklmno"} {
			if strings.Contains(lower, secret) {
				t.Errorf("%s response contains %q: %s", resp.name, secret, resp.body)
			}
		}
	}
}

func TestCreateDinnerDuplicate(t *testing.T) {
	t.Parallel()
	router, st := newDinnersRouter(t)
	createDinner(t, router, `{"name":"Chicken Tikka Masala"}`)

	for _, name := range []string{
		"chicken tikka masala",
		"Easy Chicken Tikka-Masala!",
		"The Best Homemade Chicken Tikka Masala Recipe",
	} {
		rec, body := callDinners(t, router, http.MethodPost, dinnersPath, `{"name":"`+name+`"}`)
		if rec.Code != http.StatusConflict || errorCode(body) != "duplicate_dinner" {
			t.Errorf("%q: %d %v, want 409 duplicate_dinner", name, rec.Code, body)
		}
	}
	// A name repeating the dish is a duplicate with a link too.
	rec, body := callDinners(t, router, http.MethodPost, dinnersPath, `{"name":"Chicken tikka masala","recipe_url":"https://example.com/other-curry.html"}`)
	if rec.Code != http.StatusConflict {
		t.Errorf("named duplicate with a link = %d %v, want 409", rec.Code, body)
	}

	var n int
	if err := st.Reader.QueryRow(`SELECT count(*) FROM dinners`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("dinners rows = %d, want 1", n)
	}

	// A different dish sharing words is not a duplicate.
	createDinner(t, router, `{"name":"Chicken Tikka Wraps"}`)
}

func TestCreateDinnerRejected(t *testing.T) {
	t.Parallel()
	router, _ := newDinnersRouter(t)
	for _, tc := range []struct{ name, body, code string }{
		{"empty name", `{"name":"   "}`, "invalid_dinner"},
		{"filler only", `{"name":"The Best Easy Recipe"}`, "invalid_dinner"},
		{"too long", `{"name":"` + strings.Repeat("x", dinners.MaxNameChars+1) + `"}`, "invalid_dinner"},
		{"null name", `{"name":null}`, "invalid_request"},
		{"unknown field", `{"name":"Chili","status":"quest_log"}`, "invalid_request"},
		{"not an object", `"Chili"`, "invalid_request"},
	} {
		rec, body := callDinners(t, router, http.MethodPost, dinnersPath, tc.body)
		if rec.Code != http.StatusBadRequest || errorCode(body) != tc.code {
			t.Errorf("%s: %d %v, want 400 %s", tc.name, rec.Code, body, tc.code)
		}
	}
}

func TestListDinnersFiltersByStatus(t *testing.T) {
	t.Parallel()
	router, st := newDinnersRouter(t)
	tacos := createDinner(t, router, `{"name":"Tacos"}`)
	createDinner(t, router, `{"name":"Lasagna"}`)
	pho := createDinner(t, router, `{"name":"Pho"}`)
	gone := createDinner(t, router, `{"name":"Meatloaf"}`)

	ctx := context.Background()
	if _, err := st.Writer.ExecContext(ctx, `UPDATE dinners SET status = 'quest_log', origin = 'side_quest' WHERE id = ?`, pho); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Writer.ExecContext(ctx,
		`INSERT INTO photos (id, source_url, local_path, width, height, sha256, fetched_at, created_at, updated_at)
		 VALUES ('photo1', 'https://example.com/tacos.jpg', 'media/photos/photo1', 1200, 800, 'abc', 't', 't', 't')`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Writer.ExecContext(ctx, `UPDATE dinners SET photo_id = 'photo1', grounding_status = 'grounded' WHERE id = ?`, tacos); err != nil {
		t.Fatal(err)
	}
	if rec, _ := callDinners(t, router, http.MethodDelete, dinnersPath+"/"+gone, ""); rec.Code != http.StatusOK {
		t.Fatalf("DELETE = %d", rec.Code)
	}

	if got := strings.Join(listNames(t, router, "regular"), ","); got != "Lasagna,Tacos" {
		t.Errorf("regular = %s, want Lasagna,Tacos", got)
	}
	if got := strings.Join(listNames(t, router, "quest_log"), ","); got != "Pho" {
		t.Errorf("quest_log = %s, want Pho", got)
	}

	_, body := callDinners(t, router, http.MethodGet, dinnersPath+"?status=regular", "")
	for _, d := range body["dinners"].([]any) {
		d := d.(map[string]any)
		switch d["name"] {
		case "Tacos":
			if d["photo_url"] != "/media/photos/photo1-400.jpg" || d["grounding_status"] != "grounded" {
				t.Errorf("Tacos photo_url, grounding_status = %v, %v", d["photo_url"], d["grounding_status"])
			}
		case "Lasagna":
			if d["photo_url"] != nil || d["grounding_status"] != "pending" {
				t.Errorf("Lasagna photo_url, grounding_status = %v, %v", d["photo_url"], d["grounding_status"])
			}
		}
	}

	// An empty list is [] rather than null.
	empty, _ := newDinnersRouter(t)
	rec, _ := callDinners(t, empty, http.MethodGet, dinnersPath+"?status=regular", "")
	if got := rec.Body.String(); got != `{"dinners":[]}` {
		t.Errorf("empty list = %s", got)
	}

	for _, q := range []string{"", "?status=archived", "?status=suggested", "?status=bogus"} {
		rec, body := callDinners(t, router, http.MethodGet, dinnersPath+q, "")
		if rec.Code != http.StatusBadRequest || errorCode(body) != "invalid_dinner" {
			t.Errorf("GET %q = %d %v, want 400 invalid_dinner", q, rec.Code, body)
		}
	}
}

// The Quest log leaves out a dinner on the plan for the household's current
// week or a later one: it is scheduled already. A dinner on an earlier
// week's plan only, or on none, is listed.
func TestQuestLogLeavesOutPlannedDinners(t *testing.T) {
	t.Parallel()
	router, st := newDinnersRouter(t)
	ctx := context.Background()
	names := []string{"Bibimbap", "Gumbo", "Pho", "Ramen", "Tagine"}
	ids := map[string]string{}
	for _, name := range names {
		id := createDinner(t, router, `{"name":"`+name+`"}`)
		if _, err := st.Writer.ExecContext(ctx, `UPDATE dinners SET status = 'quest_log', origin = 'side_quest' WHERE id = ?`, id); err != nil {
			t.Fatal(err)
		}
		ids[name] = id
	}

	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	thisWeek := household.WeekStart(household.Today(time.Now(), loc))
	// day is the date days after the Monday weeks after this week's.
	day := func(weeks, days int) string {
		y, m, d := thisWeek.Date()
		return time.Date(y, m, d+7*weeks+days, 0, 0, 0, 0, time.UTC).Format(time.DateOnly)
	}
	// Gumbo is on last week's plan only; Pho on this week's; Ramen on the
	// week after next; Tagine on last week's and this week's.
	plan := func(weeks int, dinners ...string) {
		t.Helper()
		weekStart := day(weeks, 0)
		planID := "plan-" + weekStart
		if _, err := st.Writer.ExecContext(ctx,
			`INSERT INTO week_plans (id, week_start, status, created_at, updated_at) VALUES (?, ?, 'draft', 't', 't')`,
			planID, weekStart); err != nil {
			t.Fatal(err)
		}
		for i, name := range dinners {
			if _, err := st.Writer.ExecContext(ctx,
				`INSERT INTO plan_days (id, plan_id, date, dinner_id, is_new, created_at, updated_at) VALUES (?, ?, ?, ?, 1, 't', 't')`,
				planID+"-"+name, planID, day(weeks, i), ids[name]); err != nil {
				t.Fatal(err)
			}
		}
	}
	plan(-1, "Gumbo", "Tagine")
	plan(0, "Pho", "Tagine")
	plan(2, "Ramen")

	if got := strings.Join(listNames(t, router, "quest_log"), ","); got != "Bibimbap,Gumbo" {
		t.Errorf("quest_log = %s, want Bibimbap,Gumbo", got)
	}
}

func TestPatchDinner(t *testing.T) {
	t.Parallel()
	router, st := newDinnersRouter(t)
	id := createDinner(t, router, `{"name":"Chili"}`)
	createDinner(t, router, `{"name":"Pot Roast"}`)
	if _, err := st.Writer.Exec(`UPDATE dinners SET updated_by = 'someone-else' WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}

	rec, body := callDinners(t, router, http.MethodPatch, dinnersPath+"/"+id,
		`{"name":"White Chicken Chili","method":"crockpot","minutes_total":480,"difficulty":"easy"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	for k, v := range map[string]any{
		"name":          "White Chicken Chili",
		"method":        "crockpot",
		"minutes_total": float64(480),
		"difficulty":    "easy",
		"status":        "regular",
		"updated_by":    testSession.Subject,
	} {
		if body[k] != v {
			t.Errorf("%s = %#v, want %#v", k, body[k], v)
		}
	}
	var normalized string
	if err := st.Reader.QueryRow(`SELECT normalized_name FROM dinners WHERE id = ?`, id).Scan(&normalized); err != nil {
		t.Fatal(err)
	}
	if normalized != "white chicken chili" {
		t.Errorf("normalized_name = %q", normalized)
	}

	// A partial patch leaves the other fields.
	rec, body = callDinners(t, router, http.MethodPatch, dinnersPath+"/"+id, `{"difficulty":"medium"}`)
	if rec.Code != http.StatusOK || body["method"] != "crockpot" || body["difficulty"] != "medium" || body["name"] != "White Chicken Chili" {
		t.Errorf("partial patch = %d %v", rec.Code, body)
	}
	// Renaming to its own name, respelled, is not a duplicate of itself.
	if rec, body := callDinners(t, router, http.MethodPatch, dinnersPath+"/"+id, `{"name":"Easy white chicken chili"}`); rec.Code != http.StatusOK {
		t.Errorf("self rename = %d %v", rec.Code, body)
	}
	// Renaming onto another dinner is.
	rec, body = callDinners(t, router, http.MethodPatch, dinnersPath+"/"+id, `{"name":"The Best Pot Roast"}`)
	if rec.Code != http.StatusConflict || errorCode(body) != "duplicate_dinner" {
		t.Errorf("rename onto Pot Roast = %d %v, want 409", rec.Code, body)
	}
}

func TestPatchDinnerValidation(t *testing.T) {
	t.Parallel()
	router, _ := newDinnersRouter(t)
	id := createDinner(t, router, `{"name":"Chili"}`)

	for _, tc := range []struct {
		name, body, code, in string
	}{
		{"bad method", `{"method":"microwave"}`, "invalid_dinner", "method"},
		{"crock pot spelling", `{"method":"crock pot"}`, "invalid_dinner", "method"},
		{"bad difficulty", `{"difficulty":"expert"}`, "invalid_dinner", "difficulty"},
		{"zero minutes", `{"minutes_total":0}`, "invalid_dinner", "minutes_total"},
		{"negative minutes", `{"minutes_total":-5}`, "invalid_dinner", "minutes_total"},
		{"too many minutes", `{"minutes_total":1441}`, "invalid_dinner", "minutes_total"},
		{"empty name", `{"name":""}`, "invalid_dinner", "name"},
		{"every problem", `{"method":"x","difficulty":"y","minutes_total":0}`, "invalid_dinner", "method: must be one of stovetop, oven, crockpot, grill, other; minutes_total"},
		{"empty patch", `{}`, "invalid_dinner", "no fields"},
		{"fractional minutes", `{"minutes_total":30.5}`, "invalid_request", "minutes_total"},
		{"string minutes", `{"minutes_total":"30"}`, "invalid_request", "minutes_total"},
		{"null method", `{"method":null}`, "invalid_request", "method"},
		{"read-only field", `{"status":"archived"}`, "invalid_request", "status"},
		{"favorite field", `{"is_favorite":true}`, "invalid_request", "is_favorite"},
	} {
		rec, body := callDinners(t, router, http.MethodPatch, dinnersPath+"/"+id, tc.body)
		msg, _ := body["error"].(map[string]any)["message"].(string)
		if rec.Code != http.StatusBadRequest || errorCode(body) != tc.code || !strings.Contains(msg, tc.in) {
			t.Errorf("%s: %d %v, want 400 %s containing %q", tc.name, rec.Code, body, tc.code, tc.in)
		}
	}

	_, body := callDinners(t, router, http.MethodGet, dinnersPath+"?status=regular", "")
	d := body["dinners"].([]any)[0].(map[string]any)
	if d["method"] != nil || d["difficulty"] != nil || d["minutes_total"] != nil || d["name"] != "Chili" {
		t.Errorf("a rejected patch changed the dinner: %v", d)
	}

	rec, body := callDinners(t, router, http.MethodPatch, dinnersPath+"/nope", `{"method":"oven"}`)
	if rec.Code != http.StatusNotFound || errorCode(body) != "not_found" {
		t.Errorf("unknown id = %d %v, want 404", rec.Code, body)
	}
}

func TestDeleteDinnerArchives(t *testing.T) {
	t.Parallel()
	router, st := newDinnersRouter(t)
	id := createDinner(t, router, `{"name":"Shepherd's Pie"}`)
	if _, err := st.Writer.Exec(`UPDATE dinners SET updated_by = 'someone-else' WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}

	rec, body := callDinners(t, router, http.MethodDelete, dinnersPath+"/"+id, "")
	if rec.Code != http.StatusOK || body["status"] != "archived" || body["updated_by"] != testSession.Subject {
		t.Fatalf("DELETE = %d %v", rec.Code, body)
	}
	var status, name, updatedBy string
	if err := st.Reader.QueryRow(`SELECT status, name, updated_by FROM dinners WHERE id = ?`, id).Scan(&status, &name, &updatedBy); err != nil {
		t.Fatalf("archived row is gone: %v", err)
	}
	if status != "archived" || name != "Shepherd's Pie" || updatedBy != testSession.Subject {
		t.Errorf("row = %s %q %s", status, name, updatedBy)
	}
	if got := listNames(t, router, "regular"); len(got) != 0 {
		t.Errorf("regular after archive = %v", got)
	}
	// An archived name may be added again.
	createDinner(t, router, `{"name":"Shepherds Pie"}`)

	rec, body = callDinners(t, router, http.MethodDelete, dinnersPath+"/nope", "")
	if rec.Code != http.StatusNotFound || errorCode(body) != "not_found" {
		t.Errorf("unknown id = %d %v, want 404", rec.Code, body)
	}
}

func TestFavoriteDinnerToggles(t *testing.T) {
	t.Parallel()
	router, st := newDinnersRouter(t)
	id := createDinner(t, router, `{"name":"Fish Tacos"}`)
	if _, err := st.Writer.Exec(`UPDATE dinners SET updated_by = 'someone-else' WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}

	for _, want := range []bool{true, false, true} {
		rec, body := callDinners(t, router, http.MethodPost, dinnersPath+"/"+id+"/favorite", "")
		if rec.Code != http.StatusOK || body["is_favorite"] != want || body["updated_by"] != testSession.Subject {
			t.Fatalf("favorite = %d %v, want is_favorite %v", rec.Code, body, want)
		}
	}
	var fav int
	if err := st.Reader.QueryRow(`SELECT is_favorite FROM dinners WHERE id = ?`, id).Scan(&fav); err != nil {
		t.Fatal(err)
	}
	if fav != 1 {
		t.Errorf("stored is_favorite = %d, want 1", fav)
	}

	rec, body := callDinners(t, router, http.MethodPost, dinnersPath+"/nope/favorite", "")
	if rec.Code != http.StatusNotFound || errorCode(body) != "not_found" {
		t.Errorf("unknown id = %d %v, want 404", rec.Code, body)
	}
}

// TestDinnerWritesRequireCSRF sends every dinners write without the CSRF
// header and with a wrong one; each is refused and changes nothing.
func TestDinnerWritesRequireCSRF(t *testing.T) {
	t.Parallel()
	router, st := newDinnersRouter(t)
	id := createDinner(t, router, `{"name":"Chili"}`)

	var before string
	snapshot := `SELECT group_concat(id || name || status || is_favorite || coalesce(method, '') || updated_at, '|') FROM dinners`
	if err := st.Reader.QueryRow(snapshot).Scan(&before); err != nil {
		t.Fatal(err)
	}

	writes := []struct{ method, target, body string }{
		{http.MethodPost, dinnersPath, `{"name":"Lasagna"}`},
		{http.MethodPatch, dinnersPath + "/" + id, `{"method":"oven"}`},
		{http.MethodDelete, dinnersPath + "/" + id, ""},
		{http.MethodPost, dinnersPath + "/" + id + "/favorite", ""},
	}
	for _, w := range writes {
		for _, header := range []*string{nil, ptr("wrong-token")} {
			req := httptest.NewRequest(w.method, w.target, strings.NewReader(w.body))
			req.AddCookie(validSessionCookie)
			if header != nil {
				req.Header.Set(CSRFHeader, *header)
			}
			rec, body := serve(t, router, req)
			if rec.Code != http.StatusForbidden || errorCode(body) != "csrf" {
				t.Errorf("%s %s (header %v) = %d %v, want 403 csrf", w.method, w.target, header != nil, rec.Code, body)
			}
		}
	}

	var after string
	if err := st.Reader.QueryRow(snapshot).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Errorf("refused writes changed dinners:\n before %s\n after  %s", before, after)
	}

	// Without a session, the list is 401 too.
	req := httptest.NewRequest(http.MethodGet, dinnersPath+"?status=regular", nil)
	if rec, body := serve(t, router, req); rec.Code != http.StatusUnauthorized || errorCode(body) != "unauthenticated" {
		t.Errorf("GET without a session = %d %v, want 401", rec.Code, body)
	}
}

func ptr[T any](v T) *T { return &v }
