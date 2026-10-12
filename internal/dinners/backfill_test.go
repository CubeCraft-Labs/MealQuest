package dinners_test

import (
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/harness"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/dinners"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/jobs"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/recipes"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/recipes/recipestest"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

// failedDinner stores a Regular named name whose grounding failed with
// no_recipe_found, without a photo or recipe_url, and returns its id.
func (e *env) failedDinner(t *testing.T, name string) string {
	t.Helper()
	q := db.New(e.st.Writer)
	now := store.Now().String()
	row, err := q.CreateDinner(t.Context(), db.CreateDinnerParams{
		ID:              store.NewID(),
		Name:            name,
		NormalizedName:  harness.Normalize(name),
		Status:          dinners.StatusRegular,
		Origin:          "regular",
		GroundingStatus: dinners.GroundingPending,
		UpdatedBy:       sql.NullString{String: member, Valid: true},
		CreatedAt:       now,
		UpdatedAt:       now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.FailPendingDinnerGrounding(t.Context(), db.FailPendingDinnerGroundingParams{
		GroundingError:    sql.NullString{String: dinners.ReasonNoRecipe, Valid: true},
		UpdatedAt:         now,
		ID:                row.ID,
		GroundingRevision: row.GroundingRevision,
	}); err != nil {
		t.Fatal(err)
	}
	return row.ID
}

func outcome(id, out string, reason *string) dinners.BackfillOutcome {
	return dinners.BackfillOutcome{DinnerID: id, Outcome: out, Reason: reason}
}

func checkOutcomes(t *testing.T, got, want []dinners.BackfillOutcome) {
	t.Helper()
	gj, _ := json.Marshal(got)
	wj, _ := json.Marshal(want)
	if string(gj) != string(wj) {
		t.Fatalf("outcomes = %s, want %s", gj, wj)
	}
}

func TestBackfillGroundsFirstCandidateWithAPhoto(t *testing.T) {
	t.Parallel()
	e := newUnstartedEnv(t)
	// curryA ranks first but its photo is gone; curryB's photo caches.
	delete(e.images.JPEGs, "https://a.example/curry.jpg")
	e.model.QueueGenerate(recipestest.NormalizedReply("stovetop", 25))
	id := e.failedDinner(t, "Chicken curry")

	got, err := e.svc.RunBackfillJob(t.Context(), dinners.BackfillJob{})
	if err != nil {
		t.Fatal(err)
	}
	checkOutcomes(t, got, []dinners.BackfillOutcome{outcome(id, dinners.BackfillGrounded, nil)})

	row := e.dinner(t, id)
	if row.GroundingStatus != dinners.GroundingGrounded || !row.PhotoID.Valid || !row.RecipeID.Valid || row.GroundingError.Valid {
		t.Fatalf("dinner = %+v, want grounded with a photo and no error", row)
	}
	rec, err := db.New(e.st.Reader).GetRecipe(t.Context(), row.RecipeID.String)
	if err != nil {
		t.Fatal(err)
	}
	if rec.SourceUrl != curryB || rec.SiteName.String != "B Kitchen" || rec.PhotoID != row.PhotoID {
		t.Errorf("recipe = %s from %q photo %s, want %s from B Kitchen photo %s", rec.SourceUrl, rec.SiteName.String, rec.PhotoID.String, curryB, row.PhotoID.String)
	}
	if got := e.search.Queries(); !slices.Equal(got, []string{"Chicken curry"}) {
		t.Errorf("searched %q, want the dinner's name", got)
	}
}

func TestBackfillReusesCachedRecipeAndPhoto(t *testing.T) {
	t.Parallel()
	e := newUnstartedEnv(t)
	e.search.Results["Curry chicken"] = []recipes.Candidate{{URL: curryA}}
	// One reply: the second dinner must reuse the recipe the first stored.
	e.model.QueueGenerate(recipestest.NormalizedReply("stovetop", 40))
	first := e.failedDinner(t, "Chicken curry")
	second := e.failedDinner(t, "Curry chicken")

	got, err := e.svc.RunBackfillJob(t.Context(), dinners.BackfillJob{})
	if err != nil {
		t.Fatal(err)
	}
	checkOutcomes(t, got, []dinners.BackfillOutcome{
		outcome(first, dinners.BackfillGrounded, nil),
		outcome(second, dinners.BackfillGrounded, nil),
	})
	a, b := e.dinner(t, first), e.dinner(t, second)
	if a.RecipeID != b.RecipeID || a.PhotoID != b.PhotoID || !a.PhotoID.Valid {
		t.Errorf("dinners grounded in recipe %s photo %s and recipe %s photo %s, want the same cached ones",
			a.RecipeID.String, a.PhotoID.String, b.RecipeID.String, b.PhotoID.String)
	}
	if n := len(e.model.Requests()); n != 1 {
		t.Errorf("normalize_recipe ran %d times, want 1", n)
	}
	if n := slices.Index(e.pages.Fetched(), curryA); n < 0 || slices.Contains(e.pages.Fetched()[n+1:], curryA) {
		t.Errorf("fetched %q, want %s fetched once", e.pages.Fetched(), curryA)
	}
}

func TestBackfillIsolatesEachDinnersFailure(t *testing.T) {
	t.Parallel()
	e := newUnstartedEnv(t)
	e.search.Fail = map[string]error{"Broken search": errors.New("search provider down")}
	e.model.QueueGenerate(recipestest.NormalizedReply("stovetop", 40))
	// Listed by normalized name: both failures come before the curry.
	broken := e.failedDinner(t, "Broken search")
	curry := e.failedDinner(t, "Chicken curry")
	absent := e.failedDinner(t, "Abalone surprise")

	got, err := e.svc.RunBackfillJob(t.Context(), dinners.BackfillJob{})
	if err != nil {
		t.Fatal(err)
	}
	checkOutcomes(t, got, []dinners.BackfillOutcome{
		outcome(absent, dinners.BackfillFailed, ptr(dinners.ReasonNoRecipe)),
		outcome(broken, dinners.BackfillFailed, ptr(dinners.ReasonSearchFailed)),
		outcome(curry, dinners.BackfillGrounded, nil),
	})
	for id, reason := range map[string]string{absent: dinners.ReasonNoRecipe, broken: dinners.ReasonSearchFailed} {
		row := e.dinner(t, id)
		if row.GroundingStatus != dinners.GroundingFailed || row.GroundingError.String != reason || row.PhotoID.Valid {
			t.Errorf("dinner %s = %+v, want failed with %s and no photo", row.Name, row, reason)
		}
	}
	if row := e.dinner(t, curry); row.GroundingStatus != dinners.GroundingGrounded || !row.PhotoID.Valid {
		t.Errorf("curry = %+v, want grounded with a photo", row)
	}
}

func TestBackfillSkipsDinnersNotMissingAPhoto(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.model.QueueGenerate(recipestest.NormalizedReply("stovetop", 40))
	c, err := e.svc.CreateRegular(t.Context(), dinners.NewRegular{Name: ptr("Chicken curry")}, member)
	if err != nil {
		t.Fatal(err)
	}
	e.waitJob(t, *c.GroundingJobID)
	if row := e.dinner(t, c.ID); row.GroundingStatus != dinners.GroundingGrounded {
		t.Fatalf("dinner = %+v, want grounded", row)
	}
	// A failed dinner with a pasted link keeps that link for a fix by hand.
	linked, err := e.svc.CreateRegular(t.Context(), dinners.NewRegular{Name: ptr("Mystery pie"), RecipeURL: ptr("https://d.example/pie")}, member)
	if err != nil {
		t.Fatal(err)
	}
	e.waitJob(t, *linked.GroundingJobID)
	if row := e.dinner(t, linked.ID); row.GroundingStatus != dinners.GroundingFailed {
		t.Fatalf("linked dinner = %+v, want failed", row)
	}
	archived := e.failedDinner(t, "Old stew")
	if _, err := e.svc.Archive(t.Context(), archived, member); err != nil {
		t.Fatal(err)
	}
	searched := len(e.search.Queries())

	id, err := e.svc.StartPhotoBackfill(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	job := e.waitJob(t, id)
	if job.Kind != dinners.KindBackfillPhotos || string(job.Result) != `{"dinners":[]}` {
		t.Fatalf("job = %+v (result %s), want a backfill with no dinners", job, job.Result)
	}
	if got := e.search.Queries()[searched:]; len(got) != 0 {
		t.Errorf("backfill searched %q, want nothing", got)
	}
}

func TestBackfillOfOneDinnerGroundsOnlyIt(t *testing.T) {
	t.Parallel()
	e := newUnstartedEnv(t)
	e.model.QueueGenerate(recipestest.NormalizedReply("stovetop", 40))
	curry := e.failedDinner(t, "Chicken curry")
	other := e.failedDinner(t, "Abalone surprise")

	got, err := e.svc.RunBackfillJob(t.Context(), dinners.BackfillJob{DinnerID: curry})
	if err != nil {
		t.Fatal(err)
	}
	checkOutcomes(t, got, []dinners.BackfillOutcome{outcome(curry, dinners.BackfillGrounded, nil)})
	if row := e.dinner(t, other); row.GroundingStatus != dinners.GroundingFailed || row.PhotoID.Valid {
		t.Errorf("other dinner = %+v, want left failed", row)
	}
	if got := e.search.Queries(); !slices.Equal(got, []string{"Chicken curry"}) {
		t.Errorf("searched %q, want only the dinner's name", got)
	}
}

func TestBackfillLeavesDinnerChangedWhileGrounding(t *testing.T) {
	t.Parallel()
	e := newUnstartedEnv(t)
	id := e.failedDinner(t, "Chicken curry")
	gate := make(chan struct{})
	e.search.Gate = gate
	e.model.QueueGenerate(recipestest.NormalizedReply("stovetop", 40))

	done := make(chan []dinners.BackfillOutcome)
	go func() {
		got, err := e.svc.RunBackfillJob(t.Context(), dinners.BackfillJob{DinnerID: id})
		if err != nil {
			t.Error(err)
		}
		done <- got
	}()
	// The backfill listed the dinner and waits at its search.
	for len(e.search.Queries()) == 0 {
		time.Sleep(time.Millisecond)
	}
	if _, err := e.svc.Update(t.Context(), id, dinners.Patch{Name: ptr("Chicken korma")}, member); err != nil {
		t.Fatal(err)
	}
	close(gate)
	checkOutcomes(t, <-done, []dinners.BackfillOutcome{outcome(id, dinners.BackfillSkipped, nil)})
	if row := e.dinner(t, id); row.GroundingStatus != dinners.GroundingFailed || row.PhotoID.Valid {
		t.Errorf("dinner = %+v, want left failed without a photo", row)
	}
}

func TestStartPhotoBackfillIsSingleFlight(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	st := openStore(t, dataDir)
	e := newUnstartedEnvOn(t, st, dataDir)
	e.failedDinner(t, "Chicken curry")
	e.search.Gate = make(chan struct{})
	e.model.QueueGenerate(recipestest.NormalizedReply("stovetop", 40))

	// Queued: a second trigger returns the queued pass.
	first, err := e.svc.StartPhotoBackfill(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if again, err := e.svc.StartPhotoBackfill(t.Context()); err != nil || again != first {
		t.Fatalf("second trigger = %q, %v; want the queued %q", again, err, first)
	}

	// After a restart the queued pass is still the one: the claim is the
	// stored job, not the process's memory.
	restarted := newUnstartedEnvOn(t, st, dataDir)
	restarted.search.Gate = e.search.Gate
	restarted.model.QueueGenerate(recipestest.NormalizedReply("stovetop", 40))
	if again, err := restarted.svc.StartPhotoBackfill(t.Context()); err != nil || again != first {
		t.Fatalf("trigger after restart = %q, %v; want the queued %q", again, err, first)
	}

	// Running: the pass waits at its search, and a trigger returns it.
	restarted.start(t)
	for len(restarted.search.Queries()) == 0 {
		time.Sleep(time.Millisecond)
	}
	if again, err := restarted.svc.StartPhotoBackfill(t.Context()); err != nil || again != first {
		t.Fatalf("trigger while running = %q, %v; want the running %q", again, err, first)
	}
	var n int
	if err := st.Reader.QueryRowContext(t.Context(), `SELECT count(*) FROM jobs WHERE kind = ?`, dinners.KindBackfillPhotos).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("%d backfill jobs, want 1", n)
	}

	// Finished: the next trigger starts a new pass.
	close(e.search.Gate)
	if job := restarted.waitJob(t, first); job.Status != jobs.StatusSucceeded {
		t.Fatalf("job = %+v (result %s), want succeeded", job, job.Result)
	}
	next, err := restarted.svc.StartPhotoBackfill(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if next == first {
		t.Fatalf("trigger after the pass finished = %q, want a new job", next)
	}
	restarted.waitJob(t, next)
}

func TestBackfillFailsJobWhenAWriteFails(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.model.QueueGenerate(recipestest.NormalizedReply("stovetop", 40))
	absent := e.failedDinner(t, "Abalone surprise")
	curry := e.failedDinner(t, "Chicken curry")
	if _, err := e.st.Writer.ExecContext(t.Context(), `CREATE TRIGGER refuse_grounded BEFORE UPDATE OF grounding_status ON dinners
		WHEN NEW.id = '`+curry+`' AND NEW.grounding_status = 'grounded'
		BEGIN SELECT RAISE(ABORT, 'grounded refused'); END`); err != nil {
		t.Fatal(err)
	}

	id, err := e.svc.StartPhotoBackfill(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	job := e.waitJob(t, id)
	if job.Status != jobs.StatusFailed || job.Error == nil || *job.Error != jobs.CodeInternal {
		t.Fatalf("job = %+v, want failed with %s", job, jobs.CodeInternal)
	}
	var got struct {
		Dinners []dinners.BackfillOutcome `json:"dinners"`
	}
	if err := json.Unmarshal(job.Result, &got); err != nil {
		t.Fatalf("result %s: %v", job.Result, err)
	}
	// The other dinner was still tried and recorded.
	checkOutcomes(t, got.Dinners, []dinners.BackfillOutcome{
		outcome(absent, dinners.BackfillFailed, ptr(dinners.ReasonNoRecipe)),
		outcome(curry, dinners.BackfillFailed, ptr(dinners.ReasonGroundingError)),
	})
	if row := e.dinner(t, curry); row.GroundingStatus != dinners.GroundingFailed || row.PhotoID.Valid {
		t.Errorf("curry = %+v, want left failed without a photo", row)
	}
}
