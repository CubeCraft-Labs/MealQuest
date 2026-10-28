// Package plan keeps the household's weekly plans: the Draft board the AI
// drafts from the Regulars and the Quest log, and the swaps the household
// makes on it. A draft runs as a job: draft_week plans seven dinners, one
// a day from Monday to Sunday, and the job stores them as the week's plan,
// replacing any draft before it unless the household edited the plan
// while draft_week ran. A plan is draft until Push to Calendar
// syncs it (Push); an edit of a synced plan makes it changed.
//
// The Rotation check follows a plan day: MarkTried moves the Quest log
// dinners planned on it to tried, and the household answers "Keep it in
// rotation?" for each (D-021). Only their keep makes a dinner a Regular.
package plan

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/skills"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/chat"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/dinners"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/household"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/jobs"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/notify"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

// KindDraftWeek is the job that drafts a week's plan.
const KindDraftWeek = "draft_week"

// Plan statuses (the week_plans.status column).
const (
	StatusDraft   = "draft"
	StatusSynced  = "synced"
	StatusChanged = "changed"
)

// Reasons a draft failed, stored as the KindDraftWeek job's failure code.
// Any other failure, such as the database's, is the runner's
// internal_error.
const (
	// ReasonNotEnoughDinners: the Regulars and Quest log hold fewer than
	// seven dinners to draft from.
	ReasonNotEnoughDinners = "not_enough_dinners"
	// ReasonAIFailed: draft_week failed; no week is made up instead.
	ReasonAIFailed = "ai_failed"
	// ReasonConfirmRequired: the plan was synced after the draft was
	// asked for without confirm, so the draft does not replace it.
	ReasonConfirmRequired = "confirm_required"
	// ReasonPlanChanged: the plan was edited while draft_week ran, so the
	// draft does not replace the edit.
	ReasonPlanChanged = "plan_changed"
	// ReasonPoolChanged: a dinner the draft planned left the pool while
	// draft_week ran (archived, tried, or its Quest log dinner planned on
	// another week), so the draft is not stored; drafting again uses the
	// pool as it is now.
	ReasonPoolChanged = "pool_changed"
)

// Reasons are every draft failure reason.
var Reasons = []string{ReasonNotEnoughDinners, ReasonAIFailed, ReasonConfirmRequired, ReasonPlanChanged, ReasonPoolChanged}

// ContextBudget is the character budget of the household context
// draft_week is given (see harness.BuildContext).
const ContextBudget = 16000

// plannable are the dinner statuses a day may be swapped to.
var plannable = []string{dinners.StatusRegular, dinners.StatusQuestLog}

// ErrNotFound is returned for a week with no plan.
var ErrNotFound = errors.New("plan not found")

// ErrDinnerNotFound is a swap to an unknown dinner.
var ErrDinnerNotFound = errors.New("dinner not found")

// ErrDinnerAlreadyPlanned is a swap to a dinner already on another day of
// the week.
var ErrDinnerAlreadyPlanned = errors.New("the dinner is already on another day of this week")

// ErrConfirmRequired is a draft over a synced or changed plan asked for
// without confirm.
var ErrConfirmRequired = errors.New("the plan was pushed to Calendar; drafting over it needs confirm")

// ErrDraftRunning is a confirmed draft asked for while a draft of the week
// asked for without confirm is queued or running.
var ErrDraftRunning = errors.New("a draft of the week without confirm is already running; ask again when it finishes")

// InvalidWeekError is a week start that is not a Monday date.
type InvalidWeekError struct {
	Value string
}

func (e *InvalidWeekError) Error() string {
	return fmt.Sprintf("week start %q is not a Monday date (YYYY-MM-DD)", e.Value)
}

// InvalidDayError is a swap of a date outside the plan's week.
type InvalidDayError struct {
	Value string
}

func (e *InvalidDayError) Error() string {
	return fmt.Sprintf("date %q is not a day of the week (YYYY-MM-DD)", e.Value)
}

// DinnerNotPlannableError is a swap to a dinner that is neither a Regular
// nor in the Quest log.
type DinnerNotPlannableError struct {
	Status string
}

func (e *DinnerNotPlannableError) Error() string {
	return fmt.Sprintf("the dinner is %s; only Regulars and Quest log dinners can be planned", e.Status)
}

// NotCookedError is a swap to a Delivery or Dining Out dinner. Those
// dinners are not planned.
type NotCookedError struct {
	MealSource string
}

// uncookedLabel is the calendar line for a dinner that is not cooked at
// home. An unknown source is an error: nothing is substituted for it.
func uncookedLabel(source string) (string, error) {
	switch source {
	case dinners.MealSourceDelivery:
		return "Delivery", nil
	case dinners.MealSourceDiningOut:
		return "Dining out", nil
	default:
		return "", fmt.Errorf("plan: meal source %q is not delivery or dining out", source)
	}
}

func (e *NotCookedError) Error() string {
	switch e.MealSource {
	case dinners.MealSourceDelivery:
		return "the dinner is delivery; only a dinner cooked at home can be planned"
	case dinners.MealSourceDiningOut:
		return "the dinner is dining out; only a dinner cooked at home can be planned"
	default:
		return fmt.Sprintf("the dinner is %s; only a dinner cooked at home can be planned", e.MealSource)
	}
}

// ParseWeekStart parses s, a household-local Monday as YYYY-MM-DD.
func ParseWeekStart(s string) (store.LocalDate, error) {
	d, err := store.ParseLocalDate(s)
	if err != nil {
		return store.LocalDate{}, &InvalidWeekError{Value: s}
	}
	if _, err := household.WeekDays(d); err != nil {
		return store.LocalDate{}, &InvalidWeekError{Value: s}
	}
	return d, nil
}

// Drafter runs draft_week on in; scope names the week. In production it is
// skills.DraftWeek with the household's harness.Config.
type Drafter func(ctx context.Context, scope string, in skills.WeekRequest) (skills.Week, error)

// Service drafts, reads, edits and pushes weekly plans.
type Service struct {
	st       *store.Store
	draft    Drafter
	cal      Calendar
	notifier *notify.Service
	// wake is called after every committed change to a plan's days, which
	// can move a crock-pot dinner's start reminder.
	wake     func()
	logger   *slog.Logger
	draftJob *jobs.Kind[DraftJob]
	// publicURL is MEALQUEST_PUBLIC_URL, under which a pushed event links
	// to its dinner.
	publicURL *url.URL
}

// NewService returns a service over st that drafts with draft, pushes to
// cal with links under publicURL, tells the household through notifier,
// which must be over st, calls wake (the scheduler's Wake) after every
// committed change to a plan's days, and registers the KindDraftWeek job on
// runner, which must not have started yet. The kind is service-only:
// StartDraft and StartScheduledDraft, which keep one draft in flight per
// week, are the only way to enqueue it.
func NewService(st *store.Store, draft Drafter, runner *jobs.Runner, cal Calendar, notifier *notify.Service, wake func(), publicURL *url.URL, logger *slog.Logger) (*Service, error) {
	switch {
	case st == nil:
		return nil, errors.New("plan: nil store")
	case draft == nil:
		return nil, errors.New("plan: nil drafter")
	case runner == nil:
		return nil, errors.New("plan: nil job runner")
	case cal == nil:
		return nil, errors.New("plan: nil calendar")
	case notifier == nil:
		return nil, errors.New("plan: nil notifier")
	case wake == nil:
		return nil, errors.New("plan: nil wake")
	case publicURL == nil:
		return nil, errors.New("plan: nil public URL")
	case logger == nil:
		return nil, errors.New("plan: nil logger")
	}
	s := &Service{st: st, draft: draft, cal: cal, notifier: notifier, wake: wake, logger: logger, publicURL: publicURL}
	kind, err := jobs.Register(runner, KindDraftWeek, s.runDraft, Reasons...)
	if err != nil {
		return nil, err
	}
	if err := kind.ServiceOnly(); err != nil {
		return nil, err
	}
	s.draftJob = kind
	return s, nil
}

// Plan is a week's plan as the API shows it.
type Plan struct {
	ID        string `json:"id"`
	WeekStart string `json:"week_start"`
	// WeekLabel is e.g. "Week 41 · Oct 5 – 11".
	WeekLabel        string  `json:"week_label"`
	Status           string  `json:"status"`
	AINote           *string `json:"ai_note"`
	CalendarSyncedAt *string `json:"calendar_synced_at"`
	Days             []Day   `json:"days"`
	UpdatedAt        string  `json:"updated_at"`
}

// Day is one day of a plan.
type Day struct {
	// Date is household-local, YYYY-MM-DD.
	Date   string `json:"date"`
	Dinner Dinner `json:"dinner"`
	// IsNew is true for a dinner planned from the Quest log.
	IsNew bool `json:"is_new"`
	// StartBy is when a crock-pot dinner must start to be ready at the
	// household's dinner time; null for any other dinner and for a
	// crock-pot dinner whose minutes are unknown.
	StartBy *time.Time `json:"start_by"`
}

// Dinner is a planned dinner as a plan day shows it.
type Dinner struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Status is the dinner's status now (dinners.Status*); a dinner
	// archived after the plan was made stays on it, archived.
	Status  string  `json:"status"`
	Photo   *string `json:"photo"`
	Minutes *int64  `json:"minutes"`
	Method  *string `json:"method"`
	Origin  string  `json:"origin"`
	// MealSource is cook, delivery or dining_out. A dinner that is not
	// cooked at home has no photo and no recipe to open.
	MealSource string `json:"meal_source"`
}

// viewable reports whether the dinner has a recipe to open: every status
// but archived, and only a dinner cooked at home. Delivery and dining out
// are not archived; they have no recipe of their own.
func (d Dinner) viewable() bool {
	return d.Status != dinners.StatusArchived && dinners.Cooked(d.MealSource)
}

// Get returns the plan of the week starting weekStart, or ErrNotFound.
// The plan and the household settings come from one read transaction.
func (s *Service) Get(ctx context.Context, weekStart store.LocalDate) (p Plan, err error) {
	tx, err := s.st.Reader.BeginTx(ctx, nil)
	if err != nil {
		return Plan{}, fmt.Errorf("plan: begin read: %w", err)
	}
	defer func() {
		err = errors.Join(err, tx.Rollback())
	}()
	return readPlan(ctx, db.New(tx), weekStart)
}

// EmitDayOfReminder emits day_of_reminder for date, keyed by the date,
// "Tonight: {dinner} · Recipe →" for the dinner planned on it, and reports
// whether it did: a date whose week has no plan emits nothing. A dinner
// archived since it was planned has no recipe, so its reminder offers none
// (chat.DayOfReminder). The day_of_reminder schedule calls it at the
// household's reminder_time (ReminderSchedules); emitting a date again
// stores no second notification.
func (s *Service) EmitDayOfReminder(ctx context.Context, date store.LocalDate) (bool, error) {
	p, err := s.Get(ctx, household.WeekStart(date))
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	d, err := p.day(date)
	if err != nil {
		return false, err
	}
	var ev notify.Event
	if d.Dinner.Status != dinners.StatusArchived && !dinners.Cooked(d.Dinner.MealSource) {
		var err error
		ev, err = chat.UncookedReminder(date, d.Dinner.Name, d.Dinner.MealSource)
		if err != nil {
			return false, fmt.Errorf("plan: %w", err)
		}
	} else {
		ev = chat.DayOfReminder(date, d.Dinner.ID, d.Dinner.Name, d.Dinner.viewable())
	}
	if err := s.notifier.Emit(ctx, ev); err != nil {
		return false, fmt.Errorf("plan: %w", err)
	}
	return true, nil
}

// readPlan reads the plan of the week starting weekStart through q, with
// each day's start_by from the household's dinner time and zone.
func readPlan(ctx context.Context, q *db.Queries, weekStart store.LocalDate) (Plan, error) {
	label, err := household.WeekLabel(weekStart)
	if err != nil {
		return Plan{}, fmt.Errorf("plan: %w", err)
	}
	row, err := q.GetWeekPlan(ctx, weekStart.String())
	if errors.Is(err, sql.ErrNoRows) {
		return Plan{}, ErrNotFound
	}
	if err != nil {
		return Plan{}, fmt.Errorf("plan: read week %s: %w", weekStart, err)
	}
	settings, err := q.GetHouseholdSettings(ctx)
	if err != nil {
		return Plan{}, fmt.Errorf("plan: read household settings: %w", err)
	}
	zone, err := household.LoadZone(settings.Timezone)
	if err != nil {
		return Plan{}, fmt.Errorf("plan: household settings: %w", err)
	}
	rows, err := q.ListPlanDays(ctx, row.ID)
	if err != nil {
		return Plan{}, fmt.Errorf("plan: read days of %s: %w", row.ID, err)
	}
	dates, err := household.WeekDays(weekStart)
	if err != nil {
		return Plan{}, fmt.Errorf("plan: %w", err)
	}
	if len(rows) != len(dates) {
		return Plan{}, fmt.Errorf("plan: week %s has %d planned days, want %d", weekStart, len(rows), len(dates))
	}
	p := Plan{
		ID:               row.ID,
		WeekStart:        row.WeekStart,
		WeekLabel:        label,
		Status:           row.Status,
		AINote:           nullString(row.AiNote),
		CalendarSyncedAt: nullString(row.CalendarSyncedAt),
		Days:             make([]Day, len(rows)),
		UpdatedAt:        row.UpdatedAt,
	}
	for i, r := range rows {
		if r.Date != dates[i].String() {
			return Plan{}, fmt.Errorf("plan: week %s day %d is %s, want %s", weekStart, i+1, r.Date, dates[i])
		}
		startBy, err := startBy(zone, dates[i], settings.DinnerTime, r.Dinner)
		if err != nil {
			return Plan{}, fmt.Errorf("plan: start by of %s: %w", r.Date, err)
		}
		p.Days[i] = Day{Date: r.Date, Dinner: fromDinner(r.Dinner), IsNew: r.IsNew == 1, StartBy: startBy}
	}
	return p, nil
}

// startBy returns when crock-pot dinner d must start on date to be ready at
// dinnerTime in zone, or nil for any other dinner and for a crock-pot
// dinner whose minutes are unknown.
func startBy(zone *time.Location, date store.LocalDate, dinnerTime string, d db.Dinner) (*time.Time, error) {
	if !dinners.Cooked(d.MealSource) || !d.Method.Valid || d.Method.String != "crockpot" || !d.MinutesTotal.Valid || d.MinutesTotal.Int64 <= 0 {
		return nil, nil
	}
	at, err := household.LocalTime(zone, date, dinnerTime)
	if err != nil {
		return nil, err
	}
	at = at.Add(-time.Duration(d.MinutesTotal.Int64) * time.Minute)
	return &at, nil
}

// Draft is a draft asked for: the job drafting the week, and whether this
// call enqueued it rather than found one already queued or running.
type Draft struct {
	JobID   string `json:"jobId"`
	Created bool   `json:"-"`
}

// StartDraft enqueues the KindDraftWeek job drafting the week starting
// weekStart, unless one is already queued or running for it, which it
// returns instead with Created false, so a week has at most one draft in
// flight. A confirmed draft joins only a confirmed job: while the week's
// job was asked for without confirm, it fails with ErrDraftRunning and
// enqueues nothing. Drafting over a synced or changed plan needs confirm,
// else it fails with ErrConfirmRequired and enqueues nothing.
func (s *Service) StartDraft(ctx context.Context, weekStart store.LocalDate, confirm bool) (Draft, error) {
	d, _, err := s.startDraft(ctx, weekStart, confirm, false)
	return d, err
}

// StartScheduledDraft is StartDraft, without confirm, for the plan_draft
// schedule: it enqueues the same KindDraftWeek job, or returns the one
// queued or running, only while the week starting weekStart has no plan.
// A week that has a plan, in any status, is left as it is: nothing is
// enqueued and planned is true.
func (s *Service) StartScheduledDraft(ctx context.Context, weekStart store.LocalDate) (d Draft, planned bool, err error) {
	return s.startDraft(ctx, weekStart, false, true)
}

// startDraft is StartDraft, and when onlyUnplanned is set it enqueues
// nothing for a week that has a plan and reports planned true.
func (s *Service) startDraft(ctx context.Context, weekStart store.LocalDate, confirm, onlyUnplanned bool) (Draft, bool, error) {
	if _, err := household.WeekDays(weekStart); err != nil {
		return Draft{}, false, &InvalidWeekError{Value: weekStart.String()}
	}
	var d Draft
	planned := false
	err := s.st.WithTx(ctx, func(tx *sql.Tx) error {
		q := db.New(tx)
		cur, err := q.GetWeekPlan(ctx, weekStart.String())
		switch {
		case errors.Is(err, sql.ErrNoRows):
		case err != nil:
			return fmt.Errorf("plan: read week %s: %w", weekStart, err)
		case onlyUnplanned:
			planned = true
			return nil
		case cur.Status != StatusDraft && !confirm:
			return ErrConfirmRequired
		}
		active, err := q.GetActiveDraftJob(ctx, db.GetActiveDraftJobParams{Kind: KindDraftWeek, WeekStart: weekStart.String()})
		switch {
		case errors.Is(err, sql.ErrNoRows):
		case err != nil:
			return fmt.Errorf("plan: read draft job of %s: %w", weekStart, err)
		case confirm && active.Confirmed != 1:
			return ErrDraftRunning
		default:
			d = Draft{JobID: active.ID}
			return nil
		}
		id, err := s.draftJob.EnqueueTx(ctx, tx, DraftJob{WeekStart: weekStart.String(), Confirm: confirm})
		if err != nil {
			return fmt.Errorf("plan: enqueue draft of %s: %w", weekStart, err)
		}
		d = Draft{JobID: id, Created: true}
		return nil
	})
	if err != nil {
		return Draft{}, false, err
	}
	if d.Created {
		s.draftJob.Wake()
	}
	return d, planned, nil
}

// Swap plans dinnerID on date of the week starting weekStart, marking the
// day new when the dinner is in the Quest log and clearing its calendar
// event id, so the next Push writes it. Swapping a synced plan makes it
// changed. The week must have a plan (else ErrNotFound), date
// must be one of its days (else *InvalidDayError), the dinner must be a
// Regular or in the Quest log (else ErrDinnerNotFound or
// *DinnerNotPlannableError), and it must not be on another day of the
// week (else ErrDinnerAlreadyPlanned). It returns the plan as swapped.
func (s *Service) Swap(ctx context.Context, weekStart store.LocalDate, date, dinnerID string) (Plan, error) {
	dates, err := household.WeekDays(weekStart)
	if err != nil {
		return Plan{}, &InvalidWeekError{Value: weekStart.String()}
	}
	day, err := store.ParseLocalDate(date)
	if err != nil || !slices.Contains(dates[:], day) {
		return Plan{}, &InvalidDayError{Value: date}
	}
	var p Plan
	err = s.st.WithTx(ctx, func(tx *sql.Tx) error {
		q := db.New(tx)
		cur, err := q.GetWeekPlan(ctx, weekStart.String())
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("plan: read week %s: %w", weekStart, err)
		}
		d, err := q.GetDinner(ctx, dinnerID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrDinnerNotFound
		}
		if err != nil {
			return fmt.Errorf("plan: read dinner %s: %w", dinnerID, err)
		}
		if !slices.Contains(plannable, d.Status) {
			return &DinnerNotPlannableError{Status: d.Status}
		}
		if !dinners.Cooked(d.MealSource) {
			return &NotCookedError{MealSource: d.MealSource}
		}
		days, err := q.ListPlanDays(ctx, cur.ID)
		if err != nil {
			return fmt.Errorf("plan: read days of %s: %w", cur.ID, err)
		}
		if slices.ContainsFunc(days, func(r db.ListPlanDaysRow) bool { return r.Date != day.String() && r.Dinner.ID == d.ID }) {
			return ErrDinnerAlreadyPlanned
		}
		now := store.Now().String()
		n, err := q.SwapPlanDay(ctx, db.SwapPlanDayParams{
			DinnerID:  sql.NullString{String: d.ID, Valid: true},
			IsNew:     isNew(d.Status),
			UpdatedAt: now,
			PlanID:    cur.ID,
			Date:      day.String(),
		})
		if err != nil {
			return fmt.Errorf("plan: swap %s of %s: %w", day, cur.ID, err)
		}
		if n != 1 {
			return fmt.Errorf("plan: %s has no day %s", cur.ID, day)
		}
		if err := q.MarkWeekPlanEdited(ctx, db.MarkWeekPlanEditedParams{UpdatedAt: now, ID: cur.ID}); err != nil {
			return fmt.Errorf("plan: mark %s edited: %w", cur.ID, err)
		}
		p, err = readPlan(ctx, q, weekStart)
		return err
	})
	if err != nil {
		return Plan{}, err
	}
	s.wake()
	return p, nil
}

// Tonight returns today, the household-local date now in the household's
// zone, and the Monday starting its week, when that week has a plan: Swap
// of that week and day sets tonight's dinner. A week with no plan is
// ErrNotFound; no plan is made for it, since a plan holds all seven days.
// The week is always today's own, so Swap never writes a different day.
func (s *Service) Tonight(ctx context.Context) (week, today store.LocalDate, err error) {
	tx, err := s.st.Reader.BeginTx(ctx, nil)
	if err != nil {
		return store.LocalDate{}, store.LocalDate{}, fmt.Errorf("plan: begin read: %w", err)
	}
	defer func() {
		err = errors.Join(err, tx.Rollback())
	}()
	q := db.New(tx)
	settings, err := q.GetHouseholdSettings(ctx)
	if err != nil {
		return store.LocalDate{}, store.LocalDate{}, fmt.Errorf("plan: read household settings: %w", err)
	}
	zone, err := household.LoadZone(settings.Timezone)
	if err != nil {
		return store.LocalDate{}, store.LocalDate{}, fmt.Errorf("plan: household settings: %w", err)
	}
	today = household.Today(time.Now(), zone)
	week = household.WeekStart(today)
	_, err = q.GetWeekPlan(ctx, week.String())
	if errors.Is(err, sql.ErrNoRows) {
		return store.LocalDate{}, store.LocalDate{}, ErrNotFound
	}
	if err != nil {
		return store.LocalDate{}, store.LocalDate{}, fmt.Errorf("plan: read week %s: %w", week, err)
	}
	return week, today, nil
}

// isNew is plan_days.is_new for a dinner with status: 1 for the Quest log.
func isNew(status string) int64 {
	if status == dinners.StatusQuestLog {
		return 1
	}
	return 0
}

func fromDinner(d db.Dinner) Dinner {
	out := Dinner{
		ID:         d.ID,
		Name:       d.Name,
		Status:     d.Status,
		Method:     nullString(d.Method),
		Origin:     d.Origin,
		MealSource: d.MealSource,
	}
	if d.MinutesTotal.Valid {
		out.Minutes = &d.MinutesTotal.Int64
	}
	if dinners.Cooked(d.MealSource) && d.PhotoID.Valid {
		u := dinners.PhotoURL(d.PhotoID.String)
		out.Photo = &u
	}
	return out
}

func nullString(s sql.NullString) *string {
	if !s.Valid {
		return nil
	}
	return &s.String
}
