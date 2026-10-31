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
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/shopping"
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

// TestDesignateDeliveryLeavesTheSupplyRun: designating a dinner that is not
// on a current or future plan day does not rewrite a Supply run. The
// current run and a run already in the Supply archive keep the group they
// had. The next generation leaves the designated dinner out.
func TestDesignateDeliveryLeavesTheSupplyRun(t *testing.T) {
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
	if c := got.Counts(); c.Needed != 4 || c.Total != 4 {
		t.Errorf("counts = %+v, want the 4 items the run already had", c)
	}
	var pizza bool
	for _, group := range got.Groups {
		if group.DinnerName == "Pizza Night" {
			pizza = true
			if len(group.Items) != 2 {
				t.Errorf("Pizza Night items = %d, want the 2 the run already had", len(group.Items))
			}
		}
	}
	if !pizza {
		t.Error("GET lost Pizza Night; designation does not rewrite the Supply run")
	}
	var current int
	if err := e.st.Reader.QueryRow(`SELECT COUNT(*) FROM shopping_groups
		JOIN shopping_lists ON shopping_lists.id = shopping_groups.list_id
		WHERE shopping_groups.dinner_id = ? AND shopping_lists.status = 'current'`, e.ids["Pizza Night"]).Scan(&current); err != nil {
		t.Fatal(err)
	}
	if current != 1 {
		t.Errorf("%d current groups for Pizza Night, want 1", current)
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
	if c := sent.Counts(); c.Needed != 4 || c.Total != 4 {
		t.Errorf("sent counts = %+v, want the 4 items the run already had", c)
	}
	posts := e.chat.Posts()
	if len(posts) != 1 {
		t.Fatalf("Google Chat got %d posts, want 1", len(posts))
	}
	text := posts[0].Text()
	if !strings.Contains(text, "Pizza Night") || !strings.Contains(text, "pizza dough") || !strings.Contains(text, "Beef Chili") {
		t.Errorf("posted %q, want the Supply run as it was, Pizza Night included", text)
	}
}

func ptr[T any](v T) *T { return &v }

// TestTitleJobSkipsADinnerThatLeftTheWeek: a title job queued while a dinner
// is on the week does not title from that dinner once the dinner has left
// the week. The title is asked for the dinners the week has now.
func TestTitleJobSkipsADinnerThatLeftTheWeek(t *testing.T) {
	e := newEnv(t)
	e.dinner(t, "Beef Chili", ing{"ground beef", "1", "lb"})
	e.dinner(t, "Pizza Night", ing{"pizza dough", "1", "lb"})
	e.plan(t, "Beef Chili", "Pizza Night")
	e.model.QueueGenerate(titled("Chili Night"))
	generated := e.generate(t)
	if generated.TitleJobID == nil {
		t.Fatal("generation enqueued no title job")
	}

	e.removeFromPlan(t, "Pizza Night")
	if _, err := e.st.Writer.Exec(`UPDATE dinners SET meal_source = 'delivery' WHERE id = ?`, e.ids["Pizza Night"]); err != nil {
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

// TestTitleWriteDropsADinnerThatLeavesDuringTheTitle: the model has already
// titled a week that included a dinner. Before that title is stored the
// dinner leaves the week. The check and the write are one transaction, so
// the stale title is not stored.
func TestTitleWriteDropsADinnerThatLeavesDuringTheTitle(t *testing.T) {
	e := newEnv(t)
	e.dinner(t, "Beef Chili", ing{"ground beef", "1", "lb"})
	e.dinner(t, "Pizza Night", ing{"pizza dough", "1", "lb"})
	e.plan(t, "Beef Chili", "Pizza Night")
	e.model.QueueGenerate(titled("Chili and Pizza"))
	generated := e.generate(t)
	e.afterTitle = func() {
		e.removeFromPlan(t, "Pizza Night")
	}
	e.start(t)
	wantJob(t, e.waitTitle(t, generated), jobs.StatusFailed, shopping.ReasonTitleFailed)
	if title := e.storedTitle(t); title != nil {
		t.Errorf("stored title = %q, want none: the title was computed from Pizza Night", *title)
	}
	reqs := e.model.Requests()
	if len(reqs) != 1 || !strings.Contains(reqs[0].Messages[0].Content, "Pizza Night") {
		t.Fatalf("title request = %+v, want the model to have been asked while Pizza Night was still on the week", reqs)
	}
}

// removeFromPlan clears name off the week's plan and records the edit, as a
// Draft-board clear does.
func (e *env) removeFromPlan(t *testing.T, name string) {
	t.Helper()
	now := store.Now().String()
	res, err := e.st.Writer.Exec(`UPDATE plan_days SET dinner_id = NULL, manual_label = NULL, is_new = 0, calendar_event_id = NULL, updated_at = ? WHERE plan_id = ? AND dinner_id = ?`, now, e.planID, e.ids[name])
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		t.Fatalf("cleared %d days of %s, want 1", n, name)
	}
	if err := db.New(e.st.Writer).MarkWeekPlanEdited(t.Context(), db.MarkWeekPlanEditedParams{UpdatedAt: now, ID: e.planID}); err != nil {
		t.Fatal(err)
	}
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
