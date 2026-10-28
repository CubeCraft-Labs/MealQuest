package plan_test

import (
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

// TestDesignateDeliveryAfterDraft: designating a dinner Delivery after the
// week is drafted leaves it on that day, but not as a cooked dinner. The
// plan's revision moves, so a push that already read it cannot record the
// old cooked event.
func TestDesignateDeliveryAfterDraft(t *testing.T) {
	e := newEnv(t)
	svc := e.dinnersService(t)
	start, dates, names, today := e.currentWeek(t)
	e.start(t)
	e.model.QueueGenerate(e.weekReplyOn(dates, "", names...))
	drafted, err := e.svc.StartDraft(t.Context(), start, false)
	if err != nil {
		t.Fatal(err)
	}
	if job := e.waitJob(t, drafted.JobID); job.Status != jobs.StatusSucceeded {
		t.Fatalf("draft job %s, error %v", job.Status, job.Error)
	}
	e.setPhoto(t, "Beef Chili")
	before := e.revision(t, start)
	cooked, err := e.svc.Get(t.Context(), start)
	if err != nil {
		t.Fatal(err)
	}
	chili := dayByDate(t, cooked, today)
	if chili.Dinner.Name != "Beef Chili" || chili.Dinner.Photo == nil || chili.StartBy == nil {
		t.Fatalf("today before designation = %+v photo %v start %v, want Beef Chili with a photo and a start-by", chili.Dinner, chili.Dinner.Photo, chili.StartBy)
	}

	e.designate(t, svc, "Beef Chili", dinners.MealSourceDelivery)

	got, err := e.svc.Get(t.Context(), start)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != plan.StatusDraft {
		t.Errorf("status = %s, want draft", got.Status)
	}
	if rev := e.revision(t, start); rev != before+1 {
		t.Errorf("revision = %d, want %d", rev, before+1)
	}
	day := dayByDate(t, got, today)
	if day.Dinner.Name != "Beef Chili" || day.Dinner.MealSource != dinners.MealSourceDelivery {
		t.Errorf("today = %+v, want Beef Chili delivery", day.Dinner)
	}
	if day.Dinner.Photo != nil || day.StartBy != nil {
		t.Errorf("today photo %v start %v, want neither", day.Dinner.Photo, day.StartBy)
	}
	if !slices.Equal(dayNames(got), names) {
		t.Errorf("days = %v, want the drafted week %v", dayNames(got), names)
	}
	date := mustDate(today)
	if ok, err := e.svc.EmitDayOfReminder(t.Context(), date); !ok || err != nil {
		t.Fatalf("EmitDayOfReminder = %v, %v", ok, err)
	}
	rows := notifytest.Rows(t, e.st, notify.DayOfReminder)
	if len(rows) != 1 {
		t.Fatalf("reminders = %+v, want one", rows)
	}
	payload := rows[0].Payload
	if strings.Contains(payload.URL, "/dinner/") || strings.Contains(payload.Title, "Recipe") || strings.Contains(payload.Body, "Start") {
		t.Errorf("reminder = %+v, want no recipe and no start time", payload)
	}
	if ok, err := e.svc.EmitStartReminder(t.Context(), date); ok || err != nil {
		t.Fatalf("EmitStartReminder = %v, %v, want no crock-pot reminder", ok, err)
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

// TestDesignateDiningOutAfterPush: designating a dinner Dining Out after
// Push to Calendar marks that week changed and clears the day's stored
// Calendar event, so the next push rewrites it without a recipe or a
// start time. A past week that planned the same dinner stays synced.
func TestDesignateDiningOutAfterPush(t *testing.T) {
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
	e.setPhoto(t, "Beef Chili")
	pastEvents := e.eventIDs(t, past)
	pastMonday := past.String()
	if pastEvents[pastMonday] == "" {
		t.Fatal("past Monday has no calendar event")
	}

	e.designate(t, svc, "Beef Chili", dinners.MealSourceDiningOut)

	got, err := e.svc.Get(t.Context(), start)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != plan.StatusChanged {
		t.Fatalf("status = %s, want changed", got.Status)
	}
	day := dayByDate(t, got, today)
	if day.Dinner.MealSource != dinners.MealSourceDiningOut || day.Dinner.Photo != nil || day.StartBy != nil {
		t.Errorf("today = %+v photo %v start %v, want dining out with neither", day.Dinner, day.Dinner.Photo, day.StartBy)
	}
	ids := e.eventIDs(t, start)
	if ids[today] != "" {
		t.Errorf("%s kept event id %q", today, ids[today])
	}
	for date, id := range ids {
		if date != today && id == "" {
			t.Errorf("%s lost its event id", date)
		}
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

	before := e.google.Events(dinnersCal.ID)
	e.google.ResetEventWrites()
	pushed, _, err := e.svc.Push(t.Context(), start)
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	if inserts, updates := e.google.EventWrites(); inserts != 0 || updates != 1 {
		t.Errorf("push after designation: %d inserts and %d updates, want 0 and 1", inserts, updates)
	}
	if pushed.Status != plan.StatusSynced {
		t.Errorf("status after push = %s, want synced", pushed.Status)
	}
	todayID := plan.EventID(start, mustDate(today))
	after := e.google.Events(dinnersCal.ID)
	if ev := after[todayID]; ev.Summary != "Dinner: Beef Chili" || ev.Description != "Beef Chili\nDining out" {
		t.Errorf("today's event = summary %q description %q, want Dining out and no recipe", ev.Summary, ev.Description)
	}
	if strings.Contains(after[todayID].Description, "http") || strings.Contains(after[todayID].Description, "Start by") {
		t.Errorf("today's event description = %q, want no recipe link and no start time", after[todayID].Description)
	}
	delete(before, todayID)
	delete(after, todayID)
	if !maps.Equal(before, after) {
		t.Errorf("events of other days changed:\nbefore %+v\nafter  %+v", before, after)
	}

	e.designate(t, svc, "Beef Chili", dinners.MealSourceCook)
	cookedAgain, err := e.svc.Get(t.Context(), start)
	if err != nil {
		t.Fatal(err)
	}
	if cookedAgain.Status != plan.StatusChanged {
		t.Fatalf("status after cooking again = %s, want changed", cookedAgain.Status)
	}
	if id := e.eventIDs(t, start)[today]; id != "" {
		t.Errorf("%s kept event id %q after it was cooked again", today, id)
	}
	again := dayByDate(t, cookedAgain, today)
	if again.Dinner.MealSource != dinners.MealSourceCook || again.Dinner.Photo == nil || again.StartBy == nil {
		t.Errorf("today after cooking again = %+v photo %v start %v, want cooked with a photo and a start-by", again.Dinner, again.Dinner.Photo, again.StartBy)
	}
	e.google.ResetEventWrites()
	if _, _, err := e.svc.Push(t.Context(), start); err != nil {
		t.Fatal(err)
	}
	if inserts, updates := e.google.EventWrites(); inserts != 0 || updates != 1 {
		t.Errorf("push after cooking again: %d inserts and %d updates, want 0 and 1", inserts, updates)
	}
	restored := e.google.Events(dinnersCal.ID)[todayID].Description
	if !strings.Contains(restored, "https://meals.example.com/dinner/"+e.ids["Beef Chili"]) || !strings.Contains(restored, "Start by 1:30 pm") {
		t.Errorf("today's event description = %q, want the recipe link and start time", restored)
	}
}
