package scheduler_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/harness"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/household"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/notify"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/notify/notifytest"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/scheduler"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

// The tests in this file run plan's day_of_reminder schedules on the fake
// clock against the real plan service and notification outbox. The plan of
// the week of Monday Oct 12 (w42) has poolNames Monday to Sunday: Beef
// Chili on Monday, Pot Roast on Tuesday. The household's dinner time is the
// seeded 17:30 and its reminder time the seeded 16:30, in Chicago.

// w42 is the Monday of 2026-W42.
var w42 = mustLocalDate("2026-10-12")

// planWeek drafts the plan of the week starting weekStart and waits for it.
func (p *planEnv) planWeek(weekStart store.LocalDate) {
	p.e.t.Helper()
	p.reply(weekStart)
	if _, err := p.svc.StartDraft(context.Background(), weekStart, false); err != nil {
		p.e.t.Fatal(err)
	}
	if jobs := p.e.drafts(); jobs[len(jobs)-1] != succeeded {
		p.e.t.Fatalf("draft of %s = %+v, want succeeded", weekStart, jobs)
	}
}

// crockPot makes the dinner name a crock-pot dinner of minutes.
func (p *planEnv) crockPot(name string, minutes int) {
	p.e.t.Helper()
	if _, err := p.e.st.Writer.Exec(`UPDATE dinners SET method = 'crockpot', minutes_total = ? WHERE id = ?`, minutes, p.ids[name]); err != nil {
		p.e.t.Fatal(err)
	}
}

// reminders returns the day_of_reminder notifications stored, oldest
// first. The seeded preference sends day_of_reminder to push only, so there
// is one row per event.
func (e *env) reminders() []notifytest.Row {
	e.t.Helper()
	rows := notifytest.Rows(e.t, e.st, notify.DayOfReminder)
	for _, r := range rows {
		if r.Channel != notify.ChannelPush {
			e.t.Fatalf("day_of_reminder row %+v, want push only", r)
		}
	}
	return rows
}

// reminderKeys returns the keys of e.reminders().
func (e *env) reminderKeys() []string {
	e.t.Helper()
	var keys []string
	for _, r := range e.reminders() {
		keys = append(keys, r.Key)
	}
	return keys
}

// wantReminder fails unless the reminder keyed key was stored with title,
// body and the link to the recipe of the dinner name, which it names.
func (p *planEnv) wantReminder(key, title, body, name string) {
	p.e.t.Helper()
	for _, r := range p.e.reminders() {
		if r.Key != key {
			continue
		}
		week := household.WeekStart(mustLocalDate(key[len(key)-len("YYYY-MM-DD"):]))
		want := notify.Payload{
			Event: notify.DayOfReminder, Title: title, Body: body, URL: "/dinner/" + p.ids[name], Tag: key,
			Dinner: &notify.Dinner{ID: p.ids[name], Name: name}, Week: week.String(),
		}
		if !reflect.DeepEqual(r.Payload, want) {
			p.e.t.Errorf("reminder %s = %+v, want %+v", key, r.Payload, want)
		}
		return
	}
	p.e.t.Errorf("no reminder %s", key)
}

// TestDayOfReminderNormalNight: a night whose dinner is not a crock-pot
// dinner gets exactly the daily reminder, at reminder_time 16:30, keyed
// day_of_reminder:YYYY-MM-DD and linking to the dinner.
func TestDayOfReminderNormalNight(t *testing.T) {
	e := newEnv(t)
	p := e.plans(e.pool())
	p.planWeek(w42)

	// Monday 08:00: the catch-up of Sunday 16:30 finds no plan for W41.
	r := e.start(at(2026, 10, 12, 8, 0), p.svc.ReminderSchedules()...)
	r.runUntil(at(2026, 10, 12, 16, 29))
	wantKeys(t, "before 16:30", e.reminderKeys(), nil)

	r.runUntil(at(2026, 10, 12, 16, 30))
	wantKeys(t, "at 16:30", e.reminderKeys(), []string{"day_of_reminder:2026-10-12"})
	p.wantReminder("day_of_reminder:2026-10-12", "Tonight: Beef Chili · Recipe →", "", "Beef Chili")

	r.runUntil(at(2026, 10, 13, 23, 59))
	wantKeys(t, "Tuesday", e.reminderKeys(), []string{"day_of_reminder:2026-10-12", "day_of_reminder:2026-10-13"})
	p.wantReminder("day_of_reminder:2026-10-13", "Tonight: Pot Roast · Recipe →", "", "Pot Roast")
	wantKeys(t, "schedule_runs", e.claimed(scheduler.DayOfReminder), []string{"2026-10-11", "2026-10-12", "2026-10-13"})
}

// TestDayOfReminderCrockPotNight: a crock-pot night whose start_by is
// earlier than reminder_time gets the start reminder at start_by minus 15
// minutes, keyed day_of_reminder:start:YYYY-MM-DD, and the daily reminder
// at reminder_time. A crock-pot night whose start_by is not earlier than
// reminder_time gets only the daily one.
func TestDayOfReminderCrockPotNight(t *testing.T) {
	e := newEnv(t)
	p := e.plans(e.pool())
	p.crockPot("Beef Chili", 480) // Monday: start by 09:30
	p.crockPot("Pot Roast", 30)   // Tuesday: start by 17:00, after 16:30
	p.planWeek(w42)

	r := e.start(at(2026, 10, 12, 8, 0), p.svc.ReminderSchedules()...)
	r.runUntil(at(2026, 10, 12, 9, 14))
	wantKeys(t, "before 09:15", e.reminderKeys(), nil)

	r.runUntil(at(2026, 10, 12, 9, 15))
	wantKeys(t, "at 09:15", e.reminderKeys(), []string{"day_of_reminder:start:2026-10-12"})
	p.wantReminder("day_of_reminder:start:2026-10-12", "Start the crock pot by 9:30 am", "Beef Chili", "Beef Chili")

	r.runUntil(at(2026, 10, 12, 16, 29))
	wantKeys(t, "before 16:30", e.reminderKeys(), []string{"day_of_reminder:start:2026-10-12"})
	r.runUntil(at(2026, 10, 12, 16, 30))
	wantKeys(t, "at 16:30", e.reminderKeys(), []string{"day_of_reminder:start:2026-10-12", "day_of_reminder:2026-10-12"})
	p.wantReminder("day_of_reminder:2026-10-12", "Tonight: Beef Chili · Recipe →", "", "Beef Chili")

	r.runUntil(at(2026, 10, 13, 23, 59))
	wantKeys(t, "Tuesday", e.reminderKeys(), []string{"day_of_reminder:start:2026-10-12", "day_of_reminder:2026-10-12", "day_of_reminder:2026-10-13"})
	wantKeys(t, "schedule_runs", e.claimed(scheduler.DayOfReminder), []string{"2026-10-11", "2026-10-12", "2026-10-13", "start:2026-10-12"})
}

// TestDayOfReminderEmptyNight: with no plan for tonight's week, here none
// at all and then only next week's, nothing is emitted and nothing fails;
// next week's crock-pot Monday is reminded on its own day.
func TestDayOfReminderEmptyNight(t *testing.T) {
	e := newEnv(t)
	p := e.plans(e.pool())

	r := e.start(at(2026, 10, 12, 8, 0), p.svc.ReminderSchedules()...)
	r.runUntil(at(2026, 10, 13, 23, 59))
	wantKeys(t, "with no plan", e.reminderKeys(), nil)

	p.crockPot("Beef Chili", 480)
	p.planWeek(w43)
	r.runUntil(at(2026, 10, 18, 23, 59))
	wantKeys(t, "with only next week's plan", e.reminderKeys(), nil)
	wantKeys(t, "schedule_runs", e.claimed(scheduler.DayOfReminder),
		[]string{"2026-10-11", "2026-10-12", "2026-10-13", "2026-10-14", "2026-10-15", "2026-10-16", "2026-10-17", "2026-10-18"})
	if logs := p.logs.String(); !strings.Contains(logs, "day-of reminder not sent: the week has no plan") || strings.Contains(logs, "level=ERROR") {
		t.Errorf("logs = %s, want the empty nights logged and no error", logs)
	}

	r.runUntil(at(2026, 10, 19, 9, 15))
	wantKeys(t, "next Monday", e.reminderKeys(), []string{"day_of_reminder:start:2026-10-19"})
	p.wantReminder("day_of_reminder:start:2026-10-19", "Start the crock pot by 9:30 am", "Beef Chili", "Beef Chili")
}

// TestDayOfReminderFollowsReminderTimeWithoutRestart: a reminder_time
// change takes effect in the running scheduler. Moved from 16:30 to 18:00,
// the daily reminder fires at 18:00, not 16:30, and a crock-pot start_by
// of 17:00, now earlier than reminder_time, gets its start reminder at
// 16:45.
func TestDayOfReminderFollowsReminderTimeWithoutRestart(t *testing.T) {
	e := newEnv(t)
	p := e.plans(e.pool())
	p.crockPot("Beef Chili", 30) // start by 17:00
	p.planWeek(w42)

	r := e.start(at(2026, 10, 12, 8, 0), p.svc.ReminderSchedules()...)
	e.update(household.SettingsPatch{ReminderTime: ptr("18:00")})
	r.changed()
	r.runUntil(at(2026, 10, 12, 16, 44))
	wantKeys(t, "before 16:45", e.reminderKeys(), nil)
	r.runUntil(at(2026, 10, 12, 16, 45))
	wantKeys(t, "at 16:45", e.reminderKeys(), []string{"day_of_reminder:start:2026-10-12"})
	p.wantReminder("day_of_reminder:start:2026-10-12", "Start the crock pot by 5:00 pm", "Beef Chili", "Beef Chili")
	r.runUntil(at(2026, 10, 12, 17, 59))
	wantKeys(t, "before 18:00", e.reminderKeys(), []string{"day_of_reminder:start:2026-10-12"})
	r.runUntil(at(2026, 10, 12, 18, 0))
	wantKeys(t, "at 18:00", e.reminderKeys(), []string{"day_of_reminder:start:2026-10-12", "day_of_reminder:2026-10-12"})
}

// TestDayOfReminderNoDuplicateAfterRestart: restarts after each reminder
// of a crock-pot night, within the catch-up window, emit neither again:
// each period is already claimed.
func TestDayOfReminderNoDuplicateAfterRestart(t *testing.T) {
	e := newEnv(t)
	ids := e.pool()
	p := e.plans(ids)
	p.crockPot("Beef Chili", 480)
	p.planWeek(w42)

	r := e.start(at(2026, 10, 12, 8, 0), p.svc.ReminderSchedules()...)
	r.runUntil(at(2026, 10, 12, 9, 15))
	r.stop()
	p.stop()

	p2 := e.plans(ids)
	r2 := e.start(at(2026, 10, 12, 9, 20), p2.svc.ReminderSchedules()...)
	wantKeys(t, "after the first restart", e.reminderKeys(), []string{"day_of_reminder:start:2026-10-12"})
	r2.runUntil(at(2026, 10, 12, 16, 30))
	r2.stop()
	p2.stop()

	p3 := e.plans(ids)
	r3 := e.start(at(2026, 10, 12, 16, 40), p3.svc.ReminderSchedules()...)
	r3.runUntil(at(2026, 10, 13, 9, 0))
	wantKeys(t, "after the second restart", e.reminderKeys(), []string{"day_of_reminder:start:2026-10-12", "day_of_reminder:2026-10-12"})
	wantKeys(t, "schedule_runs", e.claimed(scheduler.DayOfReminder), []string{"2026-10-11", "2026-10-12", "start:2026-10-12"})
}

// TestDailyAtLookupFailureIsRetried: a DailyAt rule fires at the instant
// its lookup gives, its period keyed by the schedule's Key and the date;
// a lookup that fails fires nothing and is looked up again after
// ClaimRetry, then fires its period once.
func TestDailyAtLookupFailureIsRetried(t *testing.T) {
	e := newEnv(t)
	var rec recorder
	var mu sync.Mutex
	failing := false
	at915 := func(ctx context.Context, d store.LocalDate) (time.Time, bool, error) {
		mu.Lock()
		defer mu.Unlock()
		if failing {
			return time.Time{}, false, errors.New("database down")
		}
		y, m, day := d.Date()
		return at(y, m, day, 9, 15), true, nil
	}
	sch := scheduler.Schedule{
		Name: scheduler.DayOfReminder,
		Key:  "start",
		Rule: func(household.Settings) scheduler.Rule { return scheduler.DailyAt(at915) },
		Run:  rec.run,
	}

	r := e.start(at(2026, 10, 12, 8, 0), sch)
	wantKeys(t, "catch-up", rec.keys(), []string{"start:2026-10-11"})
	mu.Lock()
	failing = true
	mu.Unlock()
	r.runUntil(at(2026, 10, 12, 9, 16))
	wantKeys(t, "while the lookup fails", rec.keys(), []string{"start:2026-10-11"})

	mu.Lock()
	failing = false
	mu.Unlock()
	r.runUntil(at(2026, 10, 12, 9, 17))
	wantKeys(t, "after the lookup recovers", rec.keys(), []string{"start:2026-10-11", "start:2026-10-12"})
	if p := rec.periods()[1]; !p.FireAt.Equal(at(2026, 10, 12, 9, 15)) || p.Date.String() != "2026-10-12" {
		t.Errorf("period = %+v, want fired at 09:15 for 2026-10-12", p)
	}
	r.runUntil(at(2026, 10, 13, 12, 0))
	wantKeys(t, "next day", rec.keys(), []string{"start:2026-10-11", "start:2026-10-12", "start:2026-10-13"})
	wantKeys(t, "schedule_runs", e.claimed(scheduler.DayOfReminder), rec.keys())
}

// questLog stores name as a Quest-log dinner, a crock-pot dinner of
// minutes, and returns its id. It is not a Regular.
func (p *planEnv) questLog(name string, minutes int) string {
	p.e.t.Helper()
	now := store.Now().String()
	d, err := db.New(p.e.st.Writer).CreateDinner(context.Background(), db.CreateDinnerParams{
		ID:              store.NewID(),
		Name:            name,
		NormalizedName:  harness.Normalize(name),
		Status:          "quest_log",
		Origin:          "side_quest",
		GroundingStatus: "manual",
		CreatedAt:       now,
		UpdatedAt:       now,
	})
	if err != nil {
		p.e.t.Fatal(err)
	}
	p.ids[name] = d.ID
	p.crockPot(name, minutes)
	return d.ID
}

// TestStartReminderAfterSwapFiresOnTime: swapping a crock-pot dinner in
// for tonight wakes the scheduler, so its start reminder fires at start_by
// minus 15 minutes, 09:15, and not at the next hourly check, 10:00. At
// 09:00 tonight is Beef Chili, not a crock-pot dinner, and the scheduler's
// next timer is 10:00; at 09:01 the household swaps in an eight-hour
// crock-pot Quest-log dinner, start by 09:30. Each reminder fires once.
func TestStartReminderAfterSwapFiresOnTime(t *testing.T) {
	e := newEnv(t)
	p := e.wakingPlans(e.pool())
	stew := p.questLog("Crock Pot Stew", 480) // start by 09:30
	p.planWeek(w42)

	r := e.start(at(2026, 10, 12, 8, 0), p.svc.ReminderSchedules()...)
	r.runUntil(at(2026, 10, 12, 9, 0))
	if next, _ := r.clock.nextDeadline(); !next.Equal(at(2026, 10, 12, 10, 0)) {
		t.Fatalf("next wake at 09:00 = %s, want the hourly 10:00", next)
	}

	r.runUntil(at(2026, 10, 12, 9, 1))
	if _, err := p.svc.Swap(context.Background(), w42, "2026-10-12", stew); err != nil {
		t.Fatal(err)
	}
	r.changed()
	r.runUntil(at(2026, 10, 12, 9, 14))
	wantKeys(t, "before 09:15", e.reminderKeys(), nil)

	r.runUntil(at(2026, 10, 12, 9, 15))
	wantKeys(t, "at 09:15", e.reminderKeys(), []string{"day_of_reminder:start:2026-10-12"})
	p.wantReminder("day_of_reminder:start:2026-10-12", "Start the crock pot by 9:30 am", "Crock Pot Stew", "Crock Pot Stew")

	r.runUntil(at(2026, 10, 13, 23, 59))
	wantKeys(t, "Tuesday", e.reminderKeys(), []string{"day_of_reminder:start:2026-10-12", "day_of_reminder:2026-10-12", "day_of_reminder:2026-10-13"})
	p.wantReminder("day_of_reminder:2026-10-12", "Tonight: Crock Pot Stew · Recipe →", "", "Crock Pot Stew")
	wantKeys(t, "schedule_runs", e.claimed(scheduler.DayOfReminder), []string{"2026-10-11", "2026-10-12", "2026-10-13", "start:2026-10-12"})
}
