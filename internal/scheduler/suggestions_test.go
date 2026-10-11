package scheduler_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/harness"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/llm"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/skills"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/household"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/jobs"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/notify"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/notify/notifytest"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/recipes"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/recipes/recipestest"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/scheduler"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/suggest"
)

// The tests in this file run suggest's suggestions_weekly schedule on the
// fake clock against the real suggestion service, job runner, harness and
// notification outbox, with the AI and recipe sources faked.

// suggestEnv is a suggestion service over a scheduler env's store, the
// "process" a scheduler runs it in. side answers suggest_side_quests and
// model normalize_recipe while grounding.
type suggestEnv struct {
	e      *env
	svc    *suggest.Service
	side   *llm.Fake
	model  *llm.Fake
	search *recipestest.Search
	pages  *recipestest.Pages
	images *recipestest.Images
	logs   *syncBuffer
	stop   func()
}

// syncBuffer collects log output written by concurrent goroutines.
type syncBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// regulars stores the Regulars Beef Chili and Chicken Fajitas, so a batch
// has Regulars to suggest from.
func (e *env) regulars() {
	e.t.Helper()
	for _, name := range []string{"Beef Chili", "Chicken Fajitas"} {
		now := store.Now().String()
		if _, err := db.New(e.st.Writer).CreateDinner(context.Background(), db.CreateDinnerParams{
			ID:              store.NewID(),
			Name:            name,
			NormalizedName:  harness.Normalize(name),
			Status:          "regular",
			Origin:          "regular",
			GroundingStatus: "manual",
			CreatedAt:       now,
			UpdatedAt:       now,
		}); err != nil {
			e.t.Fatal(err)
		}
	}
}

// suggestions builds a suggestion service over e with a started job
// runner, which stop stops (and the test's end, if not before).
func (e *env) suggestions() *suggestEnv {
	e.t.Helper()
	s := &suggestEnv{
		e:      e,
		side:   &llm.Fake{},
		model:  &llm.Fake{},
		search: &recipestest.Search{Results: map[string][]recipes.Candidate{}},
		pages:  &recipestest.Pages{Bodies: map[string][]byte{}},
		images: &recipestest.Images{JPEGs: map[string][]byte{}},
		logs:   &syncBuffer{},
	}
	g := recipestest.NewGrounder(e.t, e.st, e.t.TempDir(), s.search, s.pages, s.images, s.model)
	guard, err := harness.NewGuard(harness.DefaultMaxCalls)
	if err != nil {
		e.t.Fatal(err)
	}
	cfg := harness.Config{
		Settings:    llm.Settings{Provider: "anthropic", Model: "claude-test", Temperature: 0.7},
		NewProvider: func(llm.Settings, string) (llm.Provider, error) { return s.side, nil },
		Store:       e.st,
		Guard:       guard,
	}
	suggester := func(ctx context.Context, scope string, in skills.SideQuestsRequest) (skills.SideQuests, error) {
		return skills.SuggestSideQuests(ctx, cfg, scope, in)
	}
	logger := slog.New(slog.NewTextHandler(s.logs, nil))
	runner, err := jobs.NewRunner(e.st, logger, 1)
	if err != nil {
		e.t.Fatal(err)
	}
	s.svc, err = suggest.NewService(e.st, g, suggester, runner, notifytest.New(e.t, e.st), logger)
	if err != nil {
		e.t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := runner.Start(ctx); err != nil {
		cancel()
		e.t.Fatal(err)
	}
	var once sync.Once
	s.stop = func() {
		once.Do(func() {
			cancel()
			runner.Wait()
		})
	}
	e.t.Cleanup(s.stop)
	return s
}

// week scripts the next batch: suggest_side_quests answers with the
// Side quests named names, each grounded by its own recipe.
func (s *suggestEnv) week(names ...string) {
	s.e.t.Helper()
	var quests []string
	for _, name := range names {
		slug := strings.ReplaceAll(strings.ToLower(name), " ", "-")
		page, photo := "https://r.example/"+slug, "https://r.example/"+slug+".jpg"
		s.search.Results[name] = []recipes.Candidate{{URL: page}}
		s.pages.Bodies[page] = recipestest.RecipePage(name, "R Kitchen", photo, 35)
		s.images.JPEGs[photo] = recipestest.JPEG(s.e.t, 400, 320, uint8(len(s.images.JPEGs)+1))
		s.model.QueueGenerate(recipestest.NormalizedReply("stovetop", 35))
		quests = append(quests, fmt.Sprintf(`{"name":%q,"cuisine":"Fusion","method":"stovetop","est_minutes":35,"difficulty":"easy","reason":"Like your Beef Chili.","cited_regulars":["Beef Chili"],"search_query":%q}`, name, name))
	}
	js := `{"suggestions":[` + strings.Join(quests, ",") + `]}`
	s.side.QueueGenerate(llm.GenerateResult{Response: llm.Response{Text: js, JSON: json.RawMessage(js)}})
}

// batch is a suggestion_batches row.
type batch struct {
	ID, Trigger, Status string
}

// batches returns every suggestion batch, oldest first.
func (e *env) batches() []batch {
	e.t.Helper()
	rows, err := e.st.Reader.Query(`SELECT id, trigger, status FROM suggestion_batches ORDER BY created_at, id`)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []batch
	for rows.Next() {
		var b batch
		if err := rows.Scan(&b.ID, &b.Trigger, &b.Status); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		e.t.Fatal(err)
	}
	return out
}

// settled waits until no batch is pending or running and returns the
// batches.
func (e *env) settled() []batch {
	e.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		bs := e.batches()
		busy := false
		for _, b := range bs {
			busy = busy || b.Status == suggest.BatchPending || b.Status == suggest.BatchRunning
		}
		if !busy {
			return bs
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("batches still under way: %+v", bs)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// wantBatches fails unless the batches are scheduled with statuses want.
func wantBatches(t *testing.T, what string, got []batch, want ...string) {
	t.Helper()
	ok := len(got) == len(want)
	for i := 0; ok && i < len(got); i++ {
		ok = got[i].Trigger == suggest.TriggerScheduled && got[i].Status == want[i]
	}
	if !ok {
		t.Fatalf("%s: batches = %+v, want scheduled %q", what, got, want)
	}
}

// notified returns the new_suggestions keys stored, oldest first, and
// reports an error for any whose title is not the one titles gives it.
func (e *env) notified(titles ...string) []string {
	e.t.Helper()
	var keys []string
	rows := notifytest.Rows(e.t, e.st, notify.NewSuggestions)
	for i, r := range rows {
		keys = append(keys, r.Key)
		if i < len(titles) && r.Payload.Title != titles[i] {
			e.t.Errorf("new_suggestions %s title = %q, want %q", r.Key, r.Payload.Title, titles[i])
		}
	}
	return keys
}

// TestSuggestionsWeeklyOneBatchAndNotificationPerWeek: at the seeded
// Sunday 08:00 the schedule starts one scheduled batch a week, period
// suggestions_weekly:YYYY-Www, and each tells the household once, with
// new_suggestions:YYYY-Www counting its new Side quests.
func TestSuggestionsWeeklyOneBatchAndNotificationPerWeek(t *testing.T) {
	e := newEnv(t)
	e.regulars()
	s := e.suggestions()
	s.week("Chili Verde", "Chicken Shawarma Bowls")
	s.week("Lamb Kofta", "Black Bean Tostadas", "Pork Carnitas")

	// Wednesday Oct 7: the Sunday Oct 4 08:00 fire is days ago, missed.
	r := e.start(at(2026, 10, 7, 12, 0), s.svc.WeeklySchedule())
	r.runUntil(at(2026, 10, 11, 7, 59))
	wantBatches(t, "before Sunday 08:00", e.settled())
	wantKeys(t, "new_suggestions before Sunday 08:00", e.notified(), nil)

	r.runUntil(at(2026, 10, 11, 8, 0))
	wantBatches(t, "at Sunday 08:00", e.settled(), suggest.BatchDone)
	wantKeys(t, "new_suggestions of W41", e.notified("2 new Side quests are in"), []string{"new_suggestions:2026-W41"})

	r.runUntil(at(2026, 10, 18, 7, 59))
	wantBatches(t, "rest of W41", e.settled(), suggest.BatchDone)
	r.runUntil(at(2026, 10, 18, 9, 0))
	wantBatches(t, "W42", e.settled(), suggest.BatchDone, suggest.BatchDone)
	wantKeys(t, "new_suggestions of W41 and W42", e.notified("2 new Side quests are in", "3 new Side quests are in"),
		[]string{"new_suggestions:2026-W41", "new_suggestions:2026-W42"})
	wantKeys(t, "schedule_runs", e.claimed(scheduler.SuggestionsWeekly), []string{"2026-W40", "2026-W41", "2026-W42"})
}

// TestSuggestionsWeeklyFollowsSettings: the schedule fires at the
// household's suggestions_dow and suggestions_time.
func TestSuggestionsWeeklyFollowsSettings(t *testing.T) {
	e := newEnv(t)
	e.regulars()
	e.update(household.SettingsPatch{SuggestionsDow: ptr(3), SuggestionsTime: ptr("18:30")})
	s := e.suggestions()
	s.week("Chili Verde")

	r := e.start(at(2026, 10, 12, 12, 0), s.svc.WeeklySchedule()) // Monday
	r.runUntil(at(2026, 10, 14, 18, 29))                          // Wednesday
	wantBatches(t, "before Wednesday 18:30", e.settled())
	r.runUntil(at(2026, 10, 14, 18, 30))
	wantBatches(t, "at Wednesday 18:30", e.settled(), suggest.BatchDone)
	wantKeys(t, "new_suggestions", e.notified("1 new Side quest is in"), []string{"new_suggestions:2026-W42"})
}

// TestSuggestionsWeeklyAIFailureSendsNothing: when suggest_side_quests
// fails the batch fails with the failure logged and recorded in ai_runs,
// and the household is not notified.
func TestSuggestionsWeeklyAIFailureSendsNothing(t *testing.T) {
	e := newEnv(t)
	e.regulars()
	s := e.suggestions()
	s.side.QueueGenerate(llm.GenerateResult{Err: errors.New("provider down")})

	r := e.start(at(2026, 10, 11, 7, 0), s.svc.WeeklySchedule())
	r.runUntil(at(2026, 10, 11, 8, 0))
	wantBatches(t, "after the AI failed", e.settled(), suggest.BatchFailed)
	wantKeys(t, "new_suggestions after the AI failed", e.notified(), nil)
	// W40 is the boot's missed period, recorded without running.
	wantKeys(t, "schedule_runs", e.claimed(scheduler.SuggestionsWeekly), []string{"2026-W40", "2026-W41"})

	var status, msg string
	if err := e.st.Reader.QueryRow(`SELECT status, COALESCE(error, '') FROM ai_runs WHERE skill = 'suggest_side_quests'`).Scan(&status, &msg); err != nil {
		t.Fatalf("ai_runs row of suggest_side_quests: %v", err)
	}
	if status != string(harness.StatusProviderError) || msg == "" {
		t.Errorf("ai_runs suggest_side_quests = %s %q, want %s with its message", status, msg, harness.StatusProviderError)
	}
	if logs := s.logs.String(); !strings.Contains(logs, "suggestion batch failed") || !strings.Contains(logs, "reason="+suggest.ReasonAIFailed) {
		t.Errorf("logs = %s, want the batch failure with reason %s", logs, suggest.ReasonAIFailed)
	}
}

// TestSuggestionsWeeklyNoDuplicateAfterRestart: a restart after the week's
// batch ran, still within the catch-up window, starts no second batch and
// sends no second notification: the period is already claimed.
func TestSuggestionsWeeklyNoDuplicateAfterRestart(t *testing.T) {
	e := newEnv(t)
	e.regulars()
	s := e.suggestions()
	s.week("Chili Verde", "Chicken Shawarma Bowls")
	r := e.start(at(2026, 10, 11, 7, 0), s.svc.WeeklySchedule())
	r.runUntil(at(2026, 10, 11, 8, 0))
	wantBatches(t, "first process", e.settled(), suggest.BatchDone)
	r.stop()
	s.stop()

	// The restarted process has a new job runner, suggestion service and
	// scheduler; it would answer a second batch if one were started.
	s2 := e.suggestions()
	s2.week("Lamb Kofta")
	r2 := e.start(at(2026, 10, 11, 9, 0), s2.svc.WeeklySchedule())
	r2.runUntil(at(2026, 10, 18, 7, 59))
	wantBatches(t, "after restart", e.settled(), suggest.BatchDone)
	wantKeys(t, "new_suggestions after restart", e.notified(), []string{"new_suggestions:2026-W41"})
	wantKeys(t, "schedule_runs", e.claimed(scheduler.SuggestionsWeekly), []string{"2026-W40", "2026-W41"})
}

// TestSuggestionsWeeklyCatchUpAfterTheWeekEnds: a Sunday fire caught up on
// Monday, in the next ISO week, still runs W41's batch keyed by W41, and
// the next Sunday's batch notifies for W42.
func TestSuggestionsWeeklyCatchUpAfterTheWeekEnds(t *testing.T) {
	e := newEnv(t)
	e.regulars()
	s := e.suggestions()
	s.week("Chili Verde")
	s.week("Lamb Kofta")

	r := e.start(at(2026, 10, 12, 7, 0), s.svc.WeeklySchedule()) // Monday, 23h late
	wantBatches(t, "after catch-up", e.settled(), suggest.BatchDone)
	wantKeys(t, "new_suggestions after catch-up", e.notified(), []string{"new_suggestions:2026-W41"})
	r.runUntil(at(2026, 10, 18, 8, 0))
	wantBatches(t, "W42", e.settled(), suggest.BatchDone, suggest.BatchDone)
	wantKeys(t, "new_suggestions", e.notified(), []string{"new_suggestions:2026-W41", "new_suggestions:2026-W42"})
}

// TestSuggestionsWeeklyStartsNoBatchWhileOneIsUnderWay: a period that
// fires while a batch is pending starts no second one and says so.
func TestSuggestionsWeeklyStartsNoBatchWhileOneIsUnderWay(t *testing.T) {
	e := newEnv(t)
	e.regulars()
	s := e.suggestions()
	s.stop() // the manual batch stays pending
	manual, err := s.svc.StartBatch(context.Background(), suggest.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}

	r := e.start(at(2026, 10, 11, 7, 0), s.svc.WeeklySchedule())
	r.runUntil(at(2026, 10, 11, 8, 0))
	if bs := e.batches(); len(bs) != 1 || bs[0].ID != manual.ID {
		t.Fatalf("batches = %+v, want only the manual batch %s", bs, manual.ID)
	}
	if logs := s.logs.String(); !strings.Contains(logs, "scheduled suggestion batch not started") || !strings.Contains(logs, "period=2026-W41") {
		t.Errorf("logs = %s, want the skipped period logged", logs)
	}
}
