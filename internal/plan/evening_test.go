package plan_test

import (
	"database/sql"
	"errors"
	"strings"
	"testing"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/dinners"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/notify"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/notify/notifytest"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/plan"
)

// TestSetDeliveryAndDiningOutEvenings: each non-cook evening is stored on
// that plan day only, with no dinner, no manual label and no new dinners
// row. A source that is not delivery or dining out is refused and the
// dinner stays.
func TestSetDeliveryAndDiningOutEvenings(t *testing.T) {
	e := newEnv(t)
	e.storePlan(t, week, fullWeek...)
	before := e.countDinners(t)

	for _, source := range []string{dinners.MealSourceDelivery, dinners.MealSourceDiningOut} {
		p, err := e.svc.SetEvening(t.Context(), week, saturday, source)
		if err != nil {
			t.Fatalf("SetEvening(%s) = %v", source, err)
		}
		day := dayOn(t, p, saturday)
		if day.Dinner != nil || day.ManualLabel != nil || day.MealSource != source || day.IsNew || day.StartBy != nil {
			t.Fatalf("saturday %s = %+v, want the evening and no dinner", source, day)
		}
		if monday := dayOn(t, p, weekDates[0]); monday.Dinner == nil || monday.Dinner.Name != "Beef Chili" || monday.MealSource != dinners.MealSourceCook || monday.ManualLabel != nil {
			t.Fatalf("monday after %s = %+v, want Beef Chili unchanged", source, monday)
		}
		var dinnerID, label sql.NullString
		var stored string
		if err := e.st.Reader.QueryRowContext(t.Context(), `SELECT dinner_id, manual_label, meal_source FROM plan_days WHERE date = ?`, saturday).Scan(&dinnerID, &label, &stored); err != nil {
			t.Fatal(err)
		}
		if dinnerID.Valid || label.Valid || stored != source {
			t.Fatalf("stored saturday dinner=%v label=%v source=%q, want no dinner, no label, %s", dinnerID, label, stored, source)
		}
	}
	if got := e.countDinners(t); got != before {
		t.Fatalf("dinners = %d, want %d", got, before)
	}

	for _, source := range []string{"", "cook", "takeout", " delivery"} {
		_, err := e.svc.SetEvening(t.Context(), week, saturday, source)
		var invalid *plan.InvalidEveningError
		if !errors.As(err, &invalid) {
			t.Errorf("SetEvening(%q) = %v, want InvalidEveningError", source, err)
		}
	}
	p, err := e.svc.Get(t.Context(), week)
	if err != nil {
		t.Fatal(err)
	}
	day := dayOn(t, p, saturday)
	if day.Dinner != nil || day.ManualLabel != nil || day.MealSource != dinners.MealSourceDiningOut {
		t.Fatalf("saturday after refused sources = %+v, want dining out", day)
	}
}

// TestClearDeliveryAndDiningOutEvenings: clearing a non-cook evening leaves
// no dinner, no label and cook, the same as a cleared night.
func TestClearDeliveryAndDiningOutEvenings(t *testing.T) {
	e := newEnv(t)
	e.storePlan(t, week, fullWeek...)
	for _, source := range []string{dinners.MealSourceDelivery, dinners.MealSourceDiningOut} {
		if _, err := e.svc.SetEvening(t.Context(), week, saturday, source); err != nil {
			t.Fatal(err)
		}
		p, err := e.svc.ClearDay(t.Context(), week, saturday)
		if err != nil {
			t.Fatal(err)
		}
		day := dayOn(t, p, saturday)
		if day.Dinner != nil || day.ManualLabel != nil || day.MealSource != dinners.MealSourceCook || day.IsNew || day.StartBy != nil {
			t.Fatalf("saturday after clearing %s = %+v, want nothing planned", source, day)
		}
		var stored string
		if err := e.st.Reader.QueryRowContext(t.Context(), `SELECT meal_source FROM plan_days WHERE date = ?`, saturday).Scan(&stored); err != nil {
			t.Fatal(err)
		}
		if stored != dinners.MealSourceCook {
			t.Fatalf("stored source after clearing %s = %q, want cook", source, stored)
		}
	}
}

// TestReplaceDinnerWithDeliveryEvening: a day that had a dinner becomes a
// Delivery evening, and can be a dinner again. The other nights stay.
func TestReplaceDinnerWithDeliveryEvening(t *testing.T) {
	e := newEnv(t)
	e.storePlan(t, week, fullWeek...)
	if _, err := e.st.Writer.ExecContext(t.Context(), `UPDATE week_plans SET status = 'synced' WHERE week_start = ?`, week.String()); err != nil {
		t.Fatal(err)
	}
	before, err := e.svc.Get(t.Context(), week)
	if err != nil {
		t.Fatal(err)
	}
	was := dayOn(t, before, saturday)
	if was.Dinner == nil || was.Dinner.Name != "Spaghetti Bolognese" || was.MealSource != dinners.MealSourceCook {
		t.Fatalf("saturday before = %+v, want Spaghetti Bolognese", was)
	}

	p, err := e.svc.SetEvening(t.Context(), week, saturday, dinners.MealSourceDelivery)
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != plan.StatusChanged {
		t.Fatalf("status = %q, want changed", p.Status)
	}
	day := dayOn(t, p, saturday)
	if day.Dinner != nil || day.ManualLabel != nil || day.MealSource != dinners.MealSourceDelivery || day.IsNew || day.StartBy != nil {
		t.Fatalf("saturday = %+v, want a Delivery evening", day)
	}
	if monday := dayOn(t, p, weekDates[0]); monday.Dinner == nil || monday.Dinner.Name != "Beef Chili" {
		t.Fatalf("monday = %+v, want Beef Chili", monday)
	}

	p, err = e.svc.Swap(t.Context(), week, saturday, e.ids["Spaghetti Bolognese"])
	if err != nil {
		t.Fatal(err)
	}
	day = dayOn(t, p, saturday)
	if day.Dinner == nil || day.Dinner.Name != "Spaghetti Bolognese" || day.ManualLabel != nil || day.MealSource != dinners.MealSourceCook {
		t.Fatalf("saturday after swap = %+v, want Spaghetti Bolognese", day)
	}
}

// TestDeliveryEveningReminderHasNothingToCook: a Delivery or Dining-out
// evening, including one that replaced a crock-pot dinner, sends no day-of
// reminder and no crock-pot start reminder.
func TestDeliveryEveningReminderHasNothingToCook(t *testing.T) {
	e := newEnv(t)
	e.storePlan(t, week, fullWeek...)
	monday := weekDates[0]
	for _, tc := range []struct {
		date, source string
	}{
		{saturday, dinners.MealSourceDelivery},
		{monday, dinners.MealSourceDiningOut},
	} {
		if _, err := e.svc.SetEvening(t.Context(), week, tc.date, tc.source); err != nil {
			t.Fatal(err)
		}
		ok, err := e.svc.EmitDayOfReminder(t.Context(), mustDate(tc.date))
		if ok || !errors.Is(err, plan.ErrNoNight) {
			t.Fatalf("EmitDayOfReminder(%s %s) = %v, %v, want ErrNoNight", tc.date, tc.source, ok, err)
		}
		if ok, err := e.svc.EmitStartReminder(t.Context(), mustDate(tc.date)); err != nil || ok {
			t.Fatalf("EmitStartReminder(%s %s) = %v, %v, want nothing", tc.date, tc.source, ok, err)
		}
	}
	if rows := notifytest.Rows(t, e.st, notify.DayOfReminder); len(rows) != 0 {
		t.Fatalf("reminders = %+v, want none", rows)
	}
}

// TestPushDeliveryEveningWritesNoDinnerEvent: Push to Calendar writes no
// dinner event for a Delivery or Dining-out evening. Replacing a dinner
// that was already pushed deletes that event, the same as clearing the
// night. The other nights stay dinner events.
func TestPushDeliveryEveningWritesNoDinnerEvent(t *testing.T) {
	e := newEnv(t)
	e.connectCalendar(t)
	e.storePlan(t, week, fullWeek...)
	if _, _, err := e.svc.Push(t.Context(), week); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.SetEvening(t.Context(), week, saturday, dinners.MealSourceDelivery); err != nil {
		t.Fatal(err)
	}
	sunday := weekDates[6]
	if _, err := e.svc.SetEvening(t.Context(), week, sunday, dinners.MealSourceDiningOut); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.svc.Push(t.Context(), week); err != nil {
		t.Fatal(err)
	}
	events := e.google.Events(dinnersCal.ID)
	satID := plan.EventID(week, mustDate(saturday))
	if ev, still := events[satID]; still {
		t.Fatalf("delivery saturday is still on the calendar: %+v", ev)
	}
	sunID := plan.EventID(week, mustDate(sunday))
	if ev, still := events[sunID]; still {
		t.Fatalf("dining-out sunday is still on the calendar: %+v", ev)
	}
	monday := plan.EventID(week, mustDate(weekDates[0]))
	if events[monday].Summary != "Dinner: Beef Chili" || !strings.Contains(events[monday].Description, "https://meals.example.com/dinner/") {
		t.Fatalf("monday event = %+v, want a dinner event", events[monday])
	}
	if len(events) != 5 {
		t.Fatalf("calendar has %d events, want the five dinners", len(events))
	}
}
