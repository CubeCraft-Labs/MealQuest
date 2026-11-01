package ideastest

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/harness"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/llm"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/skills"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/household"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ideas"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/jobs"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/recipes"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/recipes/recipestest"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/server"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/server/srvtest"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

const ideasPath = "/api/v1/ideas/from-ingredients"

// jobWait is how long an ideas test polls before an async job, or a
// progressive grounding result that has not appeared, fails the test.
// The bound stays finite so a stuck job still fails.
const jobWait = 90 * time.Second

// newIdeasRouter returns a router whose ideas service runs
// ideas_from_ingredients and quick_ideas through the real harness with the
// scripted model and grounds with fake search, pages and images and the
// scripted normalize, with its job runner and store, whose household
// settings are seeded. The runner is started when start is set.
func newIdeasRouter(t *testing.T, model, normalize *llm.Fake, search *recipestest.Search, pages *recipestest.Pages, images *recipestest.Images, start bool) (http.Handler, *jobs.Runner, *store.Store) {
	t.Helper()
	st := srvtest.NewTestStore(t)
	if err := household.SeedSettings(t.Context(), st, "America/New_York"); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	runner, err := jobs.NewRunner(st, logger, 1)
	if err != nil {
		t.Fatal(err)
	}
	g := recipestest.NewGrounder(t, st, t.TempDir(), search, pages, images, normalize)
	guard, err := harness.NewGuard(harness.DefaultMaxCalls)
	if err != nil {
		t.Fatal(err)
	}
	cfg := harness.Config{
		Settings:    llm.Settings{Provider: "anthropic", Model: "claude-test", Temperature: 0.7},
		NewProvider: func(llm.Settings, string) (llm.Provider, error) { return model, nil },
		Store:       st,
		Guard:       guard,
	}
	generate := func(ctx context.Context, scope string, in skills.IdeasRequest) (skills.Ideas, error) {
		return skills.IdeasFromIngredients(ctx, cfg, scope, in)
	}
	quick := func(ctx context.Context, scope string, in skills.QuickIdeasRequest) (skills.QuickIdeaList, error) {
		return skills.QuickIdeas(ctx, cfg, scope, in)
	}
	svc, err := ideas.NewService(st, g, generate, quick, runner, logger)
	if err != nil {
		t.Fatal(err)
	}
	h, err := server.NewRouter(logger, "dev", nil, srvtest.StubAuth{}, srvtest.StubSessions{}, srvtest.NewTestSettings(t), srvtest.NewTestSearch(t), srvtest.NewTestAI(t), srvtest.NewTestChat(t),
		srvtest.NewTestDinners(t), srvtest.NewTestSuggest(t), srvtest.NewTestPlan(t), srvtest.NewTestShopping(t), srvtest.NewTestGoogle(t), srvtest.NewTestPush(t), svc, srvtest.NewTestHome(t), runner, srvtest.NewTestPhotos(t), fstest.MapFS{"index.html": {Data: []byte(srvtest.FixtureIndex)}})
	if err != nil {
		t.Fatal(err)
	}
	if start {
		startRunner(t, runner)
	}
	return h, runner, st
}

// startRunner starts runner until the test ends.
func startRunner(t *testing.T, runner *jobs.Runner) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	if err := runner.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		runner.Wait()
	})
}

// scriptIdeas scripts ideas_from_ingredients to propose names, in order;
// a recipe grounds those in grounds, the others ground none, and search
// fails as fail says. It returns the router's fakes.
func scriptIdeas(t *testing.T, names, grounds []string, fail map[string]error) (*llm.Fake, *llm.Fake, *recipestest.Search, *recipestest.Pages, *recipestest.Images) {
	t.Helper()
	model, normalize := &llm.Fake{}, &llm.Fake{}
	search := &recipestest.Search{Results: map[string][]recipes.Candidate{}, Fail: fail}
	pages := &recipestest.Pages{Bodies: map[string][]byte{}}
	images := &recipestest.Images{JPEGs: map[string][]byte{}}
	for i, name := range grounds {
		page, photo := fmt.Sprintf("https://r.example/%d", i), fmt.Sprintf("https://r.example/%d.jpg", i)
		search.Results[name] = []recipes.Candidate{{URL: page}}
		pages.Bodies[page] = recipestest.RecipePage(name, "R Kitchen", photo, 25)
		images.JPEGs[photo] = recipestest.JPEG(t, 400, 320, uint8(i+1))
		normalize.QueueGenerate(recipestest.NormalizedReply("stovetop", 25))
	}
	list := make([]string, len(names))
	for i, name := range names {
		list[i] = fmt.Sprintf(`{"name":%q,"uses":["rice"],"missing":[],"est_minutes":30,"method":"stovetop","difficulty":"easy","search_query":%q}`, name, name)
	}
	js := `{"ideas":[` + strings.Join(list, ",") + `]}`
	model.QueueGenerate(llm.GenerateResult{Response: llm.Response{Text: js, JSON: json.RawMessage(js)}})
	return model, normalize, search, pages, images
}

// startIdeasJob posts a request for ideas from rice and returns the job id.
func startIdeasJob(t *testing.T, h http.Handler) string {
	t.Helper()
	rec, body := srvtest.CallDinners(t, h, http.MethodPost, ideasPath, `{"ingredients": ["rice"], "quick_only": false}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST = %d: %s", rec.Code, rec.Body)
	}
	return body["jobId"].(string)
}

// assertFailedWithoutIdeas checks that GET /jobs/{id} reports job failed
// with code and a result showing no ideas.
func assertFailedWithoutIdeas(t *testing.T, job map[string]any, code string) {
	t.Helper()
	if job["status"] != "failed" || job["error"] != code {
		t.Fatalf("GET job = %v, want failed with %s", job, code)
	}
	res, _ := job["result"].(map[string]any)
	if res == nil {
		t.Fatalf("failed job result = %v, want one showing no ideas", job["result"])
	}
	if list, ok := res["ideas"].([]any); !ok || len(list) != 0 {
		t.Fatalf("failed job ideas = %v, want none", res["ideas"])
	}
}

// storedIdeas is how many ideas job id's stored result holds.
func storedIdeas(t *testing.T, st *store.Store, id string) int {
	t.Helper()
	row, err := db.New(st.Reader).GetJob(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	var res ideas.Result
	if err := json.Unmarshal([]byte(row.Result.String), &res); err != nil {
		t.Fatal(err)
	}
	return len(res.Ideas)
}

func TestPostIdeasFromIngredientsRefusesBadRequests(t *testing.T) {
	t.Parallel()
	h, _, _ := newIdeasRouter(t, &llm.Fake{}, &llm.Fake{}, &recipestest.Search{}, &recipestest.Pages{}, &recipestest.Images{}, false)

	for _, tc := range []struct{ body, message string }{
		{`{"quick_only": false}`, "ingredients: required"},
		{`{"ingredients": ["rice"]}`, "quick_only: required"},
		{`{"ingredients": [], "quick_only": false}`, "ingredients: list at least one ingredient"},
		{`{"ingredients": ["rice", " "], "quick_only": false}`, "ingredients: ingredient 2 is blank"},
		{`{"ingredients": ["Rice", "rice"], "quick_only": false}`, `ingredients: ingredient 2, "rice", repeats "Rice"`},
		{`{"ingredients": ["rice"], "quick_only": "yes"}`, "quick_only: want a JSON bool"},
		{`{"ingredients": ["rice"], "quick_only": false, "extra": 1}`, `invalid request body: json: unknown field "extra"`},
	} {
		rec, body := srvtest.CallDinners(t, h, http.MethodPost, ideasPath, tc.body)
		if rec.Code != http.StatusBadRequest || srvtest.ErrorCode(body) != "invalid_request" {
			t.Errorf("POST %s = %d %v, want 400 invalid_request", tc.body, rec.Code, body)
			continue
		}
		if msg := body["error"].(map[string]any)["message"]; msg != tc.message {
			t.Errorf("POST %s message = %q, want %q", tc.body, msg, tc.message)
		}
	}
}

func TestPostIdeasFromIngredientsRunsTheJob(t *testing.T) {
	t.Parallel()
	model, normalize := &llm.Fake{}, &llm.Fake{}
	search := &recipestest.Search{Results: map[string][]recipes.Candidate{"chicken fried rice": {{URL: "https://r.example/fried-rice"}}}}
	pages := &recipestest.Pages{Bodies: map[string][]byte{
		"https://r.example/fried-rice": recipestest.RecipePage("Chicken Fried Rice", "R Kitchen", "https://r.example/fried-rice.jpg", 25),
	}}
	images := &recipestest.Images{JPEGs: map[string][]byte{"https://r.example/fried-rice.jpg": recipestest.JPEG(t, 400, 320, 1)}}
	js := `{"ideas":[{"name":"Chicken Fried Rice","uses":["rice","chicken thighs","peas"],"missing":["2 eggs"],"est_minutes":25,"method":"stovetop","difficulty":"easy","search_query":"chicken fried rice"}]}`
	model.QueueGenerate(llm.GenerateResult{Response: llm.Response{Text: js, JSON: json.RawMessage(js)}})
	normalize.QueueGenerate(recipestest.NormalizedReply("stovetop", 25))
	h, runner, _ := newIdeasRouter(t, model, normalize, search, pages, images, true)

	rec, body := srvtest.CallDinners(t, h, http.MethodPost, ideasPath, `{"ingredients": ["Chicken thighs", "rice", "scallions"], "quick_only": true}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST = %d: %s", rec.Code, rec.Body)
	}
	id, _ := body["jobId"].(string)
	if id == "" || len(body) != 1 {
		t.Fatalf("POST answered %v, want only a jobId", body)
	}

	deadline := time.Now().Add(jobWait)
	for {
		job, err := runner.Get(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		if job.Status == jobs.StatusSucceeded || job.Status == jobs.StatusFailed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("job still %s", job.Status)
		}
		time.Sleep(5 * time.Millisecond)
	}
	rec, job := srvtest.CallDinners(t, h, http.MethodGet, "/api/v1/jobs/"+id, "")
	if rec.Code != http.StatusOK || job["status"] != "succeeded" || job["kind"] != "ideas_from_ingredients" {
		t.Fatalf("GET job = %d %v", rec.Code, job)
	}
	res := job["result"].(map[string]any)
	if res["stage"] != "done" || res["quick_only"] != true || res["dropped"] != 0.0 {
		t.Fatalf("result = %v", res)
	}
	list := res["ideas"].([]any)
	if len(list) != 1 {
		t.Fatalf("ideas = %v, want 1", list)
	}
	got := list[0].(map[string]any)
	for key, want := range map[string]any{
		"name":          "Chicken Fried Rice",
		"summary":       "Uses 2 of 3 · missing: 2 eggs",
		"method":        "stovetop",
		"minutes_total": 25.0,
		"quick":         true,
		"site_name":     "R Kitchen",
		"source_url":    "https://r.example/fried-rice",
	} {
		if got[key] != want {
			t.Errorf("idea %s = %v, want %v", key, got[key], want)
		}
	}
	if uses, _ := json.Marshal(got["uses"]); string(uses) != `["Chicken thighs","rice"]` {
		t.Errorf("idea uses = %s, want the household's two listed ingredients", uses)
	}
	if got["recipe_id"] == "" || got["photo_url"] == "" {
		t.Errorf("idea = %v, want a recipe and photo", got)
	}
}

func TestFailedIdeasJobShowsNoIdeas(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		// grounds are the ideas a recipe grounds; the others ground none.
		grounds []string
		ideas   []string
		fail    map[string]error
		code    string
	}{
		{"no idea kept", nil, []string{"Mystery Stew"}, nil, "no_ideas_found"},
		{"a later round fails after ideas were shown",
			[]string{"Fried Rice", "Rice Bowls", "Rice Soup", "Rice Salad"},
			[]string{"Fried Rice", "Mystery Stew", "Rice Bowls", "Rice Soup", "Rice Salad", "Rice Pilaf"},
			map[string]error{"Rice Pilaf": errors.New("boom")}, "search_failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model, normalize, search, pages, images := scriptIdeas(t, tc.ideas, tc.grounds, tc.fail)
			h, _, _ := newIdeasRouter(t, model, normalize, search, pages, images, true)
			assertFailedWithoutIdeas(t, srvtest.WaitJob(t, h, startIdeasJob(t, h)), tc.code)
		})
	}
}

// A job left running with ideas shown is failed at the next start as
// interrupted, without its handler running again; it still shows no ideas.
func TestIdeasJobInterruptedByRestartShowsNoIdeas(t *testing.T) {
	t.Parallel()
	h, runner, st := newIdeasRouter(t, &llm.Fake{}, &llm.Fake{}, &recipestest.Search{}, &recipestest.Pages{}, &recipestest.Images{}, false)
	id := startIdeasJob(t, h)

	// The previous process claimed the job and showed an idea, then stopped.
	q := db.New(st.Writer)
	now := store.Now().String()
	if _, err := q.ClaimNextJob(t.Context(), sql.NullString{String: now, Valid: true}); err != nil {
		t.Fatal(err)
	}
	partial, err := ideas.Result{
		Stage:       ideas.StageGrounding,
		Ingredients: []string{"rice"},
		Ideas:       []ideas.Idea{{Name: "Fried Rice", Uses: []string{"rice"}, Missing: []string{}, Method: "stovetop", Difficulty: "easy"}},
	}.PublicJSON()
	if err != nil {
		t.Fatal(err)
	}
	if n, err := q.PublishJobResult(t.Context(), db.PublishJobResultParams{
		Result: sql.NullString{String: string(partial), Valid: true}, UpdatedAt: now, ID: id,
	}); err != nil || n != 1 {
		t.Fatalf("publish partial result = %d, %v", n, err)
	}

	startRunner(t, runner)
	_, job := srvtest.CallDinners(t, h, http.MethodGet, "/api/v1/jobs/"+id, "")
	assertFailedWithoutIdeas(t, job, jobs.CodeInterrupted)
	if res := job["result"].(map[string]any); res["stage"] != "grounding" {
		t.Errorf("result = %v, want the rest of the partial result kept", res)
	}
	if n := storedIdeas(t, st, id); n != 1 {
		t.Fatalf("stored ideas = %d, want the interrupted job's 1 still stored", n)
	}
}

// When the write clearing a failed job's ideas itself fails, the job fails
// with the runner's internal_error and its stored result keeps the ideas;
// it still shows none.
func TestIdeasJobWhoseClearingWriteFailsShowsNoIdeas(t *testing.T) {
	t.Parallel()
	model, normalize, search, pages, images := scriptIdeas(t,
		[]string{"Fried Rice", "Mystery Stew", "Rice Bowls", "Rice Soup", "Rice Salad", "Rice Pilaf"},
		[]string{"Fried Rice", "Rice Bowls", "Rice Soup", "Rice Salad"},
		map[string]error{"Rice Pilaf": errors.New("boom")})
	h, runner, st := newIdeasRouter(t, model, normalize, search, pages, images, false)
	// Refuse any write that empties a running job's shown ideas.
	if _, err := st.Writer.ExecContext(t.Context(), `
		CREATE TRIGGER refuse_clearing_ideas BEFORE UPDATE OF result ON jobs
		WHEN NEW.status = 'running'
			AND json_array_length(OLD.result, '$.ideas') > 0
			AND json_array_length(NEW.result, '$.ideas') = 0
		BEGIN SELECT RAISE(ABORT, 'clearing refused'); END`); err != nil {
		t.Fatal(err)
	}
	startRunner(t, runner)

	id := startIdeasJob(t, h)
	assertFailedWithoutIdeas(t, srvtest.WaitJob(t, h, id), jobs.CodeInternal)
	if n := storedIdeas(t, st, id); n != 4 {
		t.Fatalf("stored ideas = %d, want the 4 the refused write left", n)
	}
}

func TestJobsEndpointRefusesIdeasKind(t *testing.T) {
	t.Parallel()
	h, _, _ := newIdeasRouter(t, &llm.Fake{}, &llm.Fake{}, &recipestest.Search{}, &recipestest.Pages{}, &recipestest.Images{}, false)
	rec, body := srvtest.CallDinners(t, h, http.MethodPost, "/api/v1/jobs", `{"kind": "ideas_from_ingredients", "payload": {"ingredients": [], "quick_only": false}}`)
	if rec.Code != http.StatusBadRequest || srvtest.ErrorCode(body) != "service_only_kind" {
		t.Fatalf("POST /jobs = %d %v, want 400 service_only_kind", rec.Code, body)
	}
}
