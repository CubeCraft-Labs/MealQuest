package server

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/dinners"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

const dinnersPath = "/api/v1/dinners"

// newDinnersRouter returns a router whose dinners service runs over the
// returned store.
func newDinnersRouter(t *testing.T) (http.Handler, *store.Store) {
	t.Helper()
	st := newTestStore(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	r, err := NewRouter(logger, "dev", nil, stubAuth{}, stubSessions{}, newTestSettings(t), newTestSearch(t), newTestAI(t),
		dinners.NewService(st), newTestRunner(t), newTestPhotos(t), fstest.MapFS{"index.html": {Data: []byte(fixtureIndex)}})
	if err != nil {
		t.Fatal(err)
	}
	return r, st
}

// callDinners sends a signed-in request with body (none when empty).
func callDinners(t *testing.T, h http.Handler, method, target, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	return serve(t, h, signedIn(method, target, r))
}

// createDinner adds a Regular and returns its id.
func createDinner(t *testing.T, h http.Handler, body string) string {
	t.Helper()
	rec, got := callDinners(t, h, http.MethodPost, dinnersPath, body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST %s = %d: %s", body, rec.Code, rec.Body)
	}
	return got["id"].(string)
}

// listNames returns the names GET /dinners?status=status lists, in order.
func listNames(t *testing.T, h http.Handler, status string) []string {
	t.Helper()
	rec, body := callDinners(t, h, http.MethodGet, dinnersPath+"?status="+status, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status=%s = %d: %s", status, rec.Code, rec.Body)
	}
	list, ok := body["dinners"].([]any)
	if !ok {
		t.Fatalf("GET status=%s has no dinners list: %v", status, body)
	}
	names := make([]string, len(list))
	for i, d := range list {
		names[i] = d.(map[string]any)["name"].(string)
	}
	return names
}

func TestCreateDinnerByName(t *testing.T) {
	t.Parallel()
	router, st := newDinnersRouter(t)

	rec, body := callDinners(t, router, http.MethodPost, dinnersPath, `{"name":"  Chicken Tikka Masala "}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body)
	}
	for k, v := range map[string]any{
		"name":             "Chicken Tikka Masala",
		"status":           "regular",
		"origin":           "regular",
		"grounding_status": "pending",
		"is_favorite":      false,
		"recipe_url":       nil,
		"photo_url":        nil,
		"method":           nil,
		"minutes_total":    nil,
		"difficulty":       nil,
		"updated_by":       testSession.Subject,
	} {
		if got, ok := body[k]; !ok || got != v {
			t.Errorf("%s = %#v (present %v), want %#v", k, got, ok, v)
		}
	}
	var normalized, updatedBy string
	if err := st.Reader.QueryRow(`SELECT normalized_name, updated_by FROM dinners WHERE id = ?`, body["id"]).Scan(&normalized, &updatedBy); err != nil {
		t.Fatal(err)
	}
	if normalized != "chicken tikka masala" || updatedBy != testSession.Subject {
		t.Errorf("stored normalized_name, updated_by = %q, %q", normalized, updatedBy)
	}
}

func TestCreateDinnerByURL(t *testing.T) {
	t.Parallel()
	router, _ := newDinnersRouter(t)

	link := "https://www.example.com/recipes/12345/beef-and-broccoli-stir-fry/"
	rec, body := callDinners(t, router, http.MethodPost, dinnersPath, `{"recipe_url":"`+link+`"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body)
	}
	for k, v := range map[string]any{
		"name":             "Beef and broccoli stir fry",
		"recipe_url":       link,
		"status":           "regular",
		"origin":           "regular",
		"grounding_status": "pending",
		"updated_by":       testSession.Subject,
	} {
		if body[k] != v {
			t.Errorf("%s = %#v, want %#v", k, body[k], v)
		}
	}

	// The same link again is a duplicate even though its name would differ.
	rec, body = callDinners(t, router, http.MethodPost, dinnersPath, `{"name":"Something else entirely"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("unrelated name = %d: %s", rec.Code, rec.Body)
	}
	rec, body = callDinners(t, router, http.MethodPost, dinnersPath, `{"recipe_url":"`+link+`"}`)
	if rec.Code != http.StatusConflict || errorCode(body) != "duplicate_dinner" {
		t.Errorf("same link again = %d %v, want 409 duplicate_dinner", rec.Code, body)
	}

	for _, tc := range []struct{ name, body string }{
		{"plain http", `{"recipe_url":"http://example.com/recipes/chili"}`},
		{"not a URL", `{"recipe_url":"chili"}`},
		{"credentials", `{"recipe_url":"https://me:pw@example.com/recipes/chili"}`},
		{"no name in path", `{"recipe_url":"https://example.com/"}`},
		{"numeric slug", `{"recipe_url":"https://example.com/recipe/98765"}`},
		{"both fields", `{"name":"Chili","recipe_url":"https://example.com/recipes/chili"}`},
		{"neither field", `{}`},
	} {
		rec, body := callDinners(t, router, http.MethodPost, dinnersPath, tc.body)
		if rec.Code != http.StatusBadRequest || errorCode(body) != "invalid_dinner" {
			t.Errorf("%s: %d %v, want 400 invalid_dinner", tc.name, rec.Code, body)
		}
	}
}

func TestCreateDinnerDuplicate(t *testing.T) {
	t.Parallel()
	router, st := newDinnersRouter(t)
	createDinner(t, router, `{"name":"Chicken Tikka Masala"}`)

	for _, name := range []string{
		"chicken tikka masala",
		"Easy Chicken Tikka-Masala!",
		"The Best Homemade Chicken Tikka Masala Recipe",
	} {
		rec, body := callDinners(t, router, http.MethodPost, dinnersPath, `{"name":"`+name+`"}`)
		if rec.Code != http.StatusConflict || errorCode(body) != "duplicate_dinner" {
			t.Errorf("%q: %d %v, want 409 duplicate_dinner", name, rec.Code, body)
		}
	}
	// A slug naming the same dish is a duplicate too.
	rec, body := callDinners(t, router, http.MethodPost, dinnersPath, `{"recipe_url":"https://example.com/easy-chicken-tikka-masala.html"}`)
	if rec.Code != http.StatusConflict {
		t.Errorf("URL slug duplicate = %d %v, want 409", rec.Code, body)
	}

	var n int
	if err := st.Reader.QueryRow(`SELECT count(*) FROM dinners`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("dinners rows = %d, want 1", n)
	}

	// A different dish sharing words is not a duplicate.
	createDinner(t, router, `{"name":"Chicken Tikka Wraps"}`)
}

func TestCreateDinnerRejected(t *testing.T) {
	t.Parallel()
	router, _ := newDinnersRouter(t)
	for _, tc := range []struct{ name, body, code string }{
		{"empty name", `{"name":"   "}`, "invalid_dinner"},
		{"filler only", `{"name":"The Best Easy Recipe"}`, "invalid_dinner"},
		{"too long", `{"name":"` + strings.Repeat("x", dinners.MaxNameChars+1) + `"}`, "invalid_dinner"},
		{"null name", `{"name":null}`, "invalid_request"},
		{"unknown field", `{"name":"Chili","status":"quest_log"}`, "invalid_request"},
		{"not an object", `"Chili"`, "invalid_request"},
	} {
		rec, body := callDinners(t, router, http.MethodPost, dinnersPath, tc.body)
		if rec.Code != http.StatusBadRequest || errorCode(body) != tc.code {
			t.Errorf("%s: %d %v, want 400 %s", tc.name, rec.Code, body, tc.code)
		}
	}
}

func TestListDinnersFiltersByStatus(t *testing.T) {
	t.Parallel()
	router, st := newDinnersRouter(t)
	tacos := createDinner(t, router, `{"name":"Tacos"}`)
	createDinner(t, router, `{"name":"Lasagna"}`)
	pho := createDinner(t, router, `{"name":"Pho"}`)
	gone := createDinner(t, router, `{"name":"Meatloaf"}`)

	ctx := context.Background()
	if _, err := st.Writer.ExecContext(ctx, `UPDATE dinners SET status = 'quest_log', origin = 'side_quest' WHERE id = ?`, pho); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Writer.ExecContext(ctx,
		`INSERT INTO photos (id, source_url, local_path, width, height, sha256, fetched_at, created_at, updated_at)
		 VALUES ('photo1', 'https://example.com/tacos.jpg', 'media/photos/photo1', 1200, 800, 'abc', 't', 't', 't')`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Writer.ExecContext(ctx, `UPDATE dinners SET photo_id = 'photo1', grounding_status = 'grounded' WHERE id = ?`, tacos); err != nil {
		t.Fatal(err)
	}
	if rec, _ := callDinners(t, router, http.MethodDelete, dinnersPath+"/"+gone, ""); rec.Code != http.StatusOK {
		t.Fatalf("DELETE = %d", rec.Code)
	}

	if got := strings.Join(listNames(t, router, "regular"), ","); got != "Lasagna,Tacos" {
		t.Errorf("regular = %s, want Lasagna,Tacos", got)
	}
	if got := strings.Join(listNames(t, router, "quest_log"), ","); got != "Pho" {
		t.Errorf("quest_log = %s, want Pho", got)
	}

	_, body := callDinners(t, router, http.MethodGet, dinnersPath+"?status=regular", "")
	for _, d := range body["dinners"].([]any) {
		d := d.(map[string]any)
		switch d["name"] {
		case "Tacos":
			if d["photo_url"] != "/media/photos/photo1-400.jpg" || d["grounding_status"] != "grounded" {
				t.Errorf("Tacos photo_url, grounding_status = %v, %v", d["photo_url"], d["grounding_status"])
			}
		case "Lasagna":
			if d["photo_url"] != nil || d["grounding_status"] != "pending" {
				t.Errorf("Lasagna photo_url, grounding_status = %v, %v", d["photo_url"], d["grounding_status"])
			}
		}
	}

	// An empty list is [] rather than null.
	empty, _ := newDinnersRouter(t)
	rec, _ := callDinners(t, empty, http.MethodGet, dinnersPath+"?status=regular", "")
	if got := rec.Body.String(); got != `{"dinners":[]}` {
		t.Errorf("empty list = %s", got)
	}

	for _, q := range []string{"", "?status=archived", "?status=suggested", "?status=bogus"} {
		rec, body := callDinners(t, router, http.MethodGet, dinnersPath+q, "")
		if rec.Code != http.StatusBadRequest || errorCode(body) != "invalid_dinner" {
			t.Errorf("GET %q = %d %v, want 400 invalid_dinner", q, rec.Code, body)
		}
	}
}

func TestPatchDinner(t *testing.T) {
	t.Parallel()
	router, st := newDinnersRouter(t)
	id := createDinner(t, router, `{"name":"Chili"}`)
	createDinner(t, router, `{"name":"Pot Roast"}`)
	if _, err := st.Writer.Exec(`UPDATE dinners SET updated_by = 'someone-else' WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}

	rec, body := callDinners(t, router, http.MethodPatch, dinnersPath+"/"+id,
		`{"name":"White Chicken Chili","method":"crockpot","minutes_total":480,"difficulty":"easy"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	for k, v := range map[string]any{
		"name":          "White Chicken Chili",
		"method":        "crockpot",
		"minutes_total": float64(480),
		"difficulty":    "easy",
		"status":        "regular",
		"updated_by":    testSession.Subject,
	} {
		if body[k] != v {
			t.Errorf("%s = %#v, want %#v", k, body[k], v)
		}
	}
	var normalized string
	if err := st.Reader.QueryRow(`SELECT normalized_name FROM dinners WHERE id = ?`, id).Scan(&normalized); err != nil {
		t.Fatal(err)
	}
	if normalized != "white chicken chili" {
		t.Errorf("normalized_name = %q", normalized)
	}

	// A partial patch leaves the other fields.
	rec, body = callDinners(t, router, http.MethodPatch, dinnersPath+"/"+id, `{"difficulty":"medium"}`)
	if rec.Code != http.StatusOK || body["method"] != "crockpot" || body["difficulty"] != "medium" || body["name"] != "White Chicken Chili" {
		t.Errorf("partial patch = %d %v", rec.Code, body)
	}
	// Renaming to its own name, respelled, is not a duplicate of itself.
	if rec, body := callDinners(t, router, http.MethodPatch, dinnersPath+"/"+id, `{"name":"Easy white chicken chili"}`); rec.Code != http.StatusOK {
		t.Errorf("self rename = %d %v", rec.Code, body)
	}
	// Renaming onto another dinner is.
	rec, body = callDinners(t, router, http.MethodPatch, dinnersPath+"/"+id, `{"name":"The Best Pot Roast"}`)
	if rec.Code != http.StatusConflict || errorCode(body) != "duplicate_dinner" {
		t.Errorf("rename onto Pot Roast = %d %v, want 409", rec.Code, body)
	}
}

func TestPatchDinnerValidation(t *testing.T) {
	t.Parallel()
	router, _ := newDinnersRouter(t)
	id := createDinner(t, router, `{"name":"Chili"}`)

	for _, tc := range []struct {
		name, body, code, in string
	}{
		{"bad method", `{"method":"microwave"}`, "invalid_dinner", "method"},
		{"crock pot spelling", `{"method":"crock pot"}`, "invalid_dinner", "method"},
		{"bad difficulty", `{"difficulty":"expert"}`, "invalid_dinner", "difficulty"},
		{"zero minutes", `{"minutes_total":0}`, "invalid_dinner", "minutes_total"},
		{"negative minutes", `{"minutes_total":-5}`, "invalid_dinner", "minutes_total"},
		{"too many minutes", `{"minutes_total":1441}`, "invalid_dinner", "minutes_total"},
		{"empty name", `{"name":""}`, "invalid_dinner", "name"},
		{"every problem", `{"method":"x","difficulty":"y","minutes_total":0}`, "invalid_dinner", "method: must be one of stovetop, oven, crockpot, grill, other; minutes_total"},
		{"empty patch", `{}`, "invalid_dinner", "no fields"},
		{"fractional minutes", `{"minutes_total":30.5}`, "invalid_request", "minutes_total"},
		{"string minutes", `{"minutes_total":"30"}`, "invalid_request", "minutes_total"},
		{"null method", `{"method":null}`, "invalid_request", "method"},
		{"read-only field", `{"status":"archived"}`, "invalid_request", "status"},
		{"favorite field", `{"is_favorite":true}`, "invalid_request", "is_favorite"},
	} {
		rec, body := callDinners(t, router, http.MethodPatch, dinnersPath+"/"+id, tc.body)
		msg, _ := body["error"].(map[string]any)["message"].(string)
		if rec.Code != http.StatusBadRequest || errorCode(body) != tc.code || !strings.Contains(msg, tc.in) {
			t.Errorf("%s: %d %v, want 400 %s containing %q", tc.name, rec.Code, body, tc.code, tc.in)
		}
	}

	_, body := callDinners(t, router, http.MethodGet, dinnersPath+"?status=regular", "")
	d := body["dinners"].([]any)[0].(map[string]any)
	if d["method"] != nil || d["difficulty"] != nil || d["minutes_total"] != nil || d["name"] != "Chili" {
		t.Errorf("a rejected patch changed the dinner: %v", d)
	}

	rec, body := callDinners(t, router, http.MethodPatch, dinnersPath+"/nope", `{"method":"oven"}`)
	if rec.Code != http.StatusNotFound || errorCode(body) != "not_found" {
		t.Errorf("unknown id = %d %v, want 404", rec.Code, body)
	}
}

func TestDeleteDinnerArchives(t *testing.T) {
	t.Parallel()
	router, st := newDinnersRouter(t)
	id := createDinner(t, router, `{"name":"Shepherd's Pie"}`)
	if _, err := st.Writer.Exec(`UPDATE dinners SET updated_by = 'someone-else' WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}

	rec, body := callDinners(t, router, http.MethodDelete, dinnersPath+"/"+id, "")
	if rec.Code != http.StatusOK || body["status"] != "archived" || body["updated_by"] != testSession.Subject {
		t.Fatalf("DELETE = %d %v", rec.Code, body)
	}
	var status, name, updatedBy string
	if err := st.Reader.QueryRow(`SELECT status, name, updated_by FROM dinners WHERE id = ?`, id).Scan(&status, &name, &updatedBy); err != nil {
		t.Fatalf("archived row is gone: %v", err)
	}
	if status != "archived" || name != "Shepherd's Pie" || updatedBy != testSession.Subject {
		t.Errorf("row = %s %q %s", status, name, updatedBy)
	}
	if got := listNames(t, router, "regular"); len(got) != 0 {
		t.Errorf("regular after archive = %v", got)
	}
	// An archived name may be added again.
	createDinner(t, router, `{"name":"Shepherds Pie"}`)

	rec, body = callDinners(t, router, http.MethodDelete, dinnersPath+"/nope", "")
	if rec.Code != http.StatusNotFound || errorCode(body) != "not_found" {
		t.Errorf("unknown id = %d %v, want 404", rec.Code, body)
	}
}

func TestFavoriteDinnerToggles(t *testing.T) {
	t.Parallel()
	router, st := newDinnersRouter(t)
	id := createDinner(t, router, `{"name":"Fish Tacos"}`)
	if _, err := st.Writer.Exec(`UPDATE dinners SET updated_by = 'someone-else' WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}

	for _, want := range []bool{true, false, true} {
		rec, body := callDinners(t, router, http.MethodPost, dinnersPath+"/"+id+"/favorite", "")
		if rec.Code != http.StatusOK || body["is_favorite"] != want || body["updated_by"] != testSession.Subject {
			t.Fatalf("favorite = %d %v, want is_favorite %v", rec.Code, body, want)
		}
	}
	var fav int
	if err := st.Reader.QueryRow(`SELECT is_favorite FROM dinners WHERE id = ?`, id).Scan(&fav); err != nil {
		t.Fatal(err)
	}
	if fav != 1 {
		t.Errorf("stored is_favorite = %d, want 1", fav)
	}

	rec, body := callDinners(t, router, http.MethodPost, dinnersPath+"/nope/favorite", "")
	if rec.Code != http.StatusNotFound || errorCode(body) != "not_found" {
		t.Errorf("unknown id = %d %v, want 404", rec.Code, body)
	}
}

// TestDinnerWritesRequireCSRF sends every dinners write without the CSRF
// header and with a wrong one; each is refused and changes nothing.
func TestDinnerWritesRequireCSRF(t *testing.T) {
	t.Parallel()
	router, st := newDinnersRouter(t)
	id := createDinner(t, router, `{"name":"Chili"}`)

	var before string
	snapshot := `SELECT group_concat(id || name || status || is_favorite || coalesce(method, '') || updated_at, '|') FROM dinners`
	if err := st.Reader.QueryRow(snapshot).Scan(&before); err != nil {
		t.Fatal(err)
	}

	writes := []struct{ method, target, body string }{
		{http.MethodPost, dinnersPath, `{"name":"Lasagna"}`},
		{http.MethodPatch, dinnersPath + "/" + id, `{"method":"oven"}`},
		{http.MethodDelete, dinnersPath + "/" + id, ""},
		{http.MethodPost, dinnersPath + "/" + id + "/favorite", ""},
	}
	for _, w := range writes {
		for _, header := range []*string{nil, ptr("wrong-token")} {
			req := httptest.NewRequest(w.method, w.target, strings.NewReader(w.body))
			req.AddCookie(validSessionCookie)
			if header != nil {
				req.Header.Set(CSRFHeader, *header)
			}
			rec, body := serve(t, router, req)
			if rec.Code != http.StatusForbidden || errorCode(body) != "csrf" {
				t.Errorf("%s %s (header %v) = %d %v, want 403 csrf", w.method, w.target, header != nil, rec.Code, body)
			}
		}
	}

	var after string
	if err := st.Reader.QueryRow(snapshot).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Errorf("refused writes changed dinners:\n before %s\n after  %s", before, after)
	}

	// Without a session, the list is 401 too.
	req := httptest.NewRequest(http.MethodGet, dinnersPath+"?status=regular", nil)
	if rec, body := serve(t, router, req); rec.Code != http.StatusUnauthorized || errorCode(body) != "unauthenticated" {
		t.Errorf("GET without a session = %d %v, want 401", rec.Code, body)
	}
}

func ptr[T any](v T) *T { return &v }
