package plan_test

import (
	"crypto/sha256"
	"database/sql"
	"encoding/base32"
	"errors"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/gcal"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/gcal/gcaltest"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/plan"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

// dinnersCal is the calendar the tests push to.
var dinnersCal = gcaltest.Calendar{ID: "dinners@group.calendar.google.com", Summary: "Dinners", AccessRole: "owner"}

// connectCalendar connects the household's Google account and chooses
// dinnersCal.
func (e *env) connectCalendar(t *testing.T) {
	t.Helper()
	e.google.SetCalendars(0, dinnersCal)
	e.google.Connect(t, e.gcal)
	if _, err := e.gcal.SelectCalendar(t.Context(), dinnersCal.ID); err != nil {
		t.Fatal(err)
	}
}

// storePlan stores a draft plan of the week starting weekStart with the
// dinners named names, Monday first, and returns its days' dates.
func (e *env) storePlan(t *testing.T, weekStart store.LocalDate, names ...string) []string {
	t.Helper()
	q := db.New(e.st.Writer)
	now := store.Now().String()
	id := store.NewID()
	if err := q.CreateWeekPlan(t.Context(), db.CreateWeekPlanParams{ID: id, WeekStart: weekStart.String(), CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	y, m, d := weekStart.Date()
	var dates []string
	for i, name := range names {
		date := store.LocalDateOf(time.Date(y, m, d+i, 0, 0, 0, 0, time.UTC)).String()
		if err := q.PutPlanDay(t.Context(), db.PutPlanDayParams{ID: store.NewID(), PlanID: id, Date: date,
			DinnerID: sql.NullString{String: e.ids[name], Valid: true}, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
		dates = append(dates, date)
	}
	return dates
}

// eventIDs returns each day's stored calendar_event_id of the week
// starting weekStart, "" for none, by date.
func (e *env) eventIDs(t *testing.T, weekStart store.LocalDate) map[string]string {
	t.Helper()
	rows, err := e.st.Reader.QueryContext(t.Context(), `SELECT plan_days.date, plan_days.calendar_event_id FROM plan_days
		JOIN week_plans ON week_plans.id = plan_days.plan_id WHERE week_plans.week_start = ?`, weekStart.String())
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var date string
		var id sql.NullString
		if err := rows.Scan(&date, &id); err != nil {
			t.Fatal(err)
		}
		out[date] = id.String
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// assertUnsynced fails unless the plan of weekStart has status, no
// calendar_synced_at and no stored event ids.
func (e *env) assertUnsynced(t *testing.T, weekStart store.LocalDate, status string) {
	t.Helper()
	p, err := e.svc.Get(t.Context(), weekStart)
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != status || p.CalendarSyncedAt != nil {
		t.Errorf("plan = %s synced at %v, want %s and never synced", p.Status, p.CalendarSyncedAt, status)
	}
	for date, id := range e.eventIDs(t, weekStart) {
		if id != "" {
			t.Errorf("%s stored event id %q after a failed push", date, id)
		}
	}
}

// assertNoSecret fails when text holds the refresh token, the access token
// or the client secret.
func assertNoSecret(t *testing.T, what, text string) {
	t.Helper()
	for _, secret := range []string{gcaltest.RefreshToken, gcaltest.AccessToken, gcaltest.ClientSecret} {
		if strings.Contains(text, secret) {
			t.Errorf("%s contains a secret: %s", what, text)
		}
	}
}

func TestEventIDIsDeterministicAndWithinGoogleRules(t *testing.T) {
	monday, tuesday := mustDate("2026-10-12"), mustDate("2026-10-13")
	id := plan.EventID(monday, tuesday)
	sum := sha256.Sum256([]byte("mealquest|2026-10-12|2026-10-13"))
	if want := strings.ToLower(base32.HexEncoding.WithPadding(base32.NoPadding).EncodeToString(sum[:])); id != want {
		t.Errorf("EventID = %s, want %s", id, want)
	}
	// Google: base32hex characters (a-v, 0-9), 5 to 1024 of them.
	if !regexp.MustCompile(`^[a-v0-9]{5,1000}$`).MatchString(id) || len(id) != 52 {
		t.Errorf("EventID = %q (%d characters), want 52 of a-v and 0-9", id, len(id))
	}
	if again := plan.EventID(monday, tuesday); again != id {
		t.Errorf("EventID changed between calls: %s, %s", id, again)
	}
	if plan.EventID(monday, monday) == id || plan.EventID(mustDate("2026-10-19"), tuesday) == id {
		t.Error("EventID is the same for another date or week")
	}
}

func TestPushCreatesOneEventPerDay(t *testing.T) {
	e := newEnv(t)
	e.connectCalendar(t)
	dates := e.storePlan(t, week, fullWeek...)

	p, err := e.svc.Push(t.Context(), week)
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	if p.Status != plan.StatusSynced || p.CalendarSyncedAt == nil {
		t.Errorf("pushed plan = %s synced at %v, want synced with a time", p.Status, p.CalendarSyncedAt)
	}
	if inserts, updates := e.google.EventWrites(); inserts != 7 || updates != 0 {
		t.Errorf("Google saw %d inserts and %d updates, want 7 and 0", inserts, updates)
	}
	events := e.google.Events(dinnersCal.ID)
	if len(events) != 7 {
		t.Fatalf("calendar has %d events, want 7", len(events))
	}
	stored := e.eventIDs(t, week)
	for i, date := range dates {
		id := plan.EventID(week, mustDate(date))
		ev, ok := events[id]
		if !ok {
			t.Errorf("%s: no event %s", date, id)
			continue
		}
		if stored[date] != id {
			t.Errorf("%s stored event id %q, want %s", date, stored[date], id)
		}
		name := fullWeek[i]
		if ev.Summary != "Dinner: "+name || ev.Status != "confirmed" {
			t.Errorf("%s event = %q %s, want \"Dinner: %s\" confirmed", date, ev.Summary, ev.Status, name)
		}
		// Dinner time 17:30 in New York, EDT all week, for an hour.
		if want := (gcaltest.EventTime{DateTime: date + "T17:30:00-04:00", TimeZone: "America/New_York"}); ev.Start != want {
			t.Errorf("%s start = %+v, want %+v", date, ev.Start, want)
		}
		if want := (gcaltest.EventTime{DateTime: date + "T18:30:00-04:00", TimeZone: "America/New_York"}); ev.End != want {
			t.Errorf("%s end = %+v, want %+v", date, ev.End, want)
		}
	}
	// Beef Chili cooks 240 minutes in the crock pot; Pot Roast's minutes
	// are unknown, so it has no start time, and Pad Thai is not a crock-pot
	// dinner.
	for _, tc := range []struct{ date, want string }{
		{"2026-10-12", "Beef Chili\nStart by 1:30 pm\nhttps://meals.example.com/dinner/" + e.ids["Beef Chili"]},
		{"2026-10-13", "Pot Roast\nhttps://meals.example.com/dinner/" + e.ids["Pot Roast"]},
		{"2026-10-14", "Pad Thai\nhttps://meals.example.com/dinner/" + e.ids["Pad Thai"]},
	} {
		if got := events[plan.EventID(week, mustDate(tc.date))].Description; got != tc.want {
			t.Errorf("%s description = %q, want %q", tc.date, got, tc.want)
		}
	}
	got, err := e.svc.Get(t.Context(), week)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != plan.StatusSynced || got.CalendarSyncedAt == nil || *got.CalendarSyncedAt != *p.CalendarSyncedAt {
		t.Errorf("stored plan = %s synced at %v, want as pushed", got.Status, got.CalendarSyncedAt)
	}
}

func TestPushAgainUpdatesOnly(t *testing.T) {
	e := newEnv(t)
	e.connectCalendar(t)
	e.storePlan(t, week, fullWeek...)
	if _, err := e.svc.Push(t.Context(), week); err != nil {
		t.Fatal(err)
	}
	// The household deletes Monday's event in Google Calendar; Google keeps
	// its id.
	monday := plan.EventID(week, week)
	e.google.CancelEvent(dinnersCal.ID, monday)
	e.google.ResetEventWrites()

	p, err := e.svc.Push(t.Context(), week)
	if err != nil {
		t.Fatalf("second Push: %v", err)
	}
	if inserts, updates := e.google.EventWrites(); inserts != 0 || updates != 7 {
		t.Errorf("second push: %d inserts and %d updates, want 0 and 7", inserts, updates)
	}
	events := e.google.Events(dinnersCal.ID)
	if len(events) != 7 {
		t.Errorf("calendar has %d events, want 7", len(events))
	}
	if events[monday].Status != "confirmed" {
		t.Errorf("Monday's event is %s, want it put back", events[monday].Status)
	}
	if p.Status != plan.StatusSynced {
		t.Errorf("status = %s, want synced", p.Status)
	}
}

func TestSwapThenPushUpdatesThatDay(t *testing.T) {
	e := newEnv(t)
	e.connectCalendar(t)
	e.storePlan(t, week, fullWeek...)
	if _, err := e.svc.Push(t.Context(), week); err != nil {
		t.Fatal(err)
	}
	before := e.google.Events(dinnersCal.ID)
	e.google.ResetEventWrites()

	fishTacos := e.dinner(t, "Fish Tacos", "regular", "stovetop", 25)
	swapped, err := e.svc.Swap(t.Context(), week, "2026-10-14", fishTacos)
	if err != nil {
		t.Fatal(err)
	}
	if swapped.Status != plan.StatusChanged {
		t.Fatalf("status after swap = %s, want changed", swapped.Status)
	}
	if id := e.eventIDs(t, week)["2026-10-14"]; id != "" {
		t.Errorf("swapped day kept event id %q", id)
	}

	p, err := e.svc.Push(t.Context(), week)
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	if inserts, updates := e.google.EventWrites(); inserts != 0 || updates != 1 {
		t.Errorf("push after swap: %d inserts and %d updates, want 0 and 1", inserts, updates)
	}
	wednesday := plan.EventID(week, mustDate("2026-10-14"))
	after := e.google.Events(dinnersCal.ID)
	if got := after[wednesday]; got.Summary != "Dinner: Fish Tacos" ||
		got.Description != "Fish Tacos\nhttps://meals.example.com/dinner/"+fishTacos {
		t.Errorf("Wednesday's event = %+v, want Fish Tacos", got)
	}
	delete(before, wednesday)
	delete(after, wednesday)
	if !maps.Equal(before, after) {
		t.Errorf("events of other days changed:\nbefore %+v\nafter  %+v", before, after)
	}
	if p.Status != plan.StatusSynced {
		t.Errorf("status = %s, want synced", p.Status)
	}
	if id := e.eventIDs(t, week)["2026-10-14"]; id != wednesday {
		t.Errorf("Wednesday stored event id %q, want %s", id, wednesday)
	}
}

func TestPushAcrossDaylightSavingTime(t *testing.T) {
	for _, tc := range []struct {
		name string
		week store.LocalDate
		// utc is each day's 17:30 New York dinner as a UTC instant.
		utc []string
	}{
		// New York leaves daylight saving time at 02:00 on Sunday
		// 2026-11-01.
		{"fall back", mustDate("2026-10-26"), []string{
			"2026-10-26T21:30:00Z", "2026-10-27T21:30:00Z", "2026-10-28T21:30:00Z", "2026-10-29T21:30:00Z",
			"2026-10-30T21:30:00Z", "2026-10-31T21:30:00Z", "2026-11-01T22:30:00Z",
		}},
		// It enters it at 02:00 on Sunday 2027-03-14.
		{"spring forward", mustDate("2027-03-08"), []string{
			"2027-03-08T22:30:00Z", "2027-03-09T22:30:00Z", "2027-03-10T22:30:00Z", "2027-03-11T22:30:00Z",
			"2027-03-12T22:30:00Z", "2027-03-13T22:30:00Z", "2027-03-14T21:30:00Z",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.connectCalendar(t)
			dates := e.storePlan(t, tc.week, fullWeek...)
			if _, err := e.svc.Push(t.Context(), tc.week); err != nil {
				t.Fatalf("Push: %v", err)
			}
			events := e.google.Events(dinnersCal.ID)
			for i, date := range dates {
				ev := events[plan.EventID(tc.week, mustDate(date))]
				start, err := time.Parse(time.RFC3339, ev.Start.DateTime)
				if err != nil {
					t.Fatalf("%s start %q: %v", date, ev.Start.DateTime, err)
				}
				end, err := time.Parse(time.RFC3339, ev.End.DateTime)
				if err != nil {
					t.Fatalf("%s end %q: %v", date, ev.End.DateTime, err)
				}
				if got := start.UTC().Format(time.RFC3339); got != tc.utc[i] {
					t.Errorf("%s starts %s (%s), want %s", date, got, ev.Start.DateTime, tc.utc[i])
				}
				if !strings.HasPrefix(ev.Start.DateTime, date+"T17:30:00") {
					t.Errorf("%s starts %s, want 17:30 local", date, ev.Start.DateTime)
				}
				if end.Sub(start) != time.Hour {
					t.Errorf("%s lasts %v, want 1h", date, end.Sub(start))
				}
				if ev.Start.TimeZone != "America/New_York" || ev.End.TimeZone != "America/New_York" {
					t.Errorf("%s zones = %s, %s", date, ev.Start.TimeZone, ev.End.TimeZone)
				}
			}
		})
	}
}

func TestPushNeedsAChosenCalendar(t *testing.T) {
	e := newEnv(t)
	e.storePlan(t, week, fullWeek...)
	if _, err := e.svc.Push(t.Context(), week); !errors.Is(err, gcal.ErrNotConnected) {
		t.Errorf("Push with nothing connected = %v, want ErrNotConnected", err)
	}
	e.google.SetCalendars(0, dinnersCal)
	e.google.Connect(t, e.gcal)
	if _, err := e.svc.Push(t.Context(), week); !errors.Is(err, gcal.ErrNoCalendar) {
		t.Errorf("Push with no calendar chosen = %v, want ErrNoCalendar", err)
	}
	if inserts, updates := e.google.EventWrites(); inserts+updates != 0 {
		t.Errorf("Google saw %d event writes", inserts+updates)
	}
	e.assertUnsynced(t, week, plan.StatusDraft)
}

func TestPushWithoutAPlan(t *testing.T) {
	e := newEnv(t)
	e.connectCalendar(t)
	before := e.google.TokenCalls()
	if _, err := e.svc.Push(t.Context(), week); !errors.Is(err, plan.ErrNotFound) {
		t.Errorf("Push = %v, want ErrNotFound", err)
	}
	if n := e.google.TokenCalls() - before; n != 0 {
		t.Errorf("token endpoint called %d times for a week with no plan", n)
	}
}

func TestPushPartialFailureLeavesPlanUnsynced(t *testing.T) {
	e := newEnv(t)
	e.connectCalendar(t)
	e.storePlan(t, week, fullWeek...)
	// Google fails the fourth event.
	e.google.FailEventWrite(4, http.StatusServiceUnavailable)

	_, err := e.svc.Push(t.Context(), week)
	var calendar *gcal.CalendarError
	if !errors.As(err, &calendar) {
		t.Fatalf("Push = %v, want *gcal.CalendarError", err)
	}
	assertNoSecret(t, "error", err.Error())
	if n := len(e.google.Events(dinnersCal.ID)); n != 3 {
		t.Fatalf("calendar has %d events, want the 3 before the failure", n)
	}
	e.assertUnsynced(t, week, plan.StatusDraft)

	// Pushing again writes every day: the three already there are updated,
	// not added twice.
	e.google.ResetEventWrites()
	p, err := e.svc.Push(t.Context(), week)
	if err != nil {
		t.Fatalf("retried Push: %v", err)
	}
	if inserts, updates := e.google.EventWrites(); inserts != 4 || updates != 3 {
		t.Errorf("retry: %d inserts and %d updates, want 4 and 3", inserts, updates)
	}
	if n := len(e.google.Events(dinnersCal.ID)); n != 7 || p.Status != plan.StatusSynced {
		t.Errorf("after retry: %d events, plan %s; want 7, synced", n, p.Status)
	}
}

func TestPushPartialFailureKeepsAChangedPlanChanged(t *testing.T) {
	e := newEnv(t)
	e.connectCalendar(t)
	e.storePlan(t, week, fullWeek...)
	first, err := e.svc.Push(t.Context(), week)
	if err != nil {
		t.Fatal(err)
	}
	for date, name := range map[string]string{"2026-10-12": "Fish Tacos", "2026-10-13": "Ramen"} {
		if _, err := e.svc.Swap(t.Context(), week, date, e.dinner(t, name, "regular", "stovetop", 25)); err != nil {
			t.Fatal(err)
		}
	}
	e.google.FailEventWrite(2, http.StatusInternalServerError)
	if _, err := e.svc.Push(t.Context(), week); err == nil {
		t.Fatal("Push succeeded, want the second event to fail")
	}
	p, err := e.svc.Get(t.Context(), week)
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != plan.StatusChanged || *p.CalendarSyncedAt != *first.CalendarSyncedAt {
		t.Errorf("plan = %s synced at %v, want changed, synced at %s", p.Status, *p.CalendarSyncedAt, *first.CalendarSyncedAt)
	}
	ids := e.eventIDs(t, week)
	if ids["2026-10-12"] != "" || ids["2026-10-13"] != "" {
		t.Errorf("swapped days stored event ids %v after a failed push", ids)
	}
}

func TestPushToStaleCalendar(t *testing.T) {
	for name, edit := range map[string]func(*gcaltest.Google){
		"deleted": func(g *gcaltest.Google) { g.SetCalendars(0) },
		"read-only": func(g *gcaltest.Google) {
			g.SetCalendars(0, gcaltest.Calendar{ID: dinnersCal.ID, Summary: "Dinners", AccessRole: "reader"})
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			e.connectCalendar(t)
			e.storePlan(t, week, fullWeek...)
			edit(e.google)
			if _, err := e.svc.Push(t.Context(), week); !errors.Is(err, gcal.ErrCalendarUnavailable) {
				t.Errorf("Push = %v, want ErrCalendarUnavailable", err)
			}
			e.assertUnsynced(t, week, plan.StatusDraft)
		})
	}
}

func TestPushGrantNoLongerAccepted(t *testing.T) {
	e := newEnv(t)
	e.connectCalendar(t)
	e.storePlan(t, week, fullWeek...)
	e.google.RevokeGrant()
	_, err := e.svc.Push(t.Context(), week)
	if !errors.Is(err, gcal.ErrReconnect) {
		t.Fatalf("Push = %v, want ErrReconnect", err)
	}
	assertNoSecret(t, "error", err.Error())
	e.assertUnsynced(t, week, plan.StatusDraft)
	// The connection is left as it is: forgetting it is not the push's to
	// decide.
	if _, err := db.New(e.st.Reader).GetIntegrationsGoogle(t.Context()); err != nil {
		t.Errorf("connection after a refused grant: %v", err)
	}
}

func TestPushFailsWhenPlanSwappedWhilePushing(t *testing.T) {
	e := newEnv(t)
	e.connectCalendar(t)
	e.storePlan(t, week, fullWeek...)
	arrived, release := e.google.HoldEvents()
	defer release()
	pushed := make(chan error, 1)
	go func() {
		_, err := e.svc.Push(t.Context(), week)
		pushed <- err
	}()
	<-arrived
	if _, err := e.svc.Swap(t.Context(), week, "2026-10-14", e.dinner(t, "Fish Tacos", "regular", "stovetop", 25)); err != nil {
		t.Fatal(err)
	}
	release()
	if err := <-pushed; !errors.Is(err, plan.ErrPlanChanged) {
		t.Fatalf("Push = %v, want ErrPlanChanged", err)
	}
	e.assertUnsynced(t, week, plan.StatusDraft)
}

// kitchenCal is the calendar the tests choose after pushing to dinnersCal.
var kitchenCal = gcaltest.Calendar{ID: "kitchen@group.calendar.google.com", Summary: "Kitchen", AccessRole: "owner"}

// assertWeekOn fails unless calendar shows each of names, Monday first, on
// its day of the week starting weekStart, and each day stores that event's
// id.
func (e *env) assertWeekOn(t *testing.T, calendar string, weekStart store.LocalDate, dates []string, names ...string) {
	t.Helper()
	events := e.google.Events(calendar)
	stored := e.eventIDs(t, weekStart)
	for i, date := range dates {
		id := plan.EventID(weekStart, mustDate(date))
		if ev := events[id]; ev.Summary != "Dinner: "+names[i] || ev.Status != "confirmed" {
			t.Errorf("%s on %s = %q %s, want \"Dinner: %s\" confirmed", date, calendar, ev.Summary, ev.Status, names[i])
		}
		if stored[date] != id {
			t.Errorf("%s stored event id %q, want %s", date, stored[date], id)
		}
	}
}

func TestPushAfterChoosingAnotherCalendarWritesTheWholeWeek(t *testing.T) {
	for _, swap := range []bool{false, true} {
		name := "push again"
		if swap {
			name = "swap then push"
		}
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			e.connectCalendar(t)
			dates := e.storePlan(t, week, fullWeek...)
			first, err := e.svc.Push(t.Context(), week)
			if err != nil {
				t.Fatal(err)
			}
			onDinners := e.google.Events(dinnersCal.ID)

			// Choosing the same calendar again keeps the plan synced.
			e.google.SetCalendars(0, dinnersCal, kitchenCal)
			if _, err := e.gcal.SelectCalendar(t.Context(), dinnersCal.ID); err != nil {
				t.Fatal(err)
			}
			if p, err := e.svc.Get(t.Context(), week); err != nil || p.Status != plan.StatusSynced {
				t.Fatalf("plan after choosing the same calendar = %s, %v; want synced", p.Status, err)
			}

			if _, err := e.gcal.SelectCalendar(t.Context(), kitchenCal.ID); err != nil {
				t.Fatal(err)
			}
			// The plan's events are on a calendar no longer chosen.
			p, err := e.svc.Get(t.Context(), week)
			if err != nil {
				t.Fatal(err)
			}
			if p.Status != plan.StatusChanged || *p.CalendarSyncedAt != *first.CalendarSyncedAt {
				t.Errorf("plan after choosing another calendar = %s synced at %v, want changed", p.Status, *p.CalendarSyncedAt)
			}
			for date, id := range e.eventIDs(t, week) {
				if id != "" {
					t.Errorf("%s kept event id %q of the calendar no longer chosen", date, id)
				}
			}

			names := slices.Clone(fullWeek)
			if swap {
				fishTacos := e.dinner(t, "Fish Tacos", "regular", "stovetop", 25)
				if _, err := e.svc.Swap(t.Context(), week, "2026-10-14", fishTacos); err != nil {
					t.Fatal(err)
				}
				names[2] = "Fish Tacos"
			}

			// A push that fails part way leaves the plan changed.
			e.google.ResetEventWrites()
			e.google.FailEventWrite(4, http.StatusServiceUnavailable)
			if _, err := e.svc.Push(t.Context(), week); err == nil {
				t.Fatal("Push succeeded, want the fourth event to fail")
			}
			if p, err := e.svc.Get(t.Context(), week); err != nil || p.Status != plan.StatusChanged {
				t.Errorf("plan after a failed push = %s, %v; want changed", p.Status, err)
			}

			p, err = e.svc.Push(t.Context(), week)
			if err != nil {
				t.Fatalf("Push: %v", err)
			}
			if p.Status != plan.StatusSynced {
				t.Errorf("status = %s, want synced", p.Status)
			}
			if n := len(e.google.Events(kitchenCal.ID)); n != 7 {
				t.Errorf("%s has %d events, want 7", kitchenCal.ID, n)
			}
			e.assertWeekOn(t, kitchenCal.ID, week, dates, names...)
			if after := e.google.Events(dinnersCal.ID); !maps.Equal(onDinners, after) {
				t.Errorf("%s changed:\nbefore %+v\nafter  %+v", dinnersCal.ID, onDinners, after)
			}
		})
	}
}

// TestChoosingAnotherCalendarWaitsForAPush chooses another calendar while a
// push is at Google: the choice waits until the push is recorded, then
// forgets its events, so the next push writes the whole week to the
// calendar now chosen.
func TestChoosingAnotherCalendarWaitsForAPush(t *testing.T) {
	e := newEnv(t)
	e.connectCalendar(t)
	dates := e.storePlan(t, week, fullWeek...)
	e.google.SetCalendars(0, dinnersCal, kitchenCal)
	arrived, release := e.google.HoldEvents()
	defer release()
	pushed := make(chan error, 1)
	go func() {
		_, err := e.svc.Push(t.Context(), week)
		pushed <- err
	}()
	<-arrived
	selected := make(chan error, 1)
	go func() {
		_, err := e.gcal.SelectCalendar(t.Context(), kitchenCal.ID)
		selected <- err
	}()
	select {
	case err := <-selected:
		t.Fatalf("SelectCalendar finished (%v) while a push was at Google", err)
	case <-time.After(200 * time.Millisecond):
	}
	release()
	if err := <-pushed; err != nil {
		t.Fatalf("Push: %v", err)
	}
	if err := <-selected; err != nil {
		t.Fatalf("SelectCalendar: %v", err)
	}
	p, err := e.svc.Get(t.Context(), week)
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != plan.StatusChanged {
		t.Errorf("plan = %s, want changed: its events are on %s", p.Status, dinnersCal.ID)
	}
	for date, id := range e.eventIDs(t, week) {
		if id != "" {
			t.Errorf("%s kept event id %q of the calendar no longer chosen", date, id)
		}
	}

	if p, err = e.svc.Push(t.Context(), week); err != nil || p.Status != plan.StatusSynced {
		t.Fatalf("Push = %s, %v; want synced", p.Status, err)
	}
	e.assertWeekOn(t, kitchenCal.ID, week, dates, fullWeek...)
}

// pushedWeeks are the weeks the disconnect tests push: the calendar and the
// connection are the household's, so a disconnect concerns every one.
var pushedWeeks = []store.LocalDate{week, mustDate("2026-10-19"), mustDate("2026-10-26")}

// assertForgotten fails unless the plan of weekStart is changed and no day
// keeps an event id.
func (e *env) assertForgotten(t *testing.T, weekStart store.LocalDate, why string) {
	t.Helper()
	p, err := e.svc.Get(t.Context(), weekStart)
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != plan.StatusChanged {
		t.Errorf("plan of %s %s = %s, want changed", weekStart, why, p.Status)
	}
	for date, id := range e.eventIDs(t, weekStart) {
		if id != "" {
			t.Errorf("%s kept event id %q %s", date, id, why)
		}
	}
}

func TestDisconnectForgetsEveryPushedWeek(t *testing.T) {
	e := newEnv(t)
	e.connectCalendar(t)
	dates := map[store.LocalDate][]string{}
	for _, w := range pushedWeeks {
		dates[w] = e.storePlan(t, w, fullWeek...)
		if _, err := e.svc.Push(t.Context(), w); err != nil {
			t.Fatalf("Push %s: %v", w, err)
		}
	}
	draftWeek := mustDate("2026-11-02")
	e.storePlan(t, draftWeek, fullWeek...)

	if err := e.gcal.Disconnect(t.Context()); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	for _, w := range pushedWeeks {
		e.assertForgotten(t, w, "after disconnecting")
	}
	e.assertUnsynced(t, draftWeek, plan.StatusDraft)

	// Connecting again chooses no calendar, so nothing is pushed until one
	// is chosen; then a push writes the whole week to it.
	e.google.Connect(t, e.gcal)
	if _, err := e.svc.Push(t.Context(), week); !errors.Is(err, gcal.ErrNoCalendar) {
		t.Fatalf("Push after connecting again = %v, want ErrNoCalendar", err)
	}
	e.google.SetCalendars(0, dinnersCal, kitchenCal)
	if _, err := e.gcal.SelectCalendar(t.Context(), kitchenCal.ID); err != nil {
		t.Fatal(err)
	}
	for _, w := range pushedWeeks {
		e.assertForgotten(t, w, "after connecting again")
		p, err := e.svc.Push(t.Context(), w)
		if err != nil || p.Status != plan.StatusSynced {
			t.Fatalf("Push %s = %s, %v; want synced", w, p.Status, err)
		}
		e.assertWeekOn(t, kitchenCal.ID, w, dates[w], fullWeek...)
	}
}

// TestDisconnectRefusedKeepsPushedWeeks: a revoke Google refuses keeps the
// connection and its calendar, so every pushed week stays synced to it.
func TestDisconnectRefusedKeepsPushedWeeks(t *testing.T) {
	e := newEnv(t)
	e.connectCalendar(t)
	dates := map[store.LocalDate][]string{}
	for _, w := range pushedWeeks {
		dates[w] = e.storePlan(t, w, fullWeek...)
		if _, err := e.svc.Push(t.Context(), w); err != nil {
			t.Fatalf("Push %s: %v", w, err)
		}
	}
	e.google.AnswerRevoke(http.StatusServiceUnavailable, "")
	var revokeErr *gcal.RevokeError
	if err := e.gcal.Disconnect(t.Context()); !errors.As(err, &revokeErr) {
		t.Fatalf("Disconnect = %v, want a *RevokeError", err)
	}
	for _, w := range pushedWeeks {
		if p, err := e.svc.Get(t.Context(), w); err != nil || p.Status != plan.StatusSynced {
			t.Errorf("plan of %s after a refused disconnect = %s, %v; want synced", w, p.Status, err)
		}
		e.assertWeekOn(t, dinnersCal.ID, w, dates[w], fullWeek...)
	}
}

// TestDisconnectWaitsForAPush disconnects while a push is at Google: the
// disconnect waits until the push is recorded, then forgets its events.
func TestDisconnectWaitsForAPush(t *testing.T) {
	e := newEnv(t)
	e.connectCalendar(t)
	e.storePlan(t, week, fullWeek...)
	arrived, release := e.google.HoldEvents()
	defer release()
	pushed := make(chan error, 1)
	go func() {
		_, err := e.svc.Push(t.Context(), week)
		pushed <- err
	}()
	<-arrived
	disconnected := make(chan error, 1)
	go func() {
		disconnected <- e.gcal.Disconnect(t.Context())
	}()
	select {
	case err := <-disconnected:
		t.Fatalf("Disconnect finished (%v) while a push was at Google", err)
	case <-time.After(200 * time.Millisecond):
	}
	release()
	if err := <-pushed; err != nil {
		t.Fatalf("Push: %v", err)
	}
	if err := <-disconnected; err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	e.assertForgotten(t, week, "after disconnecting")
}
