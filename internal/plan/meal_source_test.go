package plan_test

import (
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/llm"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/dinners"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/household"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/jobs"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/notify"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/notify/notifytest"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/plan"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/recipes/recipestest"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

// currentWeek is the household's current week in America/New_York: its
// Monday, its seven dates, and the index of today. Beef Chili is moved
// onto today so a designation exercises a current plan day.
func (e *env) currentWeek(t *testing.T) (start store.LocalDate, dates []string, names []string, today string) {
	t.Helper()
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	now := household.Today(time.Now(), loc)
	start = household.WeekStart(now)
	days, err := household.WeekDays(start)
	if err != nil {
		t.Fatal(err)
	}
	names = slices.Clone(fullWeek)
	for i, day := range days {
		dates = append(dates, day.String())
		if day.String() == now.String() {
			today = day.String()
			names[0], names[i] = names[i], names[0]
		}
	}
	if today == "" {
		t.Fatal("today is not in its week")
	}
	return start, dates, names, today
}

// dinnersService is a dinners service on e's store and job runner. The
// runner must not have started.
func (e *env) dinnersService(t *testing.T) *dinners.Service {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	grounder := recipestest.NewGrounder(t, e.st, t.TempDir(), &recipestest.Search{}, &recipestest.Pages{}, &recipestest.Images{}, &llm.Fake{})
	svc, err := dinners.NewService(e.st, grounder, e.runner, func() {}, logger)
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

// setPhoto stores a photo on the dinner named name.
func (e *env) setPhoto(t *testing.T, name string) {
	t.Helper()
	now := store.Now().String()
	photo, err := db.New(e.st.Writer).CreatePhoto(t.Context(), db.CreatePhotoParams{
		ID: store.NewID(), SourceUrl: "https://photos.example/" + name, LocalPath: "media/photos/" + name,
		Width: 400, Height: 300, Sha256: "abc", FetchedAt: now, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.st.Writer.ExecContext(t.Context(), `UPDATE dinners SET photo_id = ? WHERE id = ?`, photo.ID, e.ids[name])
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		t.Fatalf("no dinner %s to photograph", name)
	}
}

// revision is the plan revision of the week starting weekStart.
func (e *env) revision(t *testing.T, weekStart store.LocalDate) int64 {
	t.Helper()
	var rev int64
	if err := e.st.Reader.QueryRowContext(t.Context(), `SELECT revision FROM week_plans WHERE week_start = ?`, weekStart.String()).Scan(&rev); err != nil {
		t.Fatal(err)
	}
	return rev
}

// designate sets the dinner named name to source through dinners.Update.
func (e *env) designate(t *testing.T, svc *dinners.Service, name, source string) {
	t.Helper()
	if _, err := svc.Update(t.Context(), e.ids[name], dinners.Patch{MealSource: &source}, "member-1"); err != nil {
		t.Fatal(err)
	}
}

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

// TestRefuseDeliveryWhilePlanned: designating a dinner Delivery or Dining
// Out while it is on a current or future plan day is refused. The dinner,
// every plan, the Supply run and Calendar stay as they were, and Calendar
// is not called.
func TestRefuseDeliveryWhilePlanned(t *testing.T) {
	e := newEnv(t)
	svc := e.dinnersService(t)
	start, dates, names, today := e.currentWeek(t)
	e.connectCalendar(t)
	e.start(t)
	e.model.QueueGenerate(e.weekReplyOn(dates, "", names...))
	drafted, err := e.svc.StartDraft(t.Context(), start, false)
	if err != nil {
		t.Fatal(err)
	}
	if job := e.waitJob(t, drafted.JobID); job.Status != jobs.StatusSucceeded {
		t.Fatalf("draft job %s, error %v", job.Status, job.Error)
	}
	if _, _, err := e.svc.Push(t.Context(), start); err != nil {
		t.Fatal(err)
	}
	y, m, d := start.Date()
	past := store.LocalDateOf(time.Date(y, m, d-7, 0, 0, 0, 0, time.UTC))
	e.storePlan(t, past, fullWeek...)
	if _, _, err := e.svc.Push(t.Context(), past); err != nil {
		t.Fatal(err)
	}
	future := store.LocalDateOf(time.Date(y, m, d+7, 0, 0, 0, 0, time.UTC))
	futureDates := e.storePlan(t, future, names...)
	if _, _, err := e.svc.Push(t.Context(), future); err != nil {
		t.Fatal(err)
	}
	var futureChili string
	for i, name := range names {
		if name == "Beef Chili" {
			futureChili = futureDates[i]
		}
	}
	e.supplyItem(t, start, "Beef Chili", today, "ground beef")
	beforeRev := e.revision(t, start)
	beforeIDs := e.eventIDs(t, start)
	pastEvents := e.eventIDs(t, past)
	futureEvents := e.eventIDs(t, future)
	beforeGoogle := maps.Clone(e.google.Events(dinnersCal.ID))
	itemsBefore := e.supplyCount(t, "Beef Chili")
	if itemsBefore != 1 || futureChili == "" || beforeGoogle[plan.EventID(start, mustDate(today))].ID == "" {
		t.Fatalf("setup items %d future %q today event %q", itemsBefore, futureChili, beforeGoogle[plan.EventID(start, mustDate(today))].ID)
	}
	e.google.ResetEventWrites()

	for _, source := range []string{dinners.MealSourceDelivery, dinners.MealSourceDiningOut} {
		_, err = svc.Update(t.Context(), e.ids["Beef Chili"], dinners.Patch{MealSource: ptrMeal(source)}, "member-1")
		var planned *dinners.PlannedError
		if !errors.As(err, &planned) || planned.Name != "Beef Chili" {
			t.Fatalf("designate %s = %v, want PlannedError for Beef Chili", source, err)
		}
		if !strings.Contains(err.Error(), "on the plan") || !strings.Contains(err.Error(), "removed from the plan first") {
			t.Fatalf("error = %q, want it to say the dinner is on the plan and must be removed first", err)
		}
	}

	var source string
	if err := e.st.Reader.QueryRowContext(t.Context(), `SELECT meal_source FROM dinners WHERE id = ?`, e.ids["Beef Chili"]).Scan(&source); err != nil {
		t.Fatal(err)
	}
	if source != dinners.MealSourceCook {
		t.Errorf("meal_source = %s, want cook", source)
	}
	got, err := e.svc.Get(t.Context(), start)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != plan.StatusSynced {
		t.Errorf("status = %s, want synced", got.Status)
	}
	if rev := e.revision(t, start); rev != beforeRev {
		t.Errorf("revision = %d, want %d", rev, beforeRev)
	}
	day := dayByDate(t, got, today)
	if day.Dinner == nil || day.Dinner.Name != "Beef Chili" || day.Dinner.MealSource != dinners.MealSourceCook {
		t.Errorf("today = %+v, want Beef Chili still cooked and planned", day.Dinner)
	}
	if ids := e.eventIDs(t, start); !maps.Equal(ids, beforeIDs) {
		t.Errorf("event ids = %v, want %v", ids, beforeIDs)
	}
	pastPlan, err := e.svc.Get(t.Context(), past)
	if err != nil {
		t.Fatal(err)
	}
	if pastPlan.Status != plan.StatusSynced {
		t.Errorf("past week status = %s, want synced", pastPlan.Status)
	}
	if again := e.eventIDs(t, past); !maps.Equal(again, pastEvents) {
		t.Errorf("past event ids = %v, want %v", again, pastEvents)
	}
	futurePlan, err := e.svc.Get(t.Context(), future)
	if err != nil {
		t.Fatal(err)
	}
	if futurePlan.Status != plan.StatusSynced {
		t.Errorf("future week status = %s, want synced", futurePlan.Status)
	}
	if again := e.eventIDs(t, future); !maps.Equal(again, futureEvents) {
		t.Errorf("future event ids = %v, want %v", again, futureEvents)
	}
	if futureDay := dayByDate(t, futurePlan, futureChili); futureDay.Dinner == nil || futureDay.Dinner.Name != "Beef Chili" {
		t.Errorf("future %s = %+v, want Beef Chili still planned", futureChili, futureDay.Dinner)
	}
	if items := e.supplyCount(t, "Beef Chili"); items != itemsBefore {
		t.Errorf("supply items = %d, want %d", items, itemsBefore)
	}
	var item string
	if err := e.st.Reader.QueryRowContext(t.Context(), `SELECT shopping_items.name FROM shopping_items
		JOIN shopping_groups ON shopping_groups.id = shopping_items.group_id
		WHERE shopping_groups.dinner_id = ?`, e.ids["Beef Chili"]).Scan(&item); err != nil {
		t.Fatal(err)
	}
	if item != "ground beef" {
		t.Errorf("supply item = %q, want ground beef", item)
	}
	if inserts, updates := e.google.EventWrites(); inserts != 0 || updates != 0 {
		t.Errorf("calendar writes = %d inserts, %d updates, want none", inserts, updates)
	}
	if after := e.google.Events(dinnersCal.ID); !maps.Equal(beforeGoogle, after) {
		t.Errorf("calendar events changed:\nbefore %+v\nafter  %+v", beforeGoogle, after)
	}
}

// TestDesignateUnplannedDinnerThenCookAgain: a dinner on no current or
// future plan day can be marked Delivery. The draft pool leaves it out.
// Marking it cooked again puts it back, and it can be planned.
func TestDesignateUnplannedDinnerThenCookAgain(t *testing.T) {
	e := newEnv(t)
	svc := e.dinnersService(t)
	e.ids["Fish Tacos"] = e.dinner(t, "Fish Tacos", "regular", "stovetop", 25)
	e.setPhoto(t, "Fish Tacos")
	e.start(t)

	got, err := svc.Update(t.Context(), e.ids["Fish Tacos"], dinners.Patch{MealSource: ptrMeal(dinners.MealSourceDelivery)}, "member-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.MealSource != dinners.MealSourceDelivery || got.PhotoURL != nil {
		t.Fatalf("designated = source %s photo %v, want delivery and no photo", got.MealSource, got.PhotoURL)
	}

	e.reply("", fullWeek...)
	p := e.mustDraft(t, false)
	for _, day := range p.Days {
		if day.Dinner != nil && day.Dinner.Name == "Fish Tacos" {
			t.Errorf("draft planned Fish Tacos")
		}
	}
	for _, c := range e.input(0).Pool {
		if c.Name == "Fish Tacos" {
			t.Errorf("pool included Fish Tacos")
		}
	}

	cooked, err := svc.Update(t.Context(), e.ids["Fish Tacos"], dinners.Patch{MealSource: ptrMeal(dinners.MealSourceCook)}, "member-1")
	if err != nil {
		t.Fatal(err)
	}
	if cooked.MealSource != dinners.MealSourceCook || cooked.PhotoURL == nil {
		t.Fatalf("cooked again = source %s photo %v, want cook and its photo", cooked.MealSource, cooked.PhotoURL)
	}
	swapped, err := e.svc.Swap(t.Context(), week, "2026-10-12", e.ids["Fish Tacos"])
	if err != nil {
		t.Fatal(err)
	}
	if swapped.Days[0].Dinner == nil || swapped.Days[0].Dinner.Name != "Fish Tacos" || swapped.Days[0].Dinner.MealSource != dinners.MealSourceCook {
		t.Errorf("Monday = %+v, want Fish Tacos cooked", swapped.Days[0].Dinner)
	}
}

// TestDesignateDeliveryLeavesAPastPlan: a dinner that is only on a past
// plan can be marked Delivery. That plan, its Calendar event and a Supply
// run already archived with it stay as they were, and Calendar is not called.
func TestDesignateDeliveryLeavesAPastPlan(t *testing.T) {
	e := newEnv(t)
	svc := e.dinnersService(t)
	e.connectCalendar(t)
	e.start(t)
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	today := household.Today(time.Now(), loc)
	start := household.WeekStart(today)
	y, m, d := start.Date()
	past := store.LocalDateOf(time.Date(y, m, d-7, 0, 0, 0, 0, time.UTC))
	e.storePlan(t, past, fullWeek...)
	if _, _, err := e.svc.Push(t.Context(), past); err != nil {
		t.Fatal(err)
	}
	e.supplyItem(t, past, "Beef Chili", past.String(), "ground beef")
	if _, err := e.st.Writer.ExecContext(t.Context(), `UPDATE shopping_lists SET status = 'archived', frozen_at = ? WHERE week_start = ?`, store.Now().String(), past.String()); err != nil {
		t.Fatal(err)
	}
	beforeEvents := maps.Clone(e.google.Events(dinnersCal.ID))
	beforeIDs := e.eventIDs(t, past)
	e.google.ResetEventWrites()

	got, err := svc.Update(t.Context(), e.ids["Beef Chili"], dinners.Patch{MealSource: ptrMeal(dinners.MealSourceDelivery)}, "member-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.MealSource != dinners.MealSourceDelivery {
		t.Fatalf("meal source = %s, want delivery", got.MealSource)
	}
	pastPlan, err := e.svc.Get(t.Context(), past)
	if err != nil {
		t.Fatal(err)
	}
	if pastPlan.Status != plan.StatusSynced {
		t.Errorf("past status = %s, want synced", pastPlan.Status)
	}
	monday := dayByDate(t, pastPlan, past.String())
	if monday.Dinner == nil || monday.Dinner.Name != "Beef Chili" || monday.Dinner.MealSource != dinners.MealSourceDelivery {
		t.Errorf("past Monday = %+v, want Beef Chili still planned, now delivery", monday.Dinner)
	}
	if ids := e.eventIDs(t, past); !maps.Equal(ids, beforeIDs) {
		t.Errorf("past event ids = %v, want %v", ids, beforeIDs)
	}
	if item := e.supplyCount(t, "Beef Chili"); item != 1 {
		t.Errorf("archived supply items = %d, want 1", item)
	}
	if inserts, updates := e.google.EventWrites(); inserts != 0 || updates != 0 {
		t.Errorf("calendar writes = %d inserts, %d updates, want none", inserts, updates)
	}
	if after := e.google.Events(dinnersCal.ID); !maps.Equal(beforeEvents, after) {
		t.Errorf("calendar events changed")
	}

	cooked, err := svc.Update(t.Context(), e.ids["Beef Chili"], dinners.Patch{MealSource: ptrMeal(dinners.MealSourceCook)}, "member-1")
	if err != nil || cooked.MealSource != dinners.MealSourceCook {
		t.Fatalf("cook again = %+v, %v", cooked.MealSource, err)
	}
}

func dayByDate(t *testing.T, p plan.Plan, date string) plan.Day {
	t.Helper()
	for _, day := range p.Days {
		if day.Date == date {
			return day
		}
	}
	t.Fatalf("plan has no %s", date)
	return plan.Day{}
}

func ptrMeal(v string) *string { return &v }

// supplyItem stores a current Supply run for weekStart with one item of
// the dinner named dinnerName on date.
func (e *env) supplyItem(t *testing.T, weekStart store.LocalDate, dinnerName, date, item string) {
	t.Helper()
	q := db.New(e.st.Writer)
	planRow, err := q.GetWeekPlan(t.Context(), weekStart.String())
	if err != nil {
		t.Fatal(err)
	}
	now := store.Now().String()
	listID := store.NewID()
	if err := q.CreateShoppingList(t.Context(), db.CreateShoppingListParams{
		ID: listID, WeekStart: weekStart.String(), PlanID: planRow.ID, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	groupID := store.NewID()
	if err := q.CreateShoppingGroup(t.Context(), db.CreateShoppingGroupParams{
		ID: groupID, ListID: listID, DinnerID: e.ids[dinnerName], Date: date, Position: 0, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := q.CreateShoppingItem(t.Context(), db.CreateShoppingItemParams{
		ID: store.NewID(), GroupID: groupID, Name: item, Qty: sql.NullString{String: "1 lb", Valid: true},
		Position: 0, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
}

// supplyCount is how many Supply-run items the dinner named dinnerName has.
func (e *env) supplyCount(t *testing.T, dinnerName string) int {
	t.Helper()
	var n int
	if err := e.st.Reader.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM shopping_items
		JOIN shopping_groups ON shopping_groups.id = shopping_items.group_id
		WHERE shopping_groups.dinner_id = ?`, e.ids[dinnerName]).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
