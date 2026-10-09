package scheduler_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/household"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/scheduler"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/migrations"
)

const zone = "America/Chicago"

// chicago is the household zone of every test.
var chicago = mustZone(zone)

func mustZone(name string) *time.Location {
	loc, err := household.LoadZone(name)
	if err != nil {
		panic(err)
	}
	return loc
}

// at returns the instant clocks in Chicago read the given time.
func at(year int, month time.Month, day, hour, minute int) time.Time {
	return time.Date(year, month, day, hour, minute, 0, 0, chicago)
}

// fakeClock is a clock whose time moves only when the test moves it.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []fakeTimer
	// waits receives one value per After call, so the test knows the
	// scheduler finished a pass and is waiting.
	waits chan struct{}
}

type fakeTimer struct {
	deadline time.Time
	ch       chan time.Time
}

func newFakeClock(now time.Time) *fakeClock {
	return &fakeClock{now: now, waits: make(chan struct{}, 100)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	ch := make(chan time.Time, 1)
	c.timers = append(c.timers, fakeTimer{deadline: c.now.Add(d), ch: ch})
	c.mu.Unlock()
	c.waits <- struct{}{}
	return ch
}

// set moves the clock to t and fires the timers due by then, reporting
// whether any fired.
func (c *fakeClock) set(t time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
	fired := false
	kept := c.timers[:0]
	for _, tm := range c.timers {
		if tm.deadline.After(t) {
			kept = append(kept, tm)
			continue
		}
		tm.ch <- t
		fired = true
	}
	c.timers = kept
	return fired
}

// nextDeadline returns the earliest pending timer deadline.
func (c *fakeClock) nextDeadline() (time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var next time.Time
	for _, tm := range c.timers {
		if next.IsZero() || tm.deadline.Before(next) {
			next = tm.deadline
		}
	}
	return next, !next.IsZero()
}

// recorder records the periods a schedule ran.
type recorder struct {
	mu   sync.Mutex
	runs []scheduler.Period
	err  error
}

func (r *recorder) run(_ context.Context, p scheduler.Period) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runs = append(r.runs, p)
	return r.err
}

func (r *recorder) periods() []scheduler.Period {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.runs)
}

func (r *recorder) keys() []string {
	var keys []string
	for _, p := range r.periods() {
		keys = append(keys, p.Key)
	}
	return keys
}

// env is a migrated database with household settings in Chicago.
type env struct {
	t        *testing.T
	st       *store.Store
	settings *household.SettingsService
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
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
	if err := household.SeedSettings(ctx, st, zone); err != nil {
		t.Fatal(err)
	}
	return &env{t: t, st: st, settings: household.NewSettingsService(st)}
}

// update changes the household settings.
func (e *env) update(patch household.SettingsPatch) {
	e.t.Helper()
	if _, err := e.settings.Update(context.Background(), patch, "member"); err != nil {
		e.t.Fatal(err)
	}
}

// claimed returns the period keys schedule_runs holds for name.
func (e *env) claimed(name string) []string {
	e.t.Helper()
	rows, err := e.st.Reader.Query(`SELECT period_key FROM schedule_runs WHERE schedule = ? ORDER BY period_key`, name)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			e.t.Fatal(err)
		}
		keys = append(keys, k)
	}
	if err := rows.Err(); err != nil {
		e.t.Fatal(err)
	}
	return keys
}

// running is a started scheduler on a fake clock.
type running struct {
	t     *testing.T
	clock *fakeClock
	stop  func()
}

// start starts a scheduler on e at now with the schedules, and stops it
// when the test ends. It returns once the first pass, the catch-up, is done.
func (e *env) start(now time.Time, schedules ...scheduler.Schedule) *running {
	e.t.Helper()
	s, err := scheduler.New(e.st, e.settings, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		e.t.Fatal(err)
	}
	clock := newFakeClock(now)
	scheduler.SetClock(s, clock.Now, clock.After)
	for _, sch := range schedules {
		if err := s.Register(sch); err != nil {
			e.t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := s.Start(ctx); err != nil {
		cancel()
		e.t.Fatal(err)
	}
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			s.Wait()
		})
	}
	e.t.Cleanup(stop)
	r := &running{t: e.t, clock: clock, stop: stop}
	r.idle()
	return r
}

// idle waits until the scheduler finished a pass and is waiting.
func (r *running) idle() {
	r.t.Helper()
	select {
	case <-r.clock.waits:
	case <-time.After(10 * time.Second):
		r.t.Fatal("scheduler did not finish its pass")
	}
}

// runUntil moves the clock to to, stopping at every instant the scheduler
// asked to wake at on the way, as a real clock would.
func (r *running) runUntil(to time.Time) {
	r.t.Helper()
	for {
		next, ok := r.clock.nextDeadline()
		if !ok || next.After(to) {
			if r.clock.set(to) {
				r.idle()
			}
			return
		}
		if r.clock.set(next) {
			r.idle()
		}
	}
}

// changed waits for the scheduler to apply a settings change.
func (r *running) changed() {
	r.t.Helper()
	r.idle()
}

func reminder(rec *recorder) scheduler.Schedule {
	return scheduler.Schedule{
		Name: scheduler.DayOfReminder,
		Rule: func(s household.Settings) scheduler.Rule { return scheduler.Daily(s.ReminderTime) },
		Run:  rec.run,
	}
}

func suggestions(rec *recorder) scheduler.Schedule {
	return scheduler.Schedule{
		Name: scheduler.SuggestionsWeekly,
		Rule: func(s household.Settings) scheduler.Rule {
			return scheduler.Weekly(time.Weekday(s.SuggestionsDow), s.SuggestionsTime)
		},
		Run: rec.run,
	}
}

func planDraft(rec *recorder) scheduler.Schedule {
	return scheduler.Schedule{
		Name: scheduler.PlanDraft,
		Rule: func(s household.Settings) scheduler.Rule {
			return scheduler.WeeklyForNextWeek(time.Weekday(s.PlanDraftDow), s.PlanDraftTime)
		},
		Run: rec.run,
	}
}

func ptr[T any](v T) *T { return &v }

func wantKeys(t *testing.T, what string, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("%s = %q, want %q", what, got, want)
	}
}

func TestFiresOncePerPeriod(t *testing.T) {
	e := newEnv(t)
	var rec recorder
	// Seeded reminder time is 16:30; boot just after it on Oct 6 runs it.
	r := e.start(at(2026, 10, 6, 16, 31), reminder(&rec))
	wantKeys(t, "after boot", rec.keys(), []string{"2026-10-06"})

	r.runUntil(at(2026, 10, 7, 16, 29))
	wantKeys(t, "before the fire time", rec.keys(), []string{"2026-10-06"})
	r.runUntil(at(2026, 10, 9, 23, 59))
	wantKeys(t, "after three days", rec.keys(), []string{"2026-10-06", "2026-10-07", "2026-10-08", "2026-10-09"})

	for i, p := range rec.periods()[1:] {
		want := at(2026, 10, 7+i, 16, 30)
		if !p.FireAt.Equal(want) {
			t.Errorf("run %s fired at %s, want %s", p.Key, p.FireAt, want)
		}
		if p.FireAt.Location().String() != zone {
			t.Errorf("run %s fired in zone %s, want %s", p.Key, p.FireAt.Location(), zone)
		}
	}
	wantKeys(t, "schedule_runs", e.claimed(scheduler.DayOfReminder), rec.keys())
}

func TestRestartMidPeriodDoesNotRerun(t *testing.T) {
	e := newEnv(t)
	var first recorder
	r := e.start(at(2026, 10, 7, 12, 0), suggestions(&first)) // Wednesday
	// The seeded Sunday 08:00 of Oct 4 is days ago: missed, not run.
	wantKeys(t, "first boot", first.keys(), nil)
	r.runUntil(at(2026, 10, 11, 9, 0))
	wantKeys(t, "first process", first.keys(), []string{"2026-W41"})
	r.stop()

	// The restart is an hour after the Sunday 08:00 fire, mid-period.
	var second recorder
	r2 := e.start(at(2026, 10, 11, 9, 0), suggestions(&second))
	wantKeys(t, "after restart", second.keys(), nil)
	r2.runUntil(at(2026, 10, 18, 7, 59))
	wantKeys(t, "rest of the period", second.keys(), nil)
	r2.runUntil(at(2026, 10, 18, 8, 0))
	wantKeys(t, "next period", second.keys(), []string{"2026-W42"})
	wantKeys(t, "schedule_runs", e.claimed(scheduler.SuggestionsWeekly), []string{"2026-W40", "2026-W41", "2026-W42"})
}

func TestCatchUpWithin24Hours(t *testing.T) {
	e := newEnv(t)
	var rec recorder
	// Sunday Oct 11 08:00 passed 23h59m before boot.
	e.start(at(2026, 10, 12, 7, 59), suggestions(&rec))
	wantKeys(t, "catch-up", rec.keys(), []string{"2026-W41"})
	if p := rec.periods()[0]; !p.FireAt.Equal(at(2026, 10, 11, 8, 0)) {
		t.Errorf("caught-up period fired at %s, want its fire time", p.FireAt)
	}
}

func TestNoCatchUpBeyond24Hours(t *testing.T) {
	e := newEnv(t)
	var rec recorder
	// Sunday Oct 11 08:00 passed 24h01m before boot.
	r := e.start(at(2026, 10, 12, 8, 1), suggestions(&rec))
	wantKeys(t, "boot", rec.keys(), nil)
	// The missed period is recorded, so it never runs later.
	wantKeys(t, "schedule_runs", e.claimed(scheduler.SuggestionsWeekly), []string{"2026-W41"})

	// Moving the fire time to Sunday 23:00 puts that week's fire time
	// within 24 h of now. The change itself never fires a passed time...
	e.update(household.SettingsPatch{SuggestionsTime: ptr("23:00")})
	r.changed()
	wantKeys(t, "after a settings change", rec.keys(), nil)
	r.stop()

	// ...and the restart's catch-up finds the period already recorded.
	var again recorder
	r2 := e.start(at(2026, 10, 12, 8, 2), suggestions(&again))
	wantKeys(t, "after restart", again.keys(), nil)
	r2.runUntil(at(2026, 10, 18, 23, 0))
	wantKeys(t, "next period", again.keys(), []string{"2026-W42"})
}

func TestCatchUpOnlyMostRecentPeriod(t *testing.T) {
	e := newEnv(t)
	var rec recorder
	// Days of downtime: only Oct 9 16:30, 30 minutes ago, runs.
	e.start(at(2026, 10, 9, 17, 0), reminder(&rec))
	wantKeys(t, "catch-up", rec.keys(), []string{"2026-10-09"})
	wantKeys(t, "schedule_runs", e.claimed(scheduler.DayOfReminder), []string{"2026-10-09"})
}

func TestSpringForward(t *testing.T) {
	// Clocks in Chicago jump 02:00 CST → 03:00 CDT on Sunday 2026-03-08.
	e := newEnv(t)
	e.update(household.SettingsPatch{ReminderTime: ptr("08:00"), ReviewTime: ptr("02:30")})
	var daily, skipped recorder
	review := scheduler.Schedule{
		Name: scheduler.RotationCheck,
		Rule: func(s household.Settings) scheduler.Rule { return scheduler.Daily(s.ReviewTime) },
		Run:  skipped.run,
	}
	r := e.start(at(2026, 3, 7, 9, 0), reminder(&daily), review)
	r.runUntil(at(2026, 3, 9, 12, 0))

	wantKeys(t, "08:00 runs", daily.keys(), []string{"2026-03-07", "2026-03-08", "2026-03-09"})
	wantFired(t, daily.periods(),
		time.Date(2026, 3, 7, 14, 0, 0, 0, time.UTC), // 08:00 CST
		time.Date(2026, 3, 8, 13, 0, 0, 0, time.UTC), // 08:00 CDT
		time.Date(2026, 3, 9, 13, 0, 0, 0, time.UTC),
	)

	// 02:30 does not exist on Mar 8: it fires once, an hour later at
	// 03:30 CDT, keyed by Mar 8.
	wantKeys(t, "02:30 runs", skipped.keys(), []string{"2026-03-07", "2026-03-08", "2026-03-09"})
	wantFired(t, skipped.periods(),
		time.Date(2026, 3, 7, 8, 30, 0, 0, time.UTC), // 02:30 CST
		time.Date(2026, 3, 8, 8, 30, 0, 0, time.UTC), // 03:30 CDT
		time.Date(2026, 3, 9, 7, 30, 0, 0, time.UTC), // 02:30 CDT
	)
	if got := skipped.periods()[1].FireAt.Format("15:04 MST"); got != "03:30 CDT" {
		t.Errorf("skipped 02:30 fired at %s local, want 03:30 CDT", got)
	}
}

func TestFallBack(t *testing.T) {
	// Clocks in Chicago go back 02:00 CDT → 01:00 CST on Sunday 2026-11-01.
	e := newEnv(t)
	e.update(household.SettingsPatch{ReminderTime: ptr("08:00"), ReviewTime: ptr("01:30")})
	var daily, repeated recorder
	review := scheduler.Schedule{
		Name: scheduler.RotationCheck,
		Rule: func(s household.Settings) scheduler.Rule { return scheduler.Daily(s.ReviewTime) },
		Run:  repeated.run,
	}
	r := e.start(at(2026, 10, 31, 9, 0), reminder(&daily), review)
	r.runUntil(at(2026, 11, 2, 12, 0))

	wantKeys(t, "08:00 runs", daily.keys(), []string{"2026-10-31", "2026-11-01", "2026-11-02"})
	wantFired(t, daily.periods(),
		time.Date(2026, 10, 31, 13, 0, 0, 0, time.UTC), // 08:00 CDT
		time.Date(2026, 11, 1, 14, 0, 0, 0, time.UTC),  // 08:00 CST
		time.Date(2026, 11, 2, 14, 0, 0, 0, time.UTC),
	)

	// 01:30 happens twice on Nov 1: it fires once, at the first (CDT).
	wantKeys(t, "01:30 runs", repeated.keys(), []string{"2026-10-31", "2026-11-01", "2026-11-02"})
	wantFired(t, repeated.periods(),
		time.Date(2026, 10, 31, 6, 30, 0, 0, time.UTC), // 01:30 CDT
		time.Date(2026, 11, 1, 6, 30, 0, 0, time.UTC),  // first 01:30, CDT
		time.Date(2026, 11, 2, 7, 30, 0, 0, time.UTC),  // 01:30 CST
	)
}

func TestFallBackSecondOccurrenceDoesNotRerun(t *testing.T) {
	e := newEnv(t)
	e.update(household.SettingsPatch{ReviewTime: ptr("01:30")})
	var rec recorder
	review := scheduler.Schedule{
		Name: scheduler.RotationCheck,
		Rule: func(s household.Settings) scheduler.Rule { return scheduler.Daily(s.ReviewTime) },
		Run:  rec.run,
	}
	// Boot between the two 01:30s of Nov 1: the first ran in an earlier
	// process, so the second is not a new period.
	first := time.Date(2026, 11, 1, 6, 30, 0, 0, time.UTC)
	r := e.start(first.Add(-time.Minute), review)
	r.runUntil(first)
	wantKeys(t, "first 01:30", rec.keys(), []string{"2026-10-31", "2026-11-01"})
	r.stop()

	var again recorder
	review.Run = again.run
	r2 := e.start(first.Add(30*time.Minute), review)
	r2.runUntil(first.Add(2 * time.Hour)) // past the second 01:30, CST
	wantKeys(t, "second 01:30", again.keys(), nil)
}

func wantFired(t *testing.T, got []scheduler.Period, want ...time.Time) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%d runs, want %d", len(got), len(want))
	}
	for i := range want {
		if !got[i].FireAt.Equal(want[i]) {
			t.Errorf("run %s fired at %s (%s), want %s", got[i].Key, got[i].FireAt.UTC(), got[i].FireAt, want[i])
		}
	}
}

func TestISOWeek53Boundary(t *testing.T) {
	e := newEnv(t)
	var sugg, draft recorder
	// Suggestions Sunday 08:00 keys the ISO week ending that Sunday; the
	// plan draft Friday 09:00 keys the week after.
	r := e.start(at(2026, 12, 20, 9, 0), suggestions(&sugg), planDraft(&draft))
	r.runUntil(at(2027, 1, 11, 0, 0))

	wantKeys(t, "suggestions", sugg.keys(), []string{"2026-W51", "2026-W52", "2026-W53", "2027-W01"})
	// Sunday 2027-01-03 is the last day of 2026-W53.
	if p := sugg.periods()[2]; p.Date.String() != "2027-01-03" || p.Week.String() != "2026-12-28" {
		t.Errorf("2026-W53 suggestions ran on %s for week %s, want 2027-01-03 for 2026-12-28", p.Date, p.Week)
	}
	// Friday Dec 18, two days before boot, was missed.
	wantKeys(t, "plan drafts", draft.keys(), []string{"2026-W53", "2027-W01", "2027-W02"})
	// Friday 2026-12-25 drafts 2026-W53; Friday 2027-01-01 drafts 2027-W01.
	if p := draft.periods()[0]; p.Date.String() != "2026-12-25" || p.Week.String() != "2026-12-28" {
		t.Errorf("2026-W53 draft ran on %s for week %s, want 2026-12-25 for 2026-12-28", p.Date, p.Week)
	}
	if p := draft.periods()[1]; p.Date.String() != "2027-01-01" || p.Week.String() != "2027-01-04" {
		t.Errorf("2027-W01 draft ran on %s for week %s, want 2027-01-01 for 2027-01-04", p.Date, p.Week)
	}
}

func TestPeriodKey(t *testing.T) {
	date := func(s string) store.LocalDate {
		d, err := store.ParseLocalDate(s)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	tests := []struct {
		rule scheduler.Rule
		date string
		want string
	}{
		{scheduler.Daily("16:30"), "2026-12-31", "2026-12-31"},
		{scheduler.Weekly(time.Monday, "00:00"), "2026-12-28", "2026-W53"},
		{scheduler.Weekly(time.Sunday, "08:00"), "2027-01-03", "2026-W53"},
		{scheduler.Weekly(time.Monday, "00:00"), "2027-01-04", "2027-W01"},
		{scheduler.WeeklyForNextWeek(time.Friday, "09:00"), "2026-12-25", "2026-W53"},
		{scheduler.WeeklyForNextWeek(time.Friday, "09:00"), "2027-01-01", "2027-W01"},
	}
	for _, tt := range tests {
		if got := tt.rule.PeriodKey(date(tt.date)); got != tt.want {
			t.Errorf("%+v.PeriodKey(%s) = %s, want %s", tt.rule, tt.date, got, tt.want)
		}
	}
}

func TestSettingsChangeMovesNextFire(t *testing.T) {
	e := newEnv(t)
	var rec recorder
	r := e.start(at(2026, 10, 7, 12, 0), reminder(&rec))
	wantKeys(t, "boot", rec.keys(), []string{"2026-10-06"})

	// Earlier but still ahead: fires at the new time, not the old.
	e.update(household.SettingsPatch{ReminderTime: ptr("13:00")})
	r.changed()
	if next, _ := r.clock.nextDeadline(); !next.Equal(at(2026, 10, 7, 13, 0)) {
		t.Errorf("next wake %s, want the new 13:00", next)
	}
	r.runUntil(at(2026, 10, 7, 13, 0))
	wantKeys(t, "at the new time", rec.keys(), []string{"2026-10-06", "2026-10-07"})
	r.runUntil(at(2026, 10, 7, 23, 0))
	wantKeys(t, "at the old time", rec.keys(), []string{"2026-10-06", "2026-10-07"})

	// Moved to a time already passed today: nothing fires now, and
	// tomorrow fires at the new time.
	r.runUntil(at(2026, 10, 8, 12, 0))
	e.update(household.SettingsPatch{ReminderTime: ptr("11:00")})
	r.changed()
	wantKeys(t, "moved into the past", rec.keys(), []string{"2026-10-06", "2026-10-07"})
	r.runUntil(at(2026, 10, 9, 10, 59))
	wantKeys(t, "before the new time", rec.keys(), []string{"2026-10-06", "2026-10-07"})
	r.runUntil(at(2026, 10, 9, 11, 0))
	wantKeys(t, "the new time", rec.keys(), []string{"2026-10-06", "2026-10-07", "2026-10-09"})
}

func TestSettingsChangeTimezone(t *testing.T) {
	e := newEnv(t)
	var rec recorder
	r := e.start(at(2026, 10, 7, 12, 0), reminder(&rec))
	e.update(household.SettingsPatch{Timezone: ptr("America/New_York")})
	r.changed()
	// 16:30 in New York is 15:30 in Chicago.
	r.runUntil(at(2026, 10, 7, 15, 30))
	wantKeys(t, "after the zone change", rec.keys(), []string{"2026-10-06", "2026-10-07"})
	if got := rec.periods()[1].FireAt.Location().String(); got != "America/New_York" {
		t.Errorf("fired in zone %s, want America/New_York", got)
	}
}

func TestRunErrorKeepsPeriodClaimed(t *testing.T) {
	e := newEnv(t)
	rec := recorder{err: errors.New("boom")}
	r := e.start(at(2026, 10, 7, 16, 0), reminder(&rec))
	r.runUntil(at(2026, 10, 7, 23, 0))
	wantKeys(t, "runs", rec.keys(), []string{"2026-10-06", "2026-10-07"})
	r.stop()
	var again recorder
	e.start(at(2026, 10, 7, 23, 0), reminder(&again))
	wantKeys(t, "after restart", again.keys(), nil)
}

func TestRegister(t *testing.T) {
	e := newEnv(t)
	s, err := scheduler.New(e.st, e.settings, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	var rec recorder
	bad := []scheduler.Schedule{
		{Name: "hourly", Rule: reminder(&rec).Rule, Run: rec.run},
		{Name: scheduler.DayOfReminder, Run: rec.run},
		{Name: scheduler.DayOfReminder, Rule: reminder(&rec).Rule},
	}
	for _, sch := range bad {
		if err := s.Register(sch); err == nil {
			t.Errorf("Register(%q) accepted an invalid schedule", sch.Name)
		}
	}
	if err := s.Register(reminder(&rec)); err != nil {
		t.Fatal(err)
	}
	if err := s.Register(reminder(&rec)); err == nil {
		t.Error("Register accepted a second schedule with the same name")
	}
}

func TestStartRejectsInvalidRule(t *testing.T) {
	e := newEnv(t)
	s, err := scheduler.New(e.st, e.settings, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	var rec recorder
	if err := s.Register(scheduler.Schedule{
		Name: scheduler.WeekRollover,
		Rule: func(household.Settings) scheduler.Rule { return scheduler.Weekly(7, "00:00") },
		Run:  rec.run,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background()); err == nil {
		s.Wait()
		t.Fatal("Start accepted weekday 7")
	}
}

func TestStopEndsTheLoop(t *testing.T) {
	e := newEnv(t)
	var rec recorder
	r := e.start(at(2026, 10, 7, 12, 0), reminder(&rec))
	done := make(chan struct{})
	go func() {
		r.stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("scheduler did not stop")
	}
}
