package plan

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/dinners"
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

// ErrNoPendingReview is a decision on a dinner with no pending Rotation
// check: it was never tried, or its check is already decided.
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

// MarkTried moves every dinner in the Quest log planned on date, the
// household-local day just past, on a plan of any status, to tried and
// opens a pending Rotation check of it for date. It is idempotent: a
// dinner already checked for date is left as it is, so a later run never
// moves one returned to the Quest log ("We didn't make it") again. It
// returns how many dinners it moved.
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
		}
		n = len(ids)
		return nil
	})
	if err != nil {
		return 0, err
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
// kept. An unknown dinner is ErrDinnerNotFound, a dinner with no pending
// check (including a second decision) ErrNoPendingReview, and one no
// longer tried ErrDinnerNotTried; none of them changes anything.
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
	if d.PhotoID.Valid {
		u := dinners.PhotoURL(d.PhotoID.String)
		out.Photo = &u
	}
	return out
}
