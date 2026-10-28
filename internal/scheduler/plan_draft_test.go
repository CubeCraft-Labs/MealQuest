package scheduler_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/harness"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/llm"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/skills"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/gcal"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/household"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/jobs"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/notify"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/notify/notifytest"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/plan"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/scheduler"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

// The tests in this file run plan's plan_draft schedule on the fake clock
// against the real plan service, job runner, harness, draft_week and
// notification outbox, with the AI faked.

// planEnv is a plan service over a scheduler env's store, the "process" a
// scheduler runs it in. model answers draft_week.
type planEnv struct {
	e     *env
	svc   *plan.Service
	model *llm.Fake
	ids   map[string]string
	logs  *syncBuffer
	stop  func()
}

// poolNames are the seven Regulars pool stores, enough for a week.
var poolNames = []string{"Beef Chili", "Pot Roast", "Chicken Fajitas", "Spaghetti Bolognese", "Sheet Pan Salmon", "Tacos", "Pancakes"}

// pool stores poolNames as Regulars and returns their ids by name.
func (e *env) pool() map[string]string {
	e.t.Helper()
	ids := map[string]string{}
	for _, name := range poolNames {
		now := store.Now().String()
		d, err := db.New(e.st.Writer).CreateDinner(context.Background(), db.CreateDinnerParams{
			ID:              store.NewID(),
			Name:            name,
			NormalizedName:  harness.Normalize(name),
			Status:          "regular",
			Origin:          "regular",
			GroundingStatus: "manual",
			CreatedAt:       now,
			UpdatedAt:       now,
		})
		if err != nil {
			e.t.Fatal(err)
		}
		ids[name] = d.ID
	}
	return ids
}

// publicURL is MEALQUEST_PUBLIC_URL in these tests.
var publicURL = &url.URL{Scheme: "https", Host: "meals.example.com"}

// noCalendar is a Calendar the schedule must never push to.
type noCalendar struct{ t *testing.T }

func (c noCalendar) PushEvents(context.Context, []gcal.Event, func() error) error {
	c.t.Error("plan_draft pushed to Calendar")
	return errors.New("no calendar")
}

// plans builds a plan service drafting from the dinners ids over e with a
// started job runner, which stop stops (and the test's end, if not before).
// Its changes do not wake the scheduler: a draft job's wake would add a
// pass the fake clock's waits do not expect.
func (e *env) plans(ids map[string]string) *planEnv {
	e.t.Helper()
	return e.newPlans(ids, func() {})
}

// wakingPlans is plans whose changes wake the scheduler, as in the server.
// A test using it changes plans only from its own goroutine and waits for
// the wake's pass with changed.
func (e *env) wakingPlans(ids map[string]string) *planEnv {
	e.t.Helper()
	return e.newPlans(ids, e.wake)
}

// newPlans is plans whose service calls wake after a plan change.
func (e *env) newPlans(ids map[string]string, wake func()) *planEnv {
	e.t.Helper()
	p := &planEnv{e: e, model: &llm.Fake{}, ids: ids, logs: &syncBuffer{}}
	guard, err := harness.NewGuard(harness.DefaultMaxCalls)
	if err != nil {
		e.t.Fatal(err)
	}
	cfg := harness.Config{
		Settings:    llm.Settings{Provider: "anthropic", Model: "claude-test", Temperature: 0.7},
		NewProvider: func(llm.Settings, string) (llm.Provider, error) { return p.model, nil },
		Store:       e.st,
		Guard:       guard,
	}
	drafter := func(ctx context.Context, scope string, in skills.WeekRequest) (skills.Week, error) {
		return skills.DraftWeek(ctx, cfg, scope, in)
	}
	logger := slog.New(slog.NewTextHandler(p.logs, nil))
	runner, err := jobs.NewRunner(e.st, logger, 1)
	if err != nil {
		e.t.Fatal(err)
	}
	p.svc, err = plan.NewService(e.st, drafter, runner, noCalendar{e.t}, notifytest.New(e.t, e.st), wake, publicURL, logger)
	if err != nil {
		e.t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := runner.Start(ctx); err != nil {
		cancel()
		e.t.Fatal(err)
	}
	var once sync.Once
	p.stop = func() {
		once.Do(func() {
			cancel()
			runner.Wait()
		})
	}
	e.t.Cleanup(p.stop)
	return p
}

// reply scripts the next draft_week answer: poolNames on the days of the
// week starting weekStart, Monday to Sunday.
func (p *planEnv) reply(weekStart store.LocalDate) {
	p.e.t.Helper()
	dates, err := household.WeekDays(weekStart)
	if err != nil {
		p.e.t.Fatal(err)
	}
	var days []string
	for i, name := range poolNames {
		days = append(days, fmt.Sprintf(`{"date":%q,"dinner_id":%q}`, dates[i], p.ids[name]))
	}
	js := fmt.Sprintf(`{"days":[%s],"note":""}`, strings.Join(days, ","))
	p.model.QueueGenerate(llm.GenerateResult{Response: llm.Response{Text: js, JSON: json.RawMessage(js)}})
}

// draftJob is a draft_week jobs row.
type draftJob struct {
	Status, Error string
}

// drafts waits until no draft_week job is queued or running and returns
// them all, oldest first.
func (e *env) drafts() []draftJob {
	e.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		rows, err := e.st.Reader.Query(`SELECT status, COALESCE(error, '') FROM jobs WHERE kind = ? ORDER BY created_at, id`, plan.KindDraftWeek)
		if err != nil {
			e.t.Fatal(err)
		}
		var out []draftJob
		busy := false
		for rows.Next() {
			var j draftJob
			if err := rows.Scan(&j.Status, &j.Error); err != nil {
				e.t.Fatal(err)
			}
			busy = busy || j.Status == string(jobs.StatusQueued) || j.Status == string(jobs.StatusRunning)
			out = append(out, j)
		}
		if err := rows.Err(); err != nil {
			e.t.Fatal(err)
		}
		rows.Close()
		if !busy {
			return out
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("draft jobs still under way: %+v", out)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// wantDrafts fails unless the draft_week jobs are got.
func wantDrafts(t *testing.T, what string, got []draftJob, want ...draftJob) {
	t.Helper()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("%s: draft jobs = %+v, want %+v", what, got, want)
	}
}

// weekPlans returns the week_start of every plan, oldest first.
func (e *env) weekPlans() []string {
	e.t.Helper()
	rows, err := e.st.Reader.Query(`SELECT week_start FROM week_plans ORDER BY week_start`)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var w string
		if err := rows.Scan(&w); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, w)
	}
	if err := rows.Err(); err != nil {
		e.t.Fatal(err)
	}
	return out
}

// planReady returns the distinct plan_ready keys stored, oldest first.
func (e *env) planReady() []string {
	e.t.Helper()
	var keys []string
	for _, r := range notifytest.Rows(e.t, e.st, notify.PlanReady) {
		if len(keys) == 0 || keys[len(keys)-1] != r.Key {
			keys = append(keys, r.Key)
		}
	}
	return keys
}

var (
	succeeded = draftJob{Status: string(jobs.StatusSucceeded)}
	aiFailed  = draftJob{Status: string(jobs.StatusFailed), Error: plan.ReasonAIFailed}
	// w43 is the Monday of 2026-W43, the week after Friday Oct 16.
	w43 = mustLocalDate("2026-10-19")
	w44 = mustLocalDate("2026-10-26")
)

func mustLocalDate(s string) store.LocalDate {
	d, err := store.ParseLocalDate(s)
	if err != nil {
		panic(err)
	}
	return d
}

// TestPlanDraftDraftsNextWeek: at the seeded Friday 09:00 the schedule
// drafts the next ISO week through the plan service's draft job, period
// plan_draft:YYYY-Www of that week, and the draft emits plan_ready keyed
// by its Monday once.
func TestPlanDraftDraftsNextWeek(t *testing.T) {
	e := newEnv(t)
	p := e.plans(e.pool())
	p.reply(w43)
	p.reply(w44)

	// Wednesday Oct 14: the Friday Oct 9 09:00 fire is days ago, missed.
	r := e.start(at(2026, 10, 14, 12, 0), p.svc.DraftSchedule())
	r.runUntil(at(2026, 10, 16, 8, 59))
	wantDrafts(t, "before Friday 09:00", e.drafts())
	wantKeys(t, "plans before Friday 09:00", e.weekPlans(), nil)

	r.runUntil(at(2026, 10, 16, 9, 0))
	wantDrafts(t, "at Friday 09:00", e.drafts(), succeeded)
	wantKeys(t, "plans at Friday 09:00", e.weekPlans(), []string{"2026-10-19"})
	wantKeys(t, "plan_ready of W43", e.planReady(), []string{"plan_ready:2026-10-19"})
	got, err := p.svc.Get(context.Background(), w43)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Days) != 7 || got.Days[0].Dinner.Name != "Beef Chili" || got.Status != plan.StatusDraft {
		t.Errorf("plan of W43 = %+v, want the drafted week", got)
	}

	r.runUntil(at(2026, 10, 23, 9, 0))
	wantDrafts(t, "W44", e.drafts(), succeeded, succeeded)
	wantKeys(t, "plans", e.weekPlans(), []string{"2026-10-19", "2026-10-26"})
	wantKeys(t, "plan_ready of W43 and W44", e.planReady(), []string{"plan_ready:2026-10-19", "plan_ready:2026-10-26"})
	wantKeys(t, "schedule_runs", e.claimed(scheduler.PlanDraft), []string{"2026-W42", "2026-W43", "2026-W44"})
}

// TestPlanDraftLeavesAnExistingPlan: when next week already has a plan,
// here one the household drafted by hand, the period drafts nothing,
// notifies no one and says so.
func TestPlanDraftLeavesAnExistingPlan(t *testing.T) {
	e := newEnv(t)
	p := e.plans(e.pool())
	p.reply(w43)
	p.reply(w43) // a second draft would succeed too
	if _, err := p.svc.StartDraft(context.Background(), w43, false); err != nil {
		t.Fatal(err)
	}
	wantDrafts(t, "manual draft", e.drafts(), succeeded)
	wantKeys(t, "plan_ready of the manual draft", e.planReady(), []string{"plan_ready:2026-10-19"})
	before, err := p.svc.Get(context.Background(), w43)
	if err != nil {
		t.Fatal(err)
	}

	r := e.start(at(2026, 10, 16, 8, 0), p.svc.DraftSchedule())
	r.runUntil(at(2026, 10, 16, 9, 0))
	wantDrafts(t, "with a plan", e.drafts(), succeeded)
	wantKeys(t, "plan_ready with a plan", e.planReady(), []string{"plan_ready:2026-10-19"})
	wantKeys(t, "schedule_runs", e.claimed(scheduler.PlanDraft), []string{"2026-W42", "2026-W43"})
	after, err := p.svc.Get(context.Background(), w43)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Errorf("plan of W43 = %+v, want it unchanged from %+v", after, before)
	}
	if logs := p.logs.String(); !strings.Contains(logs, "the week already has a plan") || !strings.Contains(logs, "period=2026-W43") {
		t.Errorf("logs = %s, want the skipped period logged", logs)
	}
}

// TestPlanDraftNoDuplicateAfterRestart: a restart after the week was
// drafted, still within the catch-up window, drafts nothing more and sends
// no second plan_ready: the period is already claimed.
func TestPlanDraftNoDuplicateAfterRestart(t *testing.T) {
	e := newEnv(t)
	ids := e.pool()
	p := e.plans(ids)
	p.reply(w43)
	r := e.start(at(2026, 10, 16, 8, 0), p.svc.DraftSchedule())
	r.runUntil(at(2026, 10, 16, 9, 0))
	wantDrafts(t, "first process", e.drafts(), succeeded)
	r.stop()
	p.stop()

	// The restarted process has a new job runner, plan service and
	// scheduler; it would answer a second draft if one were started.
	p2 := e.plans(ids)
	p2.reply(w43)
	r2 := e.start(at(2026, 10, 16, 10, 0), p2.svc.DraftSchedule())
	r2.runUntil(at(2026, 10, 23, 8, 59))
	wantDrafts(t, "after restart", e.drafts(), succeeded)
	wantKeys(t, "plans after restart", e.weekPlans(), []string{"2026-10-19"})
	wantKeys(t, "plan_ready after restart", e.planReady(), []string{"plan_ready:2026-10-19"})
	wantKeys(t, "schedule_runs", e.claimed(scheduler.PlanDraft), []string{"2026-W42", "2026-W43"})
}

// TestPlanDraftAIFailureEmitsNothing: when draft_week fails the draft job
// fails as ai_failed with the failure recorded in ai_runs, no plan is made
// up and no one is notified; a restart within the catch-up window does not
// draft the claimed period again.
func TestPlanDraftAIFailureEmitsNothing(t *testing.T) {
	e := newEnv(t)
	ids := e.pool()
	p := e.plans(ids)
	p.model.QueueGenerate(llm.GenerateResult{Err: errors.New("provider down")})

	r := e.start(at(2026, 10, 16, 8, 0), p.svc.DraftSchedule())
	r.runUntil(at(2026, 10, 16, 9, 0))
	wantDrafts(t, "after the AI failed", e.drafts(), aiFailed)
	wantKeys(t, "plans after the AI failed", e.weekPlans(), nil)
	wantKeys(t, "plan_ready after the AI failed", e.planReady(), nil)
	wantKeys(t, "schedule_runs", e.claimed(scheduler.PlanDraft), []string{"2026-W42", "2026-W43"})

	var status, msg string
	if err := e.st.Reader.QueryRow(`SELECT status, COALESCE(error, '') FROM ai_runs WHERE skill = 'draft_week'`).Scan(&status, &msg); err != nil {
		t.Fatalf("ai_runs row of draft_week: %v", err)
	}
	if status != string(harness.StatusProviderError) || msg == "" {
		t.Errorf("ai_runs draft_week = %s %q, want %s with its message", status, msg, harness.StatusProviderError)
	}
	if logs := p.logs.String(); !strings.Contains(logs, "week draft failed") || !strings.Contains(logs, "reason="+plan.ReasonAIFailed) {
		t.Errorf("logs = %s, want the draft failure with reason %s", logs, plan.ReasonAIFailed)
	}

	r.stop()
	p.stop()
	p2 := e.plans(ids)
	p2.reply(w43)
	e.start(at(2026, 10, 16, 10, 0), p2.svc.DraftSchedule())
	wantDrafts(t, "after restart", e.drafts(), aiFailed)
	wantKeys(t, "plan_ready after restart", e.planReady(), nil)
}

// TestPlanDraftFollowsSettingsWithoutRestart: a plan_draft_dow and
// plan_draft_time change takes effect in the running scheduler; the old
// Friday 09:00 no longer fires and the new Wednesday 18:30 drafts the next
// week.
func TestPlanDraftFollowsSettingsWithoutRestart(t *testing.T) {
	e := newEnv(t)
	p := e.plans(e.pool())
	p.reply(w43)

	r := e.start(at(2026, 10, 12, 12, 0), p.svc.DraftSchedule()) // Monday
	e.update(household.SettingsPatch{PlanDraftDow: ptr(3), PlanDraftTime: ptr("18:30")})
	r.changed()
	r.runUntil(at(2026, 10, 14, 18, 29)) // Wednesday
	wantDrafts(t, "before Wednesday 18:30", e.drafts())
	r.runUntil(at(2026, 10, 14, 18, 30))
	wantDrafts(t, "at Wednesday 18:30", e.drafts(), succeeded)
	wantKeys(t, "plan_ready", e.planReady(), []string{"plan_ready:2026-10-19"})

	// Friday 09:00, the old fire time, drafts nothing more.
	r.runUntil(at(2026, 10, 16, 9, 0))
	wantDrafts(t, "at Friday 09:00", e.drafts(), succeeded)
	wantKeys(t, "schedule_runs", e.claimed(scheduler.PlanDraft), []string{"2026-W42", "2026-W43"})
}
