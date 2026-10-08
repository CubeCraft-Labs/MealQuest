package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"testing/fstest"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/harness"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/llm"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/skills"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ideas"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/jobs"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/recipes"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/recipes/recipestest"
)

const ideasPath = "/api/v1/ideas/from-ingredients"

// newIdeasRouter returns a router whose ideas service runs
// ideas_from_ingredients through the real harness with the scripted model
// and grounds with fake search, pages and images and the scripted
// normalize. Its job runner is started when start is set.
func newIdeasRouter(t *testing.T, model, normalize *llm.Fake, search *recipestest.Search, pages *recipestest.Pages, images *recipestest.Images, start bool) (http.Handler, *jobs.Runner) {
	t.Helper()
	st := newTestStore(t)
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
	svc, err := ideas.NewService(g, generate, runner, logger)
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewRouter(logger, "dev", nil, stubAuth{}, stubSessions{}, newTestSettings(t), newTestSearch(t), newTestAI(t),
		newTestDinners(t), newTestSuggest(t), newTestPlan(t), newTestShopping(t), newTestGoogle(t), svc, runner, newTestPhotos(t), fstest.MapFS{"index.html": {Data: []byte(fixtureIndex)}})
	if err != nil {
		t.Fatal(err)
	}
	if start {
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
	return h, runner
}

func TestPostIdeasFromIngredientsRefusesBadRequests(t *testing.T) {
	t.Parallel()
	h, _ := newIdeasRouter(t, &llm.Fake{}, &llm.Fake{}, &recipestest.Search{}, &recipestest.Pages{}, &recipestest.Images{}, false)

	for _, tc := range []struct{ body, message string }{
		{`{"quick_only": false}`, "ingredients: required"},
		{`{"ingredients": ["rice"]}`, "quick_only: required"},
		{`{"ingredients": [], "quick_only": false}`, "ingredients: list at least one ingredient"},
		{`{"ingredients": ["rice", " "], "quick_only": false}`, "ingredients: ingredient 2 is blank"},
		{`{"ingredients": ["Rice", "rice"], "quick_only": false}`, `ingredients: ingredient 2, "rice", repeats "Rice"`},
		{`{"ingredients": ["rice"], "quick_only": "yes"}`, "quick_only: want a JSON bool"},
		{`{"ingredients": ["rice"], "quick_only": false, "extra": 1}`, `invalid request body: json: unknown field "extra"`},
	} {
		rec, body := callDinners(t, h, http.MethodPost, ideasPath, tc.body)
		if rec.Code != http.StatusBadRequest || errorCode(body) != "invalid_request" {
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
	h, runner := newIdeasRouter(t, model, normalize, search, pages, images, true)

	rec, body := callDinners(t, h, http.MethodPost, ideasPath, `{"ingredients": ["Chicken thighs", "rice", "scallions"], "quick_only": true}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST = %d: %s", rec.Code, rec.Body)
	}
	id, _ := body["jobId"].(string)
	if id == "" || len(body) != 1 {
		t.Fatalf("POST answered %v, want only a jobId", body)
	}

	deadline := time.Now().Add(10 * time.Second)
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
	rec, job := callDinners(t, h, http.MethodGet, "/api/v1/jobs/"+id, "")
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

func TestJobsEndpointRefusesIdeasKind(t *testing.T) {
	t.Parallel()
	h, _ := newIdeasRouter(t, &llm.Fake{}, &llm.Fake{}, &recipestest.Search{}, &recipestest.Pages{}, &recipestest.Images{}, false)
	rec, body := callDinners(t, h, http.MethodPost, "/api/v1/jobs", `{"kind": "ideas_from_ingredients", "payload": {"ingredients": [], "quick_only": false}}`)
	if rec.Code != http.StatusBadRequest || errorCode(body) != "service_only_kind" {
		t.Fatalf("POST /jobs = %d %v, want 400 service_only_kind", rec.Code, body)
	}
}
