package plan

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base32"
	"errors"
	"fmt"
	"strings"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/dinners"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/gcal"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/household"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

// eventLength is how long a dinner's Calendar event lasts.
const eventLength = 60 * time.Minute

// ErrPlanChanged is a Push to Calendar of a plan edited (swapped or
// redrafted) while it was being pushed: Calendar may show the plan as it
// was, so the push is not recorded and pushing again brings Calendar up to
// date.
var ErrPlanChanged = errors.New("the plan changed while it was being pushed to Calendar; push again")

// Calendar writes events to the household's chosen calendar, then runs
// record while that calendar stays chosen. In production it is
// *gcal.Service.
type Calendar interface {
	PushEvents(ctx context.Context, events []gcal.Event, record func() error) error
}

// eventIDEncoding is base32hex, lowercase and unpadded: Google's alphabet
// for event ids.
var eventIDEncoding = base32.NewEncoding("0123456789abcdefghijklmnopqrstuv").WithPadding(base32.NoPadding)

// EventID is the Calendar event id of date of the week starting weekStart:
// the lowercase base32hex SHA-256 of "mealquest|{weekStart}|{date}", 52
// characters of a-v and 0-9, within Google's rule for event ids (that
// alphabet, 5 to 1024 characters). A day always has the same id, so
// pushing it again updates its event rather than adding another.
func EventID(weekStart, date store.LocalDate) string {
	sum := sha256.Sum256([]byte("mealquest|" + weekStart.String() + "|" + date.String()))
	return eventIDEncoding.EncodeToString(sum[:])
}

// pushDay is a day of a plan being pushed: its date and its event.
type pushDay struct {
	date  string
	event gcal.Event
}

// Push pushes the plan of the week starting weekStart to the household's
// chosen calendar and returns the plan as synced. Each dinner is an event
// "Dinner: {name}" from the household's dinner time on that date, in the
// household's zone, for an hour; its description is the dinner's name,
// "Start by {time}" for a crock-pot dinner whose minutes are known, and
// the dinner's link under the public URL. A manual night is an event
// whose summary and description are the label alone, at the same time,
// with no dinner link. A cleared night, and a Delivery or Dining-out
// evening, is deleted: there is no dinner event to write. Its id is EventID.
//
// A push writes the days Calendar does not show as planned: those never
// pushed, swapped, or redrafted to another dinner since, whose
// calendar_event_id is empty. When every day has one, Calendar shows the
// whole plan and every day is written again, which puts back anything
// changed in Calendar since: the app is the source of truth. A stored event
// id is always on the chosen calendar: choosing another
// (gcal.Service.SelectCalendar) or disconnecting (gcal.Service.Disconnect)
// clears them all, so the next push writes the whole week.
//
// Only a push that wrote every one of those events is recorded: the plan
// becomes synced with calendar_synced_at now, and each day written stores
// its event id. A push that failed part way records nothing and leaves the
// plan's status as it was; since event ids are fixed, pushing again
// updates the events already written rather than adding them twice. The
// push is recorded while the calendar written to is still the chosen one.
// A plan edited while it was pushed, or whose events were forgotten by
// choosing another calendar or disconnecting, fails with ErrPlanChanged,
// unrecorded.
//
// The week must have a plan (else ErrNotFound). The calendar's errors are
// returned as they are: gcal.ErrNotConnected, gcal.ErrNoCalendar,
// gcal.ErrCalendarUnavailable, gcal.ErrReconnect or *gcal.CalendarError.
//
// A successful Push is the household confirming the week's plan, which is
// what the week's Supply run (Feature 17) is made from; the returned plan
// is the one pushed, and revision is its revision as pushed, so the Supply
// run can be made from that plan and no later edit of it.
func (s *Service) Push(ctx context.Context, weekStart store.LocalDate) (p Plan, revision int64, err error) {
	if _, err := household.WeekDays(weekStart); err != nil {
		return Plan{}, 0, &InvalidWeekError{Value: weekStart.String()}
	}
	planID, revision, days, err := s.pushDays(ctx, weekStart)
	if err != nil {
		return Plan{}, 0, err
	}
	events := make([]gcal.Event, len(days))
	for i, d := range days {
		events[i] = d.event
	}
	record := func() error {
		return s.st.WithTx(ctx, func(tx *sql.Tx) error {
			q := db.New(tx)
			now := store.Now().String()
			n, err := q.MarkWeekPlanSynced(ctx, db.MarkWeekPlanSyncedParams{
				CalendarSyncedAt: sql.NullString{String: now, Valid: true},
				UpdatedAt:        now,
				ID:               planID,
				Revision:         revision,
			})
			if err != nil {
				return fmt.Errorf("plan: mark %s synced: %w", planID, err)
			}
			if n != 1 {
				return ErrPlanChanged
			}
			for _, d := range days {
				n, err := q.SetPlanDayCalendarEvent(ctx, db.SetPlanDayCalendarEventParams{
					CalendarEventID: sql.NullString{String: d.event.ID, Valid: true},
					UpdatedAt:       now,
					PlanID:          planID,
					Date:            d.date,
				})
				if err != nil {
					return fmt.Errorf("plan: record event of %s of %s: %w", d.date, planID, err)
				}
				if n != 1 {
					return fmt.Errorf("plan: %s has no day %s", planID, d.date)
				}
			}
			p, err = readPlan(ctx, q, weekStart)
			return err
		})
	}
	if err := s.cal.PushEvents(ctx, events, record); err != nil {
		return Plan{}, 0, fmt.Errorf("plan: push %s to Calendar: %w", weekStart, err)
	}
	return p, revision, nil
}

// pushDays reads, in one read transaction, the plan of the week starting
// weekStart, its revision, and the days a push writes with their events.
func (s *Service) pushDays(ctx context.Context, weekStart store.LocalDate) (planID string, revision int64, days []pushDay, err error) {
	tx, err := s.st.Reader.BeginTx(ctx, nil)
	if err != nil {
		return "", 0, nil, fmt.Errorf("plan: begin read: %w", err)
	}
	defer func() {
		err = errors.Join(err, tx.Rollback())
	}()
	q := db.New(tx)
	row, err := q.GetWeekPlan(ctx, weekStart.String())
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, nil, ErrNotFound
	}
	if err != nil {
		return "", 0, nil, fmt.Errorf("plan: read week %s: %w", weekStart, err)
	}
	settings, err := q.GetHouseholdSettings(ctx)
	if err != nil {
		return "", 0, nil, fmt.Errorf("plan: read household settings: %w", err)
	}
	zone, err := household.LoadZone(settings.Timezone)
	if err != nil {
		return "", 0, nil, fmt.Errorf("plan: household settings: %w", err)
	}
	rows, err := q.ListPlanSlots(ctx, row.ID)
	if err != nil {
		return "", 0, nil, fmt.Errorf("plan: read days of %s: %w", row.ID, err)
	}
	dates, err := household.WeekDays(weekStart)
	if err != nil {
		return "", 0, nil, fmt.Errorf("plan: %w", err)
	}
	if len(rows) != len(dates) {
		return "", 0, nil, fmt.Errorf("plan: week %s has %d planned days, want %d", weekStart, len(rows), len(dates))
	}
	all := make([]pushDay, len(rows))
	for i, r := range rows {
		if r.Date != dates[i].String() {
			return "", 0, nil, fmt.Errorf("plan: week %s day %d is %s, want %s", weekStart, i+1, r.Date, dates[i])
		}
		_, raw, err := planDay(ctx, q, zone, dates[i], settings.DinnerTime, r)
		if err != nil {
			return "", 0, nil, err
		}
		var event gcal.Event
		switch {
		case raw != nil:
			event, err = s.event(zone, settings.Timezone, settings.DinnerTime, weekStart, dates[i], *raw)
		case r.ManualLabel.Valid:
			event, err = s.manualEvent(zone, settings.Timezone, settings.DinnerTime, weekStart, dates[i], r.ManualLabel.String)
		default:
			// A cleared night, and a Delivery or Dining-out evening, has
			// no dinner to write. The event is removed, so Calendar does
			// not keep a dinner the plan no longer has. The id is the
			// day's own, so the removal hits the event a previous push
			// wrote. An evening that was never written is not created.
			event = gcal.Event{ID: EventID(weekStart, dates[i]), Remove: true}
		}
		if err != nil {
			return "", 0, nil, fmt.Errorf("plan: event of %s: %w", r.Date, err)
		}
		d := pushDay{date: r.Date, event: event}
		all[i] = d
		if !r.CalendarEventID.Valid {
			days = append(days, d)
		}
	}
	if len(days) == 0 {
		days = all
	}
	return row.ID, row.Revision, days, nil
}

// manualEvent is the Calendar event of a one-off night named label on
// date of the week starting weekStart, at dinnerTime in zone, named
// zoneName. The summary and description are the label alone: no "Dinner:"
// prefix and no dinner link, so the night is not pushed as a dinner.
func (s *Service) manualEvent(zone *time.Location, zoneName, dinnerTime string, weekStart, date store.LocalDate, label string) (gcal.Event, error) {
	start, err := household.LocalTime(zone, date, dinnerTime)
	if err != nil {
		return gcal.Event{}, err
	}
	return gcal.Event{
		ID:          EventID(weekStart, date),
		Summary:     label,
		Description: label,
		Start:       start,
		End:         start.Add(eventLength),
		TimeZone:    zoneName,
	}, nil
}

// event is the Calendar event of dinner d on date of the week starting
// weekStart, at dinnerTime in zone, named zoneName.
func (s *Service) event(zone *time.Location, zoneName, dinnerTime string, weekStart, date store.LocalDate, d db.Dinner) (gcal.Event, error) {
	start, err := household.LocalTime(zone, date, dinnerTime)
	if err != nil {
		return gcal.Event{}, err
	}
	lines := []string{d.Name}
	if !dinners.Cooked(d.MealSource) {
		label, err := uncookedLabel(d.MealSource)
		if err != nil {
			return gcal.Event{}, err
		}
		lines = append(lines, label)
	} else {
		startBy, err := startBy(zone, date, dinnerTime, d)
		if err != nil {
			return gcal.Event{}, err
		}
		if startBy != nil {
			lines = append(lines, "Start by "+startBy.In(zone).Format("3:04 pm"))
		}
		lines = append(lines, s.publicURL.JoinPath("dinner", d.ID).String())
	}
	return gcal.Event{
		ID:          EventID(weekStart, date),
		Summary:     "Dinner: " + d.Name,
		Description: strings.Join(lines, "\n"),
		Start:       start,
		End:         start.Add(eventLength),
		TimeZone:    zoneName,
	}, nil
}
