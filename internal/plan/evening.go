package plan

import (
	"context"
	"fmt"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/dinners"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

// InvalidEveningError is a plan-day meal source that is not a Delivery or
// Dining-out evening. cook is a dinner, a manual night or a cleared night,
// not a way to set this state.
type InvalidEveningError struct {
	MealSource string
}

func (e *InvalidEveningError) Error() string {
	return fmt.Sprintf("meal_source: %q is not delivery or dining out", e.MealSource)
}

// SetEvening marks date of the week starting weekStart as a Delivery or
// Dining-out evening. The day keeps no dinner and no manual label, so
// nothing is cooked that night and no dinners row is written. source must
// be delivery or dining_out; anything else is *InvalidEveningError. The
// day's calendar event id is cleared and a synced plan becomes changed, as
// a clear does. The week must have a plan (else ErrNotFound) and date must
// be one of its days (else *InvalidDayError).
func (s *Service) SetEvening(ctx context.Context, weekStart store.LocalDate, date, source string) (Plan, error) {
	if source != dinners.MealSourceDelivery && source != dinners.MealSourceDiningOut {
		return Plan{}, &InvalidEveningError{MealSource: source}
	}
	return s.editDay(ctx, weekStart, date, func(ctx context.Context, q *db.Queries, planID, day, now string) error {
		n, err := q.SetPlanDayEvening(ctx, db.SetPlanDayEveningParams{
			MealSource: source,
			UpdatedAt:  now,
			PlanID:     planID,
			Date:       day,
		})
		if err != nil {
			return fmt.Errorf("plan: set %s evening %s of %s: %w", source, day, planID, err)
		}
		if n != 1 {
			return fmt.Errorf("plan: %s has no day %s", planID, day)
		}
		return nil
	})
}
