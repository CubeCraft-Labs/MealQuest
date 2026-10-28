package server

import (
	"net/http"
	"testing"
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
