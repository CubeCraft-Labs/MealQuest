package dinners_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
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
	pages  *recipestest.Pages
	model  *llm.Fake
}

// newEnv returns a dinners service over a started job runner, grounding
// with fake search, pages, images and model. Searching "Chicken curry"
// finds curryA, curryB and a missing page.
func newEnv(t *testing.T) *env {
	t.Helper()
	e := newUnstartedEnv(t)
	e.start(t)
	return e
}

// newUnstartedEnv is newEnv with its job runner not yet started, so jobs
// enqueued stay queued until start.
func newUnstartedEnv(t *testing.T) *env {
	t.Helper()
	dataDir := t.TempDir()
	return newUnstartedEnvOn(t, openStore(t, dataDir), dataDir)
}

// openStore opens the store in dataDir, closing it when the test ends. It
// is not migrated.
func openStore(t *testing.T, dataDir string) *store.Store {
	t.Helper()
	st, err := store.Open(t.Context(), dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	})
	return st
}

// newUnstartedEnvOn is newUnstartedEnv over st, the store in dataDir,
// which it migrates to the latest version.
func newUnstartedEnvOn(t *testing.T, st *store.Store, dataDir string) *env {
	t.Helper()
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
	return &env{svc: svc, st: st, runner: runner, search: search, pages: pages, model: model}
}

// start starts the env's job runner, stopping it when the test ends.
func (e *env) start(t *testing.T) {
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
	if c.GroundingStatus != dinners.GroundingPending || c.GroundingJobID == nil {
		t.Fatalf("created = %+v, want pending with a job id", c)
	}
	job := e.waitJob(t, *c.GroundingJobID)
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

	c, err := e.svc.CreateRegular(t.Context(), dinners.NewRegular{Name: ptr("Beef stew"), RecipeURL: ptr(stew)}, member)
	if err != nil {
		t.Fatal(err)
	}
	if job := e.waitJob(t, *c.GroundingJobID); job.Status != jobs.StatusSucceeded {
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

// TestCreateByRecipeURLClearsLink: once background grounding resolves a
// pasted link, which may carry a personal token, the dinner no longer
// stores it; the recipe it grounded in records the page.
func TestCreateByRecipeURLClearsLink(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	const link = "https://c.example/recipes/beef-stew?access_token=personal-token-abc123"
	e.pages.Bodies[link] = recipestest.RecipePage("Beef Stew", "Stew Co", "https://c.example/stew.jpg", 150)
	e.model.QueueGenerate(recipestest.NormalizedReply("oven", 150))

	c, err := e.svc.CreateRegular(t.Context(), dinners.NewRegular{Name: ptr("Beef stew"), RecipeURL: ptr(link)}, member)
	if err != nil {
		t.Fatal(err)
	}
	if job := e.waitJob(t, *c.GroundingJobID); job.Status != jobs.StatusSucceeded {
		t.Fatalf("job = %+v", job)
	}
	row := e.dinner(t, c.ID)
	if row.GroundingStatus != "grounded" || !row.RecipeID.Valid {
		t.Fatalf("dinner = %+v, want grounded", row)
	}
	if row.RecipeUrl.Valid {
		t.Errorf("recipe_url = %q after grounding, want cleared", row.RecipeUrl.String)
	}
	rec, err := db.New(e.st.Reader).GetRecipe(t.Context(), row.RecipeID.String)
	if err != nil {
		t.Fatal(err)
	}
	if rec.SourceUrl != link {
		t.Errorf("recipe source = %q, want %q", rec.SourceUrl, link)
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
		{"unusable link", dinners.NewRegular{Name: ptr("Lost soup"), RecipeURL: ptr("https://gone.example/recipes/lost-soup")}, nil, dinners.ReasonRecipeUnusable},
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
			job := e.waitJob(t, *c.GroundingJobID)
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
	if job, err := e.runner.Get(t.Context(), *c.GroundingJobID); err != nil || (job.Status != jobs.StatusQueued && job.Status != jobs.StatusRunning) {
		t.Fatalf("job = %+v, %v; want queued or running", job, err)
	}
	close(e.search.Gate)
	if job := e.waitJob(t, *c.GroundingJobID); job.Status != jobs.StatusSucceeded {
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
	job := e.waitJob(t, *c.GroundingJobID)
	if job.Status != jobs.StatusSucceeded || string(job.Result) != `{"dinner_id":"`+c.ID+`","grounding_status":"manual"}` {
		t.Errorf("job = %+v (result %s), want succeeded reporting manual", job, job.Result)
	}
	if row := e.dinner(t, c.ID); row.GroundingStatus != "manual" || row.Method.String != "oven" {
		t.Errorf("dinner = %+v, want the manual grounding kept", row)
	}
}

// TestRenameWhilePendingGroundsNewName: a Regular renamed while its job is
// still grounding the old name never gets the old name's result, whether
// the old name would be found or not: the rename stops that job where it
// is, and the job the rename enqueues grounds the new name.
func TestRenameWhilePendingGroundsNewName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		oldName string
	}{
		{"old name found", "Chicken curry"},
		{"old name not found", "Unknown dish"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			e.search.Results["Beef stew"] = []recipes.Candidate{{URL: stew}}
			e.search.Gate = make(chan struct{})
			e.model.QueueGenerate(recipestest.NormalizedReply("oven", 150))

			c, err := e.svc.CreateRegular(t.Context(), dinners.NewRegular{Name: ptr(tt.oldName)}, member)
			if err != nil {
				t.Fatal(err)
			}
			// Rename only once the job has read the old name and is searching it.
			e.waitQuery(t, tt.oldName)
			d, err := e.svc.Update(t.Context(), c.ID, dinners.Patch{Name: ptr("Beef stew")}, member)
			if err != nil {
				t.Fatal(err)
			}
			if d.GroundingStatus != dinners.GroundingPending {
				t.Fatalf("renamed = %+v, want still pending", d)
			}
			// The old name's job ends while its search is still held.
			first := e.waitJob(t, *c.GroundingJobID)
			if first.Status != jobs.StatusSucceeded || string(first.Result) != `{"dinner_id":"`+c.ID+`","grounding_status":"pending"}` {
				t.Fatalf("old name's job = %+v (result %s), want succeeded leaving the dinner pending", first, first.Result)
			}
			close(e.search.Gate)

			row := e.waitGrounding(t, c.ID)
			if row.GroundingStatus != dinners.GroundingGrounded {
				t.Fatalf("dinner = %+v, want grounded for the new name", row)
			}
			rec, err := db.New(e.st.Reader).GetRecipe(t.Context(), row.RecipeID.String)
			if err != nil {
				t.Fatal(err)
			}
			if rec.SourceUrl != stew || rec.PhotoID != row.PhotoID || row.Method.String != "oven" || row.MinutesTotal.Int64 != 150 {
				t.Errorf("dinner = %+v recipe %s, want grounded in %s", row, rec.SourceUrl, stew)
			}
			if got := e.search.Queries(); !slices.Equal(got, []string{tt.oldName, "Beef stew"}) {
				t.Errorf("searched %q, want the old name then the new", got)
			}
			if got := len(e.model.Requests()); got != 1 {
				t.Errorf("model called %d times, want once, for the new name only", got)
			}
		})
	}
}

// TestRenameLinkWhilePendingGroundsNewName: a Regular added from a link and
// renamed while still pending drops the link and grounds its new name, not
// the old link's recipe and photo.
func TestRenameLinkWhilePendingGroundsNewName(t *testing.T) {
	t.Parallel()
	e := newUnstartedEnv(t)
	e.search.Results["Beef stew"] = []recipes.Candidate{{URL: stew}}
	e.model.QueueGenerate(recipestest.NormalizedReply("oven", 150))

	c, err := e.svc.CreateRegular(t.Context(), dinners.NewRegular{Name: ptr("Chicken curry"), RecipeURL: ptr(curryA)}, member)
	if err != nil {
		t.Fatal(err)
	}
	d, err := e.svc.Update(t.Context(), c.ID, dinners.Patch{Name: ptr("Beef stew")}, member)
	if err != nil {
		t.Fatal(err)
	}
	if d.GroundingStatus != dinners.GroundingPending {
		t.Fatalf("renamed = %+v, want still pending", d)
	}
	if row, err := db.New(e.st.Reader).GetDinner(t.Context(), c.ID); err != nil || row.RecipeUrl.Valid {
		t.Fatalf("renamed row = %+v (err %v), want no recipe_url", row, err)
	}
	e.start(t)

	row := e.waitGrounding(t, c.ID)
	if row.GroundingStatus != dinners.GroundingGrounded {
		t.Fatalf("dinner = %+v, want grounded for the new name", row)
	}
	rec, err := db.New(e.st.Reader).GetRecipe(t.Context(), row.RecipeID.String)
	if err != nil {
		t.Fatal(err)
	}
	if rec.SourceUrl != stew || rec.PhotoID != row.PhotoID || row.Method.String != "oven" || row.MinutesTotal.Int64 != 150 {
		t.Errorf("dinner = %+v recipe %s, want grounded in %s, not the old link %s", row, rec.SourceUrl, stew, curryA)
	}
	if got := e.search.Queries(); !slices.Equal(got, []string{"Beef stew"}) {
		t.Errorf("searched %q, want only the new name", got)
	}
	if row.RecipeUrl.Valid {
		t.Errorf("recipe_url = %q, want the old link dropped", row.RecipeUrl.String)
	}
	if _, err := db.New(e.st.Reader).GetRecipeBySourceURL(t.Context(), curryA); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("old link's recipe lookup err = %v, want it never grounded (sql.ErrNoRows)", err)
	}
}

// queuedJob reads the payload of queued KindGroundDinner job id.
func (e *env) queuedJob(t *testing.T, id string) dinners.GroundJob {
	t.Helper()
	var payload string
	if err := e.st.Reader.QueryRowContext(t.Context(), `SELECT payload FROM jobs WHERE id = ?`, id).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var p dinners.GroundJob
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestRenameBeforeJobStartsSkipsStaleJob: a Regular renamed before its
// first job starts is grounded only by the job its rename enqueued. The
// first job, though it would read the new name, does no work and writes
// nothing, so its failure cannot leave the dinner "Needs a recipe" while
// the rename's job finds a recipe.
func TestRenameBeforeJobStartsSkipsStaleJob(t *testing.T) {
	t.Parallel()
	e := newUnstartedEnv(t)
	e.search.Results["Beef stew"] = []recipes.Candidate{{URL: stew}}

	c, err := e.svc.CreateRegular(t.Context(), dinners.NewRegular{Name: ptr("Chicken curry")}, member)
	if err != nil {
		t.Fatal(err)
	}
	first := e.queuedJob(t, *c.GroundingJobID)
	if first.DinnerID != c.ID || first.Revision != e.dinner(t, c.ID).GroundingRevision {
		t.Fatalf("first job = %+v, want dinner %s at its revision", first, c.ID)
	}
	if _, err := e.svc.Update(t.Context(), c.ID, dinners.Patch{Name: ptr("Beef stew")}, member); err != nil {
		t.Fatal(err)
	}
	renamed := e.dinner(t, c.ID)
	if renamed.GroundingRevision != first.Revision+1 {
		t.Fatalf("revision after rename = %d, want %d", renamed.GroundingRevision, first.Revision+1)
	}

	// The first job would fail if it searched.
	e.search.Err = recipes.ErrSearchRateLimited
	status, err := e.svc.RunGroundJob(t.Context(), first)
	if err != nil || status != dinners.GroundingPending {
		t.Fatalf("first job = %q, %v; want pending, no error", status, err)
	}
	if q := e.search.Queries(); len(q) != 0 {
		t.Errorf("first job searched %q, want no work for a stale revision", q)
	}
	if row := e.dinner(t, c.ID); row != renamed {
		t.Fatalf("first job wrote the dinner: %+v, was %+v", row, renamed)
	}

	e.search.Err = nil
	e.model.QueueGenerate(recipestest.NormalizedReply("oven", 150))
	status, err = e.svc.RunGroundJob(t.Context(), dinners.GroundJob{DinnerID: c.ID, Revision: renamed.GroundingRevision})
	if err != nil || status != dinners.GroundingGrounded {
		t.Fatalf("rename's job = %q, %v; want grounded", status, err)
	}
	row := e.dinner(t, c.ID)
	rec, err := db.New(e.st.Reader).GetRecipe(t.Context(), row.RecipeID.String)
	if err != nil {
		t.Fatal(err)
	}
	if row.GroundingStatus != dinners.GroundingGrounded || rec.SourceUrl != stew {
		t.Errorf("dinner = %+v recipe %s, want grounded in %s", row, rec.SourceUrl, stew)
	}

	// A stale job finishing last changes nothing either.
	if status, err := e.svc.RunGroundJob(t.Context(), first); err != nil || status != dinners.GroundingGrounded {
		t.Errorf("first job run last = %q, %v; want grounded reported, no error", status, err)
	}
	if after := e.dinner(t, c.ID); after != row {
		t.Errorf("stale job changed the grounded dinner: %+v, was %+v", after, row)
	}
}

// TestEditWhilePendingIsKept: a method or minutes a member saves while a
// Regular is grounding is kept when grounding succeeds; what the member
// did not set comes from the recipe.
func TestEditWhilePendingIsKept(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.search.Gate = make(chan struct{})
	e.model.QueueGenerate(recipestest.NormalizedReply("stovetop", 40))

	c, err := e.svc.CreateRegular(t.Context(), dinners.NewRegular{Name: ptr("Chicken curry")}, member)
	if err != nil {
		t.Fatal(err)
	}
	e.waitQuery(t, "Chicken curry")
	if _, err := e.svc.Update(t.Context(), c.ID, dinners.Patch{Method: ptr("grill"), MinutesTotal: ptr(15)}, member); err != nil {
		t.Fatal(err)
	}
	close(e.search.Gate)

	row := e.waitGrounding(t, c.ID)
	if row.GroundingStatus != dinners.GroundingGrounded || !row.RecipeID.Valid || !row.PhotoID.Valid {
		t.Fatalf("dinner = %+v, want grounded", row)
	}
	if row.Method.String != "grill" || row.MinutesTotal.Int64 != 15 || row.Difficulty.String != "easy" {
		t.Errorf("dinner = %s, %d minutes, %s; want the member's grill and 15 minutes, the recipe's easy",
			row.Method.String, row.MinutesTotal.Int64, row.Difficulty.String)
	}
}

// TestArchiveWhilePendingStopsGrounding: a pending Regular archived before
// its job starts is not searched for, and one archived while its job runs
// is stopped there, with no page fetched or model called; neither ends up
// grounded.
func TestArchiveWhilePendingStopsGrounding(t *testing.T) {
	t.Parallel()
	t.Run("before the job starts", func(t *testing.T) {
		t.Parallel()
		e := newUnstartedEnv(t)
		e.model.QueueGenerate(recipestest.NormalizedReply("stovetop", 40))
		c, err := e.svc.CreateRegular(t.Context(), dinners.NewRegular{Name: ptr("Chicken curry")}, member)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.svc.Archive(t.Context(), c.ID, member); err != nil {
			t.Fatal(err)
		}
		e.start(t)
		if job := e.waitJob(t, *c.GroundingJobID); job.Status != jobs.StatusSucceeded {
			t.Fatalf("job = %+v, want succeeded with nothing to do", job)
		}
		if q := e.search.Queries(); len(q) != 0 {
			t.Errorf("searched %q for an archived dinner", q)
		}
		if row := e.dinner(t, c.ID); row.Status != dinners.StatusArchived || row.GroundingStatus == dinners.GroundingGrounded || row.RecipeID.Valid {
			t.Errorf("dinner = %+v, want archived and not grounded", row)
		}
	})
	t.Run("while the job runs", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t)
		e.search.Gate = make(chan struct{})
		e.model.QueueGenerate(recipestest.NormalizedReply("stovetop", 40))
		c, err := e.svc.CreateRegular(t.Context(), dinners.NewRegular{Name: ptr("Chicken curry")}, member)
		if err != nil {
			t.Fatal(err)
		}
		e.waitQuery(t, "Chicken curry")
		if _, err := e.svc.Archive(t.Context(), c.ID, member); err != nil {
			t.Fatal(err)
		}
		// The job ends while its search is still held: archiving stopped it.
		job := e.waitJob(t, *c.GroundingJobID)
		close(e.search.Gate)
		if job.Status != jobs.StatusSucceeded || string(job.Result) != `{"dinner_id":"`+c.ID+`","grounding_status":"pending"}` {
			t.Fatalf("job = %+v (result %s), want succeeded leaving the dinner pending", job, job.Result)
		}
		if got := e.pages.Fetched(); len(got) != 0 {
			t.Errorf("fetched %q for an archived dinner", got)
		}
		if got := len(e.model.Requests()); got != 0 {
			t.Errorf("model called %d times for an archived dinner", got)
		}
		if row := e.dinner(t, c.ID); row.Status != dinners.StatusArchived || row.GroundingStatus == dinners.GroundingGrounded || row.RecipeID.Valid {
			t.Errorf("dinner = %+v, want archived and not grounded", row)
		}
	})
}

// TestRenameWhilePendingEnqueueFailure: a pending Regular renamed when the
// job for its new name cannot be enqueued is failed, not left pending.
func TestRenameWhilePendingEnqueueFailure(t *testing.T) {
	t.Parallel()
	e := newUnstartedEnv(t)
	c, err := e.svc.CreateRegular(t.Context(), dinners.NewRegular{Name: ptr("Chicken curry")}, member)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.Writer.ExecContext(t.Context(),
		`CREATE TRIGGER refuse_jobs BEFORE INSERT ON jobs BEGIN SELECT RAISE(ABORT, 'no jobs'); END`); err != nil {
		t.Fatal(err)
	}
	d, err := e.svc.Update(t.Context(), c.ID, dinners.Patch{Name: ptr("Beef stew")}, member)
	if err != nil {
		t.Fatal(err)
	}
	if d.Name != "Beef stew" || d.GroundingStatus != dinners.GroundingFailed ||
		d.GroundingError == nil || *d.GroundingError != dinners.ReasonGroundingError {
		t.Fatalf("renamed = %+v, want Beef stew failed (grounding_error)", d)
	}
}

// waitQuery waits until the fake search has been asked query.
func (e *env) waitQuery(t *testing.T, query string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !slices.Contains(e.search.Queries(), query) {
		if time.Now().After(deadline) {
			t.Fatalf("search never asked %q; asked %q", query, e.search.Queries())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// waitGrounding waits for dinner id to be no longer pending and returns it.
func (e *env) waitGrounding(t *testing.T, id string) db.Dinner {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		row := e.dinner(t, id)
		if row.GroundingStatus != dinners.GroundingPending {
			return row
		}
		if time.Now().After(deadline) {
			t.Fatalf("dinner %s still pending", id)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// failedDinner creates a Regular whose background grounding fails.
func failedDinner(t *testing.T, e *env, name string) string {
	t.Helper()
	c, err := e.svc.CreateRegular(t.Context(), dinners.NewRegular{Name: ptr(name)}, member)
	if err != nil {
		t.Fatal(err)
	}
	if job := e.waitJob(t, *c.GroundingJobID); job.Status != jobs.StatusFailed {
		t.Fatalf("job = %+v, want failed", job)
	}
	return c.ID
}

// candidateID returns the candidate_id Candidates offers dinner id for the
// page titled title.
func candidateID(t *testing.T, e *env, id, title string) string {
	t.Helper()
	got, err := e.svc.Candidates(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range got {
		if c.Title == title {
			if strings.Contains(c.CandidateID, "://") {
				t.Fatalf("candidate_id = %q, want an opaque id, not the page URL", c.CandidateID)
			}
			return c.CandidateID
		}
	}
	t.Fatalf("candidates = %+v, want one titled %q", got, title)
	return ""
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
	if job := e.waitJob(t, *c.GroundingJobID); job.Status != jobs.StatusSucceeded {
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
	if first.Title != "Chicken Curry" || first.SiteName != "Curry House" ||
		first.PhotoURL == nil || *first.PhotoURL != wantPhoto || first.Minutes == nil || *first.Minutes != 40 {
		t.Errorf("first = %+v, want the stored curryA with photo %s", first, wantPhoto)
	}
	if second.Title != "Easy Weeknight Curry" || second.SiteName != "B Kitchen" ||
		second.PhotoURL != nil || second.Minutes == nil || *second.Minutes != 25 {
		t.Errorf("second = %+v, want curryB without a photo", second)
	}
	if first.CandidateID == "" || first.CandidateID == second.CandidateID ||
		first.CandidateID == curryA || second.CandidateID == curryB {
		t.Errorf("candidate ids = %q, %q, want distinct opaque ids", first.CandidateID, second.CandidateID)
	}
	again, err := e.svc.Candidates(t.Context(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 2 || again[0].CandidateID != first.CandidateID || again[1].CandidateID != second.CandidateID {
		t.Errorf("candidates again = %+v, want the same ids", again)
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
	e.search.Results["Mystery curry"] = []recipes.Candidate{{URL: curryA}, {URL: curryB}}
	cid := candidateID(t, e, id, "Easy Weeknight Curry")
	e.model.QueueGenerate(recipestest.NormalizedReply("stovetop", 25))

	d, err := e.svc.Ground(t.Context(), id, dinners.Fix{CandidateID: ptr(cid)}, "member-2")
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

// wantStaleCandidate checks that candidate cid no longer grounds dinner
// id: Ground refuses it as invalid, and the dinner is unchanged.
func wantStaleCandidate(t *testing.T, e *env, id, cid string) {
	t.Helper()
	before := e.dinner(t, id)
	_, err := e.svc.Ground(t.Context(), id, dinners.Fix{CandidateID: ptr(cid)}, member)
	var invalid *dinners.ValidationError
	if !errors.As(err, &invalid) {
		t.Fatalf("stale candidate: err = %v, want *ValidationError", err)
	}
	if after := e.dinner(t, id); after != before {
		t.Errorf("dinner changed by a stale candidate: %+v, was %+v", after, before)
	}
}

// TestGroundingOrArchivingClearsCandidates: once a dinner is grounded by
// hand — by candidate, link or manual recipe — renamed or archived, the
// service holds none of its candidates' URLs and none of them grounds it.
func TestGroundingOrArchivingClearsCandidates(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		// clear grounds or archives dinner id, offered candidate cid.
		clear func(t *testing.T, e *env, id, cid string)
		// archived says the dinner ends archived, which Ground refuses
		// before it reads a candidate.
		archived bool
	}{
		{"candidate", func(t *testing.T, e *env, id, cid string) {
			e.model.QueueGenerate(recipestest.NormalizedReply("stovetop", 40))
			if _, err := e.svc.Ground(t.Context(), id, dinners.Fix{CandidateID: ptr(cid)}, member); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"link", func(t *testing.T, e *env, id, _ string) {
			e.model.QueueGenerate(recipestest.NormalizedReply("oven", 150))
			if _, err := e.svc.Ground(t.Context(), id, dinners.Fix{RecipeURL: ptr(stew)}, member); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"manual", func(t *testing.T, e *env, id, _ string) {
			m := &dinners.ManualRecipe{Ingredients: []string{"1 chicken"}, Minutes: ptr(40), Method: ptr("stovetop")}
			if _, err := e.svc.Ground(t.Context(), id, dinners.Fix{Manual: m}, member); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"rename", func(t *testing.T, e *env, id, _ string) {
			if _, err := e.svc.Update(t.Context(), id, dinners.Patch{Name: ptr("Beef stew")}, member); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"archive", func(t *testing.T, e *env, id, _ string) {
			if _, err := e.svc.Archive(t.Context(), id, member); err != nil {
				t.Fatal(err)
			}
		}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			id := failedDinner(t, e, "Mystery curry")
			e.search.Results["Mystery curry"] = []recipes.Candidate{{URL: curryA}, {URL: curryB}}
			cidA := candidateID(t, e, id, "Chicken Curry")
			cidB := candidateID(t, e, id, "Easy Weeknight Curry")

			tt.clear(t, e, id, cidA)
			if e.svc.HasOffers(id) {
				t.Errorf("service still holds the candidates of dinner %s", id)
			}
			if !tt.archived {
				wantStaleCandidate(t, e, id, cidB)
			}
		})
	}
}

// TestRenamePendingRefusesOldCandidates: candidates offered for a pending
// dinner's old name no longer ground it once it is renamed — Ground refuses
// them and leaves the old recipe off it — while candidates for the new name
// do.
func TestRenamePendingRefusesOldCandidates(t *testing.T) {
	t.Parallel()
	// Unstarted, so the dinner stays pending with its jobs queued.
	e := newUnstartedEnv(t)
	e.search.Results["Beef stew"] = []recipes.Candidate{{URL: stew}}
	c, err := e.svc.CreateRegular(t.Context(), dinners.NewRegular{Name: ptr("Chicken curry")}, member)
	if err != nil {
		t.Fatal(err)
	}
	old := candidateID(t, e, c.ID, "Chicken Curry")

	d, err := e.svc.Update(t.Context(), c.ID, dinners.Patch{Name: ptr("Beef stew")}, member)
	if err != nil {
		t.Fatal(err)
	}
	if d.GroundingStatus != dinners.GroundingPending {
		t.Fatalf("renamed = %+v, want still pending", d)
	}
	if e.svc.HasOffers(c.ID) {
		t.Errorf("service still holds the old name's candidates of dinner %s", c.ID)
	}
	wantStaleCandidate(t, e, c.ID, old)
	if row := e.dinner(t, c.ID); row.RecipeID.Valid || row.GroundingStatus != dinners.GroundingPending {
		t.Fatalf("dinner = %+v, want pending with no recipe", row)
	}

	e.model.QueueGenerate(recipestest.NormalizedReply("oven", 150))
	g, err := e.svc.Ground(t.Context(), c.ID, dinners.Fix{CandidateID: ptr(candidateID(t, e, c.ID, "Beef Stew"))}, member)
	if err != nil {
		t.Fatal(err)
	}
	if g.GroundingStatus != dinners.GroundingGrounded || g.RecipeID == nil {
		t.Fatalf("dinner = %+v, want grounded for the new name", g)
	}
	rec, err := db.New(e.st.Reader).GetRecipe(t.Context(), *g.RecipeID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.SourceUrl != stew {
		t.Errorf("recipe source = %s, want %s", rec.SourceUrl, stew)
	}
}

// TestRenameSameNormalizedNameRefusesOldCandidates: a rename that leaves
// the normalized name as it was still drops the candidates offered before
// it, and none of them grounds the dinner.
func TestRenameSameNormalizedNameRefusesOldCandidates(t *testing.T) {
	t.Parallel()
	// Unstarted, so the dinner stays pending with its jobs queued.
	e := newUnstartedEnv(t)
	e.search.Results["Quick Chicken curry"] = e.search.Results["Chicken curry"]
	c, err := e.svc.CreateRegular(t.Context(), dinners.NewRegular{Name: ptr("Chicken curry")}, member)
	if err != nil {
		t.Fatal(err)
	}
	old := candidateID(t, e, c.ID, "Chicken Curry")
	before := e.dinner(t, c.ID)

	d, err := e.svc.Update(t.Context(), c.ID, dinners.Patch{Name: ptr("Quick Chicken curry")}, member)
	if err != nil {
		t.Fatal(err)
	}
	if e.svc.HasOffers(c.ID) {
		t.Errorf("service still holds the candidates offered before dinner %s was renamed", c.ID)
	}
	// A reply is queued, so only refusing the candidate keeps its recipe off.
	e.model.QueueGenerate(recipestest.NormalizedReply("stovetop", 40))
	wantStaleCandidate(t, e, c.ID, old)
	row := e.dinner(t, c.ID)
	if row.RecipeID.Valid {
		t.Fatalf("dinner = %+v, want no recipe", row)
	}
	if row.NormalizedName != before.NormalizedName {
		t.Fatalf("normalized name = %q, want %q unchanged", row.NormalizedName, before.NormalizedName)
	}
	// Still pending, now at the rename's revision, with a job for it.
	if d.GroundingStatus != dinners.GroundingPending || row.GroundingRevision != before.GroundingRevision+1 {
		t.Fatalf("renamed = %+v, want pending at revision %d", row, before.GroundingRevision+1)
	}
	var queued int
	if err := e.st.Reader.QueryRowContext(t.Context(),
		`SELECT count(*) FROM jobs WHERE status = 'queued' AND json_extract(payload, '$.revision') = ?`,
		row.GroundingRevision).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 1 {
		t.Fatalf("queued jobs at revision %d = %d, want 1", row.GroundingRevision, queued)
	}
}

// TestCandidatesBetweenRenameAndClearKept: a candidate search that reads
// a dinner after its rename commits, and stores its candidates before the
// rename clears the dinner's offers, keeps them: they ground the renamed
// dinner, while one offered before the rename does not, even when a search
// that read the dinner before the rename finishes after them.
func TestCandidatesBetweenRenameAndClearKept(t *testing.T) {
	// Unstarted, so the dinner stays pending with its jobs queued.
	e := newUnstartedEnv(t)
	e.search.Results["Beef stew"] = []recipes.Candidate{{URL: stew}}
	c, err := e.svc.CreateRegular(t.Context(), dinners.NewRegular{Name: ptr("Chicken curry")}, member)
	if err != nil {
		t.Fatal(err)
	}
	old := candidateID(t, e, c.ID, "Chicken Curry")
	before := e.dinner(t, c.ID)

	var fresh string
	dinners.SetUpdateCommitted(t, func() {
		fresh = candidateID(t, e, c.ID, "Beef Stew")
		e.svc.StoreOffers(c.ID, map[string]string{old: curryA}, before.GroundingRevision)
	})
	if _, err := e.svc.Update(t.Context(), c.ID, dinners.Patch{Name: ptr("Beef stew")}, member); err != nil {
		t.Fatal(err)
	}
	if fresh == "" {
		t.Fatal("no candidate search ran between the rename and clearing offers")
	}
	if !e.svc.HasOffers(c.ID) {
		t.Fatalf("clearing offers after the rename dropped the candidates found at its revision for dinner %s", c.ID)
	}
	wantStaleCandidate(t, e, c.ID, old)

	e.model.QueueGenerate(recipestest.NormalizedReply("oven", 150))
	g, err := e.svc.Ground(t.Context(), c.ID, dinners.Fix{CandidateID: ptr(fresh)}, member)
	if err != nil {
		t.Fatal(err)
	}
	if g.GroundingStatus != dinners.GroundingGrounded || g.RecipeID == nil {
		t.Fatalf("dinner = %+v, want grounded for the new name", g)
	}
	rec, err := db.New(e.st.Reader).GetRecipe(t.Context(), *g.RecipeID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.SourceUrl != stew {
		t.Errorf("recipe source = %s, want %s", rec.SourceUrl, stew)
	}
}

// TestRenameBackDuringCandidatesRefusesThem: a candidate search that read
// a dinner as A and finishes after renames A -> B -> A have committed, but
// before either has cleared the dinner's offers, stores candidates that
// do not ground it, though its name is A again.
func TestRenameBackDuringCandidatesRefusesThem(t *testing.T) {
	e := newEnv(t)
	id := failedDinner(t, e, "Mystery curry")
	e.search.Results["Mystery curry"] = []recipes.Candidate{{URL: curryA}}
	asked := len(e.search.Queries())
	gate := make(chan struct{})
	e.search.Gate = gate

	type result struct {
		got []dinners.Candidate
		err error
	}
	done := make(chan result, 1)
	go func() {
		got, err := e.svc.Candidates(t.Context(), id)
		done <- result{got, err}
	}()
	deadline := time.Now().Add(10 * time.Second)
	for len(e.search.Queries()) == asked {
		if time.Now().After(deadline) {
			t.Fatal("candidate search never started")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// The first rename's hook makes the second; the second's lets the
	// search finish and tries its candidate, before either clears offers.
	var hooks int
	var stale error
	var groundedBy *dinners.Dinner
	dinners.SetUpdateCommitted(t, func() {
		hooks++
		switch hooks {
		case 1:
			if _, err := e.svc.Update(t.Context(), id, dinners.Patch{Name: ptr("Mystery curry")}, member); err != nil {
				t.Error(err)
			}
		case 2:
			close(gate)
			r := <-done
			if r.err != nil || len(r.got) != 1 {
				t.Errorf("candidates = %+v, %v; want curryA", r.got, r.err)
				return
			}
			e.model.QueueGenerate(recipestest.NormalizedReply("stovetop", 40))
			d, err := e.svc.Ground(t.Context(), id, dinners.Fix{CandidateID: ptr(r.got[0].CandidateID)}, member)
			if err == nil {
				groundedBy = &d
			}
			stale = err
		}
	})
	if _, err := e.svc.Update(t.Context(), id, dinners.Patch{Name: ptr("Beef stew")}, member); err != nil {
		t.Fatal(err)
	}
	if hooks != 2 {
		t.Fatalf("renames committed = %d, want 2", hooks)
	}
	var invalid *dinners.ValidationError
	if !errors.As(stale, &invalid) {
		t.Fatalf("stale candidate: err = %v, grounded %+v; want *ValidationError", stale, groundedBy)
	}
	row := e.dinner(t, id)
	if row.Name != "Mystery curry" || row.RecipeID.Valid || row.GroundingStatus != dinners.GroundingFailed {
		t.Errorf("dinner = %+v, want Mystery curry still failed with no recipe", row)
	}
	if e.svc.HasOffers(id) {
		t.Errorf("service still holds the stale candidates of dinner %s", id)
	}
}

// TestCandidatesRunningWhenCleared: a candidate search still running when
// its dinner is grounded by hand, renamed or archived stores none of the
// candidates it returns, so none of them grounds the dinner.
func TestCandidatesRunningWhenCleared(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		clear func(t *testing.T, e *env, id string)
		// archived says the dinner ends archived.
		archived bool
	}{
		{"manual", func(t *testing.T, e *env, id string) {
			m := &dinners.ManualRecipe{Ingredients: []string{"1 chicken"}, Minutes: ptr(40), Method: ptr("stovetop")}
			if _, err := e.svc.Ground(t.Context(), id, dinners.Fix{Manual: m}, member); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"rename", func(t *testing.T, e *env, id string) {
			if _, err := e.svc.Update(t.Context(), id, dinners.Patch{Name: ptr("Beef stew")}, member); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"archive", func(t *testing.T, e *env, id string) {
			if _, err := e.svc.Archive(t.Context(), id, member); err != nil {
				t.Fatal(err)
			}
		}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			id := failedDinner(t, e, "Mystery curry")
			e.search.Results["Mystery curry"] = []recipes.Candidate{{URL: curryA}}
			asked := len(e.search.Queries())
			gate := make(chan struct{})
			e.search.Gate = gate

			type result struct {
				got []dinners.Candidate
				err error
			}
			done := make(chan result, 1)
			go func() {
				got, err := e.svc.Candidates(t.Context(), id)
				done <- result{got, err}
			}()
			deadline := time.Now().Add(10 * time.Second)
			for len(e.search.Queries()) == asked {
				if time.Now().After(deadline) {
					t.Fatal("candidate search never started")
				}
				time.Sleep(5 * time.Millisecond)
			}
			tt.clear(t, e, id)
			close(gate)
			r := <-done
			if r.err != nil {
				t.Fatal(r.err)
			}
			if len(r.got) != 1 {
				t.Fatalf("candidates = %+v, want curryA", r.got)
			}
			if e.svc.HasOffers(id) {
				t.Errorf("a search finishing after dinner %s was cleared stored its candidates", id)
			}
			if !tt.archived {
				wantStaleCandidate(t, e, id, r.got[0].CandidateID)
			}
		})
	}
}

// TestCandidatesExpire: once their lifetime passes, a dinner's candidates
// are dropped and no longer ground it, with no other search running.
func TestCandidatesExpire(t *testing.T) {
	dinners.SetOfferLifetime(t, 0)
	e := newEnv(t)
	id := failedDinner(t, e, "Mystery curry")
	e.search.Results["Mystery curry"] = []recipes.Candidate{{URL: curryA}}
	cid := candidateID(t, e, id, "Chicken Curry")

	waitNoOffers(t, e)
	wantStaleCandidate(t, e, id, cid)
}

// TestCandidatesExpireBeforeEviction: once their lifetime passes, a
// dinner's candidates no longer ground it, even while the service still
// holds them because their eviction has not run yet.
func TestCandidatesExpireBeforeEviction(t *testing.T) {
	dinners.SetOfferLifetime(t, time.Hour)
	e := newEnv(t)
	id := failedDinner(t, e, "Mystery curry")
	e.search.Results["Mystery curry"] = []recipes.Candidate{{URL: curryA}}
	cid := candidateID(t, e, id, "Chicken Curry")

	dinners.AdvanceClock(t, time.Hour)
	if !e.svc.HasOffers(id) {
		t.Fatal("candidates evicted already, want them held until their timer fires")
	}
	wantStaleCandidate(t, e, id, cid)
}

// TestExpiredCandidatesNotHeld: the service holds no dinner's candidates
// past their lifetime, however many dinners were offered some.
func TestExpiredCandidatesNotHeld(t *testing.T) {
	dinners.SetOfferLifetime(t, 50*time.Millisecond)
	e := newEnv(t)
	names := map[string]string{"Mystery curry": curryA, "Other curry": curryB}
	ids := make([]string, 0, len(names))
	for name, u := range names {
		ids = append(ids, failedDinner(t, e, name))
		e.search.Results[name] = []recipes.Candidate{{URL: u}}
	}
	for _, id := range ids {
		if _, err := e.svc.Candidates(t.Context(), id); err != nil {
			t.Fatal(err)
		}
	}
	waitNoOffers(t, e)
}

// waitNoOffers waits for the service to hold no dinner's candidates.
func waitNoOffers(t *testing.T, e *env) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for n := e.svc.OfferCount(); n > 0; n = e.svc.OfferCount() {
		if time.Now().After(deadline) {
			t.Fatalf("service still holds the expired candidates of %d dinners", n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestFixReplacesPendingLink: fixing a Regular added from a link that held
// no recipe drops that link, so it no longer blocks adding the link again,
// while the link of the recipe chosen does.
func TestFixReplacesPendingLink(t *testing.T) {
	t.Parallel()
	const lost = "https://gone.example/recipes/lamb-tagine"
	tests := []struct {
		name string
		// fix is the fix for dinner id.
		fix func(t *testing.T, e *env, id string) dinners.Fix
		// linkTaken is the recipe link the fixed dinner now has, if any.
		linkTaken string
	}{
		{"candidate", func(t *testing.T, e *env, id string) dinners.Fix {
			e.search.Results["Weeknight dinner"] = []recipes.Candidate{{URL: curryB}}
			return dinners.Fix{CandidateID: ptr(candidateID(t, e, id, "Easy Weeknight Curry"))}
		}, curryB},
		{"manual", func(*testing.T, *env, string) dinners.Fix {
			return dinners.Fix{Manual: &dinners.ManualRecipe{Ingredients: []string{"1 lamb"}, Minutes: ptr(90), Method: ptr("oven")}}
		}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			c, err := e.svc.CreateRegular(t.Context(), dinners.NewRegular{Name: ptr("Lamb roast"), RecipeURL: ptr(lost)}, member)
			if err != nil {
				t.Fatal(err)
			}
			if job := e.waitJob(t, *c.GroundingJobID); job.Status != jobs.StatusFailed {
				t.Fatalf("job = %+v, want failed", job)
			}
			// Renamed, so only the link could repeat it.
			if _, err := e.svc.Update(t.Context(), c.ID, dinners.Patch{Name: ptr("Weeknight dinner")}, member); err != nil {
				t.Fatal(err)
			}
			e.model.QueueGenerate(recipestest.NormalizedReply("stovetop", 25))
			if _, err := e.svc.Ground(t.Context(), c.ID, tt.fix(t, e, c.ID), member); err != nil {
				t.Fatal(err)
			}
			row, err := db.New(e.st.Reader).GetDinner(t.Context(), c.ID)
			if err != nil {
				t.Fatal(err)
			}
			if row.RecipeUrl.Valid {
				t.Errorf("recipe_url = %q after the fix, want the unusable link dropped", row.RecipeUrl.String)
			}

			if _, err := e.svc.CreateRegular(t.Context(), dinners.NewRegular{Name: ptr("Lentil soup"), RecipeURL: ptr(lost)}, member); err != nil {
				t.Errorf("adding the dropped link again: %v, want added", err)
			}
			if tt.linkTaken != "" {
				_, err := e.svc.CreateRegular(t.Context(), dinners.NewRegular{Name: ptr("Curry night"), RecipeURL: ptr(tt.linkTaken)}, member)
				var dup *dinners.DuplicateError
				if !errors.As(err, &dup) || dup.Existing.ID != c.ID {
					t.Errorf("adding the fixed dinner's recipe link: err = %v, want a duplicate of %s", err, c.ID)
				}
			}
		})
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
		"unknown candidate": {CandidateID: ptr("cand-1")},
		"candidate a URL":   {CandidateID: ptr(curryA)},
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

// TestRestartFailsInterruptedGrounding simulates a crash: one dinner's job
// was left running and another dinner was saved before its job was
// enqueued. At the next boot both end failed as interrupted, while a
// dinner whose job is still queued stays pending and is grounded.
func TestRestartFailsInterruptedGrounding(t *testing.T) {
	t.Parallel()
	e := newUnstartedEnv(t)
	e.search.Gate = make(chan struct{})
	e.model.QueueGenerate(recipestest.NormalizedReply("stovetop", 40))

	running, err := e.svc.CreateRegular(t.Context(), dinners.NewRegular{Name: ptr("Fish pie")}, member)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.Writer.ExecContext(t.Context(),
		`UPDATE jobs SET status = 'running', started_at = updated_at WHERE id = ?`, *running.GroundingJobID); err != nil {
		t.Fatal(err)
	}
	unqueued, err := e.svc.CreateRegular(t.Context(), dinners.NewRegular{Name: ptr("Lentil soup")}, member)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.Writer.ExecContext(t.Context(), `DELETE FROM jobs WHERE id = ?`, *unqueued.GroundingJobID); err != nil {
		t.Fatal(err)
	}
	queued, err := e.svc.CreateRegular(t.Context(), dinners.NewRegular{Name: ptr("Chicken curry")}, member)
	if err != nil {
		t.Fatal(err)
	}

	// Boot: the runner fails the running job, then the service fails the
	// dinners nothing is left to ground. The search is held, so the queued
	// dinner's job cannot finish meanwhile.
	e.start(t)
	n, err := e.svc.FailInterruptedGroundings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("failed %d dinners, want 2", n)
	}
	if job, err := e.runner.Get(t.Context(), *running.GroundingJobID); err != nil || job.Status != jobs.StatusFailed {
		t.Errorf("running job = %+v, %v; want failed", job, err)
	}
	for _, id := range []string{running.ID, unqueued.ID} {
		if row := e.dinner(t, id); row.GroundingStatus != dinners.GroundingFailed || row.GroundingError.String != dinners.ReasonInterrupted {
			t.Errorf("dinner %s = %s (%s), want failed (interrupted)", id, row.GroundingStatus, row.GroundingError.String)
		}
	}
	if row := e.dinner(t, queued.ID); row.GroundingStatus != dinners.GroundingPending {
		t.Errorf("queued dinner = %s, want pending", row.GroundingStatus)
	}
	close(e.search.Gate)
	if job := e.waitJob(t, *queued.GroundingJobID); job.Status != jobs.StatusSucceeded {
		t.Fatalf("queued job = %+v", job)
	}
	if row := e.dinner(t, queued.ID); row.GroundingStatus != dinners.GroundingGrounded {
		t.Errorf("queued dinner = %s, want grounded", row.GroundingStatus)
	}
}

// TestRestartKeepsArchivedPendingDinner: a pending Regular archived while
// its job runs stays pending, its job succeeded; the boot cleanup leaves it
// as it is rather than failing it as interrupted.
func TestRestartKeepsArchivedPendingDinner(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.search.Gate = make(chan struct{})
	c, err := e.svc.CreateRegular(t.Context(), dinners.NewRegular{Name: ptr("Chicken curry")}, member)
	if err != nil {
		t.Fatal(err)
	}
	e.waitQuery(t, "Chicken curry")
	if _, err := e.svc.Archive(t.Context(), c.ID, member); err != nil {
		t.Fatal(err)
	}
	job := e.waitJob(t, *c.GroundingJobID)
	close(e.search.Gate)
	if job.Status != jobs.StatusSucceeded {
		t.Fatalf("job = %+v, want succeeded", job)
	}

	// The database is now as the next boot finds it.
	n, err := e.svc.FailInterruptedGroundings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("failed %d dinners, want 0", n)
	}
	if row := e.dinner(t, c.ID); row.Status != dinners.StatusArchived ||
		row.GroundingStatus != dinners.GroundingPending || row.GroundingError.Valid {
		t.Errorf("dinner = %s, %s (%s), want archived and pending with no error",
			row.Status, row.GroundingStatus, row.GroundingError.String)
	}
}

// TestUpgradeKeepsLegacyQueuedGrounding: a pending Regular whose
// ground_dinner job was queued before migration 0010, so its payload has
// no revision, is not failed as interrupted at the first boot after the
// upgrade; its job still grounds it. The upgrade starts either from 0009
// or from a database that has already applied 0010.
func TestUpgradeKeepsLegacyQueuedGrounding(t *testing.T) {
	t.Parallel()
	for name, below := range map[string]string{
		"from 0009": "0010",
		"from 0010": "0011",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			upgradeWithLegacyQueuedGrounding(t, below)
		})
	}
}

// upgradeWithLegacyQueuedGrounding migrates a database to every migration
// named before below, adds a pending Regular with a queued ground_dinner
// job whose payload has no revision, then boots on the latest migrations.
func upgradeWithLegacyQueuedGrounding(t *testing.T, below string) {
	ctx := t.Context()
	dataDir := t.TempDir()
	st := openStore(t, dataDir)
	before := fstest.MapFS{}
	files, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range files {
		if name >= below {
			continue
		}
		body, err := fs.ReadFile(migrations.FS, name)
		if err != nil {
			t.Fatal(err)
		}
		before[name] = &fstest.MapFile{Data: body}
	}
	if err := store.Migrate(ctx, st.Writer, before); err != nil {
		t.Fatal(err)
	}
	now := store.Now().String()
	if _, err := st.Writer.ExecContext(ctx, `INSERT INTO dinners
		(id, name, normalized_name, status, origin, is_favorite, grounding_status, updated_by, created_at, updated_at)
		VALUES ('dinner-1', 'Chicken curry', 'chicken curry', 'regular', 'regular', 0, 'pending', ?, ?, ?)`,
		member, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Writer.ExecContext(ctx, `INSERT INTO jobs (id, kind, status, payload, created_at, updated_at)
		VALUES ('job-1', ?, 'queued', '{"dinner_id":"dinner-1"}', ?, ?)`,
		dinners.KindGroundDinner, now, now); err != nil {
		t.Fatal(err)
	}

	// Boot: migrate, start the runner, then fail interrupted groundings. The
	// search is held, so the job cannot finish before the cleanup runs.
	e := newUnstartedEnvOn(t, st, dataDir)
	e.search.Gate = make(chan struct{})
	e.model.QueueGenerate(recipestest.NormalizedReply("stovetop", 40))
	if p := e.queuedJob(t, "job-1"); p != (dinners.GroundJob{DinnerID: "dinner-1", Revision: 0}) {
		t.Errorf("migrated payload = %+v, want dinner-1 at revision 0", p)
	}
	e.start(t)
	n, err := e.svc.FailInterruptedGroundings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("failed %d dinners, want 0", n)
	}
	if row := e.dinner(t, "dinner-1"); row.GroundingStatus != dinners.GroundingPending {
		t.Fatalf("dinner = %s (%s), want still pending", row.GroundingStatus, row.GroundingError.String)
	}
	close(e.search.Gate)
	if job := e.waitJob(t, "job-1"); job.Status != jobs.StatusSucceeded {
		t.Fatalf("job = %+v", job)
	}
	if row := e.dinner(t, "dinner-1"); row.GroundingStatus != dinners.GroundingGrounded {
		t.Errorf("dinner = %s, want grounded", row.GroundingStatus)
	}
}

// TestCreateEnqueueFailureReturnsFailedDinner: a dinner saved when its job
// cannot be enqueued is returned, with its id, failed and no job id.
func TestCreateEnqueueFailureReturnsFailedDinner(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	if _, err := e.st.Writer.ExecContext(t.Context(),
		`CREATE TRIGGER refuse_jobs BEFORE INSERT ON jobs BEGIN SELECT RAISE(ABORT, 'no jobs'); END`); err != nil {
		t.Fatal(err)
	}
	c, err := e.svc.CreateRegular(t.Context(), dinners.NewRegular{Name: ptr("Chicken curry")}, member)
	if err != nil {
		t.Fatal(err)
	}
	if c.ID == "" || c.GroundingJobID != nil || c.GroundingStatus != dinners.GroundingFailed ||
		c.GroundingError == nil || *c.GroundingError != dinners.ReasonGroundingError {
		t.Fatalf("created = %+v, want its id, failed (grounding_error) and no job", c)
	}
	if row := e.dinner(t, c.ID); row.GroundingStatus != dinners.GroundingFailed {
		t.Errorf("stored grounding_status = %s, want failed", row.GroundingStatus)
	}
}

// TestGroundTimesOut: grounding a link by hand that runs past its deadline
// is ErrGroundTimeout and leaves the dinner as it was. It shortens the
// package deadline, so it does not run in parallel.
func TestGroundTimesOut(t *testing.T) {
	dinners.SetGroundTimeout(t, 50*time.Millisecond)
	e := newEnv(t)
	id := failedDinner(t, e, "Beef stew")
	e.pages.Gate = make(chan struct{})
	defer close(e.pages.Gate)

	_, err := e.svc.Ground(t.Context(), id, dinners.Fix{RecipeURL: ptr(stew)}, member)
	if !errors.Is(err, dinners.ErrGroundTimeout) {
		t.Fatalf("err = %v, want ErrGroundTimeout", err)
	}
	if row := e.dinner(t, id); row.GroundingStatus != dinners.GroundingFailed {
		t.Errorf("grounding_status = %s, want failed as before", row.GroundingStatus)
	}
}

// TestFixInFlightWhenDinnerChanges: a link still grounding when its dinner
// is renamed, archived or grounded some other way writes nothing; the
// change made meanwhile is kept.
func TestFixInFlightWhenDinnerChanges(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		change func(t *testing.T, e *env, id string)
		want   error
		// kept checks the dinner shows the change, not the link's recipe.
		kept func(row db.Dinner) bool
	}{
		{"renamed", func(t *testing.T, e *env, id string) {
			if _, err := e.svc.Update(t.Context(), id, dinners.Patch{Name: ptr("Chicken curry")}, member); err != nil {
				t.Fatal(err)
			}
		}, dinners.ErrGroundConflict, func(row db.Dinner) bool {
			return row.Name == "Chicken curry" && row.GroundingStatus == dinners.GroundingFailed && !row.RecipeID.Valid
		}},
		{"renamed to the same normalized name", func(t *testing.T, e *env, id string) {
			if _, err := e.svc.Update(t.Context(), id, dinners.Patch{Name: ptr("Easy Grandma's stew")}, member); err != nil {
				t.Fatal(err)
			}
		}, dinners.ErrGroundConflict, func(row db.Dinner) bool {
			return row.Name == "Easy Grandma's stew" && row.GroundingStatus == dinners.GroundingFailed && !row.RecipeID.Valid
		}},
		{"archived", func(t *testing.T, e *env, id string) {
			if _, err := e.svc.Archive(t.Context(), id, member); err != nil {
				t.Fatal(err)
			}
		}, dinners.ErrArchived, func(row db.Dinner) bool {
			return row.Status == dinners.StatusArchived && row.GroundingStatus == dinners.GroundingFailed && !row.RecipeID.Valid
		}},
		{"grounded by hand", func(t *testing.T, e *env, id string) {
			if _, err := e.svc.Ground(t.Context(), id, dinners.Fix{Manual: &dinners.ManualRecipe{
				Ingredients: []string{"1 lb beef"}, Minutes: ptr(240), Method: ptr("crockpot"),
			}}, member); err != nil {
				t.Fatal(err)
			}
		}, dinners.ErrGroundConflict, func(row db.Dinner) bool {
			return row.GroundingStatus == dinners.GroundingManual && row.Method.String == "crockpot"
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			id := failedDinner(t, e, "Grandma's stew")
			e.model.QueueGenerate(recipestest.NormalizedReply("oven", 150))
			e.pages.Gate = make(chan struct{})

			done := make(chan error, 1)
			go func() {
				_, err := e.svc.Ground(context.Background(), id, dinners.Fix{RecipeURL: ptr(stew)}, member)
				done <- err
			}()
			e.waitFetched(t, stew)
			tt.change(t, e, id)
			close(e.pages.Gate)

			if err := <-done; !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			if row := e.dinner(t, id); !tt.kept(row) {
				t.Errorf("dinner = %+v, want the %s change kept and the link's recipe not written", row, tt.name)
			}
		})
	}
}

// TestFixInFlightKeepsMemberEdits: a method, minutes or difficulty a
// member sets while a link is grounding is kept; a field they left alone
// takes the link's recipe, even one already set before grounding began.
func TestFixInFlightKeepsMemberEdits(t *testing.T) {
	t.Parallel()
	// Every field is set before grounding, then one is changed in flight.
	before := dinners.Patch{Method: ptr("stovetop"), MinutesTotal: ptr(45), Difficulty: ptr("easy")}
	tests := []struct {
		name  string
		edit  dinners.Patch
		field string
	}{
		{"method", dinners.Patch{Method: ptr("grill")}, "method"},
		{"minutes", dinners.Patch{MinutesTotal: ptr(20)}, "minutes_total"},
		{"difficulty", dinners.Patch{Difficulty: ptr("medium")}, "difficulty"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			id := failedDinner(t, e, "Grandma's stew")
			if _, err := e.svc.Update(t.Context(), id, before, member); err != nil {
				t.Fatal(err)
			}
			e.model.QueueGenerate(recipestest.NormalizedReply("oven", 150))
			e.pages.Gate = make(chan struct{})

			done := make(chan error, 1)
			go func() {
				_, err := e.svc.Ground(context.Background(), id, dinners.Fix{RecipeURL: ptr(stew)}, member)
				done <- err
			}()
			e.waitFetched(t, stew)
			if _, err := e.svc.Update(t.Context(), id, tt.edit, member); err != nil {
				t.Fatal(err)
			}
			close(e.pages.Gate)

			if err := <-done; err != nil {
				t.Fatal(err)
			}
			row := e.dinner(t, id)
			if row.GroundingStatus != dinners.GroundingGrounded || !row.RecipeID.Valid {
				t.Fatalf("dinner = %+v, want grounded in the link's recipe", row)
			}
			var recipe fields
			if err := e.st.Reader.QueryRowContext(t.Context(),
				`SELECT method, minutes_total, difficulty FROM recipes WHERE id = ?`, row.RecipeID.String,
			).Scan(&recipe.method, &recipe.minutes, &recipe.difficulty); err != nil {
				t.Fatal(err)
			}
			old, edited := patchFields(before), patchFields(tt.edit)
			want := recipe
			switch tt.field {
			case "method":
				want.method = edited.method
			case "minutes_total":
				want.minutes = edited.minutes
			case "difficulty":
				want.difficulty = edited.difficulty
			}
			if recipe.method == old.method || recipe.minutes == old.minutes || recipe.difficulty == old.difficulty {
				t.Fatalf("recipe %+v shares a value with the dinner before grounding %+v", recipe, old)
			}
			got := fields{row.Method.String, row.MinutesTotal.Int64, row.Difficulty.String}
			if got != want {
				t.Errorf("method, minutes, difficulty = %+v, want %+v: the %s edit kept, the rest from the recipe", got, want, tt.field)
			}
		})
	}
}

// fields are a dinner's or recipe's method, minutes_total and difficulty.
type fields struct {
	method     string
	minutes    int64
	difficulty string
}

// patchFields returns the fields p sets, zero where it sets none.
func patchFields(p dinners.Patch) fields {
	var f fields
	if p.Method != nil {
		f.method = *p.Method
	}
	if p.MinutesTotal != nil {
		f.minutes = int64(*p.MinutesTotal)
	}
	if p.Difficulty != nil {
		f.difficulty = *p.Difficulty
	}
	return f
}

// TestFixArchivedDinner: an archived dinner cannot be grounded by hand.
func TestFixArchivedDinner(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	id := failedDinner(t, e, "Grandma's stew")
	if _, err := e.svc.Archive(t.Context(), id, member); err != nil {
		t.Fatal(err)
	}
	before := e.dinner(t, id)
	for _, f := range []dinners.Fix{
		{RecipeURL: ptr(stew)},
		{Manual: &dinners.ManualRecipe{Ingredients: []string{"1 lb beef"}, Minutes: ptr(240), Method: ptr("crockpot")}},
	} {
		if _, err := e.svc.Ground(t.Context(), id, f, member); !errors.Is(err, dinners.ErrArchived) {
			t.Errorf("fix %+v: err = %v, want ErrArchived", f, err)
		}
	}
	if got := e.pages.Fetched(); len(got) != 0 {
		t.Errorf("fetched %q for an archived dinner", got)
	}
	if after := e.dinner(t, id); after != before {
		t.Errorf("archived dinner changed: %+v, was %+v", after, before)
	}
}

// waitFetched waits until rawURL has been fetched.
func (e *env) waitFetched(t *testing.T, rawURL string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !slices.Contains(e.pages.Fetched(), rawURL) {
		if time.Now().After(deadline) {
			t.Fatalf("%s never fetched", rawURL)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
