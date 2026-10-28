package plan

import (
	"context"
	"errors"
	"fmt"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/chat"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/household"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/scheduler"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

// StartReminderLead is how long before a crock-pot dinner's start_by its
// start reminder fires.
const StartReminderLead = 15 * time.Minute

// StartReminderKey is the period key prefix of the crock-pot start
// reminder: its periods are start:YYYY-MM-DD of the day_of_reminder
// schedule.
const StartReminderKey = "start"

// ReminderSchedules are the day_of_reminder schedules. The first fires
// every day at the household's reminder_time, period YYYY-MM-DD, and emits
// day_of_reminder:YYYY-MM-DD for tonight's dinner (EmitDayOfReminder). The
// second fires StartReminderLead before the start_by of a crock-pot dinner
// whose start_by is earlier than that day's reminder_time, period
// start:YYYY-MM-DD of the dinner's date, and emits
// day_of_reminder:start:YYYY-MM-DD (EmitStartReminder). A date whose week
// has no plan emits neither.
func (s *Service) ReminderSchedules() []scheduler.Schedule {
	return []scheduler.Schedule{
		{
			Name: scheduler.DayOfReminder,
			Rule: func(settings household.Settings) scheduler.Rule {
				return scheduler.Daily(settings.ReminderTime)
			},
			Run: func(ctx context.Context, p scheduler.Period) error {
				sent, err := s.EmitDayOfReminder(ctx, p.Date)
				if errors.Is(err, ErrNoNight) {
					s.logger.InfoContext(ctx, "day-of reminder not sent: the night has no dinner",
						"period", p.Key, "date", p.Date.String())
					return nil
				}
				if err != nil {
					return err
				}
				if !sent {
					s.logger.InfoContext(ctx, "day-of reminder not sent: the week has no plan",
						"period", p.Key, "date", p.Date.String())
				}
				return nil
			},
		},
		{
			Name: scheduler.DayOfReminder,
			Key:  StartReminderKey,
			Rule: func(household.Settings) scheduler.Rule {
				return scheduler.DailyAt(s.startReminderAt)
			},
			Run: func(ctx context.Context, p scheduler.Period) error {
				sent, err := s.EmitStartReminder(ctx, p.Date)
				if err != nil {
					return err
				}
				if !sent {
					s.logger.InfoContext(ctx, "start reminder not sent: no crock-pot dinner to start before the reminder",
						"period", p.Key, "date", p.Date.String())
				}
				return nil
			},
		},
	}
}

// EmitStartReminder emits day_of_reminder:start:{date}, "Start the crock
// pot by {time}", for the crock-pot dinner planned on date, and reports
// whether it did: it emits nothing unless date's dinner has a start_by
// earlier than date's reminder_time. A dinner archived since it was planned
// is still started, but its reminder offers no recipe. Emitting a date again
// stores no second notification.
func (s *Service) EmitStartReminder(ctx context.Context, date store.LocalDate) (bool, error) {
	d, ok, err := s.startReminder(ctx, date)
	if err != nil || !ok {
		return false, err
	}
	if err := s.notifier.Emit(ctx, chat.StartReminder(date, d.Dinner.ID, d.Dinner.Name, *d.StartBy, d.Dinner.viewable())); err != nil {
		return false, fmt.Errorf("plan: %w", err)
	}
	return true, nil
}

// startReminderAt is the start reminder's scheduler.FireAt: the
// StartReminderLead before start_by of the dinner date's start reminder,
// if it has one.
func (s *Service) startReminderAt(ctx context.Context, date store.LocalDate) (time.Time, bool, error) {
	d, ok, err := s.startReminder(ctx, date)
	if err != nil || !ok {
		return time.Time{}, false, err
	}
	return d.StartBy.Add(-StartReminderLead), true, nil
}

// startReminder returns the plan day of date when it has a start reminder:
// its dinner has a start_by (a crock-pot dinner of known minutes) earlier
// than the household's reminder_time on date. The plan and the settings
// come from one read transaction.
func (s *Service) startReminder(ctx context.Context, date store.LocalDate) (d Day, ok bool, err error) {
	tx, err := s.st.Reader.BeginTx(ctx, nil)
	if err != nil {
		return Day{}, false, fmt.Errorf("plan: begin read: %w", err)
	}
	defer func() {
		err = errors.Join(err, tx.Rollback())
	}()
	q := db.New(tx)
	p, err := readPlan(ctx, q, household.WeekStart(date))
	if errors.Is(err, ErrNotFound) {
		return Day{}, false, nil
	}
	if err != nil {
		return Day{}, false, err
	}
	d, err = p.day(date)
	if err != nil {
		return Day{}, false, err
	}
	if d.StartBy == nil {
		return Day{}, false, nil
	}
	settings, err := q.GetHouseholdSettings(ctx)
	if err != nil {
		return Day{}, false, fmt.Errorf("plan: read household settings: %w", err)
	}
	zone, err := household.LoadZone(settings.Timezone)
	if err != nil {
		return Day{}, false, fmt.Errorf("plan: household settings: %w", err)
	}
	remind, err := household.LocalTime(zone, date, settings.ReminderTime)
	if err != nil {
		return Day{}, false, fmt.Errorf("plan: reminder time of %s: %w", date, err)
	}
	return d, d.StartBy.Before(remind), nil
}

// day returns p's day on date; a plan holds all seven days of its week.
func (p Plan) day(date store.LocalDate) (Day, error) {
	for _, d := range p.Days {
		if d.Date == date.String() {
			return d, nil
		}
	}
	return Day{}, fmt.Errorf("plan: week %s has no day %s", p.WeekStart, date)
}
