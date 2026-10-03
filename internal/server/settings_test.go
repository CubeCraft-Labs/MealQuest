package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/actor"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/household"
)

const settingsPath = "/api/v1/settings/household"

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

	rec, body := serve(t, newTestRouter(t, "dev"), httptest.NewRequest(http.MethodGet, settingsPath, nil))
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
	router := newTestRouterWith(t, "dev", settings)

	var notified []household.Settings
	settings.Subscribe(func(s household.Settings) { notified = append(notified, s) })

	req := httptest.NewRequest(http.MethodPatch, settingsPath,
		strings.NewReader(`{"timezone":"Europe/Berlin","dinner_time":"18:00","plan_draft_dow":4}`))
	req = req.WithContext(actor.With(req.Context(), "sam"))
	rec, body := serve(t, router, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	for k, v := range map[string]any{
		"timezone":       "Europe/Berlin",
		"dinner_time":    "18:00",
		"plan_draft_dow": float64(4),
		"reminder_time":  "16:30", // unchanged
		"updated_by":     "sam",
	} {
		if body[k] != v {
			t.Errorf("%s = %#v, want %#v", k, body[k], v)
		}
	}
	if len(notified) != 1 || notified[0].Timezone != "Europe/Berlin" {
		t.Errorf("subscriber notified with %+v, want one change to Europe/Berlin", notified)
	}

	_, body = serve(t, router, httptest.NewRequest(http.MethodGet, settingsPath, nil))
	if body["timezone"] != "Europe/Berlin" || body["updated_by"] != "sam" {
		t.Errorf("GET after PATCH = %v", body)
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
			router := newTestRouterWith(t, "dev", settings)
			notified := 0
			settings.Subscribe(func(household.Settings) { notified++ })

			rec, body := serve(t, router, httptest.NewRequest(http.MethodPatch, settingsPath, strings.NewReader(tt.body)))
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
			_, after := serve(t, router, httptest.NewRequest(http.MethodGet, settingsPath, nil))
			if after["timezone"] != "America/New_York" || after["dinner_time"] != "17:30" || after["updated_by"] != nil {
				t.Errorf("rejected PATCH changed settings: %v", after)
			}
		})
	}
}

func TestHouseholdSettingsMethodNotAllowed(t *testing.T) {
	t.Parallel()

	rec, body := serve(t, newTestRouter(t, "dev"), httptest.NewRequest(http.MethodPut, settingsPath, strings.NewReader(`{}`)))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
	if apiErr, _ := body["error"].(map[string]any); apiErr["code"] != "method_not_allowed" {
		t.Errorf("body = %v", body)
	}
}
