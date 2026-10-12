package dinners

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

// KindBackfillPhotos is the job that grounds, by name, the dinners left
// without a photo: those whose grounding failed and that have no pasted
// recipe_url (ListPhotoBackfillDinners).
const KindBackfillPhotos = "backfill_dinner_photos"

// Photo backfill outcomes for one dinner.
const (
	BackfillGrounded = "grounded"
	BackfillFailed   = "failed"
	// BackfillSkipped: the dinner was grounded by hand, renamed or archived
	// while the backfill grounded it, so the backfill left it as it is.
	BackfillSkipped = "skipped"
)

// BackfillJob is the KindBackfillPhotos payload. DinnerID narrows the
// backfill to that dinner; empty backfills every dinner without a photo.
type BackfillJob struct {
	DinnerID string `json:"dinner_id,omitempty"`
}

// BackfillOutcome is what a photo backfill did for one dinner. Reason is
// set only for BackfillFailed: the Reason recorded as its grounding_error.
type BackfillOutcome struct {
	DinnerID string  `json:"dinner_id"`
	Outcome  string  `json:"outcome"`
	Reason   *string `json:"reason"`
}

// backfillResult is the KindBackfillPhotos result: the outcome of each
// dinner the backfill tried, in the order it tried them.
type backfillResult struct {
	Dinners []BackfillOutcome `json:"dinners"`
}

func (r backfillResult) PublicJSON() ([]byte, error) { return json.Marshal(r) }

// StartPhotoBackfill enqueues a KindBackfillPhotos job over every dinner
// without a photo and returns its id, without waiting for it. While a full
// backfill is queued or running it enqueues nothing and returns that job's
// id instead, so two passes never ground the same dinner at once; the jobs
// table holds the claim, so it holds across restarts too.
func (s *Service) StartPhotoBackfill(ctx context.Context) (string, error) {
	var id string
	created := false
	err := s.st.WithTx(ctx, func(tx *sql.Tx) error {
		active, err := db.New(tx).GetActivePhotoBackfillJob(ctx, KindBackfillPhotos)
		switch {
		case errors.Is(err, sql.ErrNoRows):
		case err != nil:
			return fmt.Errorf("dinners: read active photo backfill: %w", err)
		default:
			id = active
			return nil
		}
		id, err = s.backfillJob.EnqueueTx(ctx, tx, BackfillJob{})
		if err != nil {
			return fmt.Errorf("dinners: enqueue photo backfill: %w", err)
		}
		created = true
		return nil
	})
	if err != nil {
		return "", err
	}
	if created {
		s.backfillJob.Wake()
	}
	return id, nil
}

// errBackfillNotRecorded fails a photo backfill that could not record the
// outcome of one or more of its dinners.
var errBackfillNotRecorded = errors.New("dinners: photo backfill: dinner outcomes not recorded")

// runBackfillJob grounds each dinner ListPhotoBackfillDinners lists for p,
// one at a time, with recipes.Grounder.Ground on its name: the first ranked
// candidate whose photo caches and whose ingredients normalize grounds it,
// with the recipe's source URL and site kept as any grounding keeps them.
// Success records the recipe and photo with grounding_status grounded;
// failure leaves the dinner failed with the new Reason as its
// grounding_error, for the household to fix its recipe by hand. One
// dinner's failure, its search's, model's or database write's, never stops
// the others. A failure to list the dinners, or the job's context ending,
// fails the job at once. A write that failed fails the job once every
// dinner has been tried, as a grounding job's failed write does, with the
// outcomes published as its result, so the job never reports done for a
// dinner whose outcome is not stored.
func (s *Service) runBackfillJob(ctx context.Context, p BackfillJob, publish func(backfillResult) error) (backfillResult, error) {
	q := db.New(s.st.Reader)
	rows, err := q.ListPhotoBackfillDinners(ctx, sql.NullString{String: p.DinnerID, Valid: p.DinnerID != ""})
	if err != nil {
		return backfillResult{}, fmt.Errorf("dinners: photo backfill: list: %w", err)
	}
	res := backfillResult{Dinners: make([]BackfillOutcome, 0, len(rows))}
	grounded, unrecorded := false, false
	for _, row := range rows {
		out, recorded, err := s.backfillDinner(ctx, row)
		if err != nil {
			return backfillResult{}, err
		}
		grounded = grounded || out.Outcome == BackfillGrounded
		unrecorded = unrecorded || !recorded
		res.Dinners = append(res.Dinners, out)
	}
	if grounded {
		s.wake()
	}
	if unrecorded {
		if err := publish(res); err != nil {
			return backfillResult{}, errors.Join(errBackfillNotRecorded, err)
		}
		return backfillResult{}, errBackfillNotRecorded
	}
	return res, nil
}

// backfillDinner grounds row, a dinner ListPhotoBackfillDinners listed, as
// runBackfillJob describes, and reports whether its outcome was recorded.
// Its error is only the job's context ending.
func (s *Service) backfillDinner(ctx context.Context, row db.Dinner) (BackfillOutcome, bool, error) {
	out := BackfillOutcome{DinnerID: row.ID}
	w := db.New(s.st.Writer)
	gr, err := s.grounder.Ground(ctx, row.Name, row.Name)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return BackfillOutcome{}, false, ctxErr
	}
	if err != nil {
		reason := reasonFor(err, false)
		// Only the reason is logged: the error may quote a dependency's
		// response, which jobs never store, return or log.
		s.logger.InfoContext(ctx, "dinner photo backfill failed", "dinner_id", row.ID, "reason", reason)
		n, err := w.RefailDinnerGrounding(ctx, db.RefailDinnerGroundingParams{
			GroundingError:    sql.NullString{String: reason, Valid: true},
			UpdatedAt:         store.Now().String(),
			ID:                row.ID,
			GroundingRevision: row.GroundingRevision,
		})
		out, recorded := s.backfillRecorded(ctx, out, BackfillFailed, reason, n, err)
		return out, recorded, nil
	}
	n, err := w.GroundFailedDinner(ctx, db.GroundFailedDinnerParams{
		RecipeID:          sql.NullString{String: gr.RecipeID, Valid: true},
		PhotoID:           sql.NullString{String: gr.PhotoID, Valid: true},
		Method:            sql.NullString{String: gr.Method, Valid: true},
		MinutesTotal:      nullInt(gr.MinutesTotal),
		Difficulty:        sql.NullString{String: gr.Difficulty, Valid: true},
		UpdatedAt:         store.Now().String(),
		ID:                row.ID,
		GroundingRevision: row.GroundingRevision,
	})
	out, recorded := s.backfillRecorded(ctx, out, BackfillGrounded, "", n, err)
	return out, recorded, nil
}

// backfillRecorded is out once the backfill recorded outcome (with reason,
// for BackfillFailed) on the dinner, writing n rows with err: a dinner that
// changed meanwhile (no row) is BackfillSkipped, and one whose write failed
// is BackfillFailed with ReasonGroundingError, the dinner left as it was,
// and not recorded.
func (s *Service) backfillRecorded(ctx context.Context, out BackfillOutcome, outcome, reason string, n int64, err error) (BackfillOutcome, bool) {
	switch {
	case err != nil:
		// The error is a database error, never a dependency's text.
		s.logger.ErrorContext(ctx, "dinner photo backfill not recorded", "dinner_id", out.DinnerID, "error", err)
		out.Outcome, reason = BackfillFailed, ReasonGroundingError
	case n == 0:
		out.Outcome, reason = BackfillSkipped, ""
	default:
		out.Outcome = outcome
	}
	if reason != "" {
		out.Reason = &reason
	}
	return out, err == nil
}
