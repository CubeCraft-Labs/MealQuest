package harness

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/household"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/scheduler"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

// PurgeTime is when, every day in the household zone, ai_runs_purge runs.
// It is fixed, not a setting.
const PurgeTime = "03:00"

// PurgeSchedule is the ai_runs_purge schedule over st: every day at
// PurgeTime it deletes the ai_runs rows created more than RunRetention
// before the fire time with Purge, its period scheduler.Daily keyed
// ai_runs_purge:YYYY-MM-DD. A purge caught up late deletes no row younger
// than RunRetention; one that fails is the period's error and is not
// retried, and the next day's purge deletes what it left.
func PurgeSchedule(st *store.Store, logger *slog.Logger) (scheduler.Schedule, error) {
	if st == nil {
		return scheduler.Schedule{}, errors.New("harness: purge schedule: nil store")
	}
	if logger == nil {
		return scheduler.Schedule{}, errors.New("harness: purge schedule: nil logger")
	}
	return scheduler.Schedule{
		Name: scheduler.AIRunsPurge,
		Rule: func(household.Settings) scheduler.Rule {
			return scheduler.Daily(PurgeTime)
		},
		Run: func(ctx context.Context, p scheduler.Period) error {
			cutoff := p.FireAt.Add(-RunRetention)
			n, err := Purge(ctx, st, cutoff)
			if err != nil {
				return err
			}
			logger.InfoContext(ctx, "ai_runs purged", "period", p.Key, "deleted", n, "created_before", cutoff.UTC().Format(time.RFC3339))
			return nil
		},
	}, nil
}
