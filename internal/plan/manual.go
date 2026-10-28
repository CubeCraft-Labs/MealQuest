package plan

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/harness"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/dinners"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/household"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

// InvalidManualLabelError is a one-off night name the plan refuses: empty,
// longer than a dinner name, or with no words once normalized.
type InvalidManualLabelError struct {
	Reason string
}

func (e *InvalidManualLabelError) Error() string {
	return "manual_label: " + e.Reason
}

// SetManual names date of the week starting weekStart with label, a
// free-text night. The day keeps no dinner, so nothing is added to the
// Regulars or the Quest log and no dinners row is written. label is
// trimmed; a blank, too-long or filler name is *InvalidManualLabelError.
// The day's calendar event id is cleared and a synced plan becomes
// changed, as a swap does. The week must have a plan (else ErrNotFound)
// and date must be one of its days (else *InvalidDayError).
func (s *Service) SetManual(ctx context.Context, weekStart store.LocalDate, date, label string) (Plan, error) {
	label, err := checkManualLabel(label)
	if err != nil {
		return Plan{}, err
	}
	return s.editDay(ctx, weekStart, date, func(ctx context.Context, q *db.Queries, planID, day, now string) error {
		n, err := q.SetManualPlanDay(ctx, db.SetManualPlanDayParams{
			ManualLabel: sql.NullString{String: label, Valid: true},
			UpdatedAt:   now,
			PlanID:      planID,
			Date:        day,
		})
		if err != nil {
			return fmt.Errorf("plan: set manual %s of %s: %w", day, planID, err)
		}
		if n != 1 {
			return fmt.Errorf("plan: %s has no day %s", planID, day)
		}
		return nil
	})
}

// ClearDay clears date of the week starting weekStart: no dinner and no
// manual label. The day's calendar event id is cleared and a synced plan
// becomes changed. The week must have a plan (else ErrNotFound) and date
// must be one of its days (else *InvalidDayError).
func (s *Service) ClearDay(ctx context.Context, weekStart store.LocalDate, date string) (Plan, error) {
	return s.editDay(ctx, weekStart, date, func(ctx context.Context, q *db.Queries, planID, day, now string) error {
		n, err := q.ClearPlanDay(ctx, db.ClearPlanDayParams{UpdatedAt: now, PlanID: planID, Date: day})
		if err != nil {
			return fmt.Errorf("plan: clear %s of %s: %w", day, planID, err)
		}
		if n != 1 {
			return fmt.Errorf("plan: %s has no day %s", planID, day)
		}
		return nil
	})
}

// checkManualLabel returns raw trimmed, refusing a name that is empty, too
// long or has no words left once normalized, the same bounds as a dinner
// name.
func checkManualLabel(raw string) (string, error) {
	label := strings.TrimSpace(raw)
	switch {
	case label == "":
		return "", &InvalidManualLabelError{Reason: "must not be empty"}
	case utf8.RuneCountInString(label) > dinners.MaxNameChars:
		return "", &InvalidManualLabelError{Reason: fmt.Sprintf("must be at most %d characters", dinners.MaxNameChars)}
	case harness.Normalize(label) == "":
		return "", &InvalidManualLabelError{Reason: "must name the night, not only filler words"}
	}
	return label, nil
}

// dayWrite records one edit of a plan day inside the edit's transaction.
type dayWrite func(ctx context.Context, q *db.Queries, planID, day, now string) error

// editDay applies write to date of the week starting weekStart and marks
// the plan edited. It returns the plan as edited.
func (s *Service) editDay(ctx context.Context, weekStart store.LocalDate, date string, write dayWrite) (Plan, error) {
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
		now := store.Now().String()
		if err := write(ctx, q, cur.ID, day.String(), now); err != nil {
			return err
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
