package notify

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/household"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

// addDinner stores the dinner id, named name, with status.
func addDinner(t *testing.T, st *store.Store, id, name, status string) {
	t.Helper()
	_, err := st.Writer.Exec(`INSERT INTO dinners (id, name, normalized_name, status, origin, is_favorite, grounding_status, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'regular', 0, 'grounded', 'c', 'u')`, id, name, name, status)
	if err != nil {
		t.Fatal(err)
	}
}

// archive archives the dinner id.
func archive(t *testing.T, st *store.Store, id string) {
	t.Helper()
	res, err := st.Writer.Exec(`UPDATE dinners SET status = 'archived' WHERE id = ?`, id)
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		t.Fatalf("no dinner %s to archive", id)
	}
}

// dayOf is a day-of reminder offering the recipe of dinner-1, Tacos, as
// chat.DayOfReminder emits it.
func dayOf() Event {
	return Event{
		Type: DayOfReminder, Key: "day_of_reminder:2026-10-14", Title: "Tonight: Tacos" + RecipeCallToAction,
		URL: RecipePath("dinner-1"), Dinner: &Dinner{ID: "dinner-1", Name: "Tacos"}, Week: mustDate("2026-10-12"),
	}
}

// startReminder is a crock-pot start reminder offering the recipe of
// dinner-1, Tacos, as chat.StartReminder emits it.
func startReminder() Event {
	return Event{
		Type: DayOfReminder, Key: "day_of_reminder:start:2026-10-14", Title: "Start the crock pot by 9:30 am", Body: "Tacos",
		URL: RecipePath("dinner-1"), Dinner: &Dinner{ID: "dinner-1", Name: "Tacos"}, Week: mustDate("2026-10-12"),
	}
}

// deliverPush delivers every due row to a push sender and returns the one
// message it got.
func deliverPush(t *testing.T, svc *Service, c *clock) Message {
	t.Helper()
	got := deliverAll(t, svc, c)
	if len(got) != 1 {
		t.Fatalf("push sender got %d messages, want 1: %+v", len(got), got)
	}
	return got[0]
}

// deliverPushCount delivers every due row and returns how many the push
// sender was given.
func deliverPushCount(t *testing.T, svc *Service, c *clock) int {
	t.Helper()
	return len(deliverAll(t, svc, c))
}

func deliverAll(t *testing.T, svc *Service, c *clock) []Message {
	t.Helper()
	push := &fakeSender{}
	if err := svc.Register(ChannelPush, push); err != nil {
		t.Fatal(err)
	}
	c.set(start.Add(time.Minute))
	if _, err := svc.deliverDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	return push.messages()
}

// planDinner plans dinnerID on date, creating that week's plan when needed.
func planDinner(t *testing.T, st *store.Store, dinnerID, date string) {
	t.Helper()
	day, err := store.ParseLocalDate(date)
	if err != nil {
		t.Fatal(err)
	}
	week := household.WeekStart(day)
	q := db.New(st.Writer)
	ctx := context.Background()
	row, err := q.GetWeekPlan(ctx, week.String())
	if errors.Is(err, sql.ErrNoRows) {
		row.ID = store.NewID()
		if err := q.CreateWeekPlan(ctx, db.CreateWeekPlanParams{
			ID: row.ID, WeekStart: week.String(), CreatedAt: "c", UpdatedAt: "u",
		}); err != nil {
			t.Fatal(err)
		}
	} else if err != nil {
		t.Fatal(err)
	}
	if err := q.PutPlanDay(ctx, db.PutPlanDayParams{
		ID: store.NewID(), PlanID: row.ID, Date: date,
		DinnerID: sql.NullString{String: dinnerID, Valid: true}, CreatedAt: "c", UpdatedAt: "u",
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDeliveryOffersTheRecipeOfAViewableDinner(t *testing.T) {
	svc, st, c := newTestService(t)
	setPref(t, st, DayOfReminder, true, false)
	addDinner(t, st, "dinner-1", "Tacos", "regular")
	planDinner(t, st, "dinner-1", "2026-10-14")
	if err := svc.Emit(context.Background(), dayOf()); err != nil {
		t.Fatal(err)
	}

	got := deliverPush(t, svc, c).Payload
	want := Payload{
		Event: DayOfReminder, Title: "Tonight: Tacos · Recipe →", URL: "/dinner/dinner-1", Tag: "day_of_reminder:2026-10-14",
		Dinner: &Dinner{ID: "dinner-1", Name: "Tacos"}, Week: "2026-10-12",
	}
	if !samePayload(got, want) {
		t.Errorf("delivered payload = %s, want %s", show(got), show(want))
	}
}

func TestDeliveryWithdrawsTheRecipeOfADinnerArchivedAfterQueueing(t *testing.T) {
	cases := []struct {
		name string
		ev   Event
		want Payload
	}{
		{"day-of reminder", dayOf(), Payload{
			Event: DayOfReminder, Title: "Tonight: Tacos", Body: ArchivedBody, URL: "/plan?week=2026-10-12",
			Tag: "day_of_reminder:2026-10-14", Week: "2026-10-12",
		}},
		{"start reminder", startReminder(), Payload{
			Event: DayOfReminder, Title: "Start the crock pot by 9:30 am", Body: "Tacos", URL: "/plan?week=2026-10-12",
			Tag: "day_of_reminder:start:2026-10-14", Week: "2026-10-12",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, st, c := newTestService(t)
			setPref(t, st, DayOfReminder, true, false)
			addDinner(t, st, "dinner-1", "Tacos", "regular")
			planDinner(t, st, "dinner-1", "2026-10-14")
			if err := svc.Emit(context.Background(), tc.ev); err != nil {
				t.Fatal(err)
			}
			archive(t, st, "dinner-1")

			m := deliverPush(t, svc, c)
			if !samePayload(m.Payload, tc.want) {
				t.Errorf("delivered payload = %s, want %s", show(m.Payload), show(tc.want))
			}
			if r := onlyRow(t, st, tc.ev.Key, ChannelPush); r.Status != "sent" {
				t.Errorf("row status = %s, want sent", r.Status)
			}
		})
	}
}

func TestDeliveryWithdrawsTheRecipeOfADinnerThatIsGone(t *testing.T) {
	svc, st, c := newTestService(t)
	setPref(t, st, DayOfReminder, true, false)
	if err := svc.Emit(context.Background(), dayOf()); err != nil {
		t.Fatal(err)
	}

	if n := deliverPushCount(t, svc, c); n != 0 {
		t.Fatalf("push sender got %d messages, want the reminder suppressed", n)
	}
	if row := onlyRow(t, st, dayOf().Key, ChannelPush); row.Status != "failed" || !row.LastError.Valid || row.LastError.String != suppressedReminder {
		t.Errorf("row = status %s error %v, want failed %q", row.Status, row.LastError, suppressedReminder)
	}
}

// queueLegacy stores a pending push row of a day-of reminder as it was
// stored before reminders named their dinner: a recipe url and no dinner.
func queueLegacy(t *testing.T, st *store.Store) {
	t.Helper()
	const payload = `{"event":"day_of_reminder","title":"Tonight: Tacos · Recipe →","body":"","url":"/dinner/dinner-1",` +
		`"tag":"day_of_reminder:2026-10-14","week":"2026-10-12"}`
	now := timestamp(start)
	if _, err := db.New(st.Writer).InsertNotification(context.Background(), db.InsertNotificationParams{
		ID: store.NewID(), EventKey: "day_of_reminder:2026-10-14", Channel: string(ChannelPush), Payload: payload,
		NextAttemptAt: sql.NullString{String: now, Valid: true}, Now: now,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDeliveryOfALegacyReminder(t *testing.T) {
	cases := []struct {
		name   string
		status string
		want   Payload
	}{
		{"viewable dinner is named", "regular", Payload{
			Event: DayOfReminder, Title: "Tonight: Tacos · Recipe →", URL: "/dinner/dinner-1", Tag: "day_of_reminder:2026-10-14",
			Dinner: &Dinner{ID: "dinner-1", Name: "Tacos"}, Week: "2026-10-12",
		}},
		{"archived dinner offers no recipe", "archived", Payload{
			Event: DayOfReminder, Title: "Tonight: Tacos", Body: ArchivedBody, URL: "/plan?week=2026-10-12",
			Tag: "day_of_reminder:2026-10-14", Week: "2026-10-12",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, st, c := newTestService(t)
			addDinner(t, st, "dinner-1", "Tacos", tc.status)
			planDinner(t, st, "dinner-1", "2026-10-14")
			queueLegacy(t, st)

			got := deliverPush(t, svc, c).Payload
			if !samePayload(got, tc.want) {
				t.Errorf("delivered payload = %s, want %s", show(got), show(tc.want))
			}
		})
	}
}

func TestDeliverySuppressesAReminderWhenTheDinnerLeftThePlan(t *testing.T) {
	for _, ev := range []Event{dayOf(), startReminder()} {
		t.Run(ev.Key, func(t *testing.T) {
			svc, st, c := newTestService(t)
			setPref(t, st, DayOfReminder, true, false)
			addDinner(t, st, "dinner-1", "Tacos", "regular")
			planDinner(t, st, "dinner-1", "2026-10-14")
			if err := svc.Emit(context.Background(), ev); err != nil {
				t.Fatal(err)
			}
			res, err := st.Writer.Exec(`UPDATE plan_days SET dinner_id = NULL WHERE date = ?`, "2026-10-14")
			if err != nil {
				t.Fatal(err)
			}
			if n, _ := res.RowsAffected(); n != 1 {
				t.Fatalf("cleared %d plan days, want 1", n)
			}

			if n := deliverPushCount(t, svc, c); n != 0 {
				t.Fatalf("push sender got %d messages, want the reminder suppressed", n)
			}
			row := onlyRow(t, st, ev.Key, ChannelPush)
			if row.Status != "failed" || !row.LastError.Valid || row.LastError.String != suppressedReminder {
				t.Errorf("row = status %s error %v, want failed %q", row.Status, row.LastError, suppressedReminder)
			}
		})
	}
}

func TestDeliveryLeavesAPayloadOfferingNoRecipe(t *testing.T) {
	svc, st, c := newTestService(t)
	setPref(t, st, PlanReady, true, false)
	if err := svc.Emit(context.Background(), planReady("plan_ready:2026-10-12")); err != nil {
		t.Fatal(err)
	}
	got := deliverPush(t, svc, c).Payload
	if got.URL != "/plan" || got.Dinner != nil || got.Body != "Five dinners on the Draft board." {
		t.Errorf("delivered payload = %s, want it as stored", show(got))
	}
}

func samePayload(a, b Payload) bool {
	if (a.Dinner == nil) != (b.Dinner == nil) || (a.Dinner != nil && *a.Dinner != *b.Dinner) {
		return false
	}
	a.Dinner, b.Dinner = nil, nil
	return a == b
}

func show(p Payload) string {
	d := "<nil>"
	if p.Dinner != nil {
		d = p.Dinner.ID + "/" + p.Dinner.Name
	}
	return "{event " + string(p.Event) + ", title " + p.Title + ", body " + p.Body + ", url " + p.URL + ", tag " + p.Tag +
		", dinner " + d + ", week " + p.Week + "}"
}
