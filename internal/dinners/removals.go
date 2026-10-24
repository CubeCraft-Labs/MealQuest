package dinners

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

// Removal outcomes (the dinner_removals.outcome column): a removal is
// archived once the dinner was archived for it, and withdrawn once the PWA
// asked for it never to be made.
const (
	RemovalArchived  = "archived"
	RemovalWithdrawn = "withdrawn"
)

// ErrRemovalWithdrawn is archiving a dinner for a removal already
// withdrawn: it is never made.
var ErrRemovalWithdrawn = errors.New("dinner removal withdrawn")

// checkRemoval is an error unless removal is a removal id: a UUID in its
// canonical lower-case form, which the PWA makes with crypto.randomUUID.
func checkRemoval(removal string) error {
	if len(removal) != 36 {
		return invalid("removal", "must be a lower-case UUID")
	}
	for i, c := range removal {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return invalid("removal", "must be a lower-case UUID")
			}
		default:
			if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
				return invalid("removal", "must be a lower-case UUID")
			}
		}
	}
	return nil
}

// getRemoval is removal as recorded, and whether it is. A removal recorded
// for a dinner other than id is a ValidationError.
func getRemoval(ctx context.Context, q *db.Queries, id, removal string) (db.DinnerRemoval, bool, error) {
	recorded, err := q.GetDinnerRemoval(ctx, removal)
	if errors.Is(err, sql.ErrNoRows) {
		return db.DinnerRemoval{}, false, nil
	}
	if err != nil {
		return db.DinnerRemoval{}, false, fmt.Errorf("dinners: get removal %s: %w", removal, err)
	}
	if recorded.DinnerID != id {
		return db.DinnerRemoval{}, false, invalid("removal", "is a removal of another dinner")
	}
	return recorded, true, nil
}

func createRemoval(ctx context.Context, q *db.Queries, id, removal, outcome, createdBy, now string) error {
	err := q.CreateDinnerRemoval(ctx, db.CreateDinnerRemovalParams{
		ID:        removal,
		DinnerID:  id,
		Outcome:   outcome,
		CreatedBy: createdBy,
		CreatedAt: now,
	})
	if err != nil {
		return fmt.Errorf("dinners: record removal %s of %s as %s: %w", removal, id, outcome, err)
	}
	return nil
}

// Withdrawal is how WithdrawRemoval settled a removal: its Outcome, and
// the Status of its dinner as it stands once the removal is settled, read in
// the same transaction. A removal withdrawn says nothing of the dinner: it
// may be archived all the same, by another removal.
type Withdrawal struct {
	Outcome string
	Status  string
}

// WithdrawRemoval settles removal of dinner id for good and answers how: a
// removal already made (Archive) is RemovalArchived; any other is recorded
// RemovalWithdrawn, so it is never made, however late it arrives. Either way
// it answers the dinner's status with it, so the PWA can tell a dinner still
// shown from one another removal archived. The PWA asks this when no answer
// to a removal it sent said whether it was made. An unknown dinner is
// ErrNotFound.
func (s *Service) WithdrawRemoval(ctx context.Context, id, removal, updatedBy string) (Withdrawal, error) {
	if updatedBy == "" {
		return Withdrawal{}, errors.New("dinners: withdraw removal: no member to record as created_by")
	}
	if err := checkRemoval(removal); err != nil {
		return Withdrawal{}, err
	}
	var w Withdrawal
	err := s.st.WithTx(ctx, func(tx *sql.Tx) error {
		q := db.New(tx)
		recorded, found, err := getRemoval(ctx, q, id, removal)
		if err != nil {
			return err
		}
		row, err := q.GetDinner(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("dinners: withdraw removal of %s: %w", id, err)
		}
		w.Status = row.Status
		if found {
			w.Outcome = recorded.Outcome
			return nil
		}
		w.Outcome = RemovalWithdrawn
		return createRemoval(ctx, q, id, removal, RemovalWithdrawn, updatedBy, store.Now().String())
	})
	if err != nil {
		return Withdrawal{}, err
	}
	return w, nil
}
