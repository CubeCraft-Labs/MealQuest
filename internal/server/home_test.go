package server

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/household"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/notify"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

// GET /home answers the Home screen in its JSON shape; newTestHome's
// household (America/New_York) has no plan, no suggestions, no Supply run
// and no notifications.
func TestGetHome(t *testing.T) {
	t.Parallel()
	h := newTestRouter(t, "dev")
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	// want is the answer at instant now; the request is read between two
	// instants, so it matches the answer at one of them.
	want := func(now time.Time) string {
		week := household.WeekStart(household.Today(now, loc))
		label, err := household.WeekLabel(week)
		if err != nil {
			t.Fatal(err)
		}
		body, err := json.Marshal(map[string]any{
			"dateLabel":      now.In(loc).Format("Monday, Jan 2"),
			"tonight":        nil,
			"sideQuestCount": 0,
			"shoppingList":   map[string]any{"weekStart": week.String(), "label": label, "ready": false, "sentToChatAt": nil},
			"recentUpdates":  map[string]any{"unread": false},
		})
		if err != nil {
			t.Fatal(err)
		}
		return string(body)
	}
	before := time.Now()
	rec, _ := serve(t, h, signedIn(http.MethodGet, "/api/v1/home", nil))
	after := time.Now()
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/home = %d: %s", rec.Code, rec.Body)
	}
	var got any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	canonical, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if string(canonical) != want(before) && string(canonical) != want(after) {
		t.Errorf("GET /api/v1/home = %s, want %s", canonical, want(after))
	}
}

func TestGetHomeNeedsSession(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	newTestRouter(t, "dev").ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/home", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("GET /api/v1/home without a session = %d, want 401", rec.Code)
	}
}

// homeRouter returns a router whose Home reads st, and st.
func homeRouter(t *testing.T) (http.Handler, *store.Store) {
	t.Helper()
	st := newTestStore(t)
	svc := newTestHomeOn(t, st)
	return newTestRouterWithHome(t, "dev", newTestSettings(t), newTestRunner(t), svc), st
}

// storeEvent stores event key in st's outbox at instant at for each of
// channels, as notify stores an event, and returns at as stored.
func storeEvent(t *testing.T, st *store.Store, typ notify.EventType, key, title string, at time.Time, channels ...notify.Channel) string {
	t.Helper()
	payload, err := json.Marshal(notify.Payload{Event: typ, Title: title, Body: "Body of " + key, URL: "/plan", Tag: key})
	if err != nil {
		t.Fatal(err)
	}
	ts := store.Timestamp{Time: at.UTC().Truncate(time.Millisecond)}.String()
	for _, ch := range channels {
		if _, err := db.New(st.Writer).InsertNotification(t.Context(), db.InsertNotificationParams{
			ID: store.NewID(), EventKey: key, Channel: string(ch), Payload: string(payload),
			NextAttemptAt: sql.NullString{String: ts, Valid: true}, Now: ts,
		}); err != nil {
			t.Fatal(err)
		}
	}
	return ts
}

// unread reads recentUpdates.unread from GET /home.
func unread(t *testing.T, h http.Handler) bool {
	t.Helper()
	rec, body := serve(t, h, signedIn(http.MethodGet, "/api/v1/home", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/home = %d: %s", rec.Code, rec.Body)
	}
	updates, ok := body["recentUpdates"].(map[string]any)
	if !ok {
		t.Fatalf("GET /api/v1/home has no recentUpdates: %s", rec.Body)
	}
	return updates["unread"] == true
}

// markSeen posts body to /updates/seen and returns the answer.
func markSeen(t *testing.T, h http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, signedIn(http.MethodPost, "/api/v1/updates/seen", strings.NewReader(body)))
	return rec
}

// GET /updates lists the last distinct events newest first, one per event
// whatever its channels, in their JSON shape.
func TestGetUpdates(t *testing.T) {
	t.Parallel()
	h, st := homeRouter(t)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, signedIn(http.MethodGet, "/api/v1/updates", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != `{"updates":[]}` {
		t.Errorf("GET /api/v1/updates with none = %d %s, want 200 {\"updates\":[]}", rec.Code, rec.Body)
	}

	now := time.Now()
	older := storeEvent(t, st, notify.NewSuggestions, "new_suggestions:1", "New Side quests", now.Add(-2*time.Minute), notify.ChannelPush)
	newer := storeEvent(t, st, notify.PlanReady, "plan_ready:2026-10-12", "Your week is drafted", now.Add(-time.Minute), notify.ChannelPush, notify.ChannelChat)

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, signedIn(http.MethodGet, "/api/v1/updates", nil))
	want := `{"updates":[` +
		`{"type":"plan_ready","title":"Your week is drafted","body":"Body of plan_ready:2026-10-12","url":"/plan","created_at":"` + newer + `"},` +
		`{"type":"new_suggestions","title":"New Side quests","body":"Body of new_suggestions:1","url":"/plan","created_at":"` + older + `"}]}`
	if rec.Code != http.StatusOK || rec.Body.String() != want {
		t.Errorf("GET /api/v1/updates = %d %s, want 200 %s", rec.Code, rec.Body, want)
	}
}

// Unread is true while an event newer than the last seen one exists,
// clears once POST /updates/seen marks the newest seen, and returns when a
// newer event is stored; marking an older one seen again moves nothing.
func TestUpdatesSeen(t *testing.T) {
	t.Parallel()
	h, st := homeRouter(t)
	now := time.Now()

	if unread(t, h) {
		t.Error("unread with no events")
	}
	first := storeEvent(t, st, notify.PlanReady, "plan_ready:1", "Drafted", now.Add(-2*time.Minute), notify.ChannelPush, notify.ChannelChat)
	if !unread(t, h) {
		t.Error("not unread with an event never seen")
	}
	if rec := markSeen(t, h, `{"through":"`+first+`"}`); rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Fatalf("POST /api/v1/updates/seen = %d %s, want 204", rec.Code, rec.Body)
	}
	if unread(t, h) {
		t.Error("unread after the newest event was marked seen")
	}

	second := storeEvent(t, st, notify.ListReady, "list_ready:2", "Supply run ready", now.Add(-time.Minute), notify.ChannelPush)
	if !unread(t, h) {
		t.Error("not unread with an event newer than the last seen")
	}
	// Already seen: marking the older event seen again is accepted and
	// changes nothing.
	if rec := markSeen(t, h, `{"through":"`+first+`"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("POST /api/v1/updates/seen of an older event = %d %s, want 204", rec.Code, rec.Body)
	}
	if !unread(t, h) {
		t.Error("marking an older event seen hid a newer one")
	}
	if rec := markSeen(t, h, `{"through":"`+second+`"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("POST /api/v1/updates/seen = %d %s, want 204", rec.Code, rec.Body)
	}
	if unread(t, h) {
		t.Error("unread after the newer event was marked seen")
	}
}

// A malformed POST /updates/seen is 400 and records nothing.
func TestUpdatesSeenRejectsMalformed(t *testing.T) {
	t.Parallel()
	h, st := homeRouter(t)
	storeEvent(t, st, notify.PlanReady, "plan_ready:1", "Drafted", time.Now().Add(-time.Minute), notify.ChannelPush)

	for _, body := range []string{
		``,
		`[]`,
		`{}`,
		`{"through":null}`,
		`{"through":42}`,
		`{"through":"yesterday"}`,
		`{"through":"2026-10-13 12:00:00"}`,
		`{"through":"2026-10-13T12:00:00.000Z","extra":true}`,
		`{"through":"2026-10-13T12:00:00.000Z"} {}`,
		`{"through":"` + time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano) + `"}`,
	} {
		rec := markSeen(t, h, body)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"invalid_request"`) {
			t.Errorf("POST /api/v1/updates/seen %q = %d %s, want 400 invalid_request", body, rec.Code, rec.Body)
		}
	}
	if !unread(t, h) {
		t.Error("a malformed POST /updates/seen marked updates seen")
	}
}

// Both endpoints need a session, and POST /updates/seen the CSRF token.
func TestUpdatesNeedSessionAndCSRF(t *testing.T) {
	t.Parallel()
	h, _ := homeRouter(t)
	body := `{"through":"2026-10-01T12:00:00.000Z"}`
	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/api/v1/updates", nil),
		httptest.NewRequest(http.MethodPost, "/api/v1/updates/seen", strings.NewReader(body)),
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without a session = %d, want 401", req.Method, req.URL.Path, rec.Code)
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/updates/seen", strings.NewReader(body))
	req.AddCookie(validSessionCookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("POST /api/v1/updates/seen without the CSRF token = %d, want 403", rec.Code)
	}
}
