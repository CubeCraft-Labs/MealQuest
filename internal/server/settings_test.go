package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/auth"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/household"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/migrations"
)

const settingsPath = "/api/v1/settings/household"

// signedIn returns a request carrying stubSessions' valid session cookie and
// its CSRF token.
func signedIn(method, target string, body io.Reader) *http.Request {
	req := httptest.NewRequest(method, target, body)
	req.AddCookie(validSessionCookie)
	req.Header.Set(CSRFHeader, testSession.CSRFToken)
	return req
}

func serve(t *testing.T, h http.Handler, req *http.Request) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	return rec, body
}

func TestGetHouseholdSettings(t *testing.T) {
	t.Parallel()

	rec, body := serve(t, newTestRouter(t, "dev"), signedIn(http.MethodGet, settingsPath, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	want := map[string]any{
		"timezone":                "America/New_York",
		"dinner_time":             "17:30",
		"reminder_time":           "16:30",
		"review_time":             "09:00",
		"plan_draft_dow":          float64(5),
		"plan_draft_time":         "09:00",
		"suggestions_dow":         float64(0),
		"suggestions_time":        "08:00",
		"chat_autosend_dow":       float64(6),
		"chat_autosend_time":      "08:00",
		"onboarding_completed_at": nil,
		"updates_seen_at":         nil,
		"updated_by":              nil,
	}
	for k, v := range want {
		got, ok := body[k]
		if !ok || got != v {
			t.Errorf("%s = %#v (present %v), want %#v", k, got, ok, v)
		}
	}
	for _, k := range []string{"created_at", "updated_at"} {
		if s, ok := body[k].(string); !ok || s == "" {
			t.Errorf("%s = %#v, want a timestamp", k, body[k])
		}
	}
	if len(body) != len(want)+2 {
		t.Errorf("body has %d fields, want %d: %v", len(body), len(want)+2, body)
	}
}

func TestPatchHouseholdSettings(t *testing.T) {
	t.Parallel()
	settings := newTestSettings(t)
	router := newTestRouterWith(t, "dev", settings, newTestRunner(t))

	var notified []household.Settings
	settings.Subscribe(func(s household.Settings) { notified = append(notified, s) })

	rec, body := serve(t, router, signedIn(http.MethodPatch, settingsPath,
		strings.NewReader(`{"timezone":"Europe/Berlin","dinner_time":"18:00","plan_draft_dow":4}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	for k, v := range map[string]any{
		"timezone":       "Europe/Berlin",
		"dinner_time":    "18:00",
		"plan_draft_dow": float64(4),
		"reminder_time":  "16:30", // unchanged
		"updated_by":     testSession.Subject,
	} {
		if body[k] != v {
			t.Errorf("%s = %#v, want %#v", k, body[k], v)
		}
	}
	if len(notified) != 1 || notified[0].Timezone != "Europe/Berlin" {
		t.Errorf("subscriber notified with %+v, want one change to Europe/Berlin", notified)
	}

	_, body = serve(t, router, signedIn(http.MethodGet, settingsPath, nil))
	if body["timezone"] != "Europe/Berlin" || body["updated_by"] != testSession.Subject {
		t.Errorf("GET after PATCH = %v", body)
	}
}

// newOnboardingRouter returns a router whose settings store holds regulars
// Regulars.
func newOnboardingRouter(t *testing.T, regulars int) http.Handler {
	t.Helper()
	st := newTestStore(t)
	if err := household.SeedSettings(context.Background(), st, "America/New_York"); err != nil {
		t.Fatal(err)
	}
	for i := range regulars {
		now := store.Now().String()
		if _, err := db.New(st.Writer).CreateDinner(context.Background(), db.CreateDinnerParams{
			ID: store.NewID(), Name: fmt.Sprintf("Dinner %d", i), NormalizedName: fmt.Sprintf("dinner %d", i),
			Status: "regular", Origin: "regular", GroundingStatus: "pending", CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
	}
	return newTestRouterWith(t, "dev", household.NewSettingsService(st), newTestRunner(t))
}

func TestPatchHouseholdSettingsCompletesOnboarding(t *testing.T) {
	t.Parallel()

	rec, body := serve(t, newOnboardingRouter(t, household.MinRegulars), signedIn(http.MethodPatch, settingsPath,
		strings.NewReader(`{"onboarding_completed":true}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if at, ok := body["onboarding_completed_at"].(string); !ok || at != body["updated_at"] {
		t.Errorf("onboarding_completed_at = %#v, want the update time %#v", body["onboarding_completed_at"], body["updated_at"])
	}
}

// TestPatchHouseholdSettingsOnboardingNeedsRegulars: completing onboarding
// with fewer than three Regulars is refused and leaves it incomplete.
func TestPatchHouseholdSettingsOnboardingNeedsRegulars(t *testing.T) {
	t.Parallel()

	for regulars := range household.MinRegulars {
		t.Run(fmt.Sprintf("%d Regulars", regulars), func(t *testing.T) {
			t.Parallel()
			router := newOnboardingRouter(t, regulars)
			rec, _ := serve(t, router, signedIn(http.MethodPatch, settingsPath,
				strings.NewReader(`{"onboarding_completed":true}`)))
			if rec.Code != http.StatusBadRequest ||
				!strings.Contains(rec.Body.String(), `"code":"invalid_settings"`) ||
				!strings.Contains(rec.Body.String(), "onboarding_completed: add at least 3 Regulars first") {
				t.Fatalf("PATCH = %d %s, want 400 invalid_settings on onboarding_completed", rec.Code, rec.Body)
			}
			_, body := serve(t, router, signedIn(http.MethodGet, settingsPath, nil))
			if body["onboarding_completed_at"] != nil {
				t.Errorf("onboarding_completed_at = %#v after a refused completion, want null", body["onboarding_completed_at"])
			}
		})
	}
}

func TestPatchHouseholdSettingsRejected(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
		code string
		in   string // the error message must contain this
	}{
		{"bad timezone", `{"timezone":"Mars/Olympus_Mons"}`, "invalid_settings", "timezone"},
		{"bad time", `{"dinner_time":"25:00"}`, "invalid_settings", "dinner_time"},
		{"unpadded time", `{"reminder_time":"9:30"}`, "invalid_settings", "reminder_time"},
		{"bad dow", `{"suggestions_dow":7}`, "invalid_settings", "suggestions_dow"},
		{"negative dow", `{"chat_autosend_dow":-1}`, "invalid_settings", "chat_autosend_dow"},
		{"empty object", `{}`, "invalid_settings", "no settings to change"},
		{"fractional dow", `{"plan_draft_dow":5.5}`, "invalid_request", "plan_draft_dow"},
		{"string dow", `{"plan_draft_dow":"5"}`, "invalid_request", "plan_draft_dow"},
		{"numeric time", `{"dinner_time":1730}`, "invalid_request", "dinner_time"},
		{"null value", `{"timezone":null}`, "invalid_request", "timezone"},
		{"unknown field", `{"dinner":"17:30"}`, "invalid_request", "dinner"},
		{"read-only field", `{"updated_by":"someone"}`, "invalid_request", "updated_by"},
		{"read-only onboarding time", `{"onboarding_completed_at":"2026-10-06T12:00:00.000Z"}`, "invalid_request", "onboarding_completed_at"},
		{"onboarding undone", `{"onboarding_completed":false}`, "invalid_settings", "onboarding_completed"},
		{"not an object", `["timezone"]`, "invalid_request", "JSON object"},
		{"null body", `null`, "invalid_request", "JSON object"},
		{"trailing data", `{"dinner_time":"18:00"} {}`, "invalid_request", "JSON object"},
		{"malformed", `{"dinner_time":`, "invalid_request", "JSON object"},
		{"too large", `{"timezone":"` + strings.Repeat("a", maxSettingsBody) + `"}`, "invalid_request", "exceeds"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			settings := newTestSettings(t)
			router := newTestRouterWith(t, "dev", settings, newTestRunner(t))
			notified := 0
			settings.Subscribe(func(household.Settings) { notified++ })

			rec, body := serve(t, router, signedIn(http.MethodPatch, settingsPath, strings.NewReader(tt.body)))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body)
			}
			apiErr, ok := body["error"].(map[string]any)
			if !ok || len(body) != 1 || len(apiErr) != 3 {
				t.Fatalf("body = %s, want {\"error\":{code,message,retryable}}", rec.Body)
			}
			if apiErr["code"] != tt.code || apiErr["retryable"] != false {
				t.Errorf("error = %v, want code %q, retryable false", apiErr, tt.code)
			}
			if msg, _ := apiErr["message"].(string); !strings.Contains(msg, tt.in) {
				t.Errorf("message %q does not contain %q", msg, tt.in)
			}
			if notified != 0 {
				t.Errorf("rejected PATCH notified subscribers %d times", notified)
			}
			_, after := serve(t, router, signedIn(http.MethodGet, settingsPath, nil))
			if after["timezone"] != "America/New_York" || after["dinner_time"] != "17:30" || after["updated_by"] != nil {
				t.Errorf("rejected PATCH changed settings: %v", after)
			}
		})
	}
}

func TestHouseholdSettingsMethodNotAllowed(t *testing.T) {
	t.Parallel()

	rec, body := serve(t, newTestRouter(t, "dev"), signedIn(http.MethodPut, settingsPath, strings.NewReader(`{}`)))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
	if apiErr, _ := body["error"].(map[string]any); apiErr["code"] != "method_not_allowed" {
		t.Errorf("body = %v", body)
	}
}

// TestHouseholdSettingsWithRealSession drives the settings API through the
// production router and session manager: a member signs in, reads the CSRF
// token from GET /api/v1/me and PATCHes; updated_by comes from the session.
// Without a session, or without the CSRF token, the PATCH is refused and
// nothing changes.
func TestHouseholdSettingsWithRealSession(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := store.Migrate(ctx, st.Writer, migrations.FS); err != nil {
		t.Fatal(err)
	}
	if err := household.SeedSettings(ctx, st, "America/New_York"); err != nil {
		t.Fatal(err)
	}
	sessions, err := auth.NewSessionManager(st, time.Hour, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	router, err := NewRouter(logger, "dev", nil, stubAuth{}, sessions, household.NewSettingsService(st),
		newTestSearch(t), newTestAI(t), newTestDinners(t), newTestSuggest(t), newTestPlan(t), newTestShopping(t), newTestGoogle(t), newTestPush(t), newTestRunner(t), newTestPhotos(t), fstest.MapFS{"index.html": {Data: []byte(fixtureIndex)}})
	if err != nil {
		t.Fatal(err)
	}

	// Sign in as the callback would.
	login := httptest.NewRecorder()
	if err := sessions.StartSession(login, httptest.NewRequest(http.MethodGet, "/auth/callback", nil), auth.Claims{
		Subject: "authentik-sub-42",
		Email:   "cook@example.com",
		Name:    "Home Cook",
		Groups:  []string{"mealquest-admin"},
	}); err != nil {
		t.Fatal(err)
	}
	cookies := login.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != auth.SessionCookieName {
		t.Fatalf("StartSession set cookies %v, want the session cookie", cookies)
	}
	cookie := cookies[0]
	withCookie := func(req *http.Request) *http.Request {
		req.AddCookie(cookie)
		return req
	}

	rec, me := serve(t, router, withCookie(httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)))
	token, _ := me["csrfToken"].(string)
	if rec.Code != http.StatusOK || token == "" {
		t.Fatalf("GET /api/v1/me = %d %v, want 200 with a CSRF token", rec.Code, me)
	}

	patch := `{"dinner_time":"18:45"}`
	refused := []struct {
		name   string
		req    *http.Request
		status int
	}{
		{"no session", httptest.NewRequest(http.MethodPatch, settingsPath, strings.NewReader(patch)), http.StatusUnauthorized},
		{"no CSRF token", withCookie(httptest.NewRequest(http.MethodPatch, settingsPath, strings.NewReader(patch))), http.StatusForbidden},
	}
	for _, tt := range refused {
		if rec, body := serve(t, router, tt.req); rec.Code != tt.status {
			t.Errorf("%s: PATCH status = %d, want %d: %v", tt.name, rec.Code, tt.status, body)
		}
	}
	_, body := serve(t, router, withCookie(httptest.NewRequest(http.MethodGet, settingsPath, nil)))
	if body["dinner_time"] != household.SeedDinnerTime || body["updated_by"] != nil {
		t.Fatalf("refused PATCH changed settings: %v", body)
	}

	req := withCookie(httptest.NewRequest(http.MethodPatch, settingsPath, strings.NewReader(patch)))
	req.Header.Set(CSRFHeader, token)
	rec, body = serve(t, router, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH status = %d, want 200: %v", rec.Code, body)
	}
	if body["dinner_time"] != "18:45" || body["updated_by"] != "authentik-sub-42" {
		t.Errorf("PATCH = %v, want dinner_time 18:45 updated_by authentik-sub-42", body)
	}
}
