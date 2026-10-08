package ideas

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/harness"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/llm"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/skills"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/jobs"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/recipes"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/recipes/recipestest"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/migrations"
)

// pantry is the household's ingredient list.
var pantry = []string{"Chicken thighs", "rice", "black beans", "red onion", "cilantro"}

type env struct {
	svc    *Service
	runner *jobs.Runner
	search *recipestest.Search
	pages  *recipestest.Pages
	images *recipestest.Images
	// model answers ideas_from_ingredients; normalize answers
	// normalize_recipe while grounding.
	model     *llm.Fake
	normalize *llm.Fake
	// fail, when set, fails ideas_from_ingredients without a model call.
	fail error
}

// newEnv returns an ideas service over a migrated store and an unstarted
// job runner. It finds ideas through the real harness and
// ideas_from_ingredients with the scripted e.model, and grounds with fake
// search, pages and images and the scripted e.normalize.
func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := t.Context()
	dataDir := t.TempDir()
	st, err := store.Open(ctx, dataDir)
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
	e := &env{
		search:    &recipestest.Search{Results: map[string][]recipes.Candidate{}},
		pages:     &recipestest.Pages{Bodies: map[string][]byte{}},
		images:    &recipestest.Images{JPEGs: map[string][]byte{}},
		model:     &llm.Fake{},
		normalize: &llm.Fake{},
	}
	g := recipestest.NewGrounder(t, st, dataDir, e.search, e.pages, e.images, e.normalize)
	guard, err := harness.NewGuard(harness.DefaultMaxCalls)
	if err != nil {
		t.Fatal(err)
	}
	cfg := harness.Config{
		Settings:    llm.Settings{Provider: "anthropic", Model: "claude-test", Temperature: 0.7},
		NewProvider: func(llm.Settings, string) (llm.Provider, error) { return e.model, nil },
		Store:       st,
		Guard:       guard,
	}
	generate := func(ctx context.Context, scope string, in skills.IdeasRequest) (skills.Ideas, error) {
		if e.fail != nil {
			return skills.Ideas{}, e.fail
		}
		return skills.IdeasFromIngredients(ctx, cfg, scope, in)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	e.runner, err = jobs.NewRunner(st, logger, 1)
	if err != nil {
		t.Fatal(err)
	}
	e.svc, err = NewService(g, generate, e.runner, logger)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// recipe makes the search for name find one page that grounds it, titled
// title, with a total time of minutes, and scripts the normalize_recipe
// reply grounding it takes. The page's total time is the grounded
// minutes, and "Slow Cooker" in title makes the grounded method crockpot,
// whatever the reply says.
func (e *env) recipe(t *testing.T, name, title string, minutes int) {
	t.Helper()
	slug := strings.ReplaceAll(strings.ToLower(name), " ", "-")
	page, photo := "https://r.example/"+slug, "https://r.example/"+slug+".jpg"
	e.search.Results[name] = []recipes.Candidate{{URL: page}}
	e.pages.Bodies[page] = recipestest.RecipePage(title, "R Kitchen", photo, minutes)
	e.images.JPEGs[photo] = recipestest.JPEG(t, 400, 320, uint8(len(e.images.JPEGs)+1))
	e.normalize.QueueGenerate(recipestest.NormalizedReply("stovetop", minutes))
}

// idea renders one idea of an ideas_from_ingredients reply whose search
// query is its name.
func idea(name, method string, minutes int, uses ...string) string {
	u, _ := json.Marshal(append([]string{}, uses...))
	return fmt.Sprintf(`{"name":%q,"uses":%s,"missing":["1 lime"],"est_minutes":%d,"method":%q,"difficulty":"easy","search_query":%q}`,
		name, u, minutes, method, name)
}

// reply scripts one ideas_from_ingredients reply holding ideas.
func (e *env) reply(ideas ...string) {
	js := `{"ideas":[` + strings.Join(ideas, ",") + `]}`
	e.model.QueueGenerate(llm.GenerateResult{Response: llm.Response{Text: js, JSON: json.RawMessage(js)}})
}

// recorder collects every result a job publishes.
type recorder struct {
	mu        sync.Mutex
	published []Result
}

// latest returns the last result published.
func (r *recorder) latest() (Result, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.published) == 0 {
		return Result{}, false
	}
	return r.published[len(r.published)-1], true
}

func (r *recorder) publish(res Result) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	// The result's slices are copied, as the runner stores each publish
	// as JSON at once.
	res.Ideas = append([]Idea(nil), res.Ideas...)
	r.published = append(r.published, res)
	return nil
}

// run runs the job's handler directly on ingredients, recording each
// publish.
func (e *env) run(t *testing.T, ingredients []string, quickOnly bool) (Result, []Result, error) {
	t.Helper()
	ings, err := skills.CleanIngredients(ingredients)
	if err != nil {
		t.Fatal(err)
	}
	var rec recorder
	res, err := e.svc.run(t.Context(), FromIngredientsJob{Ingredients: ings, QuickOnly: quickOnly}, rec.publish)
	return res, rec.published, err
}

// within reports whether every name in sub appears in list, in list's
// order.
func within(sub, list []string) bool {
	for _, name := range list {
		if len(sub) > 0 && sub[0] == name {
			sub = sub[1:]
		}
	}
	return len(sub) == 0
}

func names(list []Idea) []string {
	out := []string{}
	for _, i := range list {
		out = append(out, i.Name)
	}
	return out
}

func TestResultFillsAsEachIdeaGrounds(t *testing.T) {
	e := newEnv(t)
	for _, name := range []string{"Burrito Bowls", "Fried Rice", "Black Bean Soup", "Chicken Tacos", "Rice Pilaf", "Bean Salad"} {
		e.recipe(t, name, name, 35)
	}
	// Eight ideas: two no recipe grounds, so the sixth and seventh are
	// grounded in a second round, and the eighth never is.
	e.reply(
		idea("Burrito Bowls", "stovetop", 35, "rice", "black beans"),
		idea("Mystery Stew", "stovetop", 35, "rice"),
		idea("Fried Rice", "stovetop", 20, "rice", "Chicken thighs"),
		idea("Black Bean Soup", "stovetop", 40, "black beans", "red onion"),
		idea("Unknown Casserole", "oven", 50, "rice"),
		idea("Chicken Tacos", "stovetop", 25, "Chicken thighs", "cilantro"),
		idea("Rice Pilaf", "stovetop", 30, "rice"),
		idea("Bean Salad", "other", 15, "black beans"),
	)
	res, published, err := e.run(t, pantry, false)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Burrito Bowls", "Fried Rice", "Black Bean Soup", "Chicken Tacos", "Rice Pilaf"}
	if got := names(res.Ideas); !reflect.DeepEqual(got, want) {
		t.Fatalf("ideas = %q, want %q", got, want)
	}
	if res.Stage != StageDone || res.Dropped != 2 {
		t.Fatalf("result stage %s dropped %d, want done with 2 dropped", res.Stage, res.Dropped)
	}
	if !reflect.DeepEqual(res.Ingredients, pantry) || res.QuickOnly {
		t.Fatalf("result request = %q quick %v", res.Ingredients, res.QuickOnly)
	}
	if got := e.search.Queries(); len(got) != 7 || strings.Contains(strings.Join(got, ","), "Bean Salad") {
		t.Errorf("searched %q, want the first seven ideas only", got)
	}

	// Each publish: the stage, then one more idea at a time, each idea
	// grounded when it appears.
	var stages []string
	var counts []int
	for _, p := range published {
		stages = append(stages, p.Stage)
		counts = append(counts, len(p.Ideas))
		for _, i := range p.Ideas {
			if i.RecipeID == "" || i.PhotoURL == "" || i.Method == "" {
				t.Errorf("published idea %q is not grounded: %+v", i.Name, i)
			}
		}
	}
	wantStages := []string{StageSuggesting, StageGrounding, StageGrounding, StageGrounding, StageGrounding, StageGrounding, StageGrounding}
	if !reflect.DeepEqual(stages, wantStages) || !reflect.DeepEqual(counts, []int{0, 0, 1, 2, 3, 4, 5}) {
		t.Fatalf("published stages %q with %v ideas, want %q with 0, 0, 1, 2, 3, 4, 5", stages, counts, wantStages)
	}
	// Ideas ground concurrently, so one may appear before an earlier one,
	// but each publish keeps every idea already shown, in the model's
	// order.
	for i := 3; i < len(published); i++ {
		prev, cur := names(published[i-1].Ideas), names(published[i].Ideas)
		if !within(prev, cur) || !within(cur, want) {
			t.Errorf("publish %d shows %q after %q, want earlier ideas kept in the model's order %q", i+1, cur, prev, want)
		}
	}

	first := res.Ideas[0]
	if first.Summary != "Uses 2 of 5 · missing: 1 lime" || !reflect.DeepEqual(first.Uses, []string{"rice", "black beans"}) {
		t.Errorf("first idea = %+v", first)
	}
}

func TestQuickOnlyChecksTheGroundedRecipe(t *testing.T) {
	for _, quickOnly := range []bool{true, false} {
		t.Run(fmt.Sprintf("quick_only=%v", quickOnly), func(t *testing.T) {
			e := newEnv(t)
			// The model estimates every idea quick; the grounded recipes
			// decide.
			e.recipe(t, "Fried Rice", "Fried Rice", 25)
			e.recipe(t, "Chicken Chili", "Slow Cooker Chicken Chili", 240)
			e.recipe(t, "Bean Enchiladas", "Bean Enchiladas", 55)
			e.recipe(t, "Rice Bowls", "Rice Bowls", 30)
			e.recipe(t, "Bean Bake", "Bean Bake", 31)
			e.reply(
				idea("Fried Rice", "stovetop", 20, "rice"),
				idea("Chicken Chili", "stovetop", 25, "Chicken thighs", "black beans"),
				idea("Bean Enchiladas", "oven", 25, "black beans"),
				idea("Rice Bowls", "stovetop", 15, "rice"),
				idea("Bean Bake", "oven", 30, "black beans"),
			)
			res, _, err := e.run(t, pantry, quickOnly)
			if err != nil {
				t.Fatal(err)
			}
			quick := map[string]bool{}
			for _, i := range res.Ideas {
				quick[i.Name] = i.Quick
			}
			want := map[string]bool{"Fried Rice": true, "Chicken Chili": true, "Rice Bowls": true}
			wantDropped := 2
			if !quickOnly {
				want["Bean Enchiladas"], want["Bean Bake"] = false, false
				wantDropped = 0
			}
			if !reflect.DeepEqual(quick, want) || res.Dropped != wantDropped {
				t.Fatalf("ideas quick = %v dropped %d, want %v dropped %d", quick, res.Dropped, want, wantDropped)
			}
			for _, i := range res.Ideas {
				if i.Name == "Chicken Chili" && (i.Method != "crockpot" || i.MinutesTotal == nil || *i.MinutesTotal != 240) {
					t.Errorf("Chicken Chili = %s %v, want the grounded crockpot 240", i.Method, i.MinutesTotal)
				}
			}
		})
	}
}

func TestQuickOnlyGroundsMoreToReplaceSlowIdeas(t *testing.T) {
	e := newEnv(t)
	slow := []string{"Roast One", "Roast Two", "Roast Three", "Roast Four", "Roast Five"}
	var reply []string
	for _, name := range slow {
		e.recipe(t, name, name, 90)
		reply = append(reply, idea(name, "oven", 20, "Chicken thighs"))
	}
	e.recipe(t, "Fried Rice", "Fried Rice", 20)
	reply = append(reply, idea("Fried Rice", "stovetop", 20, "rice"))
	e.reply(reply...)
	res, _, err := e.run(t, pantry, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := names(res.Ideas); !reflect.DeepEqual(got, []string{"Fried Rice"}) || res.Dropped != 5 {
		t.Fatalf("ideas = %q dropped %d, want Fried Rice with 5 dropped", got, res.Dropped)
	}
}

func TestHallucinatedUsesNeverReachTheResult(t *testing.T) {
	e := newEnv(t)
	e.recipe(t, "Burrito Bowls", "Burrito Bowls", 35)
	e.reply(
		idea("Burrito Bowls", "stovetop", 35, "rice", "sour cream", "black beans", "cheddar"),
		idea("Parmesan Pasta", "stovetop", 20, "pasta", "parmesan"),
	)
	res, _, err := e.run(t, pantry, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Ideas) != 1 || !reflect.DeepEqual(res.Ideas[0].Uses, []string{"rice", "black beans"}) {
		t.Fatalf("ideas = %+v, want Burrito Bowls using rice and black beans", res.Ideas)
	}
	if res.Ideas[0].Summary != "Uses 2 of 5 · missing: 1 lime" || res.Dropped != 1 {
		t.Fatalf("summary %q dropped %d", res.Ideas[0].Summary, res.Dropped)
	}
	if got := e.search.Queries(); !reflect.DeepEqual(got, []string{"Burrito Bowls"}) {
		t.Errorf("searched %q, want only the idea kept", got)
	}
}

func TestFailures(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(e *env)
		code  string
	}{
		{"ai fails", func(e *env) { e.fail = errors.New("provider down") }, ReasonAIFailed},
		{"bad output twice", func(e *env) { e.reply(); e.reply() }, ReasonAIFailed},
		{"search fails", func(e *env) {
			e.search.Err = errors.New("boom")
			e.reply(idea("Fried Rice", "stovetop", 20, "rice"))
		}, ReasonSearchFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			tc.setup(e)
			_, published, err := e.run(t, pantry, false)
			var failure *jobs.Failure
			if !errors.As(err, &failure) || failure.Code != tc.code {
				t.Fatalf("err = %v, want failure %s", err, tc.code)
			}
			for _, p := range published {
				if len(p.Ideas) != 0 {
					t.Fatalf("a failed job published ideas: %+v", p.Ideas)
				}
			}
		})
	}
}

func TestEachIdeaShowsAsSoonAsItGrounds(t *testing.T) {
	e := newEnv(t)
	for _, name := range []string{"Burrito Bowls", "Fried Rice", "Black Bean Soup"} {
		e.recipe(t, name, name, 35)
	}
	// The first idea's search waits, so the two after it ground first.
	gate := make(chan struct{})
	e.search.Gates = map[string]chan struct{}{"Burrito Bowls": gate}
	e.reply(
		idea("Burrito Bowls", "stovetop", 35, "rice", "black beans"),
		idea("Fried Rice", "stovetop", 20, "rice"),
		idea("Black Bean Soup", "stovetop", 40, "black beans"),
	)
	ings, err := skills.CleanIngredients(pantry)
	if err != nil {
		t.Fatal(err)
	}
	var rec recorder
	type outcome struct {
		res Result
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := e.svc.run(t.Context(), FromIngredientsJob{Ingredients: ings}, rec.publish)
		done <- outcome{res, err}
	}()

	deadline := time.Now().Add(10 * time.Second)
	for {
		if latest, ok := rec.latest(); ok && len(latest.Ideas) == 2 {
			if got := names(latest.Ideas); !reflect.DeepEqual(got, []string{"Fried Rice", "Black Bean Soup"}) {
				t.Fatalf("while the first idea grounds, ideas = %q, want the two grounded", got)
			}
			if latest.Stage != StageGrounding {
				t.Fatalf("stage = %s, want grounding", latest.Stage)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the ideas that grounded were not shown while the first idea was still grounding")
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(gate)
	out := <-done
	if out.err != nil {
		t.Fatal(out.err)
	}
	want := []string{"Burrito Bowls", "Fried Rice", "Black Bean Soup"}
	if got := names(out.res.Ideas); !reflect.DeepEqual(got, want) || out.res.Stage != StageDone {
		t.Fatalf("result = %s %q, want done with %q in the model's order", out.res.Stage, got, want)
	}
}

func TestNoIdeaKeptFailsTheJob(t *testing.T) {
	for _, tc := range []struct {
		name      string
		quickOnly bool
		setup     func(t *testing.T, e *env)
	}{
		{"no recipe grounds any", false, func(t *testing.T, e *env) {
			e.reply(idea("Mystery Stew", "stovetop", 30, "rice"), idea("Unknown Casserole", "oven", 50, "rice"))
		}},
		{"no grounded recipe is quick", true, func(t *testing.T, e *env) {
			e.recipe(t, "Roast Chicken", "Roast Chicken", 90)
			e.reply(idea("Roast Chicken", "oven", 25, "Chicken thighs"))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			tc.setup(t, e)
			_, published, err := e.run(t, pantry, tc.quickOnly)
			var failure *jobs.Failure
			if !errors.As(err, &failure) || failure.Code != ReasonNoIdeasFound {
				t.Fatalf("err = %v, want failure %s", err, ReasonNoIdeasFound)
			}
			for _, p := range published {
				if len(p.Ideas) != 0 || p.Stage == StageDone {
					t.Fatalf("a job that kept no idea published %+v", p)
				}
			}
		})
	}
}

func TestLaterRoundFailureClearsPublishedIdeas(t *testing.T) {
	e := newEnv(t)
	kept := []string{"Burrito Bowls", "Fried Rice", "Black Bean Soup", "Chicken Tacos"}
	for _, name := range kept {
		e.recipe(t, name, name, 35)
	}
	// The first round keeps four ideas; the second round's search fails.
	e.search.Fail = map[string]error{"Rice Pilaf": errors.New("boom")}
	e.reply(
		idea("Burrito Bowls", "stovetop", 35, "rice"),
		idea("Mystery Stew", "stovetop", 35, "rice"),
		idea("Fried Rice", "stovetop", 20, "rice"),
		idea("Black Bean Soup", "stovetop", 40, "black beans"),
		idea("Chicken Tacos", "stovetop", 25, "Chicken thighs"),
		idea("Rice Pilaf", "stovetop", 30, "rice"),
	)
	_, published, err := e.run(t, pantry, false)
	var failure *jobs.Failure
	if !errors.As(err, &failure) || failure.Code != ReasonSearchFailed {
		t.Fatalf("err = %v, want failure %s", err, ReasonSearchFailed)
	}
	most := 0
	for _, p := range published {
		most = max(most, len(p.Ideas))
	}
	if most != len(kept) {
		t.Fatalf("at most %d ideas published, want the first round's %d before the failure", most, len(kept))
	}
	if last := published[len(published)-1]; len(last.Ideas) != 0 {
		t.Fatalf("last publish before the failure shows %q, want no ideas", names(last.Ideas))
	}
}

func TestStartFromIngredientsRunsTheJob(t *testing.T) {
	e := newEnv(t)
	e.recipe(t, "Fried Rice", "Fried Rice", 20)
	e.reply(idea("Fried Rice", "stovetop", 20, "rice"))
	ctx, cancel := context.WithCancel(context.Background())
	if err := e.runner.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		e.runner.Wait()
	})

	if _, err := e.svc.StartFromIngredients(t.Context(), []string{"rice", "Rice"}, false); err == nil {
		t.Fatal("a repeated ingredient was accepted")
	} else if invalid := (*InvalidError)(nil); !errors.As(err, &invalid) {
		t.Fatalf("err = %v, want an *InvalidError", err)
	}

	started, err := e.svc.StartFromIngredients(t.Context(), []string{" rice ", "Chicken thighs"}, true)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	var job jobs.Job
	for {
		job, err = e.runner.Get(t.Context(), started.JobID)
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
	if job.Status != jobs.StatusSucceeded || job.Kind != KindFromIngredients {
		t.Fatalf("job = %+v", job)
	}
	var res Result
	if err := json.Unmarshal(job.Result, &res); err != nil {
		t.Fatal(err)
	}
	if res.Stage != StageDone || !reflect.DeepEqual(res.Ingredients, []string{"rice", "Chicken thighs"}) || !res.QuickOnly || len(res.Ideas) != 1 {
		t.Fatalf("result = %+v", res)
	}
	if got := res.Ideas[0]; got.Summary != "Uses 1 of 2 · missing: 1 lime" || !got.Quick {
		t.Errorf("idea = %+v", got)
	}
}

func TestIsQuick(t *testing.T) {
	m := func(n int) *int { return &n }
	for _, tc := range []struct {
		method  string
		minutes *int
		want    bool
	}{
		{"crockpot", m(480), true},
		{"crockpot", nil, true},
		{"stovetop", m(30), true},
		{"oven", m(31), false},
		{"stovetop", nil, false},
	} {
		if got := IsQuick(tc.method, tc.minutes); got != tc.want {
			t.Errorf("IsQuick(%s, %v) = %v, want %v", tc.method, tc.minutes, got, tc.want)
		}
	}
}
