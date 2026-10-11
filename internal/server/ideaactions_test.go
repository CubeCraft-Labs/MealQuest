package server

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"testing"
	"testing/fstest"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/harness"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/llm"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/skills"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/dinners"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/household"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ideas"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/jobs"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/notify/notifytest"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/plan"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/recipes/recipestest"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

// actionsZone is the household zone of newIdeaActionsRouter.
const actionsZone = "America/New_York"

// newIdeaActionsRouter returns a started router whose ideas, dinners and
// plan services share one store, whose household settings are seeded, so
// an idea's actions read the job, make the dinner and swap the plan of the
// same household. The ideas service runs both skills through the real
// harness with the scripted model and grounds with the scripted fakes.
func newIdeaActionsRouter(t *testing.T, model, normalize *llm.Fake, search *recipestest.Search, pages *recipestest.Pages, images *recipestest.Images) (http.Handler, *store.Store) {
	t.Helper()
	st := newTestStore(t)
	if err := household.SeedSettings(t.Context(), st, actionsZone); err != nil {
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
	ideasSvc, err := ideas.NewService(st, g, generate, quick, runner, logger)
	if err != nil {
		t.Fatal(err)
	}
	dinnerSvc, err := dinners.NewService(st, g, runner, func() {}, logger)
	if err != nil {
		t.Fatal(err)
	}
	draft := func(context.Context, string, skills.WeekRequest) (skills.Week, error) {
		t.Error("draft_week ran: an idea action must never draft a plan")
		return skills.Week{}, context.Canceled
	}
	planSvc, err := plan.NewService(st, draft, runner, newTestGoogle(t), notifytest.New(t, st), func() {}, &url.URL{Scheme: "https", Host: "mealquest.example"}, time.Now, logger)
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewRouter(logger, "dev", nil, stubAuth{}, stubSessions{}, newTestSettings(t), newTestSearch(t), newTestAI(t), newTestChat(t),
		dinnerSvc, newTestSuggest(t), planSvc, newTestShopping(t), newTestGoogle(t), newTestPush(t), ideasSvc, newTestHome(t), runner, newTestPhotos(t), fstest.MapFS{"index.html": {Data: []byte(fixtureIndex)}})
	if err != nil {
		t.Fatal(err)
	}
	startRunner(t, runner)
	return h, st
}

// finishedIdeasJob runs an ideas from ingredients job that keeps names, in
// order, and returns the router, store, job id and its ideas' recipe ids.
func finishedIdeasJob(t *testing.T, names ...string) (http.Handler, *store.Store, string, []string) {
	t.Helper()
	model, normalize, search, pages, images := scriptIdeas(t, names, names, nil)
	h, st := newIdeaActionsRouter(t, model, normalize, search, pages, images)
	id := startIdeasJob(t, h)
	job := waitJob(t, h, id)
	if job["status"] != "succeeded" {
		t.Fatalf("ideas job = %v", job)
	}
	var recipeIDs []string
	for _, i := range job["result"].(map[string]any)["ideas"].([]any) {
		recipeIDs = append(recipeIDs, i.(map[string]any)["recipe_id"].(string))
	}
	if len(recipeIDs) != len(names) {
		t.Fatalf("ideas job kept %d ideas, want %d", len(recipeIDs), len(names))
	}
	return h, st, id, recipeIDs
}

func ideaPath(jobID, i, action string) string {
	return "/api/v1/ideas/" + jobID + "/items/" + i + "/" + action
}

// dinnerCount is how many dinners the store holds, archived or not.
func dinnerCount(t *testing.T, st *store.Store) int {
	t.Helper()
	var n int
	if err := st.Reader.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM dinners`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// thisWeek is today in the household's zone and the Monday of its week.
func thisWeek(t *testing.T) (week, today store.LocalDate) {
	t.Helper()
	zone, err := household.LoadZone(actionsZone)
	if err != nil {
		t.Fatal(err)
	}
	today = household.Today(time.Now(), zone)
	return household.WeekStart(today), today
}

// planThisWeek stores a plan of this week with a Regular on every day and
// the given status, and returns the week's Monday and today.
func planThisWeek(t *testing.T, st *store.Store, status string) (week, today store.LocalDate) {
	t.Helper()
	ctx := t.Context()
	week, today = thisWeek(t)
	days, err := household.WeekDays(week)
	if err != nil {
		t.Fatal(err)
	}
	q := db.New(st.Writer)
	now := store.Now().String()
	planID := store.NewID()
	if err := q.CreateWeekPlan(ctx, db.CreateWeekPlanParams{ID: planID, WeekStart: week.String(), CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	for _, date := range days {
		name := "Regular " + date.String()
		d, err := q.CreateDinner(ctx, db.CreateDinnerParams{
			ID: store.NewID(), Name: name, NormalizedName: harness.Normalize(name), Status: "regular", Origin: "regular",
			GroundingStatus: "manual", CreatedAt: now, UpdatedAt: now,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := q.PutPlanDay(ctx, db.PutPlanDayParams{ID: store.NewID(), PlanID: planID, Date: date.String(),
			DinnerID: sql.NullString{String: d.ID, Valid: true}, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.Writer.ExecContext(ctx, `UPDATE week_plans SET status = ? WHERE id = ?`, status, planID); err != nil {
		t.Fatal(err)
	}
	return week, today
}

// planRevision is the revision of the plan of week.
func planRevision(t *testing.T, st *store.Store, week store.LocalDate) int64 {
	t.Helper()
	row, err := db.New(st.Reader).GetWeekPlan(t.Context(), week.String())
	if err != nil {
		t.Fatal(err)
	}
	return row.Revision
}

// Adding an idea to the Quest log makes a quest_log dinner grounded in the
// idea's own recipe, with origin ingredients, without grounding it again;
// asking again answers the same dinner and makes no second one.
func TestPostIdeaQuestLogAddsGroundedDinnerOnce(t *testing.T) {
	t.Parallel()
	h, st, id, recipeIDs := finishedIdeasJob(t, "Chicken Fried Rice", "Rice Pudding")

	rec, d := callDinners(t, h, http.MethodPost, ideaPath(id, "1", "quest-log"), "")
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST quest-log = %d: %s", rec.Code, rec.Body)
	}
	if d["name"] != "Rice Pudding" || d["status"] != "quest_log" || d["origin"] != "ingredients" ||
		d["grounding_status"] != "grounded" || d["recipe_id"] != recipeIDs[1] || d["photo_url"] == nil ||
		d["method"] != "stovetop" || d["minutes_total"] != 25.0 || d["updated_by"] != testSession.Subject {
		t.Fatalf("dinner = %v, want Rice Pudding in the Quest log grounded in %s", d, recipeIDs[1])
	}

	rec, again := callDinners(t, h, http.MethodPost, ideaPath(id, "1", "quest-log"), "")
	if rec.Code != http.StatusOK || again["id"] != d["id"] || again["status"] != "quest_log" {
		t.Fatalf("POST quest-log again = %d %v, want 200 with dinner %v", rec.Code, again, d["id"])
	}
	if n := dinnerCount(t, st); n != 1 {
		t.Fatalf("dinners = %d, want 1", n)
	}
	if names := listNames(t, h, "quest_log"); len(names) != 1 || names[0] != "Rice Pudding" {
		t.Fatalf("Quest log = %q", names)
	}
}

// A Quick dinners idea makes a dinner with origin quick.
func TestPostIdeaQuestLogQuickOrigin(t *testing.T) {
	t.Parallel()
	model, normalize, search, pages, images := newQuickFakes()
	quickRecipe(t, normalize, search, pages, images, "Chicken Fried Rice", "Chicken Fried Rice", 25)
	quickReply(model, [3]any{"Chicken Fried Rice", "stovetop", 20})
	h, _ := newIdeaActionsRouter(t, model, normalize, search, pages, images)
	rec, body := callDinners(t, h, http.MethodPost, quickPath, `{"kind": "under30", "exclude": []}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST quick = %d: %s", rec.Code, rec.Body)
	}
	id := body["jobId"].(string)
	if job := waitJob(t, h, id); job["status"] != "succeeded" {
		t.Fatalf("quick job = %v", job)
	}

	rec, d := callDinners(t, h, http.MethodPost, ideaPath(id, "0", "quest-log"), "")
	if rec.Code != http.StatusCreated || d["name"] != "Chicken Fried Rice" || d["status"] != "quest_log" || d["origin"] != "quick" {
		t.Fatalf("POST quest-log = %d %v, want a quick Quest log dinner", rec.Code, d)
	}
}

// An idea repeating a dinner the household already has is that dinner, as
// it is: a Regular stays a Regular. One awaiting the household's choice
// is refused and nothing changes.
func TestPostIdeaQuestLogReusesExistingDinner(t *testing.T) {
	t.Parallel()
	h, st, id, _ := finishedIdeasJob(t, "Chicken Fried Rice", "Rice Pudding")
	regular := createDinner(t, h, `{"name": "chicken fried rice"}`)

	rec, d := callDinners(t, h, http.MethodPost, ideaPath(id, "0", "quest-log"), "")
	if rec.Code != http.StatusOK || d["id"] != regular || d["status"] != "regular" || d["origin"] != "regular" {
		t.Fatalf("POST quest-log = %d %v, want 200 with Regular %s as it is", rec.Code, d, regular)
	}

	now := store.Now().String()
	if _, err := db.New(st.Writer).CreateDinner(t.Context(), db.CreateDinnerParams{
		ID: store.NewID(), Name: "Rice pudding", NormalizedName: harness.Normalize("Rice pudding"), Status: "suggested",
		Origin: "side_quest", GroundingStatus: "manual", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	rec, body := callDinners(t, h, http.MethodPost, ideaPath(id, "1", "quest-log"), "")
	if rec.Code != http.StatusConflict || errorCode(body) != "duplicate_dinner" {
		t.Fatalf("POST quest-log of a suggested Side quest = %d %v, want 409 duplicate_dinner", rec.Code, body)
	}
	if n := dinnerCount(t, st); n != 2 {
		t.Fatalf("dinners = %d, want the Regular and the Side quest only", n)
	}
}

// Cook tonight puts the idea's dinner on today of this week's plan through
// the plan's swap: the day is new, the plan's revision rises and a synced
// plan becomes changed. Asking again plans the same dinner.
func TestPostIdeaCookTonightSwapsToday(t *testing.T) {
	t.Parallel()
	h, st, id, recipeIDs := finishedIdeasJob(t, "Chicken Fried Rice")
	week, today := planThisWeek(t, st, plan.StatusSynced)
	before := planRevision(t, st, week)

	rec, body := callDinners(t, h, http.MethodPost, ideaPath(id, "0", "cook-tonight"), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("POST cook-tonight = %d: %s", rec.Code, rec.Body)
	}
	d := body["dinner"].(map[string]any)
	if d["name"] != "Chicken Fried Rice" || d["status"] != "quest_log" || d["origin"] != "ingredients" || d["recipe_id"] != recipeIDs[0] {
		t.Fatalf("dinner = %v", d)
	}
	assertTonight := func(body map[string]any) {
		t.Helper()
		p := body["plan"].(map[string]any)
		if p["week_start"] != week.String() || p["status"] != plan.StatusChanged {
			t.Fatalf("plan = %v, want week %s changed", p, week)
		}
		found := false
		for _, day := range p["days"].([]any) {
			day := day.(map[string]any)
			planned := day["dinner"].(map[string]any)["id"] == d["id"]
			if day["date"] == today.String() {
				found = true
				if !planned || day["is_new"] != true {
					t.Fatalf("today %s = %v, want %v new", today, day, d["id"])
				}
			} else if planned {
				t.Fatalf("dinner also on %v", day["date"])
			}
		}
		if !found {
			t.Fatalf("plan has no day %s", today)
		}
	}
	assertTonight(body)
	if after := planRevision(t, st, week); after != before+1 {
		t.Fatalf("plan revision = %d, want %d", after, before+1)
	}

	rec, body = callDinners(t, h, http.MethodPost, ideaPath(id, "0", "cook-tonight"), "")
	if rec.Code != http.StatusOK || body["dinner"].(map[string]any)["id"] != d["id"] {
		t.Fatalf("POST cook-tonight again = %d %v", rec.Code, body)
	}
	assertTonight(body)
	if n := dinnerCount(t, st); n != 8 {
		t.Fatalf("dinners = %d, want the seven Regulars and one idea", n)
	}
}

// Cook tonight reuses a dinner the household already has.
func TestPostIdeaCookTonightReusesExistingDinner(t *testing.T) {
	t.Parallel()
	h, st, id, _ := finishedIdeasJob(t, "Chicken Fried Rice")
	_, today := planThisWeek(t, st, plan.StatusDraft)
	regular := createDinner(t, h, `{"name": "Chicken fried rice"}`)

	rec, body := callDinners(t, h, http.MethodPost, ideaPath(id, "0", "cook-tonight"), "")
	if rec.Code != http.StatusOK || body["dinner"].(map[string]any)["id"] != regular {
		t.Fatalf("POST cook-tonight = %d %v, want Regular %s", rec.Code, body, regular)
	}
	for _, day := range body["plan"].(map[string]any)["days"].([]any) {
		day := day.(map[string]any)
		if day["date"] == today.String() && (day["dinner"].(map[string]any)["id"] != regular || day["is_new"] != false) {
			t.Fatalf("today = %v, want Regular %s", day, regular)
		}
	}
}

// With no plan for this week, cook tonight answers 409 no_plan and changes
// nothing: no dinner is made and no plan is drafted or stored.
func TestPostIdeaCookTonightWithoutPlan(t *testing.T) {
	t.Parallel()
	h, st, id, _ := finishedIdeasJob(t, "Chicken Fried Rice")

	rec, body := callDinners(t, h, http.MethodPost, ideaPath(id, "0", "cook-tonight"), "")
	if rec.Code != http.StatusConflict || errorCode(body) != "no_plan" {
		t.Fatalf("POST cook-tonight = %d %v, want 409 no_plan", rec.Code, body)
	}
	if n := dinnerCount(t, st); n != 0 {
		t.Fatalf("dinners = %d, want none", n)
	}
	var plans, drafts int
	if err := st.Reader.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM week_plans`).Scan(&plans); err != nil {
		t.Fatal(err)
	}
	if err := st.Reader.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM jobs WHERE kind = ?`, plan.KindDraftWeek).Scan(&drafts); err != nil {
		t.Fatal(err)
	}
	if plans != 0 || drafts != 0 {
		t.Fatalf("plans = %d, drafts = %d, want none", plans, drafts)
	}
}

// An idea that is not an index of a succeeded ideas job is refused.
func TestPostIdeaActionsRefuse(t *testing.T) {
	t.Parallel()
	h, st, id, _ := finishedIdeasJob(t, "Chicken Fried Rice")
	planThisWeek(t, st, plan.StatusDraft)
	// A Regular's grounding job is a job, but finds no ideas.
	rec, body := callDinners(t, h, http.MethodPost, dinnersPath, `{"name": "Beef Stew"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /dinners = %d: %s", rec.Code, rec.Body)
	}
	other := body["grounding_job_id"].(string)

	for _, action := range []string{"quest-log", "cook-tonight"} {
		for _, tc := range []struct {
			job, i string
			status int
			code   string
		}{
			{id, "x", http.StatusBadRequest, "invalid_request"},
			{id, "-1", http.StatusBadRequest, "invalid_request"},
			{id, "01", http.StatusBadRequest, "invalid_request"},
			{id, "1", http.StatusNotFound, "not_found"},
			{"unknown", "0", http.StatusNotFound, "not_found"},
			{other, "0", http.StatusNotFound, "not_found"},
		} {
			rec, body := callDinners(t, h, http.MethodPost, ideaPath(tc.job, tc.i, action), "")
			if rec.Code != tc.status || errorCode(body) != tc.code {
				t.Errorf("POST %s %s/%s = %d %v, want %d %s", action, tc.job, tc.i, rec.Code, body, tc.status, tc.code)
			}
		}
	}
	if n := dinnerCount(t, st); n != 8 {
		t.Fatalf("dinners = %d, want the plan's seven Regulars and Beef Stew only", n)
	}
}

// While the job still finds ideas, an idea is not acted on: its index may
// still move.
func TestPostIdeaActionsWhileJobRuns(t *testing.T) {
	t.Parallel()
	model, normalize, search, pages, images := scriptIdeas(t, []string{"Chicken Fried Rice"}, []string{"Chicken Fried Rice"}, nil)
	gate := make(chan struct{})
	search.Gates = map[string]chan struct{}{"Chicken Fried Rice": gate}
	h, st := newIdeaActionsRouter(t, model, normalize, search, pages, images)
	id := startIdeasJob(t, h)

	for _, action := range []string{"quest-log", "cook-tonight"} {
		rec, body := callDinners(t, h, http.MethodPost, ideaPath(id, "0", action), "")
		if rec.Code != http.StatusConflict || errorCode(body) != "ideas_not_ready" {
			t.Errorf("POST %s = %d %v, want 409 ideas_not_ready", action, rec.Code, body)
		}
	}
	close(gate)
	waitJob(t, h, id)
	if n := dinnerCount(t, st); n != 0 {
		t.Fatalf("dinners = %d, want none", n)
	}
}
