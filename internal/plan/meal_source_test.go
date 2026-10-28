package plan_test

import (
	"slices"
	"strings"
	"testing"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/notify"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/notify/notifytest"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/plan"
)

// setMealSource marks the dinner named name as source.
func (e *env) setMealSource(t *testing.T, name, source string) {
	t.Helper()
	res, err := e.st.Writer.ExecContext(t.Context(), `UPDATE dinners SET meal_source = ? WHERE id = ?`, source, e.ids[name])
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		t.Fatalf("no dinner %s to mark %s", name, source)
	}
}

// TestDeliveryAndDiningOutStayOutOfTheDraft: a draft never selects a
// Delivery or Dining Out dinner. Cooked dinners stay in the pool.
func TestDeliveryAndDiningOutStayOutOfTheDraft(t *testing.T) {
	e := newEnv(t)
	e.start(t)
	// Two extra cooked dinners keep seven in the pool once two are not cooked.
	e.ids["Weeknight Tacos"] = e.dinner(t, "Weeknight Tacos", "regular", "stovetop", 25)
	e.ids["Lemon Orzo"] = e.dinner(t, "Lemon Orzo", "regular", "stovetop", 20)
	e.setMealSource(t, "Weeknight Tacos", "delivery")
	e.setMealSource(t, "Lemon Orzo", "dining_out")

	cooked := []string{"Beef Chili", "Pot Roast", "Pad Thai", "Chicken Fajitas", "Shawarma Bowls", "Spaghetti Bolognese", "Sheet Pan Salmon"}
	e.reply("Cooked dinners only.", cooked...)
	p := e.mustDraft(t, false)
	for _, day := range p.Days {
		if day.Dinner.Name == "Weeknight Tacos" || day.Dinner.Name == "Lemon Orzo" {
			t.Errorf("draft planned %s", day.Dinner.Name)
		}
		if day.Dinner.Photo != nil && (day.Dinner.Name == "Weeknight Tacos" || day.Dinner.Name == "Lemon Orzo") {
			t.Errorf("%s photo = %s, want none", day.Dinner.Name, *day.Dinner.Photo)
		}
	}
	var pool []string
	for _, c := range e.input(0).Pool {
		pool = append(pool, c.Name)
	}
	if slices.Contains(pool, "Weeknight Tacos") || slices.Contains(pool, "Lemon Orzo") {
		t.Errorf("pool = %v, want no Delivery or Dining Out dinner", pool)
	}
	if !slices.Contains(pool, "Beef Chili") || !slices.Contains(pool, "Pad Thai") {
		t.Errorf("pool = %v, want the cooked Regulars and Quest log", pool)
	}
}

// TestSwapRefusesDeliveryAndDiningOut: a dinner marked Delivery or Dining
// Out cannot be planned on a day. Reversing it to cooked lets the swap.
func TestSwapRefusesDeliveryAndDiningOut(t *testing.T) {
	e := newEnv(t)
	e.start(t)
	e.ids["Fish Tacos"] = e.dinner(t, "Fish Tacos", "regular", "stovetop", 25)
	e.ids["Ramen Night"] = e.dinner(t, "Ramen Night", "regular", "stovetop", 30)
	e.reply("", fullWeek...)
	p := e.mustDraft(t, false)
	e.setMealSource(t, "Fish Tacos", "delivery")
	e.setMealSource(t, "Ramen Night", "dining_out")

	_, err := e.svc.Swap(t.Context(), week, "2026-10-12", e.ids["Fish Tacos"])
	if err == nil || !strings.Contains(err.Error(), "delivery") {
		t.Fatalf("swap delivery = %v, want a refusal that names delivery", err)
	}
	_, err = e.svc.Swap(t.Context(), week, "2026-10-12", e.ids["Ramen Night"])
	if err == nil || !strings.Contains(err.Error(), "dining out") {
		t.Fatalf("swap dining out = %v, want a refusal that names dining out", err)
	}
	if got := dayNames(p); !slices.Equal(got, fullWeek) {
		t.Errorf("plan after refused swaps = %v, want %v", got, fullWeek)
	}

	e.setMealSource(t, "Fish Tacos", "cook")
	swapped, err := e.svc.Swap(t.Context(), week, "2026-10-12", e.ids["Fish Tacos"])
	if err != nil {
		t.Fatal(err)
	}
	if swapped.Days[0].Dinner.Name != "Fish Tacos" {
		t.Errorf("Monday = %s, want Fish Tacos once it is cooked again", swapped.Days[0].Dinner.Name)
	}
}

// TestDeliveryDinnerOpensNoRotationCheck: a Quest log dinner marked
// Delivery is not tried and opens no Rotation check. A cooked one still does.
func TestDeliveryDinnerOpensNoRotationCheck(t *testing.T) {
	e := newEnv(t)
	e.putWeek(t, plan.StatusChanged)
	e.setMealSource(t, "Pad Thai", "dining_out")

	e.markTried(t, padThaiDay, 0)
	if got := e.reviews(t, "Pad Thai"); len(got) != 0 {
		t.Fatalf("Pad Thai reviews = %+v, want none", got)
	}
	if row := e.dinnerRow(t, "Pad Thai"); row.Status != "quest_log" {
		t.Errorf("Pad Thai status = %s, want quest_log", row.Status)
	}

	if ok, err := e.svc.EmitDayOfReminder(t.Context(), padThaiDay); !ok || err != nil {
		t.Fatalf("EmitDayOfReminder = %v, %v", ok, err)
	}
	rows := notifytest.Rows(t, e.st, notify.DayOfReminder)
	if len(rows) != 1 {
		t.Fatalf("reminders = %+v, want one", rows)
	}
	p := rows[0].Payload
	if strings.Contains(p.URL, "/dinner/") || strings.Contains(p.Title, "Recipe") || strings.Contains(p.Body, "removed") {
		t.Errorf("reminder = %+v, want no recipe and not the removed-dinner text", p)
	}
	if p.Body != "This dinner is dining out, so it has no recipe." {
		t.Errorf("body = %q", p.Body)
	}
	e.setMealSource(t, "Beef Chili", "delivery")
	if ok, err := e.svc.EmitStartReminder(t.Context(), mustDate("2026-10-12")); ok || err != nil {
		t.Fatalf("EmitStartReminder of a delivery crock pot = %v, %v, want no reminder", ok, err)
	}

	e.markTried(t, shawarmaDay, 1)
	if got := e.reviews(t, "Shawarma Bowls"); len(got) != 1 || got[0].status != "pending" {
		t.Fatalf("Shawarma Bowls reviews = %+v, want one pending", got)
	}
}
