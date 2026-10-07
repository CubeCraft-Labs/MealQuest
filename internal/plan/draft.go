package plan

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/harness"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/skills"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/dinners"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/household"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/jobs"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

// DraftJob is the KindDraftWeek payload: the week to draft, and whether
// the household confirmed drafting over a synced or changed plan.
type DraftJob struct {
	WeekStart string `json:"week_start"`
	Confirm   bool   `json:"confirm"`
}

// draftResult is the KindDraftWeek result: the week drafted and its plan.
type draftResult struct {
	WeekStart string `json:"week_start"`
	PlanID    string `json:"plan_id"`
}

func (r draftResult) PublicJSON() ([]byte, error) { return json.Marshal(r) }

// runDraft drafts the week p.WeekStart: draft_week plans it from the
// Regulars and the Quest log dinners no other current or later plan has,
// given the household context and last week's dinners, and the draft is
// stored as the week's plan if the plan is still as it was before
// draft_week ran. A pool under seven dinners fails with
// ReasonNotEnoughDinners and draft_week failing with ReasonAIFailed; no
// week is made up instead.
func (s *Service) runDraft(ctx context.Context, p DraftJob, _ func(draftResult) error) (draftResult, error) {
	weekStart, err := ParseWeekStart(p.WeekStart)
	if err != nil {
		return draftResult{}, fmt.Errorf("plan: draft: %w", err)
	}
	base, err := s.revision(ctx, weekStart)
	if err != nil {
		return draftResult{}, err
	}
	in, err := s.request(ctx, weekStart)
	if err != nil {
		return draftResult{}, err
	}
	week, err := s.draft(ctx, household.PeriodKey(weekStart), in)
	if err != nil {
		if ctx.Err() != nil {
			return draftResult{}, ctx.Err()
		}
		var few *skills.NotEnoughDinnersError
		if errors.As(err, &few) {
			return draftResult{}, &jobs.Failure{Code: ReasonNotEnoughDinners}
		}
		// Only the code is logged: the error may quote a model's response.
		s.logger.InfoContext(ctx, "week draft failed", "week_start", p.WeekStart, "reason", ReasonAIFailed)
		return draftResult{}, &jobs.Failure{Code: ReasonAIFailed}
	}
	for _, w := range week.Warnings {
		s.logger.InfoContext(ctx, "week draft warning", "week_start", p.WeekStart, "warning", w)
	}
	planID, err := s.storeDraft(ctx, weekStart, p.Confirm, base, week)
	if err != nil {
		return draftResult{}, err
	}
	return draftResult{WeekStart: p.WeekStart, PlanID: planID}, nil
}

// noPlan is the revision of a week with no plan.
const noPlan = -1

// revision returns the revision of the plan of the week starting
// weekStart, or noPlan when the week has none.
func (s *Service) revision(ctx context.Context, weekStart store.LocalDate) (int64, error) {
	cur, err := db.New(s.st.Reader).GetWeekPlan(ctx, weekStart.String())
	if errors.Is(err, sql.ErrNoRows) {
		return noPlan, nil
	}
	if err != nil {
		return 0, fmt.Errorf("plan: read week %s: %w", weekStart, err)
	}
	return cur.Revision, nil
}

// request reads what draft_week is given for the week starting weekStart:
// the household context, the pool, last week's dinners and the dinner
// time.
func (s *Service) request(ctx context.Context, weekStart store.LocalDate) (skills.WeekRequest, error) {
	hctx, err := harness.BuildContext(ctx, s.st, time.Now(), ContextBudget)
	if err != nil {
		return skills.WeekRequest{}, fmt.Errorf("plan: %w", err)
	}
	q := db.New(s.st.Reader)
	settings, err := q.GetHouseholdSettings(ctx)
	if err != nil {
		return skills.WeekRequest{}, fmt.Errorf("plan: read household settings: %w", err)
	}
	thisWeek := household.WeekStart(hctx.Today)
	rows, err := q.ListDraftPool(ctx, db.ListDraftPoolParams{FromWeekStart: thisWeek.String(), WeekStart: weekStart.String()})
	if err != nil {
		return skills.WeekRequest{}, fmt.Errorf("plan: list draft pool: %w", err)
	}
	pool := make([]skills.Candidate, len(rows))
	for i, d := range rows {
		kind := skills.KindRegular
		if d.Status == dinners.StatusQuestLog {
			kind = skills.KindQuestLog
		}
		pool[i] = skills.Candidate{ID: d.ID, Name: d.Name, Kind: kind, Method: d.Method.String, Minutes: int(d.MinutesTotal.Int64)}
	}
	last, err := q.ListWeekPlanDinnerIDs(ctx, addDays(weekStart, -7).String())
	if err != nil {
		return skills.WeekRequest{}, fmt.Errorf("plan: list last week's dinners: %w", err)
	}
	lastWeek := []string{}
	for _, id := range last {
		lastWeek = append(lastWeek, id.String)
	}
	return skills.WeekRequest{
		Context:    hctx,
		WeekStart:  weekStart,
		Pool:       pool,
		LastWeek:   lastWeek,
		DinnerTime: settings.DinnerTime,
	}, nil
}

// storeDraft stores week as the plan of the week starting weekStart in one
// transaction and returns the plan's id. A new plan is draft. A draft
// replaces the days of an existing plan, keeping each day's calendar
// event id; a draft plan stays draft and a synced or changed one becomes
// changed, since Calendar no longer shows it. A plan synced since the
// draft was asked for without confirm is left as it is, failing with
// ReasonConfirmRequired, and so is a plan no longer at revision base (the
// revision before draft_week ran, or noPlan), failing with
// ReasonPlanChanged.
func (s *Service) storeDraft(ctx context.Context, weekStart store.LocalDate, confirm bool, base int64, week skills.Week) (string, error) {
	var planID string
	err := s.st.WithTx(ctx, func(tx *sql.Tx) error {
		q := db.New(tx)
		now := store.Now().String()
		note := sql.NullString{String: week.Note, Valid: week.Note != ""}
		cur, err := q.GetWeekPlan(ctx, weekStart.String())
		switch {
		case errors.Is(err, sql.ErrNoRows) && base != noPlan:
			return &jobs.Failure{Code: ReasonPlanChanged}
		case errors.Is(err, sql.ErrNoRows):
			planID = store.NewID()
			if err := q.CreateWeekPlan(ctx, db.CreateWeekPlanParams{
				ID:        planID,
				WeekStart: weekStart.String(),
				AiNote:    note,
				CreatedAt: now,
				UpdatedAt: now,
			}); err != nil {
				return fmt.Errorf("plan: create week %s: %w", weekStart, err)
			}
		case err != nil:
			return fmt.Errorf("plan: read week %s: %w", weekStart, err)
		case cur.Status != StatusDraft && !confirm:
			return &jobs.Failure{Code: ReasonConfirmRequired}
		case cur.Revision != base:
			return &jobs.Failure{Code: ReasonPlanChanged}
		default:
			planID = cur.ID
			status := StatusDraft
			if cur.Status != StatusDraft {
				status = StatusChanged
			}
			if err := q.RedraftWeekPlan(ctx, db.RedraftWeekPlanParams{Status: status, AiNote: note, UpdatedAt: now, ID: cur.ID}); err != nil {
				return fmt.Errorf("plan: redraft %s: %w", cur.ID, err)
			}
		}
		for _, d := range week.Days {
			isNew := int64(0)
			if d.IsNew {
				isNew = 1
			}
			if err := q.PutPlanDay(ctx, db.PutPlanDayParams{
				ID:        store.NewID(),
				PlanID:    planID,
				Date:      d.Date,
				DinnerID:  sql.NullString{String: d.DinnerID, Valid: true},
				IsNew:     isNew,
				CreatedAt: now,
				UpdatedAt: now,
			}); err != nil {
				return fmt.Errorf("plan: put %s of %s: %w", d.Date, planID, err)
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return planID, nil
}

// addDays returns d plus n days.
func addDays(d store.LocalDate, n int) store.LocalDate {
	y, m, day := d.Date()
	return store.LocalDateOf(time.Date(y, m, day+n, 0, 0, 0, 0, time.UTC))
}
