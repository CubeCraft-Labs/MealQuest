package server

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
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
// in recipe order (not row order), its source site and the reason it was
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
		Source:          &dinners.Source{SiteName: str("Curry House"), URL: str(curryPage)},
		GroundingStatus: "grounded",
		WhyPicked:       str(reason),
		MealSource:      dinners.MealSourceCook,
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
		MealSource:      dinners.MealSourceCook,
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

// TestGetDinnerSourceURLOnlyWhenSafe: a recipe grounded in a clean https
// link shows it as source.url; one whose stored link carries a token in its
// query or userinfo, is http, or has a fragment shows its site name but
// source.url null and nothing of the link.
func TestGetDinnerSourceURLOnlyWhenSafe(t *testing.T) {
	t.Parallel()
	e := newDinnersEnv(t)
	const (
		clean      = "https://kitchen.example/recipes/chicken-curry"
		queryToken = "query-token-def456"
		queryPage  = "https://secret-query.example/curry?access_token=" + queryToken
	)
	e.pages.Bodies[clean] = recipestest.RecipePage("Chicken Curry", "Curry House", "https://a.example/curry.jpg", 40)
	e.pages.Bodies[queryPage] = recipestest.RecipePage("Weeknight Curry", "B Kitchen", "https://b.example/curry.jpg", 25)
	e.images.JPEGs["https://a.example/curry.jpg"] = recipestest.JPEG(t, 400, 320, 1)
	e.images.JPEGs["https://b.example/curry.jpg"] = recipestest.JPEG(t, 400, 320, 2)
	ground := func(dish, page string) (id string) {
		t.Helper()
		id = createDinner(t, e.h, `{"name":"`+dish+`"}`) // runner not started: stays pending
		e.model.QueueGenerate(recipestest.NormalizedReply("stovetop", 25))
		if rec, _ := callDinners(t, e.h, http.MethodPost, dinnersPath+"/"+id+"/ground", `{"recipe_url":"`+page+`"}`); rec.Code != http.StatusOK {
			t.Fatalf("POST ground %s = %d: %s", page, rec.Code, rec.Body)
		}
		var stored string
		if err := e.st.Reader.QueryRow(`SELECT source_url FROM recipes WHERE id = (SELECT recipe_id FROM dinners WHERE id = ?)`, id).Scan(&stored); err != nil {
			t.Fatal(err)
		}
		if stored != page {
			t.Fatalf("stored source_url = %q, want %q", stored, page)
		}
		return id
	}
	source := func(what, id string) map[string]any {
		t.Helper()
		raw, _ := getDetail(t, e.h, id)
		src, ok := raw["source"].(map[string]any)
		if !ok {
			t.Fatalf("source of %s = %v, want an object", what, raw["source"])
		}
		return src
	}

	cleanID := ground("Chicken curry", clean)
	if src := source("a clean link", cleanID); !reflect.DeepEqual(src, map[string]any{"site_name": "Curry House", "url": clean}) {
		t.Errorf("source of a clean link = %v, want site_name Curry House and url %s", src, clean)
	}

	queryID := ground("Weeknight curry", queryPage)
	withheld := func(what, id, site string, secrets ...string) {
		t.Helper()
		rec, _ := callDinners(t, e.h, http.MethodGet, dinnersPath+"/"+id, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d: %s", what, rec.Code, rec.Body)
		}
		for _, secret := range secrets {
			if strings.Contains(rec.Body.String(), secret) {
				t.Errorf("detail of %s contains %q: %s", what, secret, rec.Body)
			}
		}
		if src := source(what, id); !reflect.DeepEqual(src, map[string]any{"site_name": site, "url": nil}) {
			t.Errorf("source of %s = %v, want site_name %q and url null", what, src, site)
		}
	}
	withheld("a link with a query token", queryID, "B Kitchen", queryPage, "secret-query.example", queryToken, "access_token")

	// The other unsafe shapes, stored as a link grounded elsewhere would
	// leave them.
	for _, tc := range []struct{ what, link string }{
		{"a link with a userinfo token", "https://user-token-ghi789@kitchen.example/recipes/chicken-curry"},
		{"a link with a userinfo password", "https://cook:password-jkl012@kitchen.example/recipes/chicken-curry"},
		{"an http link", "http://kitchen.example/recipes/chicken-curry"},
		{"a link with a fragment", "https://kitchen.example/recipes/chicken-curry#token=fragment-mno345"},
	} {
		if _, err := e.st.Writer.Exec(`UPDATE recipes SET source_url = ? WHERE id = (SELECT recipe_id FROM dinners WHERE id = ?)`, tc.link, cleanID); err != nil {
			t.Fatal(err)
		}
		withheld(tc.what, cleanID, "Curry House", tc.link, "kitchen.example", "-token-", "password", "fragment")
	}

	// web/src/dinners/testdata/source_url_cases.json pins source as this
	// handler sends it for each stored link; the recipe screen's tests
	// render each one, so a link the server sends and the client refuses
	// fails one side or the other.
	fixture, err := os.ReadFile("../../web/src/dinners/testdata/source_url_cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var shared struct {
		Cases []struct {
			Stored string
			Source map[string]any
		}
	}
	if err := json.Unmarshal(fixture, &shared); err != nil {
		t.Fatal(err)
	}
	if len(shared.Cases) == 0 {
		t.Fatal("source_url_cases.json has no cases")
	}
	for _, c := range shared.Cases {
		if _, err := e.st.Writer.Exec(`UPDATE recipes SET source_url = ? WHERE id = (SELECT recipe_id FROM dinners WHERE id = ?)`, c.Stored, cleanID); err != nil {
			t.Fatal(err)
		}
		if src := source(c.Stored, cleanID); !reflect.DeepEqual(src, c.Source) {
			got, _ := json.Marshal(src)
			want, _ := json.Marshal(c.Source)
			t.Errorf("source of %s = %s, want %s", c.Stored, got, want)
		}
	}
}

// TestGetDinnerSiteNameNeverFromLink: a page that names no site is stored
// under its host, so a recipe grounded in a pasted link whose host carries a
// token has site_name null and nothing from the link in its detail; a page
// that names its site keeps that name, even on a token-bearing host; and a
// stored name shaped like another host, as a redirect leaves, is withheld.
// The links carry a query, so source.url is withheld too and nothing of the
// link may appear.
func TestGetDinnerSiteNameNeverFromLink(t *testing.T) {
	t.Parallel()
	e := newDinnersEnv(t)
	const (
		bareToken  = "private-token-abc123"
		namedToken = "named-token-def456"
		bare       = "https://" + bareToken + ".example.com/recipe?print=1"
		named      = "https://www." + namedToken + ".example.com/recipe?print=1"
	)
	e.pages.Bodies[bare] = recipestest.RecipePage("Chicken Curry", "", "https://a.example/curry.jpg", 40)
	e.pages.Bodies[named] = recipestest.RecipePage("Weeknight Curry", "Curry House", "https://b.example/curry.jpg", 25)
	e.images.JPEGs["https://a.example/curry.jpg"] = recipestest.JPEG(t, 400, 320, 1)
	e.images.JPEGs["https://b.example/curry.jpg"] = recipestest.JPEG(t, 400, 320, 2)
	ground := func(dish, page string) (id, recipeID string) {
		t.Helper()
		id = createDinner(t, e.h, `{"name":"`+dish+`"}`) // runner not started: stays pending
		e.model.QueueGenerate(recipestest.NormalizedReply("stovetop", 25))
		if rec, _ := callDinners(t, e.h, http.MethodPost, dinnersPath+"/"+id+"/ground", `{"recipe_url":"`+page+`"}`); rec.Code != http.StatusOK {
			t.Fatalf("POST ground %s = %d: %s", page, rec.Code, rec.Body)
		}
		if err := e.st.Reader.QueryRow(`SELECT recipe_id FROM dinners WHERE id = ?`, id).Scan(&recipeID); err != nil {
			t.Fatal(err)
		}
		return id, recipeID
	}
	storedSite := func(recipeID string) string {
		t.Helper()
		var site string
		if err := e.st.Reader.QueryRow(`SELECT site_name FROM recipes WHERE id = ?`, recipeID).Scan(&site); err != nil {
			t.Fatal(err)
		}
		return site
	}
	check := func(what, id string, leaks []string, want any) {
		t.Helper()
		rec, _ := callDinners(t, e.h, http.MethodGet, dinnersPath+"/"+id, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d: %s", what, rec.Code, rec.Body)
		}
		for _, leak := range leaks {
			if strings.Contains(strings.ToLower(rec.Body.String()), leak) {
				t.Errorf("detail of %s contains %q: %s", what, leak, rec.Body)
			}
		}
		raw, _ := getDetail(t, e.h, id)
		if src, _ := raw["source"].(map[string]any); !reflect.DeepEqual(src, map[string]any{"site_name": want, "url": nil}) {
			t.Errorf("source of %s = %v, want site_name %v and url null", what, raw["source"], want)
		}
	}

	bareID, bareRecipe := ground("Chicken curry", bare)
	if got := storedSite(bareRecipe); got != bareToken+".example.com" {
		t.Fatalf("stored site_name = %q, want the link's host", got)
	}
	check("a page naming no site", bareID, []string{bare, bareToken, "example.com"}, nil)

	namedID, namedRecipe := ground("Weeknight curry", named)
	if got := storedSite(namedRecipe); got != "Curry House" {
		t.Fatalf("stored site_name = %q, want the page's", got)
	}
	check("a page naming its site", namedID, []string{named, namedToken, "example.com"}, "Curry House")

	const redirectHost = "redirect-token-ghi789.example.net"
	if _, err := e.st.Writer.Exec(`UPDATE recipes SET site_name = ? WHERE id = ?`, redirectHost, namedRecipe); err != nil {
		t.Fatal(err)
	}
	check("a page named by a redirect's host", namedID, []string{redirectHost, "redirect-token", namedToken}, nil)
}

// TestGetDinnerWhyPickedIsCreatingSuggestion: why_picked is the reason of
// the suggestion that created the dinner, never that of an earlier
// suggestion row for it that was declined.
func TestGetDinnerWhyPickedIsCreatingSuggestion(t *testing.T) {
	t.Parallel()
	e := newDinnersEnv(t)
	id := createDinner(t, e.h, `{"name":"Chicken curry"}`)
	if _, err := e.st.Writer.Exec(`UPDATE dinners SET status = 'quest_log', origin = 'side_quest' WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	q := db.New(e.st.Writer)
	now := store.Now().String()
	batch, err := q.CreateSuggestionBatch(t.Context(), db.CreateSuggestionBatchParams{ID: store.NewID(), Trigger: "manual", CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	const reason = "Like your Tikka masala, but quicker."
	// The declined row is created first, so it is the earliest by both
	// created_at and id.
	for _, s := range []struct{ reason, status string }{
		{"Passed on last week.", "declined"},
		{reason, "accepted"},
	} {
		sid := store.NewID()
		if err := q.CreateSuggestion(t.Context(), db.CreateSuggestionParams{
			ID: sid, DinnerID: id, BatchID: batch.ID, Name: "Chicken curry",
			Reason: s.reason, CitedRegularIds: "[]", CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
		if n, err := q.DecideSuggestion(t.Context(), db.DecideSuggestionParams{
			Status: s.status, Now: sql.NullString{String: now, Valid: true}, ID: sid,
		}); err != nil || n != 1 {
			t.Fatalf("decide %s = %d, %v", s.status, n, err)
		}
	}

	if _, got := getDetail(t, e.h, id); got.WhyPicked == nil || *got.WhyPicked != reason {
		t.Errorf("why_picked = %v, want %q", got.WhyPicked, reason)
	}
}

// TestGetDinnerDuplicatePositionFails: a recipe with two ingredients or two
// steps at one position has no order anyone can trust, so its detail is a
// 500 rather than the recipe in some order.
func TestGetDinnerDuplicatePositionFails(t *testing.T) {
	t.Parallel()
	e := newDinnersEnv(t)
	for _, table := range []string{"recipe_ingredients", "recipe_steps"} {
		id := createDinner(t, e.h, `{"name":"Family chili `+table+`"}`)
		if rec, _ := callDinners(t, e.h, http.MethodPost, dinnersPath+"/"+id+"/ground",
			`{"manual":{"ingredients":["1 lb beef","1 can beans"],"steps":["Brown.","Simmer."],"minutes":240,"method":"crockpot"}}`); rec.Code != http.StatusOK {
			t.Fatalf("POST ground = %d: %s", rec.Code, rec.Body)
		}
		if _, err := e.st.Writer.Exec(`UPDATE `+table+` SET position = 0 WHERE recipe_id = (SELECT recipe_id FROM dinners WHERE id = ?)`, id); err != nil {
			t.Fatal(err)
		}
		rec, _ := callDinners(t, e.h, http.MethodGet, dinnersPath+"/"+id, "")
		if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "Simmer.") || strings.Contains(rec.Body.String(), "1 lb beef") {
			t.Errorf("GET with duplicate %s positions = %d: %s, want 500 and no recipe", table, rec.Code, rec.Body)
		}
	}
}
