package plan

import (
	"context"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/household"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/scheduler"
)

// DraftSchedule is the plan_draft schedule: at the household's
// plan_draft_dow and plan_draft_time it drafts the week after the fire
// date's, its period scheduler.WeeklyForNextWeek keys plan_draft:YYYY-Www
// for that week. The draft is the KindDraftWeek job a manual draft runs
// (StartScheduledDraft), so a stored draft emits plan_ready keyed
// plan_ready:{weekStart} and a draft that fails, as when the AI fails,
// stores no plan and emits nothing. A week that already has a plan is left
// as it is, and so is a draft of it already queued or running; either way
// the period says so.
func (s *Service) DraftSchedule() scheduler.Schedule {
	return scheduler.Schedule{
		Name: scheduler.PlanDraft,
		Rule: func(settings household.Settings) scheduler.Rule {
			return scheduler.WeeklyForNextWeek(time.Weekday(settings.PlanDraftDow), settings.PlanDraftTime)
		},
		Run: func(ctx context.Context, p scheduler.Period) error {
			d, planned, err := s.StartScheduledDraft(ctx, p.Week)
			if err != nil {
				return err
			}
			switch {
			case planned:
				s.logger.InfoContext(ctx, "scheduled week draft not started: the week already has a plan",
					"period", p.Key, "week_start", p.Week.String())
			case !d.Created:
				s.logger.WarnContext(ctx, "scheduled week draft not started: a draft of the week is already under way",
					"period", p.Key, "week_start", p.Week.String(), "job_id", d.JobID)
			}
			return nil
		},
	}
}
