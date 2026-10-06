package dinners_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"testing"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/llm"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/dinners"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/jobs"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/recipes"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/recipes/recipestest"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/migrations"
)

const member = "member-1"

// Fixture pages: two curry recipes from search and a stew at a pasted link.
const (
	curryA = "https://a.example/chicken-curry"
	curryB = "https://b.example/curry"
	stew   = "https://c.example/recipes/beef-stew"
)

type env struct {
	svc    *dinners.Service
	st     *store.Store
	runner *jobs.Runner
	search *recipestest.Search
	model  *llm.Fake
}

// newEnv returns a dinners service over a started job runner, grounding
// with fake search, pages, images and model. Searching "Chicken curry"
// finds curryA, curryB and a missing page.
func newEnv(t *testing.T) *env {
	t.Helper()
	dataDir := t.TempDir()
	st, err := store.Open(t.Context(), dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := store.Migrate(t.Context(), st.Writer, migrations.FS); err != nil {
		t.Fatal(err)
	}
	search := &recipestest.Search{Results: map[string][]recipes.Candidate{
		"Chicken curry": {{URL: curryA}, {URL: "https://missing.example/x"}, {URL: curryB}},
	}}
	pages := &recipestest.Pages{Bodies: map[string][]byte{
		curryA: recipestest.RecipePage("Chicken Curry", "Curry House", "https://a.example/curry.jpg", 40),
		curryB: recipestest.RecipePage("Easy Weeknight Curry", "B Kitchen", "https://b.example/curry.jpg", 25),
		stew:   recipestest.RecipePage("Beef Stew", "Stew Co", "https://c.example/stew.jpg", 150),
	}}
	images := &recipestest.Images{JPEGs: map[string][]byte{
		"https://a.example/curry.jpg": recipestest.JPEG(t, 400, 320, 1),
		"https://b.example/curry.jpg": recipestest.JPEG(t, 400, 320, 2),
		"https://c.example/stew.jpg":  recipestest.JPEG(t, 400, 320, 3),
	}}
	model := &llm.Fake{}
	g := recipestest.NewGrounder(t, st, dataDir, search, pages, images, model)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	runner, err := jobs.NewRunner(st, logger, 1)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := dinners.NewService(st, g, runner, logger)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := runner.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		runner.Wait()
	})
	return &env{svc: svc, st: st, runner: runner, search: search, model: model}
}

func ptr[T any](v T) *T { return &v }

// waitJob waits for job id to finish and returns it.
func (e *env) waitJob(t *testing.T, id string) jobs.Job {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		job, err := e.runner.Get(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		if job.Status == jobs.StatusSucceeded || job.Status == jobs.StatusFailed {
			return job
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s never finished; last %+v", id, job)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (e *env) dinner(t *testing.T, id string) db.Dinner {
	t.Helper()
	row, err := db.New(e.st.Reader).GetDinner(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func TestCreateGroundsInBackground(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.model.QueueGenerate(recipestest.NormalizedReply("stovetop", 40))

	c, err := e.svc.CreateRegular(t.Context(), dinners.NewRegular{Name: ptr("Chicken curry")}, member)
	if err != nil {
		t.Fatal(err)
	}
	if c.GroundingStatus != dinners.GroundingPending || c.GroundingJobID == "" {
		t.Fatalf("created = %+v, want pending with a job id", c)
	}
	job := e.waitJob(t, c.GroundingJobID)
	if job.Kind != dinners.KindGroundDinner || job.Status != jobs.StatusSucceeded ||
		string(job.Result) != `{"dinner_id":"`+c.ID+`","grounding_status":"grounded"}` {
		t.Fatalf("job = %+v (result %s)", job, job.Result)
	}

	row := e.dinner(t, c.ID)
	if row.GroundingStatus != "grounded" || !row.RecipeID.Valid || !row.PhotoID.Valid ||
		row.Method.String != "stovetop" || row.MinutesTotal.Int64 != 40 || row.Difficulty.String != "easy" || row.GroundingError.Valid {
		t.Fatalf("dinner = %+v, want grounded with recipe, photo, stovetop, 40 minutes, easy", row)
	}
	rec, err := db.New(e.st.Reader).GetRecipe(t.Context(), row.RecipeID.String)
	if err != nil {
		t.Fatal(err)
	}
	if rec.SourceUrl != curryA || rec.PhotoID != row.PhotoID {
		t.Errorf("recipe = %s photo %s, want %s photo %s", rec.SourceUrl, rec.PhotoID.String, curryA, row.PhotoID.String)
	}
	if got := e.search.Queries(); !slices.Equal(got, []string{"Chicken curry"}) {
		t.Errorf("searched %q, want the dinner's name", got)
	}
}

func TestCreateByRecipeURLGroundsThatURL(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.model.QueueGenerate(recipestest.NormalizedReply("oven", 150))

	c, err := e.svc.CreateRegular(t.Context(), dinners.NewRegular{RecipeURL: ptr(stew)}, member)
	if err != nil {
		t.Fatal(err)
	}
	if job := e.waitJob(t, c.GroundingJobID); job.Status != jobs.StatusSucceeded {
		t.Fatalf("job = %+v", job)
	}
	row := e.dinner(t, c.ID)
	rec, err := db.New(e.st.Reader).GetRecipe(t.Context(), row.RecipeID.String)
	if err != nil {
		t.Fatal(err)
	}
	if row.GroundingStatus != "grounded" || rec.SourceUrl != stew || row.Method.String != "oven" || row.MinutesTotal.Int64 != 150 {
		t.Fatalf("dinner = %+v recipe %s, want grounded in %s", row, rec.SourceUrl, stew)
	}
	if q := e.search.Queries(); len(q) != 0 {
		t.Errorf("searched %q; a recipe_url is grounded directly", q)
	}
}

func TestCreateGroundingFailureRecordsReason(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		create dinners.NewRegular
		setup  func(*env)
		reason string
	}{
		{"no candidates", dinners.NewRegular{Name: ptr("Unknown dish")}, nil, dinners.ReasonNoRecipe},
		{"unusable link", dinners.NewRegular{RecipeURL: ptr("https://gone.example/recipes/lost-soup")}, nil, dinners.ReasonRecipeUnusable},
		{"search fails", dinners.NewRegular{Name: ptr("Chicken curry")}, func(e *env) { e.search.Err = recipes.ErrSearchRateLimited }, dinners.ReasonSearchFailed},
		{"model fails", dinners.NewRegular{Name: ptr("Chicken curry")}, func(e *env) {
			// One provider failure per candidate tried.
			e.model.QueueGenerate(llm.GenerateResult{Err: llm.ErrAuth})
		}, dinners.ReasonAIFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			if tt.setup != nil {
				tt.setup(e)
			}
			c, err := e.svc.CreateRegular(t.Context(), tt.create, member)
			if err != nil {
				t.Fatal(err)
			}
			job := e.waitJob(t, c.GroundingJobID)
			if job.Status != jobs.StatusFailed || job.Error == nil || *job.Error != tt.reason {
				t.Errorf("job = %+v, want failed with %s", job, tt.reason)
			}
			row := e.dinner(t, c.ID)
			if row.GroundingStatus != "failed" || row.GroundingError.String != tt.reason || row.RecipeID.Valid {
				t.Errorf("dinner = %+v, want failed with %s", row, tt.reason)
			}
		})
	}
}

func TestCreateReturnsBeforeGroundingCompletes(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.search.Gate = make(chan struct{})
	e.model.QueueGenerate(recipestest.NormalizedReply("stovetop", 40))

	c, err := e.svc.CreateRegular(t.Context(), dinners.NewRegular{Name: ptr("Chicken curry")}, member)
	if err != nil {
		t.Fatal(err)
	}
	// The search is held, so the job cannot have finished.
	if row := e.dinner(t, c.ID); row.GroundingStatus != "pending" {
		t.Fatalf("grounding_status = %s right after create, want pending", row.GroundingStatus)
	}
	if job, err := e.runner.Get(t.Context(), c.GroundingJobID); err != nil || (job.Status != jobs.StatusQueued && job.Status != jobs.StatusRunning) {
		t.Fatalf("job = %+v, %v; want queued or running", job, err)
	}
	close(e.search.Gate)
	if job := e.waitJob(t, c.GroundingJobID); job.Status != jobs.StatusSucceeded {
		t.Fatalf("job = %+v", job)
	}
	if row := e.dinner(t, c.ID); row.GroundingStatus != "grounded" {
		t.Errorf("grounding_status = %s, want grounded", row.GroundingStatus)
	}
}

func TestManualFixWhilePendingIsKept(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.search.Gate = make(chan struct{})
	e.model.QueueGenerate(recipestest.NormalizedReply("stovetop", 40))

	c, err := e.svc.CreateRegular(t.Context(), dinners.NewRegular{Name: ptr("Chicken curry")}, member)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Ground(t.Context(), c.ID, dinners.Fix{Manual: &dinners.ManualRecipe{
		Ingredients: []string{"1 chicken"}, Minutes: ptr(30), Method: ptr("oven"),
	}}, member); err != nil {
		t.Fatal(err)
	}
	close(e.search.Gate)
	job := e.waitJob(t, c.GroundingJobID)
	if job.Status != jobs.StatusSucceeded || string(job.Result) != `{"dinner_id":"`+c.ID+`","grounding_status":"manual"}` {
		t.Errorf("job = %+v (result %s), want succeeded reporting manual", job, job.Result)
	}
	if row := e.dinner(t, c.ID); row.GroundingStatus != "manual" || row.Method.String != "oven" {
		t.Errorf("dinner = %+v, want the manual grounding kept", row)
	}
}

// failedDinner creates a Regular whose background grounding fails.
func failedDinner(t *testing.T, e *env, name string) string {
	t.Helper()
	c, err := e.svc.CreateRegular(t.Context(), dinners.NewRegular{Name: ptr(name)}, member)
	if err != nil {
		t.Fatal(err)
	}
	if job := e.waitJob(t, c.GroundingJobID); job.Status != jobs.StatusFailed {
		t.Fatalf("job = %+v, want failed", job)
	}
	return c.ID
}

func TestCandidates(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	// The background grounding stores curryA with its cached photo.
	e.model.QueueGenerate(recipestest.NormalizedReply("stovetop", 40))
	c, err := e.svc.CreateRegular(t.Context(), dinners.NewRegular{Name: ptr("Chicken curry")}, member)
	if err != nil {
		t.Fatal(err)
	}
	if job := e.waitJob(t, c.GroundingJobID); job.Status != jobs.StatusSucceeded {
		t.Fatalf("job = %+v", job)
	}

	got, err := e.svc.Candidates(t.Context(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("candidates = %+v, want the two usable pages", got)
	}
	first, second := got[0], got[1]
	wantPhoto := dinners.PhotoURL(e.dinner(t, c.ID).PhotoID.String)
	if first.CandidateID != curryA || first.Title != "Chicken Curry" || first.SiteName != "Curry House" ||
		first.PhotoURL == nil || *first.PhotoURL != wantPhoto || first.Minutes == nil || *first.Minutes != 40 {
		t.Errorf("first = %+v, want the stored curryA with photo %s", first, wantPhoto)
	}
	if second.CandidateID != curryB || second.Title != "Easy Weeknight Curry" || second.SiteName != "B Kitchen" ||
		second.PhotoURL != nil || second.Minutes == nil || *second.Minutes != 25 {
		t.Errorf("second = %+v, want curryB without a photo", second)
	}

	if _, err := e.svc.Candidates(t.Context(), "nope"); !errors.Is(err, dinners.ErrNotFound) {
		t.Errorf("unknown dinner: err = %v, want ErrNotFound", err)
	}
}

func TestCandidatesSearchFails(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.search.Err = recipes.ErrSearchRefused
	id := failedDinner(t, e, "Chicken curry")
	if _, err := e.svc.Candidates(t.Context(), id); !errors.Is(err, recipes.ErrSearchFailed) {
		t.Errorf("err = %v, want ErrSearchFailed", err)
	}
}

func TestCandidatesNone(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	id := failedDinner(t, e, "Fish tacos")
	got, err := e.svc.Candidates(t.Context(), id)
	if err != nil || len(got) != 0 {
		t.Fatalf("no search results: candidates = %+v, %v; want none", got, err)
	}
}

func TestFixViaCandidate(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	id := failedDinner(t, e, "Mystery curry")
	e.model.QueueGenerate(recipestest.NormalizedReply("stovetop", 25))

	d, err := e.svc.Ground(t.Context(), id, dinners.Fix{CandidateID: ptr(curryB)}, "member-2")
	if err != nil {
		t.Fatal(err)
	}
	if d.GroundingStatus != "grounded" || d.GroundingError != nil || d.RecipeID == nil || d.PhotoURL == nil ||
		*d.Method != "stovetop" || *d.MinutesTotal != 25 || *d.Difficulty != "easy" || *d.UpdatedBy != "member-2" {
		t.Fatalf("dinner = %+v", d)
	}
	rec, err := db.New(e.st.Reader).GetRecipe(t.Context(), *d.RecipeID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.SourceUrl != curryB {
		t.Errorf("recipe source = %s, want %s", rec.SourceUrl, curryB)
	}
}

func TestFixViaRecipeURL(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	id := failedDinner(t, e, "Grandma's stew")
	e.model.QueueGenerate(recipestest.NormalizedReply("oven", 150))

	d, err := e.svc.Ground(t.Context(), id, dinners.Fix{RecipeURL: ptr(" " + stew + " ")}, member)
	if err != nil {
		t.Fatal(err)
	}
	if d.GroundingStatus != "grounded" || d.PhotoURL == nil || *d.Method != "oven" || *d.MinutesTotal != 150 {
		t.Fatalf("dinner = %+v", d)
	}

	// A link holding no recipe changes nothing.
	before := e.dinner(t, id)
	_, err = e.svc.Ground(t.Context(), id, dinners.Fix{RecipeURL: ptr("https://gone.example/x")}, member)
	var ungroundable *recipes.ErrUngroundable
	if !errors.As(err, &ungroundable) {
		t.Fatalf("err = %v, want *recipes.ErrUngroundable", err)
	}
	if after := e.dinner(t, id); after != before {
		t.Errorf("dinner changed by a failed fix: %+v, was %+v", after, before)
	}
}

func TestFixViaManual(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	id := failedDinner(t, e, "Family chili")
	if _, err := e.svc.Update(t.Context(), id, dinners.Patch{Difficulty: ptr("medium")}, member); err != nil {
		t.Fatal(err)
	}

	d, err := e.svc.Ground(t.Context(), id, dinners.Fix{Manual: &dinners.ManualRecipe{
		Ingredients: []string{" 1 lb beef ", "1 can beans"},
		Steps:       []string{"Brown the beef.", "Simmer with the beans."},
		Minutes:     ptr(240),
		Method:      ptr("crockpot"),
	}}, member)
	if err != nil {
		t.Fatal(err)
	}
	if d.GroundingStatus != "manual" || d.GroundingError != nil || d.RecipeID == nil || d.PhotoURL != nil ||
		*d.Method != "crockpot" || *d.MinutesTotal != 240 || *d.Difficulty != "medium" {
		t.Fatalf("dinner = %+v", d)
	}
	q := db.New(e.st.Reader)
	rec, err := q.GetRecipe(t.Context(), *d.RecipeID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Title != "Family chili" || rec.SourceUrl != "mealquest:manual:"+rec.ID || rec.MinutesTotal.Int64 != 240 || rec.PhotoID.Valid {
		t.Errorf("recipe = %+v", rec)
	}
	ings, err := q.ListRecipeIngredients(t.Context(), rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	steps, err := q.ListRecipeSteps(t.Context(), rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ings) != 2 || ings[0].RawText != "1 lb beef" || len(steps) != 2 || steps[1].Text != "Simmer with the beans." {
		t.Errorf("ingredients = %+v, steps = %+v", ings, steps)
	}
}

func TestFixValidation(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	id := failedDinner(t, e, "Pad thai")
	good := func() *dinners.ManualRecipe {
		return &dinners.ManualRecipe{Ingredients: []string{"noodles"}, Minutes: ptr(20), Method: ptr("stovetop")}
	}
	tests := map[string]dinners.Fix{
		"nothing":           {},
		"two":               {CandidateID: ptr(curryA), RecipeURL: ptr(stew)},
		"candidate not URL": {CandidateID: ptr("cand-1")},
		"http link":         {RecipeURL: ptr("http://a.example/x")},
		"no ingredients":    {Manual: &dinners.ManualRecipe{Minutes: ptr(20), Method: ptr("oven")}},
		"blank ingredient":  {Manual: &dinners.ManualRecipe{Ingredients: []string{" "}, Minutes: ptr(20), Method: ptr("oven")}},
		"blank step":        {Manual: func() *dinners.ManualRecipe { m := good(); m.Steps = []string{""}; return m }()},
		"no minutes":        {Manual: func() *dinners.ManualRecipe { m := good(); m.Minutes = nil; return m }()},
		"zero minutes":      {Manual: func() *dinners.ManualRecipe { m := good(); m.Minutes = ptr(0); return m }()},
		"unknown method":    {Manual: func() *dinners.ManualRecipe { m := good(); m.Method = ptr("microwave"); return m }()},
		"no method":         {Manual: func() *dinners.ManualRecipe { m := good(); m.Method = nil; return m }()},
		"too many lines":    {Manual: func() *dinners.ManualRecipe { m := good(); m.Ingredients = make([]string, 101); return m }()},
		"manual and a link": {RecipeURL: ptr(stew), Manual: good()},
		"over-long ing line": {Manual: func() *dinners.ManualRecipe {
			m := good()
			m.Ingredients = []string{string(make([]byte, 501))}
			return m
		}()},
	}
	before := e.dinner(t, id)
	for name, fix := range tests {
		_, err := e.svc.Ground(t.Context(), id, fix, member)
		var invalid *dinners.ValidationError
		if !errors.As(err, &invalid) {
			t.Errorf("%s: err = %v, want *ValidationError", name, err)
		}
	}
	if after := e.dinner(t, id); after != before {
		t.Errorf("dinner changed by invalid fixes: %+v", after)
	}
	if _, err := e.svc.Ground(t.Context(), "nope", dinners.Fix{Manual: good()}, member); !errors.Is(err, dinners.ErrNotFound) {
		t.Errorf("unknown dinner, manual: err = %v, want ErrNotFound", err)
	}
	if _, err := e.svc.Ground(t.Context(), "nope", dinners.Fix{RecipeURL: ptr(stew)}, member); !errors.Is(err, dinners.ErrNotFound) {
		t.Errorf("unknown dinner, link: err = %v, want ErrNotFound", err)
	}
}
