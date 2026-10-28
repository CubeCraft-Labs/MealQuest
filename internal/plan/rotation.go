package plan

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/chat"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/dinners"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/household"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/scheduler"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

// Rotation check decisions (the rotation_reviews.decision column), the
// household's answer to "Keep it in rotation?" (D-021).
const (
	// DecisionKeep makes the tried dinner a Regular.
	DecisionKeep = "keep"
	// DecisionDrop ("Not for us") archives it; its name stays among the
	// passed names, so it is never suggested again.
	DecisionDrop = "drop"
	// DecisionNotMade ("We didn't make it") returns it to the Quest log.
	DecisionNotMade = "not_made"
)

// decisionStatus is the status each decision moves a tried dinner to. Keep
// is the only way a tried dinner becomes a Regular: nothing automatic
// promotes one (D-021).
var decisionStatus = map[string]string{
	DecisionKeep:    dinners.StatusRegular,
	DecisionDrop:    dinners.StatusArchived,
	DecisionNotMade: dinners.StatusQuestLog,
}

// ErrReviewNotFound is a decision on a dinner that has never had a
// Rotation check: it was never tried.
var ErrReviewNotFound = errors.New("the dinner has no Rotation check")

// ErrNoPendingReview is a decision on a dinner whose Rotation checks are
// all decided, such as a second decision.
var ErrNoPendingReview = errors.New("the dinner has no pending Rotation check")

// ErrDinnerNotTried is a decision on a pending Rotation check whose dinner
// is no longer tried, such as one archived meanwhile.
var ErrDinnerNotTried = errors.New("the dinner is no longer tried")

// InvalidDecisionError is a decision that is not keep, drop or not_made.
type InvalidDecisionError struct {
	Value string
}

func (e *InvalidDecisionError) Error() string {
	return fmt.Sprintf("decision %q is not one of keep, drop, not_made", e.Value)
}

// ReviewDinner is the dinner of a Rotation check.
type ReviewDinner struct {
	ID    string  `json:"id"`
	Name  string  `json:"name"`
	Photo *string `json:"photo"`
}

// Review is a pending Rotation check: the dinner tried and the plan date
// it was planned on.
type Review struct {
	Dinner ReviewDinner `json:"dinner"`
	// PlanDate is household-local, YYYY-MM-DD.
	PlanDate string `json:"plan_date"`
}

// Decided is a Rotation check as decided.
type Decided struct {
	Dinner   ReviewDinner `json:"dinner"`
	PlanDate string       `json:"plan_date"`
	Decision string       `json:"decision"`
	// DinnerStatus is the status the decision moved the dinner to.
	DinnerStatus string `json:"dinner_status"`
	DecidedAt    string `json:"decided_at"`
	UpdatedBy    string `json:"updated_by"`
}

// RotationCheckSchedule is the rotation_check schedule: every day at the
// household's review_time it runs MarkTried for the day before, so each
// Quest log dinner planned yesterday is tried and the household is asked
// "Keep it in rotation?". Its period is the fire date, claimed as
// rotation_check:YYYY-MM-DD. It never makes a dinner a Regular.
func (s *Service) RotationCheckSchedule() scheduler.Schedule {
	return scheduler.Schedule{
		Name: scheduler.RotationCheck,
		Rule: func(settings household.Settings) scheduler.Rule {
			return scheduler.Daily(settings.ReviewTime)
		},
		PeriodKey: func(p scheduler.Period) string {
			return scheduler.RotationCheck + ":" + p.Key
		},
		Run: func(ctx context.Context, p scheduler.Period) error {
			yesterday := addDays(p.Date, -1)
			n, err := s.MarkTried(ctx, yesterday)
			if err != nil {
				return err
			}
			s.logger.InfoContext(ctx, "Rotation check: Quest log dinners marked tried",
				"period", p.Key, "plan_date", yesterday.String(), "tried", n)
			return nil
		},
	}
}

// MarkTried moves every dinner in the Quest log planned on date, the
// household-local day just past, on a plan of any status, to tried, opens
// a pending Rotation check of it for date and emits its rotation_review
// (chat.RotationReview), all in one transaction. It is idempotent: a
// dinner already checked for date is left as it is and not notified of
// again, so a later run never moves one returned to the Quest log ("We
// didn't make it") again. It returns how many dinners it moved.
func (s *Service) MarkTried(ctx context.Context, date store.LocalDate) (int, error) {
	var n int
	err := s.st.WithTx(ctx, func(tx *sql.Tx) error {
		q := db.New(tx)
		ids, err := q.ListTriedCandidates(ctx, date.String())
		if err != nil {
			return fmt.Errorf("plan: list Quest log dinners planned on %s: %w", date, err)
		}
		now := store.Now().String()
		for _, id := range ids {
			d, err := q.GetDinner(ctx, id)
			if err != nil {
				return fmt.Errorf("plan: read dinner %s: %w", id, err)
			}
			moved, err := q.MarkDinnerTried(ctx, db.MarkDinnerTriedParams{UpdatedAt: now, ID: id})
			if err != nil {
				return fmt.Errorf("plan: mark %s tried: %w", id, err)
			}
			if moved != 1 {
				return fmt.Errorf("plan: dinner %s left the Quest log while marking it tried", id)
			}
			if err := q.CreateRotationReview(ctx, db.CreateRotationReviewParams{
				ID: store.NewID(), DinnerID: id, PlanDate: date.String(), CreatedAt: now, UpdatedAt: now,
			}); err != nil {
				return fmt.Errorf("plan: open Rotation check of %s for %s: %w", id, date, err)
			}
			if err := s.notifier.EmitTx(ctx, tx, chat.RotationReview(id, d.Name, date)); err != nil {
				return fmt.Errorf("plan: %w", err)
			}
		}
		n = len(ids)
		return nil
	})
	if err != nil {
		return 0, err
	}
	if n > 0 {
		s.notifier.Wake()
	}
	return n, nil
}

// PendingReviews returns the pending Rotation checks, oldest plan date
// first.
func (s *Service) PendingReviews(ctx context.Context) ([]Review, error) {
	rows, err := db.New(s.st.Reader).ListPendingRotationReviews(ctx)
	if err != nil {
		return nil, fmt.Errorf("plan: list pending Rotation checks: %w", err)
	}
	out := make([]Review, len(rows))
	for i, r := range rows {
		out[i] = Review{Dinner: reviewDinner(r.Dinner), PlanDate: r.PlanDate}
	}
	return out, nil
}

// DecideReview applies decision to the pending Rotation check of dinnerID
// and closes it, recording updatedBy: keep makes the dinner a Regular,
// drop archives it and not_made returns it to the Quest log; its origin is
// kept; a drop records the dinner's name on the check, so renaming the
// dinner later never takes it off the passed names. An unknown dinner is
// ErrDinnerNotFound, a dinner never checked ErrReviewNotFound, one whose
// checks are all decided (such as a second decision) ErrNoPendingReview,
// and one no longer tried ErrDinnerNotTried; none of them changes anything.
func (s *Service) DecideReview(ctx context.Context, dinnerID, decision, updatedBy string) (Decided, error) {
	status, ok := decisionStatus[decision]
	if !ok {
		return Decided{}, &InvalidDecisionError{Value: decision}
	}
	if updatedBy == "" {
		return Decided{}, errors.New("plan: decide Rotation check: no member to record as updated_by")
	}
	var out Decided
	err := s.st.WithTx(ctx, func(tx *sql.Tx) error {
		q := db.New(tx)
		d, err := q.GetDinner(ctx, dinnerID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrDinnerNotFound
		}
		if err != nil {
			return fmt.Errorf("plan: read dinner %s: %w", dinnerID, err)
		}
		review, err := q.GetPendingRotationReview(ctx, d.ID)
		if errors.Is(err, sql.ErrNoRows) {
			checks, err := q.CountRotationReviews(ctx, d.ID)
			if err != nil {
				return fmt.Errorf("plan: count Rotation checks of %s: %w", d.ID, err)
			}
			if checks == 0 {
				return ErrReviewNotFound
			}
			return ErrNoPendingReview
		}
		if err != nil {
			return fmt.Errorf("plan: read Rotation check of %s: %w", d.ID, err)
		}
		now := store.Now().String()
		moved, err := q.MoveTriedDinner(ctx, db.MoveTriedDinnerParams{
			Status:    status,
			UpdatedBy: sql.NullString{String: updatedBy, Valid: true},
			UpdatedAt: now,
			ID:        d.ID,
		})
		if err != nil {
			return fmt.Errorf("plan: move %s to %s: %w", d.ID, status, err)
		}
		if moved != 1 {
			return ErrDinnerNotTried
		}
		closed, err := q.DecideRotationReview(ctx, db.DecideRotationReviewParams{
			Decision:  sql.NullString{String: decision, Valid: true},
			Name:      sql.NullString{String: d.Name, Valid: decision == DecisionDrop},
			Now:       sql.NullString{String: now, Valid: true},
			UpdatedBy: sql.NullString{String: updatedBy, Valid: true},
			ID:        review.ID,
		})
		if err != nil {
			return fmt.Errorf("plan: close Rotation check %s: %w", review.ID, err)
		}
		if closed != 1 {
			return fmt.Errorf("plan: Rotation check %s was decided while deciding it", review.ID)
		}
		out = Decided{
			Dinner:       reviewDinner(d),
			PlanDate:     review.PlanDate,
			Decision:     decision,
			DinnerStatus: status,
			DecidedAt:    now,
			UpdatedBy:    updatedBy,
		}
		return nil
	})
	if err != nil {
		return Decided{}, err
	}
	return out, nil
}

func reviewDinner(d db.Dinner) ReviewDinner {
	out := ReviewDinner{ID: d.ID, Name: d.Name}
	if dinners.Cooked(d.MealSource) && d.PhotoID.Valid {
		u := dinners.PhotoURL(d.PhotoID.String)
		out.Photo = &u
	}
	return out
}
