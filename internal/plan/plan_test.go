package plan_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/harness"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/llm"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/skills"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/chat"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/dinners"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/gcal"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/gcal/gcaltest"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/household"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/jobs"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/notify"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/notify/notifytest"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/plan"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/secretbox"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/migrations"
)

// week is the Monday the tests draft, and weekDates its days.
var (
	week      = mustDate("2026-10-12")
	weekDates = []string{"2026-10-12", "2026-10-13", "2026-10-14", "2026-10-15", "2026-10-16", "2026-10-17", "2026-10-18"}
)

// publicURL is MEALQUEST_PUBLIC_URL in these tests.
var publicURL = &url.URL{Scheme: "https", Host: "meals.example.com"}

func mustDate(s string) store.LocalDate {
	d, err := store.ParseLocalDate(s)
	if err != nil {
		panic(err)
	}
	return d
}

type env struct {
	svc    *plan.Service
	st     *store.Store
	runner *jobs.Runner
	model  *llm.Fake
	// google is the fake Google the service pushes to through gcal.
	google *gcaltest.Google
	gcal   *gcal.Service
	// ids are the seeded dinners' ids by name.
	ids map[string]string

	mu     sync.Mutex
	inputs []skills.WeekRequest
}

// newEnv returns a plan service over a migrated store in zone
// America/New_York with dinner time 17:30, the Regulars below and Quest
// log dinners Pad Thai and Shawarma Bowls, and an unstarted job runner
// with two workers. It drafts through the real harness and draft_week with
// the scripted e.model, and pushes through gcal to e.google, which starts
// unconnected.
func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := t.Context()
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
	if err := household.SeedSettings(ctx, st, "America/New_York"); err != nil {
		t.Fatal(err)
	}
	e := &env{st: st, model: &llm.Fake{}, ids: map[string]string{}}
	for _, d := range []struct {
		name, status, method string
		minutes              int64
	}{
		{"Beef Chili", "regular", "crockpot", 240},
		{"Pot Roast", "regular", "crockpot", 0},
		{"Chicken Fajitas", "regular", "stovetop", 30},
		{"Spaghetti Bolognese", "regular", "stovetop", 45},
		{"Sheet Pan Salmon", "regular", "oven", 35},
		{"Pad Thai", "quest_log", "stovetop", 35},
		{"Shawarma Bowls", "quest_log", "oven", 40},
	} {
		e.ids[d.name] = e.dinner(t, d.name, d.status, d.method, d.minutes)
	}
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
	drafter := func(ctx context.Context, scope string, in skills.WeekRequest) (skills.Week, error) {
		e.mu.Lock()
		e.inputs = append(e.inputs, in)
		e.mu.Unlock()
		return skills.DraftWeek(ctx, cfg, scope, in)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	// Two workers, as cmd/server runs, so drafts of a week could overlap.
	e.runner, err = jobs.NewRunner(st, logger, 2)
	if err != nil {
		t.Fatal(err)
	}
	e.google = gcaltest.New(t, "https://meals.example.com"+gcal.CallbackPath)
	key := make([]byte, secretbox.KeySize)
	rand.Read(key)
	box, err := secretbox.New(base64.StdEncoding.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}
	e.gcal, err = gcal.NewService(st, box, gcal.Config{
		ClientID:     gcaltest.ClientID,
		ClientSecret: gcaltest.ClientSecret,
		PublicURL:    publicURL,
		SecretKey:    []byte("server-secret-key"),
		Endpoint:     e.google.Endpoint(),
		RevokeURL:    e.google.RevokeURL(),
		CalendarAPI:  e.google.CalendarAPI(),
	})
	if err != nil {
		t.Fatal(err)
	}
	e.svc, err = plan.NewService(st, drafter, e.runner, e.gcal, notifytest.New(t, st), func() {}, publicURL, logger)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// input returns the request of the ith draft_week run.
func (e *env) input(i int) skills.WeekRequest {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.inputs[i]
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

// dinner stores a dinner and returns its id; minutes 0 is unknown.
func (e *env) dinner(t *testing.T, name, status, method string, minutes int64) string {
	t.Helper()
	now := store.Now().String()
	origin := "regular"
	if status != "regular" {
		origin = "side_quest"
	}
	d, err := db.New(e.st.Writer).CreateDinner(t.Context(), db.CreateDinnerParams{
		ID:              store.NewID(),
		Name:            name,
		NormalizedName:  harness.Normalize(name),
		Status:          status,
		Origin:          origin,
		Method:          sql.NullString{String: method, Valid: method != ""},
		MinutesTotal:    sql.NullInt64{Int64: minutes, Valid: minutes > 0},
		GroundingStatus: "manual",
		CreatedAt:       now,
		UpdatedAt:       now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return d.ID
}

// reply scripts one draft_week reply planning the dinners named names on
// weekDates, in order.
func (e *env) reply(note string, names ...string) {
	e.model.QueueGenerate(e.weekReply(note, names...))
}

// weekReply is the draft_week reply planning the dinners named names on
// weekDates, in order.
func (e *env) weekReply(note string, names ...string) llm.GenerateResult {
	return e.weekReplyOn(weekDates, note, names...)
}

// weekReplyOn is the draft_week reply planning the dinners named names on
// dates, in order.
func (e *env) weekReplyOn(dates []string, note string, names ...string) llm.GenerateResult {
	var days []string
	for i, name := range names {
		days = append(days, fmt.Sprintf(`{"date":%q,"dinner_id":%q}`, dates[i], e.ids[name]))
	}
	js := fmt.Sprintf(`{"days":[%s],"note":%q}`, strings.Join(days, ","), note)
	return llm.GenerateResult{Response: llm.Response{Text: js, JSON: json.RawMessage(js)}}
}

// fullWeek plans every seeded dinner, Beef Chili on Monday and Pot Roast
// on Tuesday.
var fullWeek = []string{"Beef Chili", "Pot Roast", "Pad Thai", "Chicken Fajitas", "Shawarma Bowls", "Spaghetti Bolognese", "Sheet Pan Salmon"}

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
			t.Fatalf("job %s still %s", id, job.Status)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// draft asks for a draft of week with confirm and waits for its job.
func (e *env) draft(t *testing.T, confirm bool) jobs.Job {
	t.Helper()
	d, err := e.svc.StartDraft(t.Context(), week, confirm)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Created {
		t.Fatalf("draft job %s was already queued", d.JobID)
	}
	return e.waitJob(t, d.JobID)
}

// mustDraft drafts week with confirm, requiring the job to succeed, and
// returns the plan.
func (e *env) mustDraft(t *testing.T, confirm bool) plan.Plan {
	t.Helper()
	job := e.draft(t, confirm)
	if job.Status != jobs.StatusSucceeded {
		t.Fatalf("draft job %s, error %v", job.Status, *job.Error)
	}
	p, err := e.svc.Get(t.Context(), week)
	if err != nil {
		t.Fatal(err)
	}
	var res struct {
		WeekStart string `json:"week_start"`
		PlanID    string `json:"plan_id"`
	}
	if err := json.Unmarshal(job.Result, &res); err != nil {
		t.Fatal(err)
	}
	if res.WeekStart != week.String() || res.PlanID != p.ID {
		t.Fatalf("draft result = %+v, want week %s plan %s", res, week, p.ID)
	}
	return p
}

// setStatus sets the plan of week to status, as Push to Calendar would.
func (e *env) setStatus(t *testing.T, status string) {
	t.Helper()
	if _, err := e.st.Writer.ExecContext(t.Context(), `UPDATE week_plans SET status = ?, calendar_synced_at = ? WHERE week_start = ?`,
		status, store.Now().String(), week.String()); err != nil {
		t.Fatal(err)
	}
}

func dayNames(p plan.Plan) []string {
	var names []string
	for _, d := range p.Days {
		names = append(names, d.Dinner.Name)
	}
	return names
}

func TestDraftCreatesPlan(t *testing.T) {
	e := newEnv(t)
	e.start(t)
	e.reply("Five Regulars and two from your Quest log.", fullWeek...)

	p := e.mustDraft(t, false)

	if p.Status != plan.StatusDraft || p.WeekStart != "2026-10-12" || p.WeekLabel != "Week 42 · Oct 12 – 18" {
		t.Errorf("plan = %s %s %q, want draft 2026-10-12 \"Week 42 · Oct 12 – 18\"", p.Status, p.WeekStart, p.WeekLabel)
	}
	if p.AINote == nil || *p.AINote != "Five Regulars and two from your Quest log." {
		t.Errorf("ai_note = %v", p.AINote)
	}
	if p.CalendarSyncedAt != nil {
		t.Errorf("calendar_synced_at = %v, want null", *p.CalendarSyncedAt)
	}
	if got := dayNames(p); !slices.Equal(got, fullWeek) {
		t.Errorf("days = %v, want %v", got, fullWeek)
	}
	for i, d := range p.Days {
		if d.Date != weekDates[i] {
			t.Errorf("day %d date = %s, want %s", i, d.Date, weekDates[i])
		}
		wantNew := d.Dinner.Name == "Pad Thai" || d.Dinner.Name == "Shawarma Bowls"
		if d.IsNew != wantNew {
			t.Errorf("%s is_new = %v, want %v", d.Dinner.Name, d.IsNew, wantNew)
		}
		if d.Dinner.ID != e.ids[d.Dinner.Name] {
			t.Errorf("%s id = %s", d.Dinner.Name, d.Dinner.ID)
		}
	}
	chili := p.Days[0]
	if chili.Dinner.Method == nil || *chili.Dinner.Method != "crockpot" || chili.Dinner.Minutes == nil || *chili.Dinner.Minutes != 240 || chili.Dinner.Origin != "regular" {
		t.Errorf("Beef Chili dinner = %+v", chili.Dinner)
	}

	// The pool is every Regular and Quest log dinner; last week has no
	// plan.
	in := e.input(0)
	var pool []string
	for _, c := range in.Pool {
		pool = append(pool, c.Name+"/"+c.Kind)
	}
	slices.Sort(pool)
	want := []string{"Beef Chili/regular", "Chicken Fajitas/regular", "Pad Thai/quest_log", "Pot Roast/regular",
		"Shawarma Bowls/quest_log", "Sheet Pan Salmon/regular", "Spaghetti Bolognese/regular"}
	if !slices.Equal(pool, want) {
		t.Errorf("pool = %v, want %v", pool, want)
	}
	if in.WeekStart != week || in.DinnerTime != "17:30" || len(in.LastWeek) != 0 {
		t.Errorf("request week %s dinner time %s last week %v", in.WeekStart, in.DinnerTime, in.LastWeek)
	}
}

// TestDraftEmitsPlanReady: the AI's first draft of a week stores plan_ready
// keyed by the week, on push and Google Chat, naming the dinners Monday to
// Sunday; a redraft (Rebuild week) and a manual swap store nothing more.
func TestDraftEmitsPlanReady(t *testing.T) {
	e := newEnv(t)
	e.start(t)
	e.reply("", fullWeek...)
	e.mustDraft(t, false)

	zone, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	want := chat.PlanReady(week, household.WeekStart(household.Today(time.Now(), zone)), fullWeek)
	rows := notifytest.Rows(t, e.st, notify.PlanReady)
	if len(rows) != 2 || rows[0].Channel != notify.ChannelChat || rows[1].Channel != notify.ChannelPush {
		t.Fatalf("plan_ready rows = %+v, want one on chat and one on push", rows)
	}
	for _, r := range rows {
		p := r.Payload
		if r.Key != "plan_ready:2026-10-12" || p.Tag != r.Key || p.URL != "/plan?week=2026-10-12" || p.Week != "2026-10-12" ||
			p.Title != want.Title || p.Body != "Beef Chili, Pot Roast, Pad Thai + 4 more" {
			t.Errorf("%s row = %+v, want key plan_ready:2026-10-12, title %q, the drafted dinners and /plan?week=2026-10-12", r.Channel, r, want.Title)
		}
	}

	reversed := slices.Clone(fullWeek)
	slices.Reverse(reversed)
	e.reply("", reversed...)
	e.mustDraft(t, false)
	e.ids["Fish Tacos"] = e.dinner(t, "Fish Tacos", "regular", "stovetop", 25)
	if _, err := e.svc.Swap(t.Context(), week, "2026-10-14", e.ids["Fish Tacos"]); err != nil {
		t.Fatal(err)
	}
	if again := notifytest.Rows(t, e.st, notify.PlanReady); !slices.Equal(again, rows) {
		t.Errorf("plan_ready rows after a redraft and a swap = %+v, want the first draft's only", again)
	}
}

// TestFailedDraftEmitsNothing: a draft that stores no plan notifies no one.
func TestFailedDraftEmitsNothing(t *testing.T) {
	e := newEnv(t)
	e.start(t)
	e.model.QueueGenerate(llm.GenerateResult{Err: errors.New("provider down")})
	if job := e.draft(t, false); job.Status != jobs.StatusFailed {
		t.Fatalf("job %s, want failed", job.Status)
	}
	if rows := notifytest.Rows(t, e.st, notify.PlanReady); len(rows) != 0 {
		t.Errorf("plan_ready rows = %+v, want none", rows)
	}
}

// TestEmitDayOfReminder: the reminder of a planned date names its dinner,
// keyed by the date; a date with no plan emits nothing, and a date emitted
// again stores nothing more.
func TestEmitDayOfReminder(t *testing.T) {
	e := newEnv(t)
	e.start(t)
	if ok, err := e.svc.EmitDayOfReminder(t.Context(), mustDate("2026-10-14")); ok || err != nil {
		t.Fatalf("EmitDayOfReminder with no plan = %v, %v, want false", ok, err)
	}
	e.reply("", fullWeek...)
	e.mustDraft(t, false)
	for range 2 {
		if ok, err := e.svc.EmitDayOfReminder(t.Context(), mustDate("2026-10-14")); !ok || err != nil {
			t.Fatalf("EmitDayOfReminder = %v, %v, want true", ok, err)
		}
	}
	rows := notifytest.Rows(t, e.st, notify.DayOfReminder)
	if len(rows) != 1 {
		t.Fatalf("day_of_reminder rows = %+v, want one on push", rows)
	}
	r := rows[0]
	if r.Key != "day_of_reminder:2026-10-14" || r.Channel != notify.ChannelPush || r.Payload.Title != "Tonight: Pad Thai · Recipe →" ||
		r.Payload.Body != "" || r.Payload.URL != "/dinner/"+e.ids["Pad Thai"] || r.Payload.Week != "2026-10-12" ||
		r.Payload.Dinner == nil || *r.Payload.Dinner != (notify.Dinner{ID: e.ids["Pad Thai"], Name: "Pad Thai"}) {
		t.Errorf("day_of_reminder row = %+v", r)
	}
}

// TestEmitDayOfReminderArchivedDinnerOffersNoRecipe: a dinner archived
// after the plan was made has no recipe, so neither its day-of reminder nor
// its crock-pot start reminder carries or offers one: no recipe URL, no
// dinner and no "Recipe →"; each opens the week's plan and says the recipe
// is gone. Another night's dinner still offers its recipe.
func TestEmitDayOfReminderArchivedDinnerOffersNoRecipe(t *testing.T) {
	e := newEnv(t)
	e.start(t)
	e.reply("", fullWeek...)
	e.mustDraft(t, false)
	if _, err := e.st.Writer.ExecContext(t.Context(), `UPDATE dinners SET status = 'archived' WHERE id = ?`, e.ids["Beef Chili"]); err != nil {
		t.Fatal(err)
	}
	for _, date := range []string{"2026-10-12", "2026-10-14"} {
		if ok, err := e.svc.EmitDayOfReminder(t.Context(), mustDate(date)); !ok || err != nil {
			t.Fatalf("EmitDayOfReminder(%s) = %v, %v, want true", date, ok, err)
		}
	}
	if ok, err := e.svc.EmitStartReminder(t.Context(), mustDate("2026-10-12")); !ok || err != nil {
		t.Fatalf("EmitStartReminder = %v, %v, want true", ok, err)
	}
	got := map[string]notify.Payload{}
	for _, r := range notifytest.Rows(t, e.st, notify.DayOfReminder) {
		got[r.Key] = r.Payload
	}
	for key, want := range map[string]notify.Payload{
		"day_of_reminder:2026-10-12": {
			Event: notify.DayOfReminder, Title: "Tonight: Beef Chili", Body: "This dinner was removed, so its recipe is gone.",
			URL: "/plan?week=2026-10-12", Tag: "day_of_reminder:2026-10-12", Week: "2026-10-12",
		},
		"day_of_reminder:start:2026-10-12": {
			Event: notify.DayOfReminder, Title: "Start the crock pot by 1:30 pm", Body: "Beef Chili",
			URL: "/plan?week=2026-10-12", Tag: "day_of_reminder:start:2026-10-12", Week: "2026-10-12",
		},
		"day_of_reminder:2026-10-14": {
			Event: notify.DayOfReminder, Title: "Tonight: Pad Thai · Recipe →", URL: "/dinner/" + e.ids["Pad Thai"],
			Tag: "day_of_reminder:2026-10-14", Dinner: &notify.Dinner{ID: e.ids["Pad Thai"], Name: "Pad Thai"}, Week: "2026-10-12",
		},
	} {
		if p, ok := got[key]; !ok || !reflect.DeepEqual(p, want) {
			t.Errorf("%s = %+v (stored %v), want %+v", key, p, ok, want)
		}
	}
	for key, p := range got {
		if p.URL != "/dinner/"+e.ids["Pad Thai"] && (strings.HasPrefix(p.URL, "/dinner/") || p.Dinner != nil || strings.Contains(p.Title, "Recipe")) {
			t.Errorf("%s offers a recipe for the archived Beef Chili: %+v", key, p)
		}
	}
}

func TestStartByIsDinnerTimeMinusMinutesForCrockPot(t *testing.T) {
	e := newEnv(t)
	e.start(t)
	e.reply("", fullWeek...)
	p := e.mustDraft(t, false)

	newYork, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	// Beef Chili: crock pot, 240 minutes, dinner at 17:30 EDT.
	if got, want := p.Days[0].StartBy, time.Date(2026, 10, 12, 13, 30, 0, 0, newYork); got == nil || !got.Equal(want) {
		t.Errorf("Beef Chili start_by = %v, want %v", got, want)
	}
	// Pot Roast is a crock-pot dinner with no minutes; the rest are not
	// crock-pot dinners.
	for _, d := range p.Days[1:] {
		if d.StartBy != nil {
			t.Errorf("%s start_by = %v, want null", d.Dinner.Name, *d.StartBy)
		}
	}
	if p.AINote != nil {
		t.Errorf("ai_note = %q, want null for an empty note", *p.AINote)
	}

	// start_by follows the household's dinner time.
	dinnerTime := "18:45"
	if _, err := household.NewSettingsService(e.st).Update(t.Context(), household.SettingsPatch{DinnerTime: &dinnerTime}, "member-1"); err != nil {
		t.Fatal(err)
	}
	p, err = e.svc.Get(t.Context(), week)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := p.Days[0].StartBy, time.Date(2026, 10, 12, 14, 45, 0, 0, newYork); got == nil || !got.Equal(want) {
		t.Errorf("Beef Chili start_by = %v, want %v", got, want)
	}
	if b, err := json.Marshal(p.Days[0]); err != nil || !strings.Contains(string(b), `"start_by":"2026-10-12T14:45:00-04:00"`) {
		t.Errorf("day JSON = %s, %v; want start_by in the household zone", b, err)
	}
}

func TestStartByAcrossDaylightSavingTimeEnd(t *testing.T) {
	e := newEnv(t)
	// New York leaves daylight saving time at 02:00 on Sunday 2026-11-01.
	// Beef Chili (240 minutes) is planned Monday to Saturday and a 20-hour
	// crock-pot dinner on Sunday, so its start crosses the change.
	stew := e.dinner(t, "Overnight Stew", "regular", "crockpot", 1200)
	q := db.New(e.st.Writer)
	now := store.Now().String()
	id := store.NewID()
	dst := mustDate("2026-10-26")
	if err := q.CreateWeekPlan(t.Context(), db.CreateWeekPlanParams{ID: id, WeekStart: dst.String(), CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	dates := []string{"2026-10-26", "2026-10-27", "2026-10-28", "2026-10-29", "2026-10-30", "2026-10-31", "2026-11-01"}
	for i, date := range dates {
		dinner := e.ids["Beef Chili"]
		if i == 6 {
			dinner = stew
		}
		if err := q.PutPlanDay(t.Context(), db.PutPlanDayParams{ID: store.NewID(), PlanID: id, Date: date,
			DinnerID: sql.NullString{String: dinner, Valid: true}, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	p, err := e.svc.Get(t.Context(), dst)
	if err != nil {
		t.Fatal(err)
	}
	// Saturday's dinner is 17:30 EDT; Sunday's is 17:30 EST, 22:30 UTC, and
	// twenty hours before it is 22:30 EDT on Saturday.
	for _, tc := range []struct {
		day  int
		want string
	}{
		{5, `"2026-10-31T13:30:00-04:00"`},
		{6, `"2026-10-31T22:30:00-04:00"`},
	} {
		b, err := json.Marshal(p.Days[tc.day].StartBy)
		if err != nil || string(b) != tc.want {
			t.Errorf("%s start_by = %s, %v; want %s", dates[tc.day], b, err, tc.want)
		}
	}
	// Swapped to Short Ribs (240 minutes), Sunday starts at 13:30 EST.
	ribs := e.dinner(t, "Short Ribs", "regular", "crockpot", 240)
	if _, err := e.svc.Swap(t.Context(), dst, "2026-11-01", ribs); err != nil {
		t.Fatal(err)
	}
	if p, err = e.svc.Get(t.Context(), dst); err != nil {
		t.Fatal(err)
	}
	if b, err := json.Marshal(p.Days[6].StartBy); err != nil || string(b) != `"2026-11-01T13:30:00-05:00"` {
		t.Errorf("Sunday Short Ribs start_by = %s, %v; want 13:30 EST", b, err)
	}
}

func TestDraftGivesLastWeek(t *testing.T) {
	e := newEnv(t)
	e.start(t)
	last := mustDate("2026-10-05")
	q := db.New(e.st.Writer)
	now := store.Now().String()
	id := store.NewID()
	if err := q.CreateWeekPlan(t.Context(), db.CreateWeekPlanParams{ID: id, WeekStart: last.String(), CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := q.PutPlanDay(t.Context(), db.PutPlanDayParams{ID: store.NewID(), PlanID: id, Date: "2026-10-06",
		DinnerID: sql.NullString{String: e.ids["Chicken Fajitas"], Valid: true}, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	e.reply("", fullWeek...)
	e.mustDraft(t, false)
	if got := e.input(0).LastWeek; !slices.Equal(got, []string{e.ids["Chicken Fajitas"]}) {
		t.Errorf("last week = %v, want Chicken Fajitas", got)
	}
}

func TestDraftPoolLeavesOutQuestLogPlannedOnAnotherWeek(t *testing.T) {
	e := newEnv(t)
	e.start(t)
	// Pad Thai is on a later week's plan, so the Quest log no longer
	// offers it; an eighth dinner keeps the pool at seven.
	q := db.New(e.st.Writer)
	now := store.Now().String()
	later := store.NewID()
	if err := q.CreateWeekPlan(t.Context(), db.CreateWeekPlanParams{ID: later, WeekStart: "2099-01-05", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := q.PutPlanDay(t.Context(), db.PutPlanDayParams{ID: store.NewID(), PlanID: later, Date: "2099-01-05",
		DinnerID: sql.NullString{String: e.ids["Pad Thai"], Valid: true}, IsNew: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	e.ids["Fish Tacos"] = e.dinner(t, "Fish Tacos", "regular", "", 0)
	names := []string{"Beef Chili", "Pot Roast", "Fish Tacos", "Chicken Fajitas", "Shawarma Bowls", "Spaghetti Bolognese", "Sheet Pan Salmon"}
	e.reply("", names...)
	p := e.mustDraft(t, false)
	if got := dayNames(p); !slices.Equal(got, names) {
		t.Errorf("days = %v", got)
	}
	for _, c := range e.input(0).Pool {
		if c.Name == "Pad Thai" {
			t.Errorf("pool has Pad Thai, planned on a later week")
		}
	}
	// A redraft of the week keeps offering its own Quest log dinners.
	e.reply("", names...)
	e.mustDraft(t, false)
	if !slices.ContainsFunc(e.input(1).Pool, func(c skills.Candidate) bool { return c.Name == "Shawarma Bowls" }) {
		t.Errorf("redraft pool left out Shawarma Bowls, planned on this week")
	}
}

func TestDraftFailsWhenPoolDinnerLeavesWhileDrafting(t *testing.T) {
	for _, status := range []string{"archived", "tried"} {
		t.Run(status, func(t *testing.T) {
			e := newEnv(t)
			e.start(t)
			// Pad Thai leaves the Quest log while draft_week plans it.
			started, release := make(chan struct{}), make(chan struct{})
			r := e.weekReply("", fullWeek...)
			r.Started, r.Release = started, release
			e.model.QueueGenerate(r)
			d, err := e.svc.StartDraft(t.Context(), week, false)
			if err != nil {
				t.Fatal(err)
			}
			<-started
			if _, err := e.st.Writer.ExecContext(t.Context(), `UPDATE dinners SET status = ? WHERE id = ?`, status, e.ids["Pad Thai"]); err != nil {
				t.Fatal(err)
			}
			close(release)
			job := e.waitJob(t, d.JobID)
			if job.Status != jobs.StatusFailed || *job.Error != plan.ReasonPoolChanged {
				t.Fatalf("job %s %v, want failed %s", job.Status, job.Error, plan.ReasonPoolChanged)
			}
			if _, err := e.svc.Get(t.Context(), week); !errors.Is(err, plan.ErrNotFound) {
				t.Errorf("Get = %v, want ErrNotFound: no partial plan", err)
			}
		})
	}
}

func TestDraftsOfTwoWeeksDoNotShareAQuestLogDinner(t *testing.T) {
	e := newEnv(t)
	e.start(t)
	// This week's draft plans Pad Thai; while draft_week runs, the other
	// worker drafts a later week with Pad Thai too and stores it first.
	started, release := make(chan struct{}), make(chan struct{})
	r := e.weekReply("", fullWeek...)
	r.Started, r.Release = started, release
	e.model.QueueGenerate(r)
	d, err := e.svc.StartDraft(t.Context(), week, false)
	if err != nil {
		t.Fatal(err)
	}
	<-started
	later := mustDate("2099-01-05")
	laterDates := []string{"2099-01-05", "2099-01-06", "2099-01-07", "2099-01-08", "2099-01-09", "2099-01-10", "2099-01-11"}
	e.model.QueueGenerate(e.weekReplyOn(laterDates, "", fullWeek...))
	other, err := e.svc.StartDraft(t.Context(), later, false)
	if err != nil {
		t.Fatal(err)
	}
	if job := e.waitJob(t, other.JobID); job.Status != jobs.StatusSucceeded {
		t.Fatalf("later week's draft %s %v", job.Status, job.Error)
	}
	close(release)
	job := e.waitJob(t, d.JobID)
	if job.Status != jobs.StatusFailed || *job.Error != plan.ReasonPoolChanged {
		t.Fatalf("job %s %v, want failed %s", job.Status, job.Error, plan.ReasonPoolChanged)
	}
	if _, err := e.svc.Get(t.Context(), week); !errors.Is(err, plan.ErrNotFound) {
		t.Errorf("Get = %v, want ErrNotFound: no partial plan", err)
	}
}

func TestDraftFailsWithNotEnoughDinners(t *testing.T) {
	e := newEnv(t)
	e.start(t)
	if _, err := e.st.Writer.ExecContext(t.Context(), `UPDATE dinners SET status = 'archived' WHERE id = ?`, e.ids["Pad Thai"]); err != nil {
		t.Fatal(err)
	}
	job := e.draft(t, false)
	if job.Status != jobs.StatusFailed || *job.Error != plan.ReasonNotEnoughDinners {
		t.Fatalf("job %s %v, want failed %s", job.Status, job.Error, plan.ReasonNotEnoughDinners)
	}
	if n := len(e.model.Requests()); n != 0 {
		t.Errorf("model called %d times", n)
	}
	if _, err := e.svc.Get(t.Context(), week); !errors.Is(err, plan.ErrNotFound) {
		t.Errorf("Get = %v, want ErrNotFound", err)
	}
}

func TestDraftFailsWhenAIFails(t *testing.T) {
	e := newEnv(t)
	e.start(t)
	e.model.QueueGenerate(llm.GenerateResult{Err: errors.New("provider down")})
	job := e.draft(t, false)
	if job.Status != jobs.StatusFailed || *job.Error != plan.ReasonAIFailed {
		t.Fatalf("job %s %v, want failed %s", job.Status, job.Error, plan.ReasonAIFailed)
	}
	if _, err := e.svc.Get(t.Context(), week); !errors.Is(err, plan.ErrNotFound) {
		t.Errorf("Get = %v, want ErrNotFound", err)
	}
}

func TestStartDraftIsSingleFlight(t *testing.T) {
	e := newEnv(t)
	first, err := e.svc.StartDraft(t.Context(), week, false)
	if err != nil {
		t.Fatal(err)
	}
	again, err := e.svc.StartDraft(t.Context(), week, false)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Created || again.Created || again.JobID != first.JobID {
		t.Fatalf("drafts = %+v then %+v, want the second to join the first", first, again)
	}
	other, err := e.svc.StartDraft(t.Context(), mustDate("2026-10-19"), false)
	if err != nil {
		t.Fatal(err)
	}
	if !other.Created || other.JobID == first.JobID {
		t.Errorf("another week's draft = %+v, want its own job", other)
	}
}

func TestSimultaneousStartDraftsShareOneJob(t *testing.T) {
	for _, confirmFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("first confirm %v", confirmFirst), func(t *testing.T) {
			e := newEnv(t)
			first, err := e.svc.StartDraft(t.Context(), week, confirmFirst)
			if err != nil {
				t.Fatal(err)
			}
			// Twenty simultaneous drafts, half of them confirmed: every
			// one joins the queued job, except a confirmed one while the
			// job is unconfirmed, which is refused.
			const n = 20
			drafts := make([]plan.Draft, n)
			errs := make([]error, n)
			var wg sync.WaitGroup
			for i := range n {
				wg.Go(func() {
					drafts[i], errs[i] = e.svc.StartDraft(context.Background(), week, i%2 == 1)
				})
			}
			wg.Wait()
			for i, d := range drafts {
				if i%2 == 1 && !confirmFirst {
					if !errors.Is(errs[i], plan.ErrDraftRunning) {
						t.Errorf("confirmed draft %d = %+v, %v, want ErrDraftRunning", i, d, errs[i])
					}
					continue
				}
				if errs[i] != nil || d.JobID != first.JobID || d.Created {
					t.Errorf("draft %d = %+v, %v, want it to join %s", i, d, errs[i], first.JobID)
				}
			}
			var queued int
			if err := e.st.Reader.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM jobs WHERE kind = ?`, plan.KindDraftWeek).Scan(&queued); err != nil {
				t.Fatal(err)
			}
			if queued != 1 {
				t.Errorf("%d draft jobs, want 1", queued)
			}
		})
	}
}

func TestConfirmedDraftWhileUnconfirmedDraftRuns(t *testing.T) {
	e := newEnv(t)
	e.start(t)
	e.reply("first", fullWeek...)
	e.mustDraft(t, false)

	// An unconfirmed redraft is running when the plan is synced; the
	// household then confirms a redraft. With a second worker idle, a
	// second job would draft alongside the first, so the confirmed draft
	// is refused and only the first job runs draft_week.
	reversed := slices.Clone(fullWeek)
	slices.Reverse(reversed)
	started, release := make(chan struct{}), make(chan struct{})
	r := e.weekReply("unconfirmed", fullWeek...)
	r.Started, r.Release = started, release
	e.model.QueueGenerate(r)
	e.reply("concurrent", reversed...)
	unconfirmed, err := e.svc.StartDraft(t.Context(), week, false)
	if err != nil {
		t.Fatal(err)
	}
	<-started
	e.setStatus(t, plan.StatusSynced)
	if d, err := e.svc.StartDraft(t.Context(), week, true); !errors.Is(err, plan.ErrDraftRunning) {
		t.Fatalf("confirmed draft = %+v, %v, want ErrDraftRunning", d, err)
	}
	close(release)
	if job := e.waitJob(t, unconfirmed.JobID); job.Status != jobs.StatusFailed || *job.Error != plan.ReasonConfirmRequired {
		t.Errorf("unconfirmed job %s %v, want failed %s", job.Status, job.Error, plan.ReasonConfirmRequired)
	}
	var queued int
	if err := e.st.Reader.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM jobs WHERE kind = ?`, plan.KindDraftWeek).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 2 {
		t.Errorf("%d draft jobs, want the first and the unconfirmed redraft", queued)
	}
	p, err := e.svc.Get(t.Context(), week)
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != plan.StatusSynced || *p.AINote != "first" || !slices.Equal(dayNames(p), fullWeek) {
		t.Errorf("plan = %s %q %v, want the synced first draft", p.Status, *p.AINote, dayNames(p))
	}

	// Asked again once the unconfirmed job has finished, the confirmed
	// draft replaces the plan.
	if p := e.mustDraft(t, true); p.Status != plan.StatusChanged || *p.AINote != "concurrent" || !slices.Equal(dayNames(p), reversed) {
		t.Errorf("plan = %s %q %v, want the confirmed draft, changed", p.Status, *p.AINote, dayNames(p))
	}
}

func TestDraftFailsWhenPlanSwappedWhileDrafting(t *testing.T) {
	e := newEnv(t)
	e.start(t)
	e.reply("first", fullWeek...)
	e.mustDraft(t, false)

	// The household swaps Wednesday while draft_week runs; the draft must
	// not erase the swap.
	reversed := slices.Clone(fullWeek)
	slices.Reverse(reversed)
	started, release := make(chan struct{}), make(chan struct{})
	r := e.weekReply("second", reversed...)
	r.Started, r.Release = started, release
	e.model.QueueGenerate(r)
	d, err := e.svc.StartDraft(t.Context(), week, false)
	if err != nil {
		t.Fatal(err)
	}
	<-started
	e.ids["Fish Tacos"] = e.dinner(t, "Fish Tacos", "regular", "stovetop", 25)
	if _, err := e.svc.Swap(t.Context(), week, "2026-10-14", e.ids["Fish Tacos"]); err != nil {
		t.Fatal(err)
	}
	close(release)
	job := e.waitJob(t, d.JobID)
	if job.Status != jobs.StatusFailed || *job.Error != plan.ReasonPlanChanged {
		t.Fatalf("job %s %v, want failed %s", job.Status, job.Error, plan.ReasonPlanChanged)
	}
	p, err := e.svc.Get(t.Context(), week)
	if err != nil {
		t.Fatal(err)
	}
	want := slices.Clone(fullWeek)
	want[2] = "Fish Tacos"
	if *p.AINote != "first" || !slices.Equal(dayNames(p), want) {
		t.Errorf("plan = %q %v, want the first draft with the swap %v", *p.AINote, dayNames(p), want)
	}

	// A draft after the swap replaces the plan.
	e.reply("third", reversed...)
	if p := e.mustDraft(t, false); *p.AINote != "third" || !slices.Equal(dayNames(p), reversed) {
		t.Errorf("redraft after the swap = %q %v, want the third draft", *p.AINote, dayNames(p))
	}
}

func TestRedraftOverDraftNeedsNoConfirm(t *testing.T) {
	e := newEnv(t)
	e.start(t)
	e.reply("first", fullWeek...)
	first := e.mustDraft(t, false)
	reversed := slices.Clone(fullWeek)
	slices.Reverse(reversed)
	e.reply("second", reversed...)
	second := e.mustDraft(t, false)
	if second.ID != first.ID || second.Status != plan.StatusDraft || *second.AINote != "second" {
		t.Errorf("redraft = %s %s %v, want plan %s draft \"second\"", second.ID, second.Status, *second.AINote, first.ID)
	}
	if got := dayNames(second); !slices.Equal(got, reversed) {
		t.Errorf("days = %v, want %v", got, reversed)
	}
}

func TestDraftOverSyncedOrChangedPlanNeedsConfirm(t *testing.T) {
	for _, status := range []string{plan.StatusSynced, plan.StatusChanged} {
		t.Run(status, func(t *testing.T) {
			e := newEnv(t)
			e.start(t)
			e.reply("first", fullWeek...)
			first := e.mustDraft(t, false)
			e.setStatus(t, status)
			// Push to Calendar records each day's event id.
			if _, err := e.st.Writer.ExecContext(t.Context(), `UPDATE plan_days SET calendar_event_id = 'ev-' || date`); err != nil {
				t.Fatal(err)
			}

			if _, err := e.svc.StartDraft(t.Context(), week, false); !errors.Is(err, plan.ErrConfirmRequired) {
				t.Fatalf("StartDraft without confirm = %v, want ErrConfirmRequired", err)
			}
			var queued int
			if err := e.st.Reader.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM jobs WHERE kind = ?`, plan.KindDraftWeek).Scan(&queued); err != nil {
				t.Fatal(err)
			}
			if queued != 1 {
				t.Errorf("%d draft jobs, want only the first", queued)
			}

			reversed := slices.Clone(fullWeek)
			slices.Reverse(reversed)
			e.reply("second", reversed...)
			p := e.mustDraft(t, true)
			if p.ID != first.ID || p.Status != plan.StatusChanged || p.CalendarSyncedAt == nil {
				t.Errorf("confirmed redraft = %s %s synced %v, want plan %s changed, still synced once", p.ID, p.Status, p.CalendarSyncedAt, first.ID)
			}
			if got := dayNames(p); !slices.Equal(got, reversed) {
				t.Errorf("days = %v, want %v", got, reversed)
			}
			// Reversed, only Thursday keeps its dinner, so only Thursday
			// keeps its event id: Calendar no longer shows the others as
			// planned.
			rows, err := e.st.Reader.QueryContext(t.Context(), `SELECT date FROM plan_days WHERE calendar_event_id = 'ev-' || date ORDER BY date`)
			if err != nil {
				t.Fatal(err)
			}
			var kept []string
			for rows.Next() {
				var date string
				if err := rows.Scan(&date); err != nil {
					t.Fatal(err)
				}
				kept = append(kept, date)
			}
			if err := errors.Join(rows.Err(), rows.Close()); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(kept, []string{"2026-10-15"}) {
				t.Errorf("days that kept their calendar event id = %v, want only the unchanged 2026-10-15", kept)
			}
		})
	}
}

func TestDraftFailsWhenPlanSyncedAfterAskingWithoutConfirm(t *testing.T) {
	e := newEnv(t)
	e.start(t)
	e.reply("first", fullWeek...)
	e.mustDraft(t, false)

	// The redraft is asked for while the plan is draft; the plan is synced
	// while draft_week runs, so the draft must not replace it.
	reversed := slices.Clone(fullWeek)
	slices.Reverse(reversed)
	started, release := make(chan struct{}), make(chan struct{})
	r := e.weekReply("second", reversed...)
	r.Started, r.Release = started, release
	e.model.QueueGenerate(r)
	d, err := e.svc.StartDraft(t.Context(), week, false)
	if err != nil {
		t.Fatal(err)
	}
	<-started
	e.setStatus(t, plan.StatusSynced)
	close(release)
	job := e.waitJob(t, d.JobID)
	if job.Status != jobs.StatusFailed || *job.Error != plan.ReasonConfirmRequired {
		t.Fatalf("job %s %v, want failed %s", job.Status, job.Error, plan.ReasonConfirmRequired)
	}
	p, err := e.svc.Get(t.Context(), week)
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != plan.StatusSynced || *p.AINote != "first" || !slices.Equal(dayNames(p), fullWeek) {
		t.Errorf("plan = %s %q %v, want the synced first draft", p.Status, *p.AINote, dayNames(p))
	}
}

func TestParseWeekStart(t *testing.T) {
	if d, err := plan.ParseWeekStart("2026-10-12"); err != nil || d != week {
		t.Errorf("ParseWeekStart(Monday) = %v, %v", d, err)
	}
	// 2026-02-30 and 2027-02-29 would be Mondays were they dates.
	for _, s := range []string{"2026-10-13", "2026-10-18", "2026-10-5", "2026-02-30", "2027-02-29", "2026-13-05", "next-week", ""} {
		var invalid *plan.InvalidWeekError
		if _, err := plan.ParseWeekStart(s); !errors.As(err, &invalid) {
			t.Errorf("ParseWeekStart(%q) = %v, want *InvalidWeekError", s, err)
		}
	}
}

func TestSwapDay(t *testing.T) {
	e := newEnv(t)
	e.start(t)
	e.reply("", fullWeek...)
	e.mustDraft(t, false)
	e.ids["Fish Tacos"] = e.dinner(t, "Fish Tacos", "regular", "stovetop", 25)
	e.ids["Ramen"] = e.dinner(t, "Ramen", "quest_log", "crockpot", 300)

	// A Quest log dinner is new, and a crock-pot one gets its start_by.
	p, err := e.svc.Swap(t.Context(), week, "2026-10-14", e.ids["Ramen"])
	if err != nil {
		t.Fatal(err)
	}
	day := p.Days[2]
	if day.Dinner.Name != "Ramen" || !day.IsNew || day.StartBy == nil || day.StartBy.Format("15:04") != "12:30" {
		t.Errorf("Wednesday = %+v, want Ramen, new, start by 12:30", day)
	}
	if p.Status != plan.StatusDraft {
		t.Errorf("status = %s, want a swapped draft to stay draft", p.Status)
	}

	// A Regular in place of a Quest log dinner is not new.
	p, err = e.svc.Swap(t.Context(), week, "2026-10-14", e.ids["Fish Tacos"])
	if err != nil {
		t.Fatal(err)
	}
	if day := p.Days[2]; day.Dinner.Name != "Fish Tacos" || day.IsNew || day.StartBy != nil {
		t.Errorf("Wednesday = %+v, want Fish Tacos, not new, no start_by", day)
	}
	want := slices.Clone(fullWeek)
	want[2] = "Fish Tacos"
	if got := dayNames(p); !slices.Equal(got, want) {
		t.Errorf("days = %v, want %v", got, want)
	}
	got, err := e.svc.Get(t.Context(), week)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(dayNames(got), want) {
		t.Errorf("Get days = %v, want %v", dayNames(got), want)
	}
}

// TestGetShowsDinnerStatus reads each day's dinner status as it is now: a
// dinner archived after the plan was made stays on it, archived, and its
// day can still be swapped.
func TestGetShowsDinnerStatus(t *testing.T) {
	e := newEnv(t)
	e.start(t)
	e.reply("", fullWeek...)
	for _, d := range e.mustDraft(t, false).Days {
		want := dinners.StatusRegular
		if d.IsNew {
			want = dinners.StatusQuestLog
		}
		if d.Dinner.Status != want {
			t.Errorf("drafted %s status = %q, want %q", d.Dinner.Name, d.Dinner.Status, want)
		}
	}
	if _, err := e.st.Writer.ExecContext(t.Context(), `UPDATE dinners SET status = 'archived' WHERE id = ?`, e.ids["Pot Roast"]); err != nil {
		t.Fatal(err)
	}

	got, err := e.svc.Get(t.Context(), week)
	if err != nil {
		t.Fatal(err)
	}
	i := slices.Index(dayNames(got), "Pot Roast")
	if i < 0 {
		t.Fatalf("days = %v, want Pot Roast still planned", dayNames(got))
	}
	if s := got.Days[i].Dinner.Status; s != dinners.StatusArchived {
		t.Errorf("Pot Roast status = %q, want archived", s)
	}

	e.ids["Fish Tacos"] = e.dinner(t, "Fish Tacos", "regular", "stovetop", 25)
	p, err := e.svc.Swap(t.Context(), week, got.Days[i].Date, e.ids["Fish Tacos"])
	if err != nil {
		t.Fatal(err)
	}
	if d := p.Days[i].Dinner; d.Name != "Fish Tacos" || d.Status != dinners.StatusRegular {
		t.Errorf("swapped day dinner = %+v, want Fish Tacos regular", d)
	}
}

func TestSwapStatusTransitions(t *testing.T) {
	for _, tc := range []struct{ from, want string }{
		{plan.StatusDraft, plan.StatusDraft},
		{plan.StatusSynced, plan.StatusChanged},
		{plan.StatusChanged, plan.StatusChanged},
	} {
		t.Run(tc.from, func(t *testing.T) {
			e := newEnv(t)
			e.start(t)
			e.reply("", fullWeek...)
			e.mustDraft(t, false)
			e.setStatus(t, tc.from)
			fishTacos := e.dinner(t, "Fish Tacos", "regular", "stovetop", 25)
			p, err := e.svc.Swap(t.Context(), week, "2026-10-12", fishTacos)
			if err != nil {
				t.Fatal(err)
			}
			if p.Status != tc.want {
				t.Errorf("status after swap = %s, want %s", p.Status, tc.want)
			}
		})
	}
}

func TestSwapRefusals(t *testing.T) {
	e := newEnv(t)
	e.start(t)
	e.reply("", fullWeek...)
	e.mustDraft(t, false)
	e.setStatus(t, plan.StatusSynced)
	suggested := e.dinner(t, "Laksa", "suggested", "stovetop", 30)
	archived := e.dinner(t, "Old Stew", "archived", "", 0)

	var notPlannable *plan.DinnerNotPlannableError
	var badDay *plan.InvalidDayError
	for _, tc := range []struct {
		name      string
		week      store.LocalDate
		date, id  string
		wantError func(error) bool
	}{
		{"no plan", mustDate("2026-10-19"), "2026-10-19", e.ids["Pad Thai"], func(err error) bool { return errors.Is(err, plan.ErrNotFound) }},
		{"unknown dinner", week, "2026-10-12", "no-such-dinner", func(err error) bool { return errors.Is(err, plan.ErrDinnerNotFound) }},
		{"dinner on another day", week, "2026-10-12", e.ids["Pot Roast"], func(err error) bool { return errors.Is(err, plan.ErrDinnerAlreadyPlanned) }},
		{"suggested dinner", week, "2026-10-12", suggested, func(err error) bool { return errors.As(err, &notPlannable) }},
		{"archived dinner", week, "2026-10-12", archived, func(err error) bool { return errors.As(err, &notPlannable) }},
		{"day of another week", week, "2026-10-19", e.ids["Pad Thai"], func(err error) bool { return errors.As(err, &badDay) }},
		{"not a date", week, "monday", e.ids["Pad Thai"], func(err error) bool { return errors.As(err, &badDay) }},
		{"impossible date", week, "2026-10-32", e.ids["Pad Thai"], func(err error) bool { return errors.As(err, &badDay) }},
	} {
		if _, err := e.svc.Swap(t.Context(), tc.week, tc.date, tc.id); !tc.wantError(err) {
			t.Errorf("%s: Swap = %v", tc.name, err)
		}
	}
	p, err := e.svc.Get(t.Context(), week)
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != plan.StatusSynced || !slices.Equal(dayNames(p), fullWeek) {
		t.Errorf("plan = %s %v, want it unchanged by refused swaps", p.Status, dayNames(p))
	}
}
