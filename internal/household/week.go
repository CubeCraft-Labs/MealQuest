// Package household owns household-local calendar and clock logic. Every
// function works in the household's IANA zone, passed explicitly, and never
// in the process zone (time.Local).
//
// Weeks are ISO weeks: Monday to Sunday, numbered by ISO 8601, so the period
// key of 2026-12-28 is "2026-W53" and of 2027-01-04 is "2027-W01".
//
// Calendar arithmetic is done on UTC midnights, which have no DST, so a date
// plus seven days is always the same weekday a week later.
package household

import (
	"errors"
	"fmt"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

// Today returns the household-local date at instant now in zone loc.
func Today(now time.Time, loc *time.Location) store.LocalDate {
	return store.LocalDateOf(now.In(loc))
}

// WeekStart returns the ISO Monday of the week containing d. It panics if d is
// the zero LocalDate.
func WeekStart(d store.LocalDate) store.LocalDate {
	t := midnightUTC(d)
	// Weekday counts from Sunday = 0; shift so Monday = 0 and Sunday = 6.
	sinceMonday := (int(t.Weekday()) + 6) % 7
	return store.LocalDateOf(t.AddDate(0, 0, -sinceMonday))
}

// WeekDays returns the seven dates Monday to Sunday of the week starting on
// weekStart. It is an error if weekStart is not a Monday.
func WeekDays(weekStart store.LocalDate) ([7]store.LocalDate, error) {
	t, err := monday(weekStart)
	if err != nil {
		return [7]store.LocalDate{}, err
	}
	var days [7]store.LocalDate
	for i := range days {
		days[i] = store.LocalDateOf(t.AddDate(0, 0, i))
	}
	return days, nil
}

// WeekLabel returns the display label of the week starting on weekStart, such
// as "Week 41 · Oct 5 – 11", or "Week 40 · Sep 28 – Oct 4" when the week
// spans two months. It is an error if weekStart is not a Monday.
func WeekLabel(weekStart store.LocalDate) (string, error) {
	start, err := monday(weekStart)
	if err != nil {
		return "", err
	}
	_, week := start.ISOWeek()
	end := start.AddDate(0, 0, 6)
	if end.Month() == start.Month() {
		return fmt.Sprintf("Week %d · %s – %d", week, start.Format("Jan 2"), end.Day()), nil
	}
	return fmt.Sprintf("Week %d · %s – %s", week, start.Format("Jan 2"), end.Format("Jan 2")), nil
}

// PeriodKey returns the ISO week key of the week containing d, such as
// "2026-W41". The year is the ISO week-numbering year, which differs from the
// calendar year around New Year. It panics if d is the zero LocalDate.
func PeriodKey(d store.LocalDate) string {
	year, week := midnightUTC(d).ISOWeek()
	return fmt.Sprintf("%04d-W%02d", year, week)
}

// LocalTime returns the instant at which household clocks in zone loc read
// hhmm (24-hour "HH:MM") on date d.
//
// DST rule: a wall time that does not exist (spring forward) or occurs twice
// (fall back) is read with the UTC offset in effect before the transition. A
// skipped time therefore moves forward by the length of the gap (02:30 on a
// day clocks jump 02:00→03:00 is 03:30), and a repeated time is its first
// occurrence. The rule assumes at most one offset change in the 48 hours
// around the wall time, which holds for every real zone.
func LocalTime(loc *time.Location, d store.LocalDate, hhmm string) (time.Time, error) {
	if d.IsZero() {
		return time.Time{}, errors.New("household: zero date")
	}
	clock, err := time.Parse("15:04", hhmm)
	if err != nil || clock.Format("15:04") != hhmm {
		return time.Time{}, fmt.Errorf("household: invalid time %q: want HH:MM", hhmm)
	}
	y, m, day := d.Date()
	// wall is the wall-clock reading expressed as if it were UTC; the real
	// instant is wall minus the zone's UTC offset at that instant.
	wall := time.Date(y, m, day, clock.Hour(), clock.Minute(), 0, 0, time.UTC)
	before := offset(wall.Add(-24*time.Hour), loc)
	after := offset(wall.Add(24*time.Hour), loc)
	early := wall.Add(-before)
	late := wall.Add(-after)
	// Use the post-transition offset only when the pre-transition reading is
	// not a real wall time and the post-transition one is.
	if offset(early, loc) != before && offset(late, loc) == after {
		return late.In(loc), nil
	}
	return early.In(loc), nil
}

// offset returns the UTC offset of zone loc at instant t.
func offset(t time.Time, loc *time.Location) time.Duration {
	_, secs := t.In(loc).Zone()
	return time.Duration(secs) * time.Second
}

// midnightUTC returns d as midnight UTC. It panics if d is the zero LocalDate.
func midnightUTC(d store.LocalDate) time.Time {
	if d.IsZero() {
		panic("household: zero date")
	}
	y, m, day := d.Date()
	return time.Date(y, m, day, 0, 0, 0, 0, time.UTC)
}

// monday returns weekStart as midnight UTC, or an error if it is not a Monday.
func monday(weekStart store.LocalDate) (time.Time, error) {
	if weekStart.IsZero() {
		return time.Time{}, errors.New("household: zero week start")
	}
	t := midnightUTC(weekStart)
	if t.Weekday() != time.Monday {
		return time.Time{}, fmt.Errorf("household: week start %s is a %s, want Monday", weekStart, t.Weekday())
	}
	return t, nil
}
