package server

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/recipes"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/recipes/recipestest"
)

// Fixture pages: two curries found by searching "Chicken curry" and a stew
// at a pasted link.
const (
	curryPage  = "https://a.example/chicken-curry"
	curry2Page = "https://b.example/curry"
	stewPage   = "https://c.example/recipes/beef-stew"
)

// withRecipes adds the fixture pages, their photos and the search for
// "Chicken curry" to e.
func (e *dinnersEnv) withRecipes(t *testing.T) *dinnersEnv {
	t.Helper()
	e.search.Results["Chicken curry"] = []recipes.Candidate{{URL: curryPage}, {URL: curry2Page}}
	e.pages.Bodies[curryPage] = recipestest.RecipePage("Chicken Curry", "Curry House", "https://a.example/curry.jpg", 40)
	e.pages.Bodies[curry2Page] = recipestest.RecipePage("Weeknight Curry", "B Kitchen", "https://b.example/curry.jpg", 25)
	e.pages.Bodies[stewPage] = recipestest.RecipePage("Beef Stew", "Stew Co", "https://c.example/stew.jpg", 150)
	e.images.JPEGs["https://a.example/curry.jpg"] = recipestest.JPEG(t, 400, 320, 1)
	e.images.JPEGs["https://b.example/curry.jpg"] = recipestest.JPEG(t, 400, 320, 2)
	e.images.JPEGs["https://c.example/stew.jpg"] = recipestest.JPEG(t, 400, 320, 3)
	return e
}

// waitJob polls GET /jobs/{id} until the job ends and returns its body.
func waitJob(t *testing.T, h http.Handler, id string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		rec, body := callDinners(t, h, http.MethodGet, "/api/v1/jobs/"+id, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET job = %d: %s", rec.Code, rec.Body)
		}
		if s := body["status"]; s == "succeeded" || s == "failed" {
			return body
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s never finished; last %v", id, body)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// listedDinner returns dinner id as GET /dinners?status=regular lists it.
func listedDinner(t *testing.T, h http.Handler, id string) map[string]any {
	t.Helper()
	rec, body := callDinners(t, h, http.MethodGet, dinnersPath+"?status=regular", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET dinners = %d: %s", rec.Code, rec.Body)
	}
	for _, d := range body["dinners"].([]any) {
		if d := d.(map[string]any); d["id"] == id {
			return d
		}
	}
	t.Fatalf("dinner %s not listed", id)
	return nil
}

// candidateID returns the candidate_id GET recipe-candidates offers dinner
// id for the page titled title.
func candidateID(t *testing.T, h http.Handler, id, title string) string {
	t.Helper()
	rec, body := callDinners(t, h, http.MethodGet, dinnersPath+"/"+id+"/recipe-candidates", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET candidates = %d: %s", rec.Code, rec.Body)
	}
	for _, c := range body["candidates"].([]any) {
		if c := c.(map[string]any); c["title"] == title {
			cid := c["candidate_id"].(string)
			if strings.Contains(cid, "://") {
				t.Fatalf("candidate_id = %q, want an opaque id, not the page URL", cid)
			}
			return cid
		}
	}
	t.Fatalf("candidates = %v, want one titled %q", body, title)
	return ""
}

func TestPostDinnerReturnsBeforeGroundingCompletes(t *testing.T) {
	t.Parallel()
	e := newDinnersEnv(t).withRecipes(t)
	e.search.Gate = make(chan struct{})
	e.model.QueueGenerate(recipestest.NormalizedReply("stovetop", 40))
	e.start(t)

	rec, created := callDinners(t, e.h, http.MethodPost, dinnersPath, `{"name":"Chicken curry"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST = %d: %s", rec.Code, rec.Body)
	}
	jobID, _ := created["grounding_job_id"].(string)
	if created["grounding_status"] != "pending" || created["grounding_error"] != nil || jobID == "" {
		t.Fatalf("created = %v, want pending with a grounding_job_id", created)
	}
	// The search is held, so grounding cannot have completed.
	_, job := callDinners(t, e.h, http.MethodGet, "/api/v1/jobs/"+jobID, "")
	if s := job["status"]; s != "queued" && s != "running" {
		t.Fatalf("job right after POST = %v, want queued or running", job)
	}
	if d := listedDinner(t, e.h, created["id"].(string)); d["grounding_status"] != "pending" {
		t.Fatalf("dinner right after POST = %v, want pending", d)
	}

	close(e.search.Gate)
	job = waitJob(t, e.h, jobID)
	if job["status"] != "succeeded" || job["kind"] != "ground_dinner" {
		t.Fatalf("job = %v, want ground_dinner succeeded", job)
	}
	d := listedDinner(t, e.h, created["id"].(string))
	photo, _ := d["photo_url"].(string)
	if d["grounding_status"] != "grounded" || d["recipe_id"] == nil || !strings.HasSuffix(photo, "-400.jpg") ||
		d["method"] != "stovetop" || d["minutes_total"] != 40.0 || d["difficulty"] != "easy" || d["grounding_error"] != nil {
		t.Errorf("dinner = %v, want grounded with recipe, photo, method, minutes and difficulty", d)
	}
}

func TestPostDinnerGroundingFailure(t *testing.T) {
	t.Parallel()
	e := newDinnersEnv(t).withRecipes(t)
	e.start(t)

	rec, created := callDinners(t, e.h, http.MethodPost, dinnersPath, `{"name":"Nothing matches this"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST = %d: %s", rec.Code, rec.Body)
	}
	job := waitJob(t, e.h, created["grounding_job_id"].(string))
	if job["status"] != "failed" || job["error"] != "no_recipe_found" {
		t.Errorf("job = %v, want failed with no_recipe_found", job)
	}
	d := listedDinner(t, e.h, created["id"].(string))
	if d["grounding_status"] != "failed" || d["grounding_error"] != "no_recipe_found" || d["recipe_id"] != nil {
		t.Errorf("dinner = %v, want failed with no_recipe_found", d)
	}
}

func TestGetRecipeCandidates(t *testing.T) {
	t.Parallel()
	e := newDinnersEnv(t).withRecipes(t)
	id := createDinner(t, e.h, `{"name":"Chicken curry"}`) // runner not started: stays pending

	rec, body := callDinners(t, e.h, http.MethodGet, dinnersPath+"/"+id+"/recipe-candidates", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d: %s", rec.Code, rec.Body)
	}
	list, _ := body["candidates"].([]any)
	if len(list) != 2 {
		t.Fatalf("candidates = %v, want 2", body)
	}
	first := list[0].(map[string]any)
	if cid, _ := first["candidate_id"].(string); cid == "" || strings.Contains(cid, "a.example") {
		t.Errorf("candidate_id = %#v, want an opaque id", first["candidate_id"])
	}
	want := map[string]any{
		"candidate_id": first["candidate_id"],
		"title":        "Chicken Curry",
		"site_name":    "Curry House",
		"photo_url":    nil,
		"minutes":      40.0,
	}
	if len(first) != len(want) {
		t.Errorf("candidate keys = %v, want exactly %v", first, want)
	}
	for k, v := range want {
		if got, ok := first[k]; !ok || got != v {
			t.Errorf("%s = %#v (present %v), want %#v", k, got, ok, v)
		}
	}

	rec, body = callDinners(t, e.h, http.MethodGet, dinnersPath+"/nope/recipe-candidates", "")
	if rec.Code != http.StatusNotFound || errorCode(body) != "not_found" {
		t.Errorf("unknown dinner = %d %v, want 404", rec.Code, body)
	}
}

// TestGetRecipeCandidatesSiteNames: a candidate's site_name follows the
// recipe detail's rule (dinners.SourceOf), not a hostname fallback of its
// own: a name the page gave is offered as it stands, even over a link with
// a token; a page naming no site is named by its link's host only when
// the link is free of secrets, and is null over a token-bearing link, so
// nothing of that link reaches the client.
func TestGetRecipeCandidatesSiteNames(t *testing.T) {
	t.Parallel()
	e := newDinnersEnv(t)
	const (
		token    = "offer-token-abc123"
		named    = "https://named.example/curry?access_token=" + token
		bare     = "https://" + token + ".example.com/curry?print=1"
		ordinary = "https://www.kitchen.example/recipes/curry"
	)
	e.search.Results["Chicken curry"] = []recipes.Candidate{{URL: named}, {URL: bare}, {URL: ordinary}}
	e.pages.Bodies[named] = recipestest.RecipePage("Named Curry", "Curry House", "https://a.example/curry.jpg", 40)
	e.pages.Bodies[bare] = recipestest.RecipePage("Bare Curry", "", "https://b.example/curry.jpg", 30)
	e.pages.Bodies[ordinary] = recipestest.RecipePage("Ordinary Curry", "", "https://c.example/curry.jpg", 25)
	id := createDinner(t, e.h, `{"name":"Chicken curry"}`) // runner not started: stays pending

	rec, body := callDinners(t, e.h, http.MethodGet, dinnersPath+"/"+id+"/recipe-candidates", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d: %s", rec.Code, rec.Body)
	}
	for _, leak := range []string{token, "access_token", "example.com"} {
		if strings.Contains(rec.Body.String(), leak) {
			t.Errorf("candidates contain %q: %s", leak, rec.Body)
		}
	}
	want := map[string]any{"Named Curry": "Curry House", "Bare Curry": nil, "Ordinary Curry": "kitchen.example"}
	list, _ := body["candidates"].([]any)
	if len(list) != len(want) {
		t.Fatalf("candidates = %v, want %d", body, len(want))
	}
	for _, c := range list {
		c := c.(map[string]any)
		title, _ := c["title"].(string)
		site, ok := c["site_name"]
		if w, known := want[title]; !known || !ok || site != w {
			t.Errorf("candidate %q site_name = %#v (present %v), want %#v", title, site, ok, w)
		}
	}
}

func TestGetRecipeCandidatesSearchFails(t *testing.T) {
	t.Parallel()
	e := newDinnersEnv(t)
	// The provider's text must reach neither the response nor the logs.
	const leak = "provider-said-token-abc123"
	e.search.Err = fmt.Errorf("%w: %s", recipes.ErrSearchRefused, leak)
	id := createDinner(t, e.h, `{"name":"Chicken curry"}`)
	rec, body := callDinners(t, e.h, http.MethodGet, dinnersPath+"/"+id+"/recipe-candidates", "")
	if rec.Code != http.StatusBadGateway || errorCode(body) != "search_failed" {
		t.Errorf("GET = %d %v, want 502 search_failed", rec.Code, body)
	}
	if strings.Contains(rec.Body.String(), leak) {
		t.Errorf("response quotes the provider: %s", rec.Body)
	}
	logs := e.logs.String()
	if strings.Contains(logs, leak) || !strings.Contains(logs, "reason=search_failed") {
		t.Errorf("logs = %q, want reason=search_failed and no provider text", logs)
	}
}

// TestGetRecipeCandidatesStoredLookupFailure: a candidate whose stored
// recipe cannot be read, or is stored incomplete, is a 500 logged by reason
// and dinner id, never with the candidate's URL, which can carry a personal
// token, or the database error.
func TestGetRecipeCandidatesStoredLookupFailure(t *testing.T) {
	t.Parallel()
	const (
		token = "personal-token-abc123"
		host  = "secret-candidate.example"
		page  = "https://" + host + "/curry?access_token=" + token
	)
	for _, tc := range []struct {
		name, sabotage string
		leaks          []string
	}{
		// Renaming a column the lookup selects fails the read.
		{"lookup", `ALTER TABLE recipes RENAME COLUMN yield TO yield_gone`, []string{"no such column", "yield"}},
		// A recipe stored without its photo, method or difficulty is
		// incomplete.
		{"incomplete", `INSERT INTO recipes (id, source_url, title, raw_jsonld, fetched_at, created_at, updated_at)
			VALUES ('r-incomplete', '` + page + `', 'Curry', '{}', 'now', 'now', 'now')`, []string{"r-incomplete", "no photo"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newDinnersEnv(t)
			e.search.Results["Chicken curry"] = []recipes.Candidate{{URL: page}}
			id := createDinner(t, e.h, `{"name":"Chicken curry"}`) // runner not started: stays pending
			if _, err := e.st.Writer.ExecContext(t.Context(), tc.sabotage); err != nil {
				t.Fatal(err)
			}

			rec, body := callDinners(t, e.h, http.MethodGet, dinnersPath+"/"+id+"/recipe-candidates", "")
			if rec.Code != http.StatusInternalServerError || errorCode(body) != "internal" {
				t.Errorf("GET = %d %s, want 500 internal", rec.Code, rec.Body)
			}
			logs := e.logs.String()
			for _, secret := range append([]string{page, host, token, "access_token"}, tc.leaks...) {
				if strings.Contains(rec.Body.String(), secret) {
					t.Errorf("response contains %q: %s", secret, rec.Body)
				}
				if strings.Contains(logs, secret) {
					t.Errorf("logs contain %q: %s", secret, logs)
				}
			}
			if !strings.Contains(logs, "reason=candidates_failed") || !strings.Contains(logs, "dinner_id="+id) {
				t.Errorf("logs = %q, want reason=candidates_failed and the dinner id", logs)
			}
		})
	}
}

func TestPostGroundViaCandidate(t *testing.T) {
	t.Parallel()
	e := newDinnersEnv(t).withRecipes(t)
	id := createDinner(t, e.h, `{"name":"Chicken curry"}`)
	e.model.QueueGenerate(recipestest.NormalizedReply("stovetop", 25))

	cid := candidateID(t, e.h, id, "Weeknight Curry")
	rec, d := callDinners(t, e.h, http.MethodPost, dinnersPath+"/"+id+"/ground", `{"candidate_id":"`+cid+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST = %d: %s", rec.Code, rec.Body)
	}
	photo, _ := d["photo_url"].(string)
	if d["grounding_status"] != "grounded" || d["recipe_id"] == nil || photo == "" ||
		d["method"] != "stovetop" || d["minutes_total"] != 25.0 || d["difficulty"] != "easy" || d["updated_by"] != testSession.Subject {
		t.Errorf("dinner = %v", d)
	}
	// The candidate is now stored, so it is offered with its photo.
	_, body := callDinners(t, e.h, http.MethodGet, dinnersPath+"/"+id+"/recipe-candidates", "")
	for _, c := range body["candidates"].([]any) {
		if c := c.(map[string]any); c["candidate_id"] == cid && c["photo_url"] != photo {
			t.Errorf("grounded candidate photo_url = %v, want %s", c["photo_url"], photo)
		}
	}
}

// TestRecipeCandidatesHideTokenURLs: a candidate page whose URL carries a
// token in its path or query is offered by an opaque id alone, which
// grounds the dinner in that page.
func TestRecipeCandidatesHideTokenURLs(t *testing.T) {
	t.Parallel()
	e := newDinnersEnv(t)
	const (
		pathToken  = "path-token-abc123"
		queryToken = "query-token-def456"
		pathPage   = "https://secret-path.example/u/" + pathToken + "/chicken-curry"
		queryPage  = "https://secret-query.example/curry?access_token=" + queryToken
	)
	e.search.Results["Chicken curry"] = []recipes.Candidate{{URL: pathPage}, {URL: queryPage}}
	e.pages.Bodies[pathPage] = recipestest.RecipePage("Chicken Curry", "Curry House", "https://a.example/curry.jpg", 40)
	e.pages.Bodies[queryPage] = recipestest.RecipePage("Weeknight Curry", "B Kitchen", "https://b.example/curry.jpg", 25)
	e.images.JPEGs["https://b.example/curry.jpg"] = recipestest.JPEG(t, 400, 320, 2)
	id := createDinner(t, e.h, `{"name":"Chicken curry"}`) // runner not started: stays pending

	rec, body := callDinners(t, e.h, http.MethodGet, dinnersPath+"/"+id+"/recipe-candidates", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d: %s", rec.Code, rec.Body)
	}
	if list, _ := body["candidates"].([]any); len(list) != 2 {
		t.Fatalf("candidates = %v, want 2", body)
	}
	for _, secret := range []string{pathPage, queryPage, "secret-path.example", "secret-query.example", pathToken, queryToken, "access_token"} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Errorf("candidates response contains %q: %s", secret, rec.Body)
		}
	}

	e.model.QueueGenerate(recipestest.NormalizedReply("stovetop", 25))
	cid := candidateID(t, e.h, id, "Weeknight Curry")
	rec, d := callDinners(t, e.h, http.MethodPost, dinnersPath+"/"+id+"/ground", `{"candidate_id":"`+cid+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST = %d: %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), queryToken) {
		t.Errorf("ground response contains the token: %s", rec.Body)
	}
	var source string
	if err := e.st.Reader.QueryRow(`SELECT source_url FROM recipes WHERE id = ?`, d["recipe_id"]).Scan(&source); err != nil {
		t.Fatal(err)
	}
	if d["grounding_status"] != "grounded" || source != queryPage {
		t.Errorf("dinner = %v in recipe %s, want grounded in %s", d, source, queryPage)
	}
}

// TestPostGroundRejectsCandidateNotOffered: a candidate_id is only one
// offered for that dinner; a page URL, or another dinner's candidate, is
// refused.
func TestPostGroundRejectsCandidateNotOffered(t *testing.T) {
	t.Parallel()
	e := newDinnersEnv(t).withRecipes(t)
	id := createDinner(t, e.h, `{"name":"Chicken curry"}`)
	other := createDinner(t, e.h, `{"name":"Pad thai"}`)
	cid := candidateID(t, e.h, id, "Weeknight Curry")
	for _, tc := range []struct{ name, dinner, cid string }{
		{"page URL", id, curry2Page},
		{"another dinner's", other, cid},
	} {
		rec, body := callDinners(t, e.h, http.MethodPost, dinnersPath+"/"+tc.dinner+"/ground", `{"candidate_id":"`+tc.cid+`"}`)
		if rec.Code != http.StatusBadRequest || errorCode(body) != "invalid_dinner" {
			t.Errorf("%s: %d %v, want 400 invalid_dinner", tc.name, rec.Code, body)
		}
	}
}

func TestPostGroundViaRecipeURL(t *testing.T) {
	t.Parallel()
	e := newDinnersEnv(t).withRecipes(t)
	id := createDinner(t, e.h, `{"name":"Sunday stew"}`)
	e.model.QueueGenerate(recipestest.NormalizedReply("oven", 150))

	rec, d := callDinners(t, e.h, http.MethodPost, dinnersPath+"/"+id+"/ground", `{"recipe_url":"`+stewPage+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST = %d: %s", rec.Code, rec.Body)
	}
	if d["grounding_status"] != "grounded" || d["photo_url"] == nil || d["method"] != "oven" || d["minutes_total"] != 150.0 {
		t.Errorf("dinner = %v", d)
	}

	rec, body := callDinners(t, e.h, http.MethodPost, dinnersPath+"/"+id+"/ground", `{"recipe_url":"https://gone.example/soup"}`)
	if rec.Code != http.StatusUnprocessableEntity || errorCode(body) != "ungroundable" || strings.Contains(rec.Body.String(), "gone.example") {
		t.Errorf("unusable link = %d %v, want 422 ungroundable", rec.Code, body)
	}
}

// TestPostGroundViaRecipeURLStorageFailure: a pasted link that fails for a
// reason other than its recipe is a 500 logged by reason and dinner id,
// never with the link, which can carry a personal token, or the dependency
// error.
func TestPostGroundViaRecipeURLStorageFailure(t *testing.T) {
	t.Parallel()
	e := newDinnersEnv(t).withRecipes(t)
	const token = "personal-token-abc123"
	link := stewPage + "?access_token=" + token
	e.pages.Bodies[link] = e.pages.Bodies[stewPage]
	id := createDinner(t, e.h, `{"name":"Sunday stew"}`)
	e.model.QueueGenerate(recipestest.NormalizedReply("oven", 150))
	if _, err := e.st.Writer.ExecContext(t.Context(),
		`CREATE TRIGGER refuse_recipes BEFORE INSERT ON recipes BEGIN SELECT RAISE(ABORT, 'disk full'); END`); err != nil {
		t.Fatal(err)
	}

	rec, body := callDinners(t, e.h, http.MethodPost, dinnersPath+"/"+id+"/ground", `{"recipe_url":"`+link+`"}`)
	if rec.Code != http.StatusInternalServerError || errorCode(body) != "internal" || strings.Contains(rec.Body.String(), token) {
		t.Errorf("POST = %d %s, want 500 internal without the link", rec.Code, rec.Body)
	}
	logs := e.logs.String()
	if strings.Contains(logs, token) || strings.Contains(logs, "c.example") || strings.Contains(logs, "disk full") ||
		!strings.Contains(logs, "reason=ground_failed") || !strings.Contains(logs, "dinner_id="+id) {
		t.Errorf("logs = %q, want reason=ground_failed and the dinner id, without the link or dependency error", logs)
	}
}

// TestPostGroundViaRecipeURLDinnerWriteFailure: a pasted link whose recipe
// is stored but whose dinner cannot be written is a 500 logged by reason
// and dinner id, never with the dependency error.
func TestPostGroundViaRecipeURLDinnerWriteFailure(t *testing.T) {
	t.Parallel()
	e := newDinnersEnv(t).withRecipes(t)
	id := createDinner(t, e.h, `{"name":"Sunday stew"}`)
	e.model.QueueGenerate(recipestest.NormalizedReply("oven", 150))
	if _, err := e.st.Writer.ExecContext(t.Context(),
		`CREATE TRIGGER refuse_grounding BEFORE UPDATE OF recipe_id ON dinners WHEN NEW.recipe_id IS NOT NULL BEGIN SELECT RAISE(ABORT, 'disk full'); END`); err != nil {
		t.Fatal(err)
	}

	rec, body := callDinners(t, e.h, http.MethodPost, dinnersPath+"/"+id+"/ground", `{"recipe_url":"`+stewPage+`"}`)
	if rec.Code != http.StatusInternalServerError || errorCode(body) != "internal" {
		t.Errorf("POST = %d %s, want 500 internal", rec.Code, rec.Body)
	}
	var stored int
	if err := e.st.Reader.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM recipes WHERE source_url = ?`, stewPage).Scan(&stored); err != nil || stored != 1 {
		t.Fatalf("recipes stored for the link = %d (%v), want 1: the failure must be the dinner write", stored, err)
	}
	logs := e.logs.String()
	if strings.Contains(logs, "disk full") || !strings.Contains(logs, "reason=ground_failed") || !strings.Contains(logs, "dinner_id="+id) {
		t.Errorf("logs = %q, want reason=ground_failed and the dinner id, without the dependency error", logs)
	}
}

// TestPostGroundUnexpectedFailures: grounding by hand that fails reading
// the dinner, or storing a manual recipe, is a 500 logged by reason and
// dinner id, never with the database error.
func TestPostGroundUnexpectedFailures(t *testing.T) {
	t.Parallel()
	const (
		// Renaming a column every dinner read selects fails the read.
		breakRead = `ALTER TABLE dinners RENAME COLUMN cuisine TO cuisine_gone`
		// The trigger's message stands in for the database error.
		breakStore = `CREATE TRIGGER refuse_recipes BEFORE INSERT ON recipes BEGIN SELECT RAISE(ABORT, 'disk full'); END`
		manual     = `{"manual":{"ingredients":["1 lb beef"],"steps":["Brown."],"minutes":240,"method":"crockpot"}}`
	)
	for _, tc := range []struct {
		name, sabotage, fix, leak string
	}{
		{"link read", breakRead, `{"recipe_url":"` + stewPage + `"}`, "cuisine"},
		{"manual read", breakRead, manual, "cuisine"},
		{"manual store", breakStore, manual, "disk full"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newDinnersEnv(t).withRecipes(t)
			id := createDinner(t, e.h, `{"name":"Sunday stew"}`)
			if _, err := e.st.Writer.ExecContext(t.Context(), tc.sabotage); err != nil {
				t.Fatal(err)
			}

			rec, body := callDinners(t, e.h, http.MethodPost, dinnersPath+"/"+id+"/ground", tc.fix)
			if rec.Code != http.StatusInternalServerError || errorCode(body) != "internal" {
				t.Errorf("POST = %d %s, want 500 internal", rec.Code, rec.Body)
			}
			logs := e.logs.String()
			if strings.Contains(logs, tc.leak) || !strings.Contains(logs, "reason=ground_failed") || !strings.Contains(logs, "dinner_id="+id) {
				t.Errorf("logs = %q, want reason=ground_failed and the dinner id, without the database error", logs)
			}
		})
	}
}

// TestPostGroundArchived: grounding an archived dinner by hand is a 409.
func TestPostGroundArchived(t *testing.T) {
	t.Parallel()
	e := newDinnersEnv(t)
	id := createDinner(t, e.h, `{"name":"Family chili"}`)
	if rec, _ := callDinners(t, e.h, http.MethodDelete, dinnersPath+"/"+id, ""); rec.Code != http.StatusOK {
		t.Fatalf("DELETE = %d: %s", rec.Code, rec.Body)
	}
	rec, body := callDinners(t, e.h, http.MethodPost, dinnersPath+"/"+id+"/ground",
		`{"manual":{"ingredients":["1 lb beef"],"minutes":240,"method":"crockpot"}}`)
	if rec.Code != http.StatusConflict || errorCode(body) != "dinner_archived" {
		t.Errorf("POST = %d %v, want 409 dinner_archived", rec.Code, body)
	}
}

func TestPostGroundViaManual(t *testing.T) {
	t.Parallel()
	e := newDinnersEnv(t)
	id := createDinner(t, e.h, `{"name":"Family chili"}`)

	rec, d := callDinners(t, e.h, http.MethodPost, dinnersPath+"/"+id+"/ground",
		`{"manual":{"ingredients":["1 lb beef","1 can beans"],"steps":["Brown.","Simmer."],"minutes":240,"method":"crockpot"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST = %d: %s", rec.Code, rec.Body)
	}
	if d["grounding_status"] != "manual" || d["recipe_id"] == nil || d["photo_url"] != nil ||
		d["method"] != "crockpot" || d["minutes_total"] != 240.0 || d["grounding_error"] != nil {
		t.Errorf("dinner = %v", d)
	}
	var lines int
	if err := e.st.Reader.QueryRow(`SELECT count(*) FROM recipe_ingredients WHERE recipe_id = ?`, d["recipe_id"]).Scan(&lines); err != nil {
		t.Fatal(err)
	}
	if lines != 2 {
		t.Errorf("stored %d ingredient lines, want 2", lines)
	}
}

func TestPostGroundRejects(t *testing.T) {
	t.Parallel()
	e := newDinnersEnv(t)
	id := createDinner(t, e.h, `{"name":"Pad thai"}`)
	path := dinnersPath + "/" + id + "/ground"

	for _, tc := range []struct{ name, body, code string }{
		{"empty", `{}`, "invalid_dinner"},
		{"two kinds", `{"candidate_id":"https://a.example/x","recipe_url":"https://a.example/x"}`, "invalid_dinner"},
		{"candidate not offered", `{"candidate_id":"c1"}`, "invalid_dinner"},
		{"candidate a page URL", `{"candidate_id":"https://a.example/x"}`, "invalid_dinner"},
		{"http link", `{"recipe_url":"http://a.example/x"}`, "invalid_dinner"},
		{"null", `{"recipe_url":null}`, "invalid_request"},
		{"unknown field", `{"url":"https://a.example/x"}`, "invalid_request"},
		{"unknown manual field", `{"manual":{"ingredients":["x"],"minutes":5,"method":"oven","serves":4}}`, "invalid_request"},
		{"manual without method", `{"manual":{"ingredients":["x"],"minutes":5}}`, "invalid_dinner"},
		{"manual without ingredients", `{"manual":{"ingredients":[],"minutes":5,"method":"oven"}}`, "invalid_dinner"},
		{"manual minutes too many", `{"manual":{"ingredients":["x"],"minutes":5000,"method":"oven"}}`, "invalid_dinner"},
		{"not an object", `[]`, "invalid_request"},
	} {
		rec, body := callDinners(t, e.h, http.MethodPost, path, tc.body)
		if rec.Code != http.StatusBadRequest || errorCode(body) != tc.code {
			t.Errorf("%s: %d %v, want 400 %s", tc.name, rec.Code, body, tc.code)
		}
	}
	if d := listedDinner(t, e.h, id); d["grounding_status"] != "pending" {
		t.Errorf("dinner after rejected fixes = %v, want unchanged", d)
	}

	rec, body := callDinners(t, e.h, http.MethodPost, dinnersPath+"/nope/ground",
		`{"manual":{"ingredients":["x"],"minutes":5,"method":"oven"}}`)
	if rec.Code != http.StatusNotFound || errorCode(body) != "not_found" {
		t.Errorf("unknown dinner = %d %v, want 404", rec.Code, body)
	}
}

func TestPostDinnerEnqueueFailureAnswersFailedDinner(t *testing.T) {
	t.Parallel()
	e := newDinnersEnv(t)
	if _, err := e.st.Writer.ExecContext(t.Context(),
		`CREATE TRIGGER refuse_jobs BEFORE INSERT ON jobs BEGIN SELECT RAISE(ABORT, 'no jobs'); END`); err != nil {
		t.Fatal(err)
	}
	rec, created := callDinners(t, e.h, http.MethodPost, dinnersPath, `{"name":"Chicken curry"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST = %d: %s", rec.Code, rec.Body)
	}
	id, _ := created["id"].(string)
	if id == "" || created["grounding_status"] != "failed" || created["grounding_error"] != "grounding_error" || created["grounding_job_id"] != nil {
		t.Fatalf("created = %v, want its id, failed (grounding_error) and a null grounding_job_id", created)
	}
	if got := listedDinner(t, e.h, id); got["grounding_status"] != "failed" {
		t.Errorf("listed = %v, want failed", got)
	}
}

func TestPostPhotoBackfillGroundsFailedDinners(t *testing.T) {
	t.Parallel()
	e := newDinnersEnv(t).withRecipes(t)
	e.start(t)

	// No model reply is scripted, so grounding on creation fails.
	rec, created := callDinners(t, e.h, http.MethodPost, dinnersPath, `{"name":"Chicken curry"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST = %d: %s", rec.Code, rec.Body)
	}
	id := created["id"].(string)
	waitJob(t, e.h, created["grounding_job_id"].(string))
	if d := listedDinner(t, e.h, id); d["grounding_status"] != "failed" || d["photo_url"] != nil {
		t.Fatalf("dinner = %v, want failed without a photo", d)
	}

	e.model.QueueGenerate(recipestest.NormalizedReply("stovetop", 40))
	rec, started := callDinners(t, e.h, http.MethodPost, "/api/v1/settings/photo-backfill", "")
	jobID, _ := started["id"].(string)
	if rec.Code != http.StatusAccepted || jobID == "" {
		t.Fatalf("POST photo-backfill = %d: %s, want 202 with a job id", rec.Code, rec.Body)
	}
	job := waitJob(t, e.h, jobID)
	want := fmt.Sprintf(`map[dinners:[map[dinner_id:%s outcome:grounded reason:<nil>]]]`, id)
	if job["status"] != "succeeded" || job["kind"] != "backfill_dinner_photos" || fmt.Sprint(job["result"]) != want {
		t.Fatalf("job = %v, want backfill_dinner_photos succeeded with %s", job, want)
	}
	d := listedDinner(t, e.h, id)
	photo, _ := d["photo_url"].(string)
	if d["grounding_status"] != "grounded" || d["grounding_error"] != nil || !strings.HasSuffix(photo, "-400.jpg") {
		t.Errorf("dinner = %v, want grounded with a photo", d)
	}

	// The backfill is started only by its own endpoint.
	rec, _ = callDinners(t, e.h, http.MethodPost, "/api/v1/jobs", `{"kind":"backfill_dinner_photos","payload":{}}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "service_only_kind") {
		t.Errorf("POST /jobs backfill = %d: %s, want 400 service_only_kind", rec.Code, rec.Body)
	}
}
