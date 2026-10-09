package server

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/recipes"
)

const (
	recipeSearchPath     = "/api/v1/settings/recipe-search"
	recipeSearchTestPath = "/api/v1/settings/recipe-search/test"
	testBraveKey         = "BSA-secret-brave-key-9f3c"
)

// newRecipeSearchRouter returns a router serving search.
func newRecipeSearchRouter(t *testing.T, search *recipes.SearchSettingsService) http.Handler {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	r, err := NewRouter(logger, "dev", nil, stubAuth{}, stubSessions{}, newTestSettings(t), search, newTestAI(t), newTestChat(t), newTestDinners(t), newTestSuggest(t), newTestPlan(t), newTestShopping(t), newTestGoogle(t), newTestPush(t), newTestRunner(t),
		newTestPhotos(t), fstest.MapFS{"index.html": {Data: []byte(fixtureIndex)}})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRecipeSearchSettingsAPI(t *testing.T) {
	t.Parallel()
	router := newRecipeSearchRouter(t, newTestSearch(t))
	var bodies []string
	call := func(method, target, body string) (*httptest.ResponseRecorder, map[string]any) {
		t.Helper()
		var r io.Reader
		if body != "" {
			r = strings.NewReader(body)
		}
		rec, decoded := serve(t, router, signedIn(method, target, r))
		bodies = append(bodies, rec.Body.String())
		return rec, decoded
	}

	rec, body := call(http.MethodGet, recipeSearchPath, "")
	if def, ok := body["default_base_url"]; rec.Code != http.StatusOK || body["provider"] != nil || body["api_key_set"] != false || !ok || def != nil {
		t.Errorf("GET unconfigured = %d %v", rec.Code, body)
	}
	rec, body = call(http.MethodPost, recipeSearchTestPath, "")
	if rec.Code != http.StatusConflict || body["error"].(map[string]any)["code"] != "not_configured" {
		t.Errorf("POST test unconfigured = %d %v", rec.Code, body)
	}

	for _, bad := range []string{
		`{"provider":"google"}`,
		`{"provider":"brave"}`,
		`{"provider":"searxng","base_url":"not a url"}`,
		`{"provider":"brave","api_key":null}`,
		`{"provider":"brave","apikey":"x"}`,
	} {
		if rec, _ := call(http.MethodPatch, recipeSearchPath, bad); rec.Code != http.StatusBadRequest {
			t.Errorf("PATCH %s = %d, want 400", bad, rec.Code)
		}
	}

	rec, body = call(http.MethodPatch, recipeSearchPath, `{"provider":"brave","api_key":"`+testBraveKey+`"}`)
	if rec.Code != http.StatusOK || body["provider"] != "brave" || body["api_key_set"] != true || body["api_key_last4"] != "9f3c" {
		t.Errorf("PATCH Brave = %d %v", rec.Code, body)
	}
	if _, ok := body["api_key"]; ok {
		t.Errorf("PATCH response has an api_key field: %v", body)
	}
	rec, body = call(http.MethodGet, recipeSearchPath, "")
	if rec.Code != http.StatusOK || body["provider"] != "brave" || body["api_key_last4"] != "9f3c" {
		t.Errorf("GET Brave = %d %v", rec.Code, body)
	}

	// SearXNG keeps the stored key; the test search goes to the instance.
	searx := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"results":[{"url":"https://a.example/1"},{"url":"https://b.example/2"},{"url":"https://c.example/3"}]}`))
	}))
	t.Cleanup(searx.Close)
	rec, body = call(http.MethodPatch, recipeSearchPath, `{"provider":"searxng","base_url":"`+searx.URL+`"}`)
	if rec.Code != http.StatusOK || body["base_url"] != searx.URL || body["api_key_set"] != true {
		t.Errorf("PATCH SearXNG = %d %v", rec.Code, body)
	}
	rec, body = call(http.MethodPost, recipeSearchTestPath, "")
	if rec.Code != http.StatusOK || body["count"] != float64(3) || body["query"] != recipes.SearchTestQuery {
		t.Errorf("POST test = %d %v, want count 3", rec.Code, body)
	}

	searx.Close()
	rec, body = call(http.MethodPost, recipeSearchTestPath, "")
	if rec.Code != http.StatusBadGateway || body["error"].(map[string]any)["code"] != "search_failed" {
		t.Errorf("POST test against a stopped instance = %d %v, want 502 search_failed", rec.Code, body)
	}

	for _, b := range bodies {
		if strings.Contains(b, testBraveKey) || strings.Contains(b, testBraveKey[:12]) {
			t.Errorf("response holds the Brave key: %s", b)
		}
	}
}

func TestRecipeSearchSettingsNeedCSRF(t *testing.T) {
	t.Parallel()
	router := newRecipeSearchRouter(t, newTestSearch(t))
	for _, method := range []string{http.MethodPatch, http.MethodPost} {
		target := recipeSearchPath
		if method == http.MethodPost {
			target = recipeSearchTestPath
		}
		req := httptest.NewRequest(method, target, strings.NewReader(`{"provider":"brave","api_key":"`+testBraveKey+`"}`))
		req.AddCookie(validSessionCookie)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s without CSRF token = %d, want 403", method, target, rec.Code)
		}
	}
}
