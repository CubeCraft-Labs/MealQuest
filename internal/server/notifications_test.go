package server

import (
	"net/http"
	"reflect"
	"strings"
	"testing"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/household"
)

const notificationsPath = "/api/v1/settings/notifications"

// seedPrefs is the seed notification preferences as GET answers them.
var seedPrefs = []any{
	map[string]any{"event": "plan_ready", "push": true, "chat": true},
	map[string]any{"event": "list_ready", "push": true, "chat": true},
	map[string]any{"event": "day_of_reminder", "push": true, "chat": false},
	map[string]any{"event": "new_suggestions", "push": true, "chat": false},
	map[string]any{"event": "rotation_review", "push": true, "chat": false},
}

func TestGetNotificationSettings(t *testing.T) {
	t.Parallel()
	router := newTestRouterWith(t, "dev", newTestSettings(t), newTestRunner(t))

	rec, body := serve(t, router, signedIn(http.MethodGet, notificationsPath, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	want := map[string]any{"prefs": seedPrefs, "reminder_time": "16:30"}
	if !reflect.DeepEqual(body, want) {
		t.Errorf("body = %v, want %v", body, want)
	}
}

const allChanged = `{"prefs":[
	{"event":"plan_ready","push":false,"chat":true},
	{"event":"list_ready","push":true,"chat":false},
	{"event":"day_of_reminder","push":true,"chat":true},
	{"event":"new_suggestions","push":false,"chat":false},
	{"event":"rotation_review","push":false,"chat":true}
],"reminder_time":"15:45"}`

func TestPutNotificationSettings(t *testing.T) {
	t.Parallel()
	settings := newTestSettings(t)
	router := newTestRouterWith(t, "dev", settings, newTestRunner(t))

	var notified []household.Settings
	settings.Subscribe(func(s household.Settings) { notified = append(notified, s) })

	// The events come in any order and are answered in screen order.
	rec, body := serve(t, router, signedIn(http.MethodPut, notificationsPath, strings.NewReader(`{"reminder_time":"15:45","prefs":[
		{"event":"rotation_review","push":false,"chat":true},
		{"event":"new_suggestions","push":false,"chat":false},
		{"event":"day_of_reminder","push":true,"chat":true},
		{"event":"list_ready","push":true,"chat":false},
		{"event":"plan_ready","push":false,"chat":true}
	]}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	want := map[string]any{
		"prefs": []any{
			map[string]any{"event": "plan_ready", "push": false, "chat": true},
			map[string]any{"event": "list_ready", "push": true, "chat": false},
			map[string]any{"event": "day_of_reminder", "push": true, "chat": true},
			map[string]any{"event": "new_suggestions", "push": false, "chat": false},
			map[string]any{"event": "rotation_review", "push": false, "chat": true},
		},
		"reminder_time": "15:45",
	}
	if !reflect.DeepEqual(body, want) {
		t.Errorf("PUT body = %v, want %v", body, want)
	}
	if _, got := serve(t, router, signedIn(http.MethodGet, notificationsPath, nil)); !reflect.DeepEqual(got, want) {
		t.Errorf("GET after PUT = %v, want %v", got, want)
	}

	// reminder_time is the household setting: there is one copy of it.
	_, hs := serve(t, router, signedIn(http.MethodGet, settingsPath, nil))
	if hs["reminder_time"] != "15:45" || hs["updated_by"] != testSession.Subject {
		t.Errorf("household settings after PUT = %v, want reminder_time 15:45 updated by %s", hs, testSession.Subject)
	}
	if len(notified) != 1 || notified[0].ReminderTime != "15:45" {
		t.Errorf("subscriber notified with %+v, want one change to 15:45", notified)
	}

	// A household settings change to reminder_time is what GET answers.
	if rec, _ := serve(t, router, signedIn(http.MethodPatch, settingsPath, strings.NewReader(`{"reminder_time":"17:05"}`))); rec.Code != http.StatusOK {
		t.Fatalf("PATCH household status = %d: %s", rec.Code, rec.Body)
	}
	if _, got := serve(t, router, signedIn(http.MethodGet, notificationsPath, nil)); got["reminder_time"] != "17:05" {
		t.Errorf("GET reminder_time after PATCH household = %v, want 17:05", got["reminder_time"])
	}
}

func TestPutNotificationSettingsRejected(t *testing.T) {
	t.Parallel()
	settings := newTestSettings(t)
	router := newTestRouterWith(t, "dev", settings, newTestRunner(t))

	const four = `{"event":"plan_ready","push":true,"chat":true},
		{"event":"list_ready","push":true,"chat":true},
		{"event":"day_of_reminder","push":true,"chat":true},
		{"event":"new_suggestions","push":true,"chat":true}`
	for _, tc := range []struct {
		name, body, code, message string
	}{
		{"bad time", strings.Replace(allChanged, "15:45", "24:00", 1), "invalid_settings", `reminder_time: invalid time "24:00"`},
		{"unpadded time", strings.Replace(allChanged, "15:45", "9:00", 1), "invalid_settings", `reminder_time: invalid time "9:00"`},
		{"missing event", `{"prefs":[` + four + `],"reminder_time":"16:30"}`, "invalid_settings", `prefs: event "rotation_review" is missing`},
		{"unknown event", `{"prefs":[` + four + `,{"event":"rotation_review","push":true,"chat":true},{"event":"weather","push":true,"chat":true}],"reminder_time":"16:30"}`, "invalid_settings", `prefs: unknown event "weather"`},
		{"duplicate event", `{"prefs":[` + four + `,{"event":"plan_ready","push":true,"chat":true}],"reminder_time":"16:30"}`, "invalid_settings", `prefs: event "plan_ready" is listed more than once`},
		{"unknown field", strings.Replace(allChanged, `"reminder_time"`, `"review_time":"09:00","reminder_time"`, 1), "invalid_request", `unknown field "review_time"`},
		{"unknown pref field", strings.Replace(allChanged, `"chat":true}`, `"chat":true,"email":true}`, 1), "invalid_request", `unknown field "email"`},
		{"missing reminder_time", `{"prefs":[]}`, "invalid_request", "prefs and reminder_time are required"},
		{"missing channel", strings.Replace(allChanged, `"push":false,"chat":true}`, `"push":false}`, 1), "invalid_request", "prefs[0]: event, push and chat are required"},
		{"null channel", strings.Replace(allChanged, `"chat":true}`, `"chat":null}`, 1), "invalid_request", "prefs[0]: event, push and chat are required"},
		{"null field", `{"prefs":null,"reminder_time":"16:30"}`, "invalid_request", "prefs: must not be null"},
		{"wrong type", strings.Replace(allChanged, `"push":false`, `"push":"no"`, 1), "invalid_request", "want a JSON bool"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec, body := serve(t, router, signedIn(http.MethodPut, notificationsPath, strings.NewReader(tc.body)))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body)
			}
			e, _ := body["error"].(map[string]any)
			if e["code"] != tc.code || !strings.Contains(e["message"].(string), tc.message) {
				t.Errorf("error = %v, want code %s with %q", e, tc.code, tc.message)
			}
		})
	}

	// Nothing was changed.
	_, got := serve(t, router, signedIn(http.MethodGet, notificationsPath, nil))
	if want := (map[string]any{"prefs": seedPrefs, "reminder_time": "16:30"}); !reflect.DeepEqual(got, want) {
		t.Errorf("GET after rejected PUTs = %v, want the seed %v", got, want)
	}
}

func TestPutNotificationSettingsNeedsCSRF(t *testing.T) {
	t.Parallel()
	router := newTestRouterWith(t, "dev", newTestSettings(t), newTestRunner(t))

	req := signedIn(http.MethodPut, notificationsPath, strings.NewReader(allChanged))
	req.Header.Del(CSRFHeader)
	if rec, _ := serve(t, router, req); rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403: %s", rec.Code, rec.Body)
	}
}
