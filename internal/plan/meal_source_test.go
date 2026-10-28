package plan_test

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/llm"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/skills"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/chat"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/dinners"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/home"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/household"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/jobs"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/notify"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/notify/notifytest"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/plan"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/recipes/recipestest"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/secretbox"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/shopping"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/suggest"
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
	svc.SetPlanReconciler(e.svc)
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
// week is drafted takes it off today, like a cleared night. The Draft board
// no longer has its card, and Home no longer names it. The plan's revision
// moves. A later night of the same dinner, and every other dinner, stays.
func TestDesignateDeliveryAfterDraft(t *testing.T) {
	e := newEnv(t)
	svc := e.dinnersService(t)
	homeSvc := e.homeScreen(t)
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
	if day.Dinner != nil || day.ManualLabel != nil || day.StartBy != nil {
		t.Errorf("today = dinner %+v label %v start %v, want a cleared night", day.Dinner, day.ManualLabel, day.StartBy)
	}
	wantNights := slices.Clone(names)
	for i, date := range dates {
		if date == today {
			wantNights[i] = ""
		}
	}
	if !slices.Equal(nightNames(got), wantNights) {
		t.Errorf("days = %v, want the drafted week with today cleared %v", nightNames(got), wantNights)
	}
	screen, err := homeSvc.Get(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if screen.Tonight != nil || screen.ManualTonight != nil {
		t.Errorf("Home tonight = %+v manual %v, want nothing named", screen.Tonight, screen.ManualTonight)
	}
	date := mustDate(today)
	if ok, err := e.svc.EmitDayOfReminder(t.Context(), date); ok || !errors.Is(err, plan.ErrNoNight) {
		t.Fatalf("EmitDayOfReminder = %v, %v, want no reminder for a cleared night", ok, err)
	}
	if rows := notifytest.Rows(t, e.st, notify.DayOfReminder); len(rows) != 0 {
		t.Fatalf("reminders = %+v, want none", rows)
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
// Push to Calendar removes it from today and from a future week, marks
// those plans changed, and deletes the Calendar events immediately, so
// Google no longer advertises a recipe or a start-by time. A past week
// that planned the same dinner stays synced, event and all. Marking it
// cooked again does not put the night back.
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
	future := store.LocalDateOf(time.Date(y, m, d+7, 0, 0, 0, 0, time.UTC))
	futureDates := e.storePlan(t, future, names...)
	if _, _, err := e.svc.Push(t.Context(), future); err != nil {
		t.Fatal(err)
	}
	pastEvents := e.eventIDs(t, past)
	pastMonday := past.String()
	if pastEvents[pastMonday] == "" {
		t.Fatal("past Monday has no calendar event")
	}
	var futureChili string
	for i, name := range names {
		if name == "Beef Chili" {
			futureChili = futureDates[i]
		}
	}
	if futureChili == "" || e.eventIDs(t, future)[futureChili] == "" {
		t.Fatal("future Beef Chili day has no calendar event")
	}
	before := e.google.Events(dinnersCal.ID)
	todayID := plan.EventID(start, mustDate(today))
	futureID := plan.EventID(future, mustDate(futureChili))
	if _, ok := before[todayID]; !ok {
		t.Fatal("today has no calendar event")
	}
	if _, ok := before[futureID]; !ok {
		t.Fatal("future Beef Chili day has no google event")
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
	if day.Dinner != nil || day.ManualLabel != nil || day.StartBy != nil {
		t.Errorf("today = dinner %+v label %v start %v, want a cleared night", day.Dinner, day.ManualLabel, day.StartBy)
	}
	cleared := slices.Clone(names)
	for i, name := range cleared {
		if name == "Beef Chili" {
			cleared[i] = ""
		}
	}
	if !slices.Equal(nightNames(got), cleared) {
		t.Errorf("days = %v, want today cleared %v", nightNames(got), cleared)
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
	futurePlan, err := e.svc.Get(t.Context(), future)
	if err != nil {
		t.Fatal(err)
	}
	if futurePlan.Status != plan.StatusChanged {
		t.Errorf("future week status = %s, want changed", futurePlan.Status)
	}
	if futureDay := dayByDate(t, futurePlan, futureChili); futureDay.Dinner != nil {
		t.Errorf("future %s = %+v, want a cleared night", futureChili, futureDay.Dinner)
	}
	if id := e.eventIDs(t, future)[futureChili]; id != "" {
		t.Errorf("future %s kept event id %q", futureChili, id)
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
	if monday := dayByDate(t, pastPlan, pastMonday); monday.Dinner == nil || monday.Dinner.Name != "Beef Chili" {
		t.Errorf("past Monday = %+v, want Beef Chili left on the past plan", monday.Dinner)
	}

	// Google's calendar is already right: no second Push to Calendar.
	after := e.google.Events(dinnersCal.ID)
	if _, ok := after[todayID]; ok {
		t.Errorf("today's event is still on Calendar: %+v", after[todayID])
	}
	if _, ok := after[futureID]; ok {
		t.Errorf("future event is still on Calendar: %+v", after[futureID])
	}
	delete(before, todayID)
	delete(before, futureID)
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
	if again := dayByDate(t, cookedAgain, today); again.Dinner != nil {
		t.Errorf("today after cooking again = %+v, want it to stay cleared", again.Dinner)
	}
	if _, ok := e.google.Events(dinnersCal.ID)[todayID]; ok {
		t.Error("cooking again put today's event back on Calendar")
	}
}

// TestDesignateDeliveryRollsBackWhenCalendarDeleteFails: a Calendar delete
// that fails refuses the designation. The dinner stays cooked, today stays
// planned, and Google still has the recipe event.
func TestDesignateDeliveryRollsBackWhenCalendarDeleteFails(t *testing.T) {
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
	beforeRev := e.revision(t, start)
	beforeIDs := e.eventIDs(t, start)
	todayID := plan.EventID(start, mustDate(today))
	beforeEvent := e.google.Events(dinnersCal.ID)[todayID]
	if beforeEvent.ID == "" || !strings.Contains(beforeEvent.Description, "https://") {
		t.Fatalf("today's event = %+v, want a recipe event", beforeEvent)
	}
	e.google.FailEventWrite(1, http.StatusBadRequest)

	_, err = svc.Update(t.Context(), e.ids["Beef Chili"], dinners.Patch{MealSource: ptrMeal(dinners.MealSourceDelivery)}, "member-1")
	if err == nil {
		t.Fatal("designate succeeded, want a calendar failure and no change")
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
	if ev := e.google.Events(dinnersCal.ID)[todayID]; ev.Description != beforeEvent.Description || ev.Summary != beforeEvent.Summary {
		t.Errorf("today's event = summary %q description %q, want it unchanged", ev.Summary, ev.Description)
	}
}

func ptrMeal(v string) *string { return &v }

// nightNames is each day's dinner name, its manual label, or "" when the
// night is cleared.
func nightNames(p plan.Plan) []string {
	names := make([]string, len(p.Days))
	for i, d := range p.Days {
		switch {
		case d.Dinner != nil:
			names[i] = d.Dinner.Name
		case d.ManualLabel != nil:
			names[i] = *d.ManualLabel
		}
	}
	return names
}

// homeScreen reads Home from e's plan service. Call it before e.start: it
// registers jobs on e's runner.
func (e *env) homeScreen(t *testing.T) *home.Service {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	notifier := notifytest.New(t, e.st)
	grounder := recipestest.NewGrounder(t, e.st, t.TempDir(), &recipestest.Search{}, &recipestest.Pages{}, &recipestest.Images{}, &llm.Fake{})
	suggestSvc, err := suggest.NewService(e.st, grounder, func(context.Context, string, skills.SideQuestsRequest) (skills.SideQuests, error) {
		return skills.SideQuests{}, errors.New("no model in this test")
	}, e.runner, notifier, logger)
	if err != nil {
		t.Fatal(err)
	}
	key := make([]byte, secretbox.KeySize)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	box, err := secretbox.New(base64.StdEncoding.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}
	chatSvc, err := chat.NewService(e.st, box, publicURL, http.DefaultTransport)
	if err != nil {
		t.Fatal(err)
	}
	shop, err := shopping.NewService(e.st, func(context.Context, string, skills.ArchiveTitleRequest) (skills.ArchiveTitle, error) {
		return skills.ArchiveTitle{}, errors.New("no model in this test")
	}, e.runner, notifier, chatSvc, logger)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := home.NewService(household.NewSettingsService(e.st), e.svc, suggestSvc, shop, notifier)
	if err != nil {
		t.Fatal(err)
	}
	return svc
}
