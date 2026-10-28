package server

import (
	"database/sql"
	"net/http"
	"strings"
	"testing"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/household"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

// TestPatchMealSourceSetAndReverse: a household member can mark a dinner
// Delivery or Dining Out, and mark it cooked at home again. The stored
// value is the one sent; an unknown source is refused and changes nothing.
func TestPatchMealSourceSetAndReverse(t *testing.T) {
	t.Parallel()
	router, st := newDinnersRouter(t)
	id := createDinner(t, router, `{"name":"Pizza night"}`)

	rec, body := callDinners(t, router, http.MethodPatch, dinnersPath+"/"+id, `{"meal_source":"delivery"}`)
	if rec.Code != http.StatusOK || body["meal_source"] != "delivery" {
		t.Fatalf("set delivery = %d %v, want 200 meal_source delivery", rec.Code, body)
	}
	rec, body = callDinners(t, router, http.MethodPatch, dinnersPath+"/"+id, `{"meal_source":"dining_out"}`)
	if rec.Code != http.StatusOK || body["meal_source"] != "dining_out" {
		t.Fatalf("set dining out = %d %v, want 200 meal_source dining_out", rec.Code, body)
	}
	rec, body = callDinners(t, router, http.MethodPatch, dinnersPath+"/"+id, `{"meal_source":"cook"}`)
	if rec.Code != http.StatusOK || body["meal_source"] != "cook" {
		t.Fatalf("reverse to cook = %d %v, want 200 meal_source cook", rec.Code, body)
	}
	var stored string
	if err := st.Reader.QueryRow(`SELECT meal_source FROM dinners WHERE id = ?`, id).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != "cook" {
		t.Errorf("stored meal_source = %q, want cook", stored)
	}

	rec, body = callDinners(t, router, http.MethodPatch, dinnersPath+"/"+id, `{"meal_source":"takeout"}`)
	if rec.Code != http.StatusBadRequest || errorCode(body) != "invalid_dinner" {
		t.Fatalf("bad meal_source = %d %v, want 400 invalid_dinner", rec.Code, body)
	}
	if err := st.Reader.QueryRow(`SELECT meal_source FROM dinners WHERE id = ?`, id).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != "cook" {
		t.Errorf("after a refused source, meal_source = %q, want cook", stored)
	}
}

// TestPatchMealSourceRefusedWhilePlanned: marking a dinner Delivery while it
// is on today's plan is 409 planned and changes nothing. A dinner that is
// only on a past plan can be marked Delivery.
func TestPatchMealSourceRefusedWhilePlanned(t *testing.T) {
	t.Parallel()
	router, st := newDinnersRouter(t)
	id := createDinner(t, router, `{"name":"Pizza night"}`)
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	today := household.Today(time.Now(), loc)
	start := household.WeekStart(today)
	y, m, d := start.Date()
	past := store.LocalDateOf(time.Date(y, m, d-7, 0, 0, 0, 0, time.UTC))
	planDinner(t, st, id, past, past.String())

	rec, body := callDinners(t, router, http.MethodPatch, dinnersPath+"/"+id, `{"meal_source":"delivery"}`)
	if rec.Code != http.StatusOK || body["meal_source"] != "delivery" {
		t.Fatalf("past plan only = %d %v, want 200 delivery", rec.Code, body)
	}
	rec, body = callDinners(t, router, http.MethodPatch, dinnersPath+"/"+id, `{"meal_source":"cook"}`)
	if rec.Code != http.StatusOK || body["meal_source"] != "cook" {
		t.Fatalf("back to cook = %d %v, want 200 cook", rec.Code, body)
	}

	planDinner(t, st, id, start, today.String())
	rec, body = callDinners(t, router, http.MethodPatch, dinnersPath+"/"+id, `{"meal_source":"delivery"}`)
	msg, _ := body["error"].(map[string]any)["message"].(string)
	if rec.Code != http.StatusConflict || errorCode(body) != "planned" || !strings.Contains(msg, "Pizza night") || !strings.Contains(msg, "on the plan") || !strings.Contains(msg, "removed from the plan first") {
		t.Fatalf("planned dinner = %d %v, want 409 planned naming the dinner and the plan", rec.Code, body)
	}
	var stored string
	if err := st.Reader.QueryRow(`SELECT meal_source FROM dinners WHERE id = ?`, id).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != "cook" {
		t.Errorf("meal_source = %q, want cook", stored)
	}
	var planned string
	if err := st.Reader.QueryRow(`SELECT dinner_id FROM plan_days WHERE date = ?`, today.String()).Scan(&planned); err != nil {
		t.Fatal(err)
	}
	if planned != id {
		t.Errorf("today's dinner = %q, want %s", planned, id)
	}
}

// planDinner plans dinner id on date of the week starting weekStart.
func planDinner(t *testing.T, st *store.Store, id string, weekStart store.LocalDate, date string) {
	t.Helper()
	q := db.New(st.Writer)
	ctx := t.Context()
	now := store.Now().String()
	row, err := q.GetWeekPlan(ctx, weekStart.String())
	if err != nil {
		planID := store.NewID()
		if err := q.CreateWeekPlan(ctx, db.CreateWeekPlanParams{
			ID: planID, WeekStart: weekStart.String(), CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
		row.ID = planID
	}
	if err := q.PutPlanDay(ctx, db.PutPlanDayParams{
		ID: store.NewID(), PlanID: row.ID, Date: date,
		DinnerID: sql.NullString{String: id, Valid: true}, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
}
