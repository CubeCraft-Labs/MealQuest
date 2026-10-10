package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/household"
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
