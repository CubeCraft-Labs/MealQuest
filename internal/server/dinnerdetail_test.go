package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/dinners"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/recipes/recipestest"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

// getDetail answers GET /dinners/{id} for id, failing unless it is 200, and
// returns the body both raw (to tell null from absent) and decoded.
func getDetail(t *testing.T, h http.Handler, id string) (map[string]any, dinners.Detail) {
	t.Helper()
	rec, raw := callDinners(t, h, http.MethodGet, dinnersPath+"/"+id, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET dinner = %d: %s", rec.Code, rec.Body)
	}
	var d dinners.Detail
	dec := json.NewDecoder(rec.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	return raw, d
}

// wantNull fails unless each key is present in raw and null.
func wantNull(t *testing.T, raw map[string]any, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if v, ok := raw[k]; !ok || v != nil {
			t.Errorf("%s = %v (present %v), want null", k, v, ok)
		}
	}
}

// TestGetDinnerGroundedSideQuest: a Side quest grounded in a fetched page
// shows its photo at both stored sizes, its recipe's ingredients and steps
// in recipe order (not row order), its source page and the reason it was
// suggested.
func TestGetDinnerGroundedSideQuest(t *testing.T) {
	t.Parallel()
	e := newDinnersEnv(t).withRecipes(t)
	e.model.QueueGenerate(recipestest.NormalizedReply("stovetop", 40))
	e.start(t)
	rec, created := callDinners(t, e.h, http.MethodPost, dinnersPath, `{"name":"Chicken curry"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST = %d: %s", rec.Code, rec.Body)
	}
	if job := waitJob(t, e.h, created["grounding_job_id"].(string)); job["status"] != "succeeded" {
		t.Fatalf("grounding job = %v", job)
	}
	id := created["id"].(string)

	// Make it an accepted Side quest with the suggestion that created it,
	// and reverse its recipe's positions so row order disagrees with
	// recipe order.
	var recipeID, photoID string
	if err := e.st.Reader.QueryRow(`SELECT recipe_id, photo_id FROM dinners WHERE id = ?`, id).Scan(&recipeID, &photoID); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`UPDATE dinners SET status = 'quest_log', origin = 'side_quest', is_favorite = 1 WHERE id = ?`,
		`UPDATE recipe_ingredients SET position = 1 - position WHERE recipe_id = (SELECT recipe_id FROM dinners WHERE id = ?)`,
		`UPDATE recipe_steps SET position = 1 - position WHERE recipe_id = (SELECT recipe_id FROM dinners WHERE id = ?)`,
	} {
		if _, err := e.st.Writer.Exec(stmt, id); err != nil {
			t.Fatal(err)
		}
	}
	q := db.New(e.st.Writer)
	now := store.Now().String()
	batch, err := q.CreateSuggestionBatch(t.Context(), db.CreateSuggestionBatchParams{ID: store.NewID(), Trigger: "manual", CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	const reason = "Like your Tikka masala, but quicker."
	if err := q.CreateSuggestion(t.Context(), db.CreateSuggestionParams{
		ID: store.NewID(), DinnerID: id, BatchID: batch.ID, Name: "Chicken curry",
		Reason: reason, CitedRegularIds: "[]", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	_, got := getDetail(t, e.h, id)
	str := func(s string) *string { return &s }
	minutes := int64(40)
	want := dinners.Detail{
		ID:           id,
		Name:         "Chicken curry",
		Status:       "quest_log",
		Origin:       "side_quest",
		Method:       str("stovetop"),
		MinutesTotal: &minutes,
		Difficulty:   str("easy"),
		IsFavorite:   true,
		PhotoURLs: map[string]string{
			"400":  "/media/photos/" + photoID + "-400.jpg",
			"1200": "/media/photos/" + photoID + "-1200.jpg",
		},
		Ingredients: []dinners.Ingredient{
			{Name: str("garlic"), Qty: str("2"), Unit: str("cloves"), Note: str("minced"), RawText: "2 cloves garlic, minced"},
			{Name: str("onion"), Qty: str("1"), RawText: "1 onion"},
		},
		Steps:           []string{"Fry with the garlic.", "Chop the onion."},
		Source:          &dinners.Source{URL: curryPage, SiteName: str("Curry House")},
		GroundingStatus: "grounded",
		WhyPicked:       str(reason),
	}
	if !reflect.DeepEqual(got, want) {
		gj, _ := json.Marshal(got)
		wj, _ := json.Marshal(want)
		t.Errorf("detail =\n %s\nwant\n %s", gj, wj)
	}
	// Each photo URL is served.
	for _, u := range got.PhotoURLs {
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, signedIn(http.MethodGet, u, nil))
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/jpeg" {
			t.Errorf("GET %s = %d %s", u, rec.Code, rec.Header().Get("Content-Type"))
		}
	}
}

// TestGetDinnerManualRegular: a Regular added by hand and given a recipe
// typed in has no photo, no source and no reason, and its ingredients are
// the typed lines with nothing normalized.
func TestGetDinnerManualRegular(t *testing.T) {
	t.Parallel()
	e := newDinnersEnv(t)
	id := createDinner(t, e.h, `{"name":"Family chili"}`)
	if rec, _ := callDinners(t, e.h, http.MethodPost, dinnersPath+"/"+id+"/ground",
		`{"manual":{"ingredients":["1 lb beef","1 can beans"],"steps":["Brown.","Simmer."],"minutes":240,"method":"crockpot"}}`); rec.Code != http.StatusOK {
		t.Fatalf("POST ground = %d: %s", rec.Code, rec.Body)
	}

	raw, got := getDetail(t, e.h, id)
	wantNull(t, raw, "photo_urls", "source", "why_picked", "difficulty", "grounding_error")
	str := func(s string) *string { return &s }
	minutes := int64(240)
	want := dinners.Detail{
		ID:              id,
		Name:            "Family chili",
		Status:          "regular",
		Origin:          "regular",
		Method:          str("crockpot"),
		MinutesTotal:    &minutes,
		Ingredients:     []dinners.Ingredient{{RawText: "1 lb beef"}, {RawText: "1 can beans"}},
		Steps:           []string{"Brown.", "Simmer."},
		GroundingStatus: "manual",
	}
	if !reflect.DeepEqual(got, want) {
		gj, _ := json.Marshal(got)
		wj, _ := json.Marshal(want)
		t.Errorf("detail =\n %s\nwant\n %s", gj, wj)
	}
}

// TestGetDinnerFailedGrounding: a dinner whose grounding failed shows its
// real state — failed with its reason, no photo, no source and empty
// ingredients and steps — never a pretend recipe.
func TestGetDinnerFailedGrounding(t *testing.T) {
	t.Parallel()
	e := newDinnersEnv(t).withRecipes(t)
	e.start(t)
	rec, created := callDinners(t, e.h, http.MethodPost, dinnersPath, `{"name":"Nothing matches this"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST = %d: %s", rec.Code, rec.Body)
	}
	if job := waitJob(t, e.h, created["grounding_job_id"].(string)); job["status"] != "failed" {
		t.Fatalf("grounding job = %v, want failed", job)
	}

	raw, got := getDetail(t, e.h, created["id"].(string))
	wantNull(t, raw, "photo_urls", "source", "why_picked", "method", "minutes_total", "difficulty")
	if ings, ok := raw["ingredients"].([]any); !ok || len(ings) != 0 {
		t.Errorf("ingredients = %v, want []", raw["ingredients"])
	}
	if steps, ok := raw["steps"].([]any); !ok || len(steps) != 0 {
		t.Errorf("steps = %v, want []", raw["steps"])
	}
	if got.GroundingStatus != "failed" || got.GroundingError == nil || *got.GroundingError != dinners.ReasonNoRecipe ||
		got.Name != "Nothing matches this" || got.Status != "regular" || got.Origin != "regular" {
		t.Errorf("detail = %+v, want the failed Regular with no_recipe_found", got)
	}
}

// TestGetDinnerNotFound: an unknown dinner and an archived one are both
// 404, and the detail needs a session like every /api/v1 route.
func TestGetDinnerNotFound(t *testing.T) {
	t.Parallel()
	e := newDinnersEnv(t)
	id := createDinner(t, e.h, `{"name":"Old favourite"}`)
	if rec, _ := callDinners(t, e.h, http.MethodDelete, dinnersPath+"/"+id, ""); rec.Code != http.StatusOK {
		t.Fatalf("DELETE = %d: %s", rec.Code, rec.Body)
	}
	for _, target := range []string{id, store.NewID()} {
		rec, body := callDinners(t, e.h, http.MethodGet, dinnersPath+"/"+target, "")
		if rec.Code != http.StatusNotFound || errorCode(body) != "not_found" {
			t.Errorf("GET %s = %d %v, want 404 not_found", target, rec.Code, body)
		}
	}

	live := createDinner(t, e.h, `{"name":"Taco night"}`)
	req := httptest.NewRequest(http.MethodGet, dinnersPath+"/"+live, nil)
	if rec, body := serve(t, e.h, req); rec.Code != http.StatusUnauthorized || errorCode(body) != "unauthenticated" {
		t.Errorf("GET without a session = %d %v, want 401", rec.Code, body)
	}
}
