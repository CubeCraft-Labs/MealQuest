// Package plan keeps the household's weekly plans: the Draft board the AI
// drafts from the Regulars and the Quest log, and the swaps the household
// makes on it. A draft runs as a job: draft_week plans seven dinners, one
// a day from Monday to Sunday, and the job stores them as the week's plan,
// replacing any draft before it unless the household edited the plan
// while draft_week ran. A plan is draft until Push to Calendar
// syncs it (Feature 16); an edit of a synced plan makes it changed.
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
	"slices"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/skills"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/dinners"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/household"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/jobs"
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

// Service drafts, reads and edits weekly plans.
type Service struct {
	st       *store.Store
	draft    Drafter
	logger   *slog.Logger
	draftJob *jobs.Kind[DraftJob]
}

// NewService returns a service over st that drafts with draft and
// registers the KindDraftWeek job on runner, which must not have started
// yet. The kind is service-only: StartDraft, which keeps one draft in
// flight per week, is the only way to enqueue it.
func NewService(st *store.Store, draft Drafter, runner *jobs.Runner, logger *slog.Logger) (*Service, error) {
	switch {
	case st == nil:
		return nil, errors.New("plan: nil store")
	case draft == nil:
		return nil, errors.New("plan: nil drafter")
	case runner == nil:
		return nil, errors.New("plan: nil job runner")
	case logger == nil:
		return nil, errors.New("plan: nil logger")
	}
	s := &Service{st: st, draft: draft, logger: logger}
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
	ID      string  `json:"id"`
	Name    string  `json:"name"`
	Photo   *string `json:"photo"`
	Minutes *int64  `json:"minutes"`
	Method  *string `json:"method"`
	Origin  string  `json:"origin"`
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
	if !d.Method.Valid || d.Method.String != "crockpot" || !d.MinutesTotal.Valid || d.MinutesTotal.Int64 <= 0 {
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
	if _, err := household.WeekDays(weekStart); err != nil {
		return Draft{}, &InvalidWeekError{Value: weekStart.String()}
	}
	var d Draft
	err := s.st.WithTx(ctx, func(tx *sql.Tx) error {
		q := db.New(tx)
		cur, err := q.GetWeekPlan(ctx, weekStart.String())
		switch {
		case errors.Is(err, sql.ErrNoRows):
		case err != nil:
			return fmt.Errorf("plan: read week %s: %w", weekStart, err)
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
		return Draft{}, err
	}
	if d.Created {
		s.draftJob.Wake()
	}
	return d, nil
}

// Swap plans dinnerID on date of the week starting weekStart, marking the
// day new when the dinner is in the Quest log. Swapping a synced plan
// makes it changed. The week must have a plan (else ErrNotFound), date
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
	return p, nil
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
		ID:     d.ID,
		Name:   d.Name,
		Method: nullString(d.Method),
		Origin: d.Origin,
	}
	if d.MinutesTotal.Valid {
		out.Minutes = &d.MinutesTotal.Int64
	}
	if d.PhotoID.Valid {
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
