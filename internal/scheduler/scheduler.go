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
// When the scheduler starts, and whenever it wakes late, it handles, oldest
// first, every fire time of each schedule that passed within CatchUpWindow,
// and the most recent fire time even when it passed longer ago. Each
// unclaimed period within CatchUpWindow runs once. A most recent one that
// passed longer ago is claimed without running and logged as missed, so it
// can never run later, not after a restart and not after a settings change
// moves the fire time. Other fire times older than CatchUpWindow are never
// looked at.
//
// # Claim failures
//
// A claim that fails (the database refused the insert) is never skipped:
// the scheduler logs it and keeps that period's fire time, and every
// ClaimRetry claims that same period again until the claim succeeds. Only
// then does it apply the catch-up rule to it, running it if its fire time
// is within CatchUpWindow of the successful claim and recording it as
// missed otherwise, and only then does it go on to later fire times, even
// when one has passed meanwhile. Each period is claimed by its own fire
// time, so the later period runs once as well.
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
// # Fire times from data
//
// A DailyAt rule fires at a time read from the household's data rather
// than the settings: its function gives, for each household-local date,
// the instant the date's period fires, or none. The period is that date
// even when the instant falls on another day. The scheduler asks again on
// every pass, and whoever changes that data calls Wake once the change is
// committed, so the next pass comes at once and a fire time the change
// moved fires on time rather than up to maxWait late; the claim still runs
// a period at most once. A lookup that fails is logged and repeated every
// ClaimRetry, as a failed claim is.
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
	"slices"
	"strings"
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
// day, or on Weekday only when Weekly is set, or every day at the instant
// At gives. Build one with Daily, Weekly, WeeklyForNextWeek or DailyAt.
type Rule struct {
	Weekly  bool
	Weekday time.Weekday
	Time    string
	// NextWeek keys a weekly period by the ISO week after the fire date,
	// for work done ahead for the coming week.
	NextWeek bool
	// At, instead of Time, gives the instant the period of a
	// household-local date fires.
	At FireAt
}

// FireAt gives the instant the period of household-local date fires, or
// false when that date's period does not fire.
type FireAt func(ctx context.Context, date store.LocalDate) (time.Time, bool, error)

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

// DailyAt fires at most once a day, at the instant at gives for the date;
// its period is that date, wherever the instant falls.
func DailyAt(at FireAt) Rule {
	return Rule{At: at}
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
	if r.At != nil {
		if r.Weekly || r.Time != "" {
			return errors.New("a DailyAt rule has no weekday or time")
		}
		return nil
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
	// Key, when set, prefixes the schedule's period keys with Key and a
	// colon ("start:2026-10-14"), so two schedules of one Name claim
	// different periods. Name and Key together are unique.
	Key string
	// PeriodKey, when set, gives the period key claimed for p in place of
	// the rule's, such as "list_ready:chat:2026-10-19" for the week p.Week;
	// p.Key is the rule's key. It must give each period its own key. A
	// schedule sets Key or PeriodKey, not both.
	PeriodKey func(p Period) string
	// Rule returns when the schedule fires under settings. It is called
	// again after every settings change.
	Rule func(settings household.Settings) Rule
	// Run does the period's work. It runs on the scheduler's goroutine,
	// one schedule at a time, so it must return promptly: long work is
	// enqueued as a job. ctx is cancelled when the scheduler stops. An
	// error is logged; the period stays claimed and is not retried.
	Run func(ctx context.Context, p Period) error
}

// label names sch in errors and logs: its Name, and its Key if set.
func (sch Schedule) label() string {
	if sch.Key == "" {
		return sch.Name
	}
	return sch.Name + ":" + sch.Key
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

	// wake holds one token when the settings or the data a DailyAt rule
	// reads changed since the loop last looked; pending is the settings
	// changes to apply, oldest first.
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
		return fmt.Errorf("scheduler: register %s: nil rule", sch.label())
	}
	if sch.Run == nil {
		return fmt.Errorf("scheduler: register %s: nil run", sch.label())
	}
	if strings.Contains(sch.Key, ":") {
		return fmt.Errorf("scheduler: register %s: key has a colon", sch.label())
	}
	if sch.Key != "" && sch.PeriodKey != nil {
		return fmt.Errorf("scheduler: register %s: both key and period key", sch.label())
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return fmt.Errorf("scheduler: register %s: already started", sch.label())
	}
	for _, have := range s.schedules {
		if have.Name == sch.Name && have.Key == sch.Key {
			return fmt.Errorf("scheduler: register %s: already registered", sch.label())
		}
	}
	s.schedules = append(s.schedules, sch)
	return nil
}

// Start reads the settings and starts the scheduler, which first catches
// up on each schedule's recent periods and then fires schedules as
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
	s.signal()
}

// Wake makes the scheduler look up its fire times again now rather than at
// its next timer. Call it after committing a change to the data a DailyAt
// rule reads, such as tonight's planned dinner. It never blocks.
func (s *Scheduler) Wake() {
	s.signal()
}

// Now is the time on the scheduler's clock, by which its schedules fire.
func (s *Scheduler) Now() time.Time {
	return s.now()
}

// signal leaves one token in wake for the loop.
func (s *Scheduler) signal() {
	select {
	case s.wake <- struct{}{}:
	default: // the loop already has a token and looks again then
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
			return nil, fmt.Errorf("scheduler: %s: %w", sch.label(), err)
		}
		p.items[i] = item{sch: sch, rule: rule}
	}
	return p, nil
}

// loop fires schedules until ctx is cancelled. done is the instant up to
// which fire times have been handled; it starts at the zero time so the
// first pass catches up. retry is the periods whose claim failed, each
// with its own fire time. Each pass first claims retry again; while any
// claim there still fails, the pass stops and is repeated after
// ClaimRetry. Otherwise it applies the pending settings changes in order,
// handling fire times up to a change under the plan it replaces, then
// handles fire times up to now under the current plan, stopping after the
// first stretch in which a claim failed.
func (s *Scheduler) loop(ctx context.Context, p *plan) {
	var done time.Time
	var pending []change
	var retry []target
	for {
		now := s.now()
		pending = append(pending, s.takeChanges()...)
		retry = s.retry(ctx, retry, now)
		for len(retry) == 0 && len(pending) > 0 {
			c := pending[0]
			if c.at.After(done) {
				if p != nil {
					retry = s.fire(ctx, p, done, c.at, now)
				}
				done = c.at
			}
			p = s.compileChange(ctx, c.settings)
			pending = pending[1:]
		}
		if len(retry) == 0 {
			if p != nil {
				retry = s.fire(ctx, p, done, now, now)
			}
			done = now
		}
		if ctx.Err() != nil {
			return
		}

		var timeout <-chan time.Time
		switch {
		case len(retry) > 0:
			timeout = s.after(ClaimRetry)
		case p != nil:
			next, ok, err := p.next(ctx, now)
			switch {
			case err != nil:
				s.logger.ErrorContext(ctx, "scheduler: look up next fire time failed; retrying", "error", err, "retry_in", ClaimRetry.String())
				timeout = s.after(ClaimRetry)
			case ok:
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

// target is one schedule's periods still to handle, oldest first, each
// named by its fire time and the household-local date of its period. A
// target whose fire times could not be looked up has lookup set instead,
// with the stretch (done, until] to look them up in again.
type target struct {
	it    item
	loc   *time.Location
	fires []fireTime

	lookup      bool
	done, until time.Time
}

// fire handles, for every schedule, its catch-up fire times after done and
// at or before until; now is the current instant. It returns the
// targets whose lookup or claim failed, to be handled again.
func (s *Scheduler) fire(ctx context.Context, p *plan, done, until, now time.Time) []target {
	var failed []target
	for _, it := range p.items {
		if ctx.Err() != nil {
			return failed
		}
		if t, ok := s.fireItem(ctx, it, p.loc, done, until, now); !ok {
			failed = append(failed, t)
		}
	}
	return failed
}

// fireItem handles, oldest first, it's fire times after done and at or
// before until that passed within CatchUpWindow of now, and the most recent
// of them even when it passed longer ago. It reports false, with the target
// to handle again, when the fire times could not be looked up or a period's
// claim failed.
func (s *Scheduler) fireItem(ctx context.Context, it item, loc *time.Location, done, until, now time.Time) (target, bool) {
	fs, err := fireBetween(ctx, it.rule, loc, done, until)
	if err != nil {
		s.logger.ErrorContext(ctx, "scheduler: look up fire time failed; retrying",
			"schedule", it.sch.label(), "error", err, "retry_in", ClaimRetry.String())
		return target{it: it, loc: loc, lookup: true, done: done, until: until}, false
	}
	i := 0
	for i < len(fs)-1 && now.Sub(fs[i].at) > CatchUpWindow {
		i++
	}
	return s.handleAll(ctx, target{it: it, loc: loc, fires: fs[i:]}, now)
}

// handleAll handles the periods of t in order, stopping at the first whose
// claim failed. It reports false, with that period and those after it,
// when one did.
func (s *Scheduler) handleAll(ctx context.Context, t target, now time.Time) (target, bool) {
	for i, f := range t.fires {
		if !s.handle(ctx, t.it, f, now) {
			t.fires = t.fires[i:]
			return t, false
		}
	}
	return target{}, true
}

// retry handles targets again at now and returns those whose lookup or
// claim still failed.
func (s *Scheduler) retry(ctx context.Context, targets []target, now time.Time) []target {
	var failed []target
	for i, t := range targets {
		if ctx.Err() != nil {
			return append(failed, targets[i:]...)
		}
		if t.lookup {
			if again, ok := s.fireItem(ctx, t.it, t.loc, t.done, t.until, now); !ok {
				failed = append(failed, again)
			}
			continue
		}
		if again, ok := s.handleAll(ctx, t, now); !ok {
			failed = append(failed, again)
		}
	}
	return failed
}

// handle claims the period of it's fire time f and runs it, or records it
// as missed when f is more than CatchUpWindow before now. It reports false
// when the claim failed and the period is still unclaimed.
func (s *Scheduler) handle(ctx context.Context, it item, f fireTime, now time.Time) bool {
	at, date := f.at, f.date
	period := Period{Key: it.rule.PeriodKey(date), FireAt: at, Date: date}
	if it.rule.NextWeek {
		period.Week = household.WeekStart(addDays(date, 7))
	} else {
		period.Week = household.WeekStart(date)
	}
	switch {
	case it.sch.Key != "":
		period.Key = it.sch.Key + ":" + period.Key
	case it.sch.PeriodKey != nil:
		period.Key = it.sch.PeriodKey(period)
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
// when there is none.
func (p *plan) next(ctx context.Context, now time.Time) (time.Time, bool, error) {
	var next time.Time
	for _, it := range p.items {
		f, ok, err := fireAfter(ctx, it.rule, p.loc, now)
		if err != nil {
			return time.Time{}, false, fmt.Errorf("%s: %w", it.sch.label(), err)
		}
		if ok && (next.IsZero() || f.at.Before(next)) {
			next = f.at
		}
	}
	return next, !next.IsZero(), nil
}

// window is how many days either side of a date fireBetween and fireAfter
// search: more than a week, so a weekly rule always has a fire time on
// each side.
const window = 8

// atWindow is how many days either side of a date fireBetween and fireAfter
// look up a DailyAt rule's fire times. A period firing further off is
// looked up on a later pass, which comes within maxWait.
const atWindow = 2

// fireTime is a fire time and the household-local date of its period.
type fireTime struct {
	at   time.Time
	date store.LocalDate
}

// fireBetween returns the fire times of r in zone loc after from and at or
// before t, oldest first.
func fireBetween(ctx context.Context, r Rule, loc *time.Location, from, t time.Time) ([]fireTime, error) {
	fs, err := fireTimes(ctx, r, loc, t)
	if err != nil {
		return nil, err
	}
	var out []fireTime
	for _, f := range fs {
		if f.at.After(from) && !f.at.After(t) {
			out = append(out, f)
		}
	}
	slices.SortFunc(out, func(a, b fireTime) int { return a.at.Compare(b.at) })
	return out, nil
}

// fireAfter returns the earliest fire time of r in zone loc after t.
func fireAfter(ctx context.Context, r Rule, loc *time.Location, t time.Time) (fireTime, bool, error) {
	fs, err := fireTimes(ctx, r, loc, t)
	if err != nil {
		return fireTime{}, false, err
	}
	var best fireTime
	for _, f := range fs {
		if f.at.After(t) && (best.at.IsZero() || f.at.Before(best.at)) {
			best = f
		}
	}
	return best, !best.at.IsZero(), nil
}

// fireTimes returns r's fire times for the local dates within window days
// of t's, or atWindow days for a DailyAt rule. A Time that fails to
// resolve was validated at compile, so it cannot; a DailyAt lookup that
// fails is an error.
func fireTimes(ctx context.Context, r Rule, loc *time.Location, t time.Time) ([]fireTime, error) {
	today := household.Today(t, loc)
	span := window
	if r.At != nil {
		span = atWindow
	}
	var out []fireTime
	for i := -span; i <= span; i++ {
		d := addDays(today, i)
		if r.Weekly && weekday(d) != r.Weekday {
			continue
		}
		if r.At != nil {
			at, ok, err := r.At(ctx, d)
			if err != nil {
				return nil, fmt.Errorf("fire time of %s: %w", d, err)
			}
			if ok {
				out = append(out, fireTime{at: at, date: d})
			}
			continue
		}
		at, err := household.LocalTime(loc, d, r.Time)
		if err != nil {
			panic(fmt.Sprintf("scheduler: validated rule %+v: %v", r, err))
		}
		out = append(out, fireTime{at: at, date: d})
	}
	return out, nil
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
