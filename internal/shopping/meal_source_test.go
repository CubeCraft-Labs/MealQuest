package shopping_test

import (
	"testing"
)

// TestDeliveryDinnerAddsNoShoppingItems: a Delivery or Dining Out dinner
// on the plan contributes no group and no ingredients. A cooked dinner on
// the same plan still does. Reversing the designation puts its ingredients
// back on the next Supply run.
func TestDeliveryDinnerAddsNoShoppingItems(t *testing.T) {
	e := newEnv(t)
	e.dinner(t, "Beef Chili", ing{"ground beef", "1", "lb"}, ing{"onion", "1", ""})
	e.dinner(t, "Pizza Night", ing{"pizza dough", "1", "lb"}, ing{"mozzarella", "8", "oz"})
	e.dinner(t, "Sushi Night", ing{"sushi rice", "2", "cups"}, ing{"nori", "4", ""})
	e.dinner(t, "Pot Roast", ing{"chuck roast", "3", "lb"})
	e.dinner(t, "Chicken Fajitas", ing{"chicken breast", "500", "g"})
	e.dinner(t, "Spaghetti Bolognese", ing{"spaghetti", "1", "lb"})
	e.dinner(t, "Sheet Pan Salmon", ing{"salmon fillets", "4", ""})
	if _, err := e.st.Writer.Exec(`UPDATE dinners SET meal_source = 'delivery' WHERE id = ?`, e.ids["Pizza Night"]); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.Writer.Exec(`UPDATE dinners SET meal_source = 'dining_out' WHERE id = ?`, e.ids["Sushi Night"]); err != nil {
		t.Fatal(err)
	}
	e.plan(t, "Beef Chili", "Pizza Night", "Sushi Night", "Pot Roast", "Chicken Fajitas", "Spaghetti Bolognese", "Sheet Pan Salmon")

	g := e.generate(t)
	for _, group := range g.List.Groups {
		if group.DinnerName == "Pizza Night" || group.DinnerName == "Sushi Night" {
			t.Errorf("group %s has %d items, want no group", group.DinnerName, len(group.Items))
		}
		for _, item := range group.Items {
			if item.Name == "pizza dough" || item.Name == "mozzarella" || item.Name == "sushi rice" || item.Name == "nori" {
				t.Errorf("item %q of %s, want no Delivery or Dining Out ingredient", item.Name, group.DinnerName)
			}
		}
	}
	if len(g.List.Groups) != 5 {
		t.Errorf("%d groups, want the 5 cooked dinners", len(g.List.Groups))
	}
	for _, skipped := range g.Skipped {
		if skipped.DinnerName == "Pizza Night" || skipped.DinnerName == "Sushi Night" {
			t.Errorf("skipped %s as %s, want it left out of the Supply run", skipped.DinnerName, skipped.Reason)
		}
	}

	if _, err := e.st.Writer.Exec(`UPDATE dinners SET meal_source = 'cook' WHERE id = ?`, e.ids["Pizza Night"]); err != nil {
		t.Fatal(err)
	}
	again := e.generate(t)
	var found bool
	for _, group := range again.List.Groups {
		if group.DinnerName == "Pizza Night" {
			found = true
			if len(group.Items) != 2 {
				t.Errorf("Pizza Night items = %d, want 2 once it is cooked again", len(group.Items))
			}
		}
	}
	if !found {
		t.Error("Pizza Night has no group once it is cooked again")
	}
}
