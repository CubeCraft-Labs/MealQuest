package suggest

import (
	"context"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/household"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/scheduler"
)

// WeeklySchedule is the suggestions_weekly schedule: at the household's
// suggestions_dow and suggestions_time it starts a scheduled batch for the
// ISO week of the fire date, its period scheduler.Weekly keys
// suggestions_weekly:YYYY-Www. The batch tells the household of the Side
// quests it adds with new_suggestions:YYYY-Www for that week, once; a batch
// that fails before adding any, as when the AI fails, sends nothing. When a
// batch is already pending or running the period starts none, and says so.
func (s *Service) WeeklySchedule() scheduler.Schedule {
	return scheduler.Schedule{
		Name: scheduler.SuggestionsWeekly,
		Rule: func(settings household.Settings) scheduler.Rule {
			return scheduler.Weekly(time.Weekday(settings.SuggestionsDow), settings.SuggestionsTime)
		},
		Run: func(ctx context.Context, p scheduler.Period) error {
			b, err := s.StartScheduledBatch(ctx, p.Week)
			if err != nil {
				return err
			}
			if !b.Created {
				s.logger.WarnContext(ctx, "scheduled suggestion batch not started: a batch is already under way",
					"period", p.Key, "batch_id", b.ID, "trigger", b.Trigger)
			}
			return nil
		},
	}
}
