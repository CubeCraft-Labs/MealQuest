package shopping_test

import (
	"testing"
)

// TestGenerateSkipsDeliveryEvening: a week containing a Delivery evening
// adds no Supply-run items, group or skipped dinner for that day. The
// other nights' ingredients stay.
func TestGenerateSkipsDeliveryEvening(t *testing.T) {
	e := newEnv(t)
	e.dinner(t, "Beef Chili", ing{"ground beef", "1", "lb"})
	e.dinner(t, "Pot Roast", ing{"chuck roast", "3", "lb"})
	e.dinner(t, "Chicken Fajitas", ing{"chicken breast", "500", "g"})
	e.dinner(t, "Spaghetti Bolognese", ing{"spaghetti", "1", "lb"})
	e.dinner(t, "Sheet Pan Salmon", ing{"salmon fillets", "4", ""})
	e.dinner(t, "Pad Thai", ing{"rice noodles", "8", "oz"}, ing{"bean sprouts", "2", "cups"})
	e.dinner(t, "Shawarma Bowls", ing{"chicken thighs", "2", "lb"})
	e.plan(t, "Beef Chili", "Pot Roast", "Chicken Fajitas", "Spaghetti Bolognese", "Sheet Pan Salmon", "Pad Thai", "Shawarma Bowls")
	// Saturday is Pad Thai. Replace that dinner with a Delivery evening.
	res, err := e.st.Writer.ExecContext(t.Context(), `UPDATE plan_days SET dinner_id = NULL, manual_label = NULL, meal_source = 'delivery', is_new = 0 WHERE date = ?`, "2026-10-17")
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		t.Fatalf("updated %d plan days, want 1", n)
	}

	g := e.generate(t)
	var sawGroup, sawIngredient bool
	for _, grp := range g.List.Groups {
		if grp.Date == "2026-10-17" || grp.DinnerName == "Pad Thai" {
			t.Errorf("group = %+v, want no Saturday group", grp)
		}
		if grp.DinnerName == "Chicken Fajitas" {
			sawGroup = true
			for _, item := range grp.Items {
				if item.Name == "chicken breast" {
					sawIngredient = true
				}
			}
		}
	}
	if !sawGroup {
		t.Error("Supply run has no Chicken Fajitas group, want the other night's group")
	}
	if !sawIngredient {
		t.Error("Chicken Fajitas has no chicken breast, want the other night's ingredient")
	}
	if len(g.List.Groups) != 6 {
		t.Errorf("groups = %d, want 6", len(g.List.Groups))
	}
	for _, skip := range g.Skipped {
		if skip.Date == "2026-10-17" || skip.DinnerName == "Pad Thai" {
			t.Errorf("skipped = %+v, want the Delivery evening ignored", skip)
		}
	}
}
