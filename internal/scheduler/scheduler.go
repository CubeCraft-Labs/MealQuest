// Package scheduler runs the household's timed work: each registered
// schedule fires once per period at a household-local time read from the
// household settings.
//
// # Periods and the claim
//
// Every fire time belongs to one period, named by a period key derived in
// the household zone from the schedule's cadence (see Rule.PeriodKey): the
// local date "2026-10-09" for a daily rule, and the ISO week "2026-W41" of
// the fire date, or of the week after it, for a weekly rule. Before running,
// the scheduler inserts schedule_runs(schedule, period_key); the insert is
// the claim, and a period already claimed, by an earlier run, an earlier
// process or another instance, is skipped. A period therefore runs at most
// once: a Run that fails is logged and its period is not retried.
//
// # Catch-up
//
// When the scheduler starts, and whenever it wakes late, it looks only at
// each schedule's most recent fire time. If that time passed within
// CatchUpWindow and the period is unclaimed, the period runs once. If it
// passed longer ago, the period is claimed without running and logged as
// missed, so it can never run later, not after a restart and not after a
// settings change moves the fire time. Fire times before the most recent
// one are never looked at.
//
// # Claim failures
//
// A claim that fails (the database refused the insert) is never skipped:
// the scheduler logs it and does not advance past it, but repeats the
// whole pass every ClaimRetry until the claim succeeds. Periods the pass
// already claimed are claimed again and skipped, so each still runs once.
//
// # Time zone and DST
//
// Fire times are wall-clock times in the household zone, never the process
// zone, turned into instants by household.LocalTime. Its rule is the
// scheduler's DST rule: a time that does not exist on a spring-forward day
// fires later by the length of the gap (02:30 fires at 03:30 local), and a
// time that occurs twice on a fall-back day fires once, at its first
// occurrence. Either way the period key is that local date's, so the
// period still runs exactly once.
//
// # Settings changes
//
// The scheduler subscribes to the household settings and records each change
// with the instant it was saved. A change takes effect at once without a
// restart: fire times up to that instant follow the old settings and later
// ones the new, even when the scheduler gets to the change late. So moving
// a fire time to earlier the same day does not fire the passed time, and a
// new fire time that was still ahead when the change was saved fires.
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/household"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

// Schedule names, as the schedule_runs.schedule CHECK constraint allows.
const (
	SuggestionsWeekly    = "suggestions_weekly"
	PlanDraft            = "plan_draft"
	ShoppingChatAutosend = "shopping_chat_autosend"
	DayOfReminder        = "day_of_reminder"
	RotationCheck        = "rotation_check"
	WeekRollover         = "week_rollover"
	AIRunsPurge          = "ai_runs_purge"
)

// names are the schedule names Register accepts.
var names = map[string]bool{
	SuggestionsWeekly:    true,
	PlanDraft:            true,
	ShoppingChatAutosend: true,
	DayOfReminder:        true,
	RotationCheck:        true,
	WeekRollover:         true,
	AIRunsPurge:          true,
}

// CatchUpWindow is how long after its fire time a period that has not run
// still runs. A period found later than that is recorded as missed.
const CatchUpWindow = 24 * time.Hour

// ClaimRetry is how long the scheduler waits before repeating a pass in
// which a claim failed.
const ClaimRetry = time.Minute

// maxWait bounds one sleep, so the scheduler checks the wall clock at least
// this often even if the host was suspended or its clock stepped.
const maxWait = time.Hour

// Rule is when a schedule fires: at household-local Time ("HH:MM") every
// day, or on Weekday only when Weekly is set. Build one with Daily, Weekly
// or WeeklyForNextWeek.
type Rule struct {
	Weekly  bool
	Weekday time.Weekday
	Time    string
	// NextWeek keys a weekly period by the ISO week after the fire date,
	// for work done ahead for the coming week.
	NextWeek bool
}

// Daily fires every day at hhmm; its period is the local date.
func Daily(hhmm string) Rule {
	return Rule{Time: hhmm}
}

// Weekly fires on day at hhmm; its period is the ISO week of the fire date.
func Weekly(day time.Weekday, hhmm string) Rule {
	return Rule{Weekly: true, Weekday: day, Time: hhmm}
}

// WeeklyForNextWeek fires on day at hhmm; its period is the ISO week after
// the fire date's.
func WeeklyForNextWeek(day time.Weekday, hhmm string) Rule {
	return Rule{Weekly: true, Weekday: day, Time: hhmm, NextWeek: true}
}

// PeriodKey returns the period key of a fire on household-local date
// fireDate: the date ("2026-10-09") for a daily rule, the ISO week
// ("2026-W53") for a weekly one, and the ISO week after that ("2027-W01")
// when NextWeek is set. It panics if fireDate is the zero LocalDate.
func (r Rule) PeriodKey(fireDate store.LocalDate) string {
	if !r.Weekly {
		return fireDate.String()
	}
	if r.NextWeek {
		return household.PeriodKey(addDays(fireDate, 7))
	}
	return household.PeriodKey(fireDate)
}

// validate reports whether r can fire.
func (r Rule) validate() error {
	if r.Weekly && (r.Weekday < time.Sunday || r.Weekday > time.Saturday) {
		return fmt.Errorf("invalid weekday %d (want 0-6, Sunday = 0)", r.Weekday)
	}
	if !r.Weekly && (r.Weekday != time.Sunday || r.NextWeek) {
		return errors.New("a daily rule has no weekday or next week")
	}
	// Any date checks the time's form.
	_, err := household.LocalTime(time.UTC, store.LocalDateOf(time.Unix(0, 0).UTC()), r.Time)
	return err
}

// Period is one period of a schedule, as given to its Run.
type Period struct {
	// Key is the period key claimed in schedule_runs.
	Key string
	// FireAt is the instant the period fired, in the household zone.
	FireAt time.Time
	// Date is the household-local date of FireAt.
	Date store.LocalDate
	// Week is the ISO Monday of the week Key names; for a daily rule, of
	// the week containing Date.
	Week store.LocalDate
}

// Schedule is registered work that runs once per period.
type Schedule struct {
	// Name is one of the schedule name constants.
	Name string
	// Rule returns when the schedule fires under settings. It is called
	// again after every settings change.
	Rule func(settings household.Settings) Rule
	// Run does the period's work. It runs on the scheduler's goroutine,
	// one schedule at a time, so it must return promptly: long work is
	// enqueued as a job. ctx is cancelled when the scheduler stops. An
	// error is logged; the period stays claimed and is not retried.
	Run func(ctx context.Context, p Period) error
}

// Settings is the household settings source: *household.SettingsService.
type Settings interface {
	Get(ctx context.Context) (household.Settings, error)
	Subscribe(fn func(household.Settings)) (unsubscribe func())
}

// Scheduler runs the registered schedules.
type Scheduler struct {
	st       *store.Store
	settings Settings
	logger   *slog.Logger

	// now is the clock and after the timer; tests replace both.
	now   func() time.Time
	after func(d time.Duration) <-chan time.Time

	// wake holds one token when the settings changed since the loop last
	// looked; pending is the changes to apply, oldest first.
	wake      chan struct{}
	pendingMu sync.Mutex
	pending   []change

	mu        sync.Mutex
	schedules []Schedule
	started   bool

	wg sync.WaitGroup
}

// New returns a scheduler over st reading the household settings from
// settings.
func New(st *store.Store, settings Settings, logger *slog.Logger) (*Scheduler, error) {
	if st == nil {
		return nil, errors.New("scheduler: nil store")
	}
	if settings == nil {
		return nil, errors.New("scheduler: nil settings")
	}
	if logger == nil {
		return nil, errors.New("scheduler: nil logger")
	}
	return &Scheduler{
		st:       st,
		settings: settings,
		logger:   logger,
		now:      time.Now,
		after:    time.After,
		wake:     make(chan struct{}, 1),
	}, nil
}

// Register adds sch. It must be called before Start, once per name.
func (s *Scheduler) Register(sch Schedule) error {
	if !names[sch.Name] {
		return fmt.Errorf("scheduler: register: unknown schedule %q", sch.Name)
	}
	if sch.Rule == nil {
		return fmt.Errorf("scheduler: register %s: nil rule", sch.Name)
	}
	if sch.Run == nil {
		return fmt.Errorf("scheduler: register %s: nil run", sch.Name)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return fmt.Errorf("scheduler: register %s: already started", sch.Name)
	}
	for _, have := range s.schedules {
		if have.Name == sch.Name {
			return fmt.Errorf("scheduler: register %s: already registered", sch.Name)
		}
	}
	s.schedules = append(s.schedules, sch)
	return nil
}

// Start reads the settings and starts the scheduler, which first catches
// up on each schedule's most recent period and then fires schedules as
// their times come. Settings whose zone or rules are invalid are an error
// and nothing starts. The scheduler stops when ctx is cancelled; Wait waits
// for it.
func (s *Scheduler) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return errors.New("scheduler: already started")
	}
	// Subscribing first means a change saved after the read below still
	// wakes the loop.
	unsubscribe := s.settings.Subscribe(s.changed)
	settings, err := s.settings.Get(ctx)
	if err != nil {
		unsubscribe()
		return fmt.Errorf("scheduler: %w", err)
	}
	p, err := compile(s.schedules, settings)
	if err != nil {
		unsubscribe()
		return err
	}
	s.started = true
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer unsubscribe()
		s.loop(ctx, p)
	}()
	return nil
}

// Wait blocks until the scheduler has stopped.
func (s *Scheduler) Wait() {
	s.wg.Wait()
}

// change is a settings change and the instant it was saved.
type change struct {
	at       time.Time
	settings household.Settings
}

// changed is the settings subscriber. It runs on the updating goroutine, so
// it only records the change and wakes the loop.
func (s *Scheduler) changed(settings household.Settings) {
	s.pendingMu.Lock()
	s.pending = append(s.pending, change{at: s.now(), settings: settings})
	s.pendingMu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default: // the loop already has a token and reads pending then
	}
}

// takeChanges returns and clears the pending changes.
func (s *Scheduler) takeChanges() []change {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	out := s.pending
	s.pending = nil
	return out
}

// plan is the schedules compiled against one version of the settings.
type plan struct {
	loc   *time.Location
	items []item
}

type item struct {
	sch  Schedule
	rule Rule
}

// compile builds the plan for settings.
func compile(schedules []Schedule, settings household.Settings) (*plan, error) {
	loc, err := household.LoadZone(settings.Timezone)
	if err != nil {
		return nil, fmt.Errorf("scheduler: household zone: %w", err)
	}
	p := &plan{loc: loc, items: make([]item, len(schedules))}
	for i, sch := range schedules {
		rule := sch.Rule(settings)
		if err := rule.validate(); err != nil {
			return nil, fmt.Errorf("scheduler: %s: %w", sch.Name, err)
		}
		p.items[i] = item{sch: sch, rule: rule}
	}
	return p, nil
}

// loop fires schedules until ctx is cancelled. done is the instant up to
// which fire times have been handled; it starts at the zero time so the
// first pass catches up. Each pass first applies the pending settings
// changes in order, handling fire times up to a change under the plan it
// replaces, then handles fire times up to now under the current plan. A
// pass with a failed claim keeps done and its unapplied changes where they
// were and is repeated after ClaimRetry.
func (s *Scheduler) loop(ctx context.Context, p *plan) {
	var done time.Time
	var pending []change
	for {
		now := s.now()
		pending = append(pending, s.takeChanges()...)
		failed := false
		for len(pending) > 0 {
			c := pending[0]
			if c.at.After(done) {
				if p != nil && !s.fire(ctx, p, done, c.at, now) {
					failed = true
					break
				}
				done = c.at
			}
			p = s.compileChange(ctx, c.settings)
			pending = pending[1:]
		}
		if !failed {
			if p == nil || s.fire(ctx, p, done, now, now) {
				done = now
			} else {
				failed = true
			}
		}
		if ctx.Err() != nil {
			return
		}

		var timeout <-chan time.Time
		switch {
		case failed:
			timeout = s.after(ClaimRetry)
		case p != nil:
			if next, ok := p.next(now); ok {
				timeout = s.after(min(next.Sub(now), maxWait))
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-timeout:
		case <-s.wake:
		}
	}
}

// compileChange returns the plan for changed settings, or nil when they
// are unusable.
func (s *Scheduler) compileChange(ctx context.Context, settings household.Settings) *plan {
	p, err := compile(s.schedules, settings)
	if err != nil {
		// Settings are validated before they are saved, so this is a
		// bug; firing at a guessed time would be worse than not firing
		// until the settings are fixed.
		s.logger.ErrorContext(ctx, "scheduler: settings unusable; no schedule fires until they change", "error", err)
		return nil
	}
	return p
}

// fire handles, for every schedule, its most recent fire time at or before
// until if it is after done; now is the current instant. It reports false
// when a claim failed, so the fire times it covered must be handled again.
func (s *Scheduler) fire(ctx context.Context, p *plan, done, until, now time.Time) bool {
	ok := true
	for _, it := range p.items {
		if ctx.Err() != nil {
			return ok
		}
		at, due := fireBefore(it.rule, p.loc, until)
		if !due || !at.After(done) {
			continue
		}
		if !s.handle(ctx, it, p.loc, at, now) {
			ok = false
		}
	}
	return ok
}

// handle claims the period of it fired at at and runs it, or records it as
// missed when at is more than CatchUpWindow before now. It reports false
// when the claim failed and the period is still unclaimed.
func (s *Scheduler) handle(ctx context.Context, it item, loc *time.Location, at, now time.Time) bool {
	date := household.Today(at, loc)
	period := Period{Key: it.rule.PeriodKey(date), FireAt: at, Date: date}
	if it.rule.NextWeek {
		period.Week = household.WeekStart(addDays(date, 7))
	} else {
		period.Week = household.WeekStart(date)
	}
	log := s.logger.With("schedule", it.sch.Name, "period", period.Key, "fire_at", at.Format(time.RFC3339))

	claimed, err := s.claim(ctx, it.sch.Name, period.Key, now)
	if err != nil {
		log.ErrorContext(ctx, "scheduler: claim period failed; retrying", "error", err, "retry_in", ClaimRetry.String())
		return false
	}
	if !claimed {
		return true
	}
	if late := now.Sub(at); late > CatchUpWindow {
		log.WarnContext(ctx, "scheduler: period missed; recorded without running", "late", late.String())
		return true
	}
	if err := it.sch.Run(ctx, period); err != nil {
		log.ErrorContext(ctx, "scheduler: run failed", "error", err)
		return true
	}
	log.InfoContext(ctx, "scheduler: ran")
	return true
}

// claim inserts schedule_runs(name, key) and reports whether this call
// inserted it.
func (s *Scheduler) claim(ctx context.Context, name, key string, now time.Time) (bool, error) {
	n, err := db.New(s.st.Writer).ClaimScheduleRun(ctx, db.ClaimScheduleRunParams{
		ID:        store.NewID(),
		Schedule:  name,
		PeriodKey: key,
		CreatedAt: store.Timestamp{Time: now.UTC().Truncate(time.Millisecond)}.String(),
	})
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

// next returns the earliest fire time of any schedule after now, or false
// when there is no schedule.
func (p *plan) next(now time.Time) (time.Time, bool) {
	var next time.Time
	for _, it := range p.items {
		at, ok := fireAfter(it.rule, p.loc, now)
		if ok && (next.IsZero() || at.Before(next)) {
			next = at
		}
	}
	return next, !next.IsZero()
}

// window is how many days either side of a date fireBefore and fireAfter
// search: more than a week, so a weekly rule always has a fire time on
// each side.
const window = 8

// fireBefore returns the latest fire time of r in zone loc at or before t.
func fireBefore(r Rule, loc *time.Location, t time.Time) (time.Time, bool) {
	var best time.Time
	for _, at := range fireTimes(r, loc, t) {
		if !at.After(t) && (best.IsZero() || at.After(best)) {
			best = at
		}
	}
	return best, !best.IsZero()
}

// fireAfter returns the earliest fire time of r in zone loc after t.
func fireAfter(r Rule, loc *time.Location, t time.Time) (time.Time, bool) {
	var best time.Time
	for _, at := range fireTimes(r, loc, t) {
		if at.After(t) && (best.IsZero() || at.Before(best)) {
			best = at
		}
	}
	return best, !best.IsZero()
}

// fireTimes returns r's fire times on the local dates within window days
// of t's. A rule that fails to resolve was validated at compile, so it
// cannot.
func fireTimes(r Rule, loc *time.Location, t time.Time) []time.Time {
	today := household.Today(t, loc)
	var out []time.Time
	for i := -window; i <= window; i++ {
		d := addDays(today, i)
		if r.Weekly && weekday(d) != r.Weekday {
			continue
		}
		at, err := household.LocalTime(loc, d, r.Time)
		if err != nil {
			panic(fmt.Sprintf("scheduler: validated rule %+v: %v", r, err))
		}
		out = append(out, at)
	}
	return out
}

// addDays returns d plus n calendar days.
func addDays(d store.LocalDate, n int) store.LocalDate {
	y, m, day := d.Date()
	return store.LocalDateOf(time.Date(y, m, day+n, 0, 0, 0, 0, time.UTC))
}

// weekday returns the day of the week of d.
func weekday(d store.LocalDate) time.Weekday {
	y, m, day := d.Date()
	return time.Date(y, m, day, 0, 0, 0, 0, time.UTC).Weekday()
}
