package plan_test

import (
	"database/sql"
	"errors"
	"strings"
	"testing"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/notify"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/notify/notifytest"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/plan"
)

// saturday is the Saturday of week, Spaghetti Bolognese in fullWeek.
const saturday = "2026-10-17"

// TestSetManualRecordsLabelWithoutDinner: a free-text night is stored on
// that plan day only, trimmed, with no dinner and no new dinners row.
func TestSetManualRecordsLabelWithoutDinner(t *testing.T) {
	e := newEnv(t)
	e.storePlan(t, week, fullWeek...)
	before := e.countDinners(t)

	p, err := e.svc.SetManual(t.Context(), week, saturday, "  mummy dogs  ")
	if err != nil {
		t.Fatal(err)
	}
	day := dayOn(t, p, saturday)
	if day.Dinner != nil || day.ManualLabel == nil || *day.ManualLabel != "mummy dogs" || day.IsNew || day.StartBy != nil {
		t.Fatalf("saturday = %+v, want the label mummy dogs and no dinner", day)
	}
	if monday := dayOn(t, p, weekDates[0]); monday.Dinner == nil || monday.Dinner.Name != "Beef Chili" || monday.ManualLabel != nil {
		t.Fatalf("monday = %+v, want Beef Chili unchanged", monday)
	}
	if got := e.countDinners(t); got != before {
		t.Fatalf("dinners = %d, want %d", got, before)
	}
	var named int
	if err := e.st.Reader.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM dinners WHERE name = 'mummy dogs'`).Scan(&named); err != nil {
		t.Fatal(err)
	}
	if named != 0 {
		t.Fatalf("dinners named mummy dogs = %d, want 0", named)
	}
	var dinnerID sql.NullString
	var label sql.NullString
	if err := e.st.Reader.QueryRowContext(t.Context(), `SELECT dinner_id, manual_label FROM plan_days WHERE date = ?`, saturday).Scan(&dinnerID, &label); err != nil {
		t.Fatal(err)
	}
	if dinnerID.Valid || !label.Valid || label.String != "mummy dogs" {
		t.Fatalf("stored saturday dinner_id=%v label=%v, want no dinner and mummy dogs", dinnerID, label)
	}
}

// TestSetManualRejectsALabelThatIsNotAName: a blank, too-long or filler
// label is refused and the day stays a dinner.
func TestSetManualRejectsALabelThatIsNotAName(t *testing.T) {
	e := newEnv(t)
	e.storePlan(t, week, fullWeek...)
	for _, label := range []string{"", "   ", strings.Repeat("a", 121), "!!!"} {
		_, err := e.svc.SetManual(t.Context(), week, saturday, label)
		var invalid *plan.InvalidManualLabelError
		if !errors.As(err, &invalid) {
			t.Errorf("SetManual(%q) = %v, want InvalidManualLabelError", label, err)
		}
	}
	p, err := e.svc.Get(t.Context(), week)
	if err != nil {
		t.Fatal(err)
	}
	day := dayOn(t, p, saturday)
	if day.Dinner == nil || day.Dinner.Name != "Spaghetti Bolognese" || day.ManualLabel != nil {
		t.Fatalf("saturday after refused labels = %+v, want Spaghetti Bolognese", day)
	}
}

// TestManualNightCanBeReplacedAndCleared: a manual night is swapped back
// to a dinner, or cleared, and either edit leaves no label.
func TestManualNightCanBeReplacedAndCleared(t *testing.T) {
	e := newEnv(t)
	e.storePlan(t, week, fullWeek...)
	if _, err := e.svc.SetManual(t.Context(), week, saturday, "mummy dogs"); err != nil {
		t.Fatal(err)
	}
	p, err := e.svc.Swap(t.Context(), week, saturday, e.ids["Spaghetti Bolognese"])
	if err != nil {
		t.Fatal(err)
	}
	day := dayOn(t, p, saturday)
	if day.Dinner == nil || day.Dinner.Name != "Spaghetti Bolognese" || day.ManualLabel != nil || day.IsNew {
		t.Fatalf("saturday after swap = %+v, want Spaghetti Bolognese", day)
	}
	p, err = e.svc.SetManual(t.Context(), week, saturday, "leftovers")
	if err != nil {
		t.Fatal(err)
	}
	p, err = e.svc.ClearDay(t.Context(), week, saturday)
	if err != nil {
		t.Fatal(err)
	}
	day = dayOn(t, p, saturday)
	if day.Dinner != nil || day.ManualLabel != nil || day.IsNew || day.StartBy != nil {
		t.Fatalf("saturday after clear = %+v, want nothing planned", day)
	}
}

// TestManualNightMakesSyncedPlanChanged: setting a manual night is a plan
// edit, so a synced plan becomes changed.
func TestManualNightMakesSyncedPlanChanged(t *testing.T) {
	e := newEnv(t)
	e.storePlan(t, week, fullWeek...)
	if _, err := e.st.Writer.ExecContext(t.Context(), `UPDATE week_plans SET status = 'synced' WHERE week_start = ?`, week.String()); err != nil {
		t.Fatal(err)
	}
	p, err := e.svc.SetManual(t.Context(), week, saturday, "mummy dogs")
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != plan.StatusChanged {
		t.Fatalf("status = %q, want changed", p.Status)
	}
}

// TestManualNightOpensNoRotationReview: MarkTried ignores a manual night
// and still opens a review for a Quest-log dinner on another day.
func TestManualNightOpensNoRotationReview(t *testing.T) {
	e := newEnv(t)
	e.storePlan(t, week, fullWeek...)
	if _, err := e.svc.SetManual(t.Context(), week, saturday, "mummy dogs"); err != nil {
		t.Fatal(err)
	}
	n, err := e.svc.MarkTried(t.Context(), mustDate(saturday))
	if err != nil || n != 0 {
		t.Fatalf("MarkTried(saturday) = %d, %v, want 0", n, err)
	}
	reviews, err := e.svc.PendingReviews(t.Context())
	if err != nil || len(reviews) != 0 {
		t.Fatalf("reviews after the manual night = %+v, %v, want none", reviews, err)
	}
	n, err = e.svc.MarkTried(t.Context(), padThaiDay)
	if err != nil || n != 1 {
		t.Fatalf("MarkTried(Pad Thai) = %d, %v, want 1", n, err)
	}
	reviews, err = e.svc.PendingReviews(t.Context())
	if err != nil || len(reviews) != 1 || reviews[0].Dinner.ID != e.ids["Pad Thai"] || reviews[0].PlanDate != padThaiDay.String() {
		t.Fatalf("reviews = %+v, %v, want Pad Thai", reviews, err)
	}
}

// TestManualNightReminderOffersNoRecipe: the day-of reminder of a manual
// night names the label and opens the plan, with no dinner and no recipe.
// A cleared night sends nothing. A manual night has no crock-pot start.
func TestManualNightReminderOffersNoRecipe(t *testing.T) {
	e := newEnv(t)
	e.storePlan(t, week, fullWeek...)
	if _, err := e.svc.SetManual(t.Context(), week, saturday, "mummy dogs"); err != nil {
		t.Fatal(err)
	}
	ok, err := e.svc.EmitDayOfReminder(t.Context(), mustDate(saturday))
	if err != nil || !ok {
		t.Fatalf("EmitDayOfReminder = %v, %v, want sent", ok, err)
	}
	if ok, err := e.svc.EmitStartReminder(t.Context(), mustDate(saturday)); err != nil || ok {
		t.Fatalf("EmitStartReminder = %v, %v, want nothing", ok, err)
	}
	rows := notifytest.Rows(t, e.st, notify.DayOfReminder)
	if len(rows) != 1 {
		t.Fatalf("reminders = %+v, want one", rows)
	}
	r := rows[0]
	if r.Payload.Title != "Tonight: mummy dogs" || r.Payload.Body != "" || r.Payload.URL != "/plan?week=2026-10-12" ||
		r.Payload.Dinner != nil || strings.Contains(r.Payload.Title, "Recipe") {
		t.Fatalf("reminder = %+v, want the label with no recipe and no dinner", r.Payload)
	}
	if _, err := e.svc.ClearDay(t.Context(), week, saturday); err != nil {
		t.Fatal(err)
	}
	ok, err = e.svc.EmitDayOfReminder(t.Context(), mustDate(saturday))
	if ok || !errors.Is(err, plan.ErrNoNight) {
		t.Fatalf("EmitDayOfReminder after clear = %v, %v, want ErrNoNight", ok, err)
	}
	if again := notifytest.Rows(t, e.st, notify.DayOfReminder); len(again) != 1 {
		t.Fatalf("reminders after clear = %+v, want the one already sent", again)
	}
}

// TestPushManualNightIsNotADinnerEvent: Push to Calendar writes a manual
// night as the label alone, with no dinner link, and deletes that event
// when the night is cleared. The other nights stay dinner events.
func TestPushManualNightIsNotADinnerEvent(t *testing.T) {
	e := newEnv(t)
	e.connectCalendar(t)
	e.storePlan(t, week, fullWeek...)
	if _, err := e.svc.SetManual(t.Context(), week, saturday, "mummy dogs"); err != nil {
		t.Fatal(err)
	}
	p, _, err := e.svc.Push(t.Context(), week)
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != plan.StatusSynced {
		t.Fatalf("status = %q, want synced", p.Status)
	}
	events := e.google.Events(dinnersCal.ID)
	id := plan.EventID(week, mustDate(saturday))
	ev, ok := events[id]
	if !ok {
		t.Fatalf("calendar has no event for saturday")
	}
	if ev.Summary != "mummy dogs" || ev.Description != "mummy dogs" || ev.Status != "confirmed" || strings.Contains(ev.Description, "http") || strings.Contains(ev.Summary, "Dinner:") {
		t.Fatalf("saturday event = %+v, want the label alone", ev)
	}
	monday := plan.EventID(week, mustDate(weekDates[0]))
	if events[monday].Summary != "Dinner: Beef Chili" || !strings.Contains(events[monday].Description, "https://meals.example.com/dinner/") {
		t.Fatalf("monday event = %+v, want a dinner event", events[monday])
	}
	if _, err := e.svc.ClearDay(t.Context(), week, saturday); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.svc.Push(t.Context(), week); err != nil {
		t.Fatal(err)
	}
	if _, still := e.google.Events(dinnersCal.ID)[id]; still {
		t.Fatal("cleared saturday is still on the calendar")
	}
	if _, still := e.google.Events(dinnersCal.ID)[monday]; !still {
		t.Fatal("monday's dinner event was removed with the cleared night")
	}
}

// TestPushClearedNightThatWasNeverWritten: a night cleared before any push
// is not created on the calendar, and the push of the other nights still
// succeeds.
func TestPushClearedNightThatWasNeverWritten(t *testing.T) {
	e := newEnv(t)
	e.connectCalendar(t)
	e.storePlan(t, week, fullWeek...)
	if _, err := e.svc.ClearDay(t.Context(), week, saturday); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.svc.Push(t.Context(), week); err != nil {
		t.Fatal(err)
	}
	events := e.google.Events(dinnersCal.ID)
	if _, ok := events[plan.EventID(week, mustDate(saturday))]; ok {
		t.Fatal("a cleared night was created on the calendar")
	}
	if events[plan.EventID(week, mustDate(weekDates[0]))].Summary != "Dinner: Beef Chili" {
		t.Fatalf("monday event = %+v, want Beef Chili", events[plan.EventID(week, mustDate(weekDates[0]))])
	}
	if len(events) != 6 {
		t.Fatalf("calendar has %d events, want the six dinners", len(events))
	}
}

func dayOn(t *testing.T, p plan.Plan, date string) plan.Day {
	t.Helper()
	for _, d := range p.Days {
		if d.Date == date {
			return d
		}
	}
	t.Fatalf("plan has no %s", date)
	return plan.Day{}
}

func (e *env) countDinners(t *testing.T) int {
	t.Helper()
	var n int
	if err := e.st.Reader.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM dinners`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
