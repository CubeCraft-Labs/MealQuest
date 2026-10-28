package shopping_test

import (
	"database/sql"
	"io"
	"log/slog"
	"strings"
	"testing"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/llm"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/chat/chattest"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/dinners"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/household"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/jobs"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/recipes/recipestest"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
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

// TestDesignateDeliveryDropsTheCurrentSupplyRun: a Supply run generated
// while a dinner is cooked loses that dinner's group when the dinner is
// designated Delivery. GET and Send to Google Chat no longer count or
// post it. A run already in the Supply archive keeps the group it had.
func TestDesignateDeliveryDropsTheCurrentSupplyRun(t *testing.T) {
	e := newEnv(t)
	if err := household.SeedSettings(t.Context(), e.st, "America/New_York"); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	grounder := recipestest.NewGrounder(t, e.st, t.TempDir(), &recipestest.Search{}, &recipestest.Pages{}, &recipestest.Images{}, &llm.Fake{})
	svc, err := dinners.NewService(e.st, grounder, e.runner, func() {}, logger)
	if err != nil {
		t.Fatal(err)
	}
	e.dinner(t, "Beef Chili", ing{"ground beef", "1", "lb"}, ing{"onion", "1", ""})
	e.dinner(t, "Pizza Night", ing{"pizza dough", "1", "lb"}, ing{"mozzarella", "8", "oz"})
	e.plan(t, "Beef Chili", "Pizza Night")
	if _, err := e.chatSvc.Put(t.Context(), chattest.Webhook, "Family"); err != nil {
		t.Fatal(err)
	}
	generated := e.generate(t)
	if len(generated.List.Groups) != 2 {
		t.Fatalf("%d groups, want Beef Chili and Pizza Night", len(generated.List.Groups))
	}
	archivedItem := e.archiveDinnerGroup(t, "Pizza Night", "pizza dough")

	if _, err := svc.Update(t.Context(), e.ids["Pizza Night"], dinners.Patch{MealSource: ptr(dinners.MealSourceDelivery)}, "member-1"); err != nil {
		t.Fatal(err)
	}

	got, err := e.svc.Get(t.Context(), week)
	if err != nil {
		t.Fatal(err)
	}
	if c := got.Counts(); c.Needed != 2 || c.Total != 2 {
		t.Errorf("counts = %+v, want the 2 Beef Chili items", c)
	}
	for _, group := range got.Groups {
		if group.DinnerName == "Pizza Night" || group.DinnerID == e.ids["Pizza Night"] {
			t.Errorf("GET still has %s with %d items", group.DinnerName, len(group.Items))
		}
		for _, item := range group.Items {
			if item.Name == "pizza dough" || item.Name == "mozzarella" {
				t.Errorf("GET still counts %q", item.Name)
			}
		}
	}
	var current int
	if err := e.st.Reader.QueryRow(`SELECT COUNT(*) FROM shopping_groups
		JOIN shopping_lists ON shopping_lists.id = shopping_groups.list_id
		WHERE shopping_groups.dinner_id = ? AND shopping_lists.status = 'current'`, e.ids["Pizza Night"]).Scan(&current); err != nil {
		t.Fatal(err)
	}
	if current != 0 {
		t.Errorf("%d current groups for Pizza Night, want 0", current)
	}
	var archived string
	if err := e.st.Reader.QueryRow(`SELECT name FROM shopping_items WHERE id = ?`, archivedItem).Scan(&archived); err != nil {
		t.Fatal(err)
	}
	if archived != "pizza dough" {
		t.Errorf("archived item = %q, want pizza dough kept", archived)
	}

	sent, err := e.svc.SendToChat(t.Context(), week, false)
	if err != nil {
		t.Fatal(err)
	}
	if c := sent.Counts(); c.Needed != 2 || c.Total != 2 {
		t.Errorf("sent counts = %+v, want the 2 Beef Chili items", c)
	}
	posts := e.chat.Posts()
	if len(posts) != 1 {
		t.Fatalf("Google Chat got %d posts, want 1", len(posts))
	}
	text := posts[0].Text()
	if strings.Contains(text, "Pizza Night") || strings.Contains(text, "pizza dough") || strings.Contains(text, "mozzarella") {
		t.Errorf("posted %q, want no Pizza Night group", text)
	}
	if !strings.Contains(text, "Beef Chili") || !strings.Contains(text, "ground beef") {
		t.Errorf("posted %q, want Beef Chili's ingredients", text)
	}
}

func ptr[T any](v T) *T { return &v }

// TestDesignateDeliveryReplacesStaleTitle: a title job queued while a dinner
// is cooked does not title the Supply run with that dinner once it has been
// designated Delivery, even when the job runs only after the designation.
// The title is asked for the dinners the Supply run still has.
func TestDesignateDeliveryReplacesStaleTitle(t *testing.T) {
	e := newEnv(t)
	if err := household.SeedSettings(t.Context(), e.st, "America/New_York"); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	grounder := recipestest.NewGrounder(t, e.st, t.TempDir(), &recipestest.Search{}, &recipestest.Pages{}, &recipestest.Images{}, &llm.Fake{})
	svc, err := dinners.NewService(e.st, grounder, e.runner, func() {}, logger)
	if err != nil {
		t.Fatal(err)
	}
	e.dinner(t, "Beef Chili", ing{"ground beef", "1", "lb"})
	e.dinner(t, "Pizza Night", ing{"pizza dough", "1", "lb"})
	e.plan(t, "Beef Chili", "Pizza Night")
	e.model.QueueGenerate(titled("Chili Night"))
	generated := e.generate(t)
	if generated.TitleJobID == nil {
		t.Fatal("generation enqueued no title job")
	}

	if _, err := svc.Update(t.Context(), e.ids["Pizza Night"], dinners.Patch{MealSource: ptr(dinners.MealSourceDelivery)}, "member-1"); err != nil {
		t.Fatal(err)
	}
	e.start(t)
	wantJob(t, e.waitTitle(t, generated), jobs.StatusSucceeded, "")
	reqs := e.model.Requests()
	if len(reqs) != 1 {
		t.Fatalf("%d model calls, want 1", len(reqs))
	}
	user := reqs[0].Messages[0].Content
	if strings.Contains(user, "Pizza Night") {
		t.Errorf("title request = %q, want no Pizza Night", user)
	}
	if !strings.Contains(user, "Beef Chili") {
		t.Errorf("title request = %q, want Beef Chili", user)
	}
	wantTitle(t, "stored title", e.storedTitle(t), "Chili Night")
}

// archiveDinnerGroup stores an archived Supply run of the previous week
// with one item of the dinner named name, and returns the item id.
func (e *env) archiveDinnerGroup(t *testing.T, name, item string) string {
	t.Helper()
	q := db.New(e.st.Writer)
	now := store.Now().String()
	planID := store.NewID()
	if err := q.CreateWeekPlan(t.Context(), db.CreateWeekPlanParams{
		ID: planID, WeekStart: "2026-10-05", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := q.PutPlanDay(t.Context(), db.PutPlanDayParams{
		ID: store.NewID(), PlanID: planID, Date: "2026-10-05",
		DinnerID: sql.NullString{String: e.ids[name], Valid: true}, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	listID := store.NewID()
	if err := q.CreateShoppingList(t.Context(), db.CreateShoppingListParams{
		ID: listID, WeekStart: "2026-10-05", PlanID: planID, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.Writer.Exec(`UPDATE shopping_lists SET status = 'archived', frozen_at = ? WHERE id = ?`, now, listID); err != nil {
		t.Fatal(err)
	}
	groupID := store.NewID()
	if err := q.CreateShoppingGroup(t.Context(), db.CreateShoppingGroupParams{
		ID: groupID, ListID: listID, DinnerID: e.ids[name], Date: "2026-10-05", Position: 0,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	itemID := store.NewID()
	if err := q.CreateShoppingItem(t.Context(), db.CreateShoppingItemParams{
		ID: itemID, GroupID: groupID, Name: item, Qty: sql.NullString{String: "1 lb", Valid: true},
		Position: 0, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	return itemID
}
