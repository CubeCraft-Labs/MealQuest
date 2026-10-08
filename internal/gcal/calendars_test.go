package gcal_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"testing"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/gcal"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/gcal/gcaltest"
)

// Calendars the fake lists: two the household can push to, two it cannot,
// and a third writable one with Google fields MealQuest must not pass on.
var (
	ownPrimary = gcaltest.Calendar{ID: "household@gmail.com", Summary: "household@gmail.com", Primary: true, AccessRole: "owner"}
	family     = gcaltest.Calendar{ID: "family@group.calendar.google.com", Summary: "Family", AccessRole: "writer",
		Description: "private notes", SummaryOverride: "Fam"}
	holidays = gcaltest.Calendar{ID: "en.usa#holiday@group.v.calendar.google.com", Summary: "Holidays", AccessRole: "reader"}
	busy     = gcaltest.Calendar{ID: "coworker@example.com", Summary: "Coworker", AccessRole: "freeBusyReader"}
	dinners  = gcaltest.Calendar{ID: "dinners@group.calendar.google.com", Summary: "Dinners", AccessRole: "owner"}
)

// connected is a harness with the household's Google account connected
// and the fake listing every calendar above, two to a page.
func connected(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	c := h.connect(t, session)
	if _, err := h.callback(c, session, h.consented(c)); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	h.google.SetCalendars(2, ownPrimary, holidays, family, busy, dinners)
	return h
}

func TestCalendarsListsWritableCalendars(t *testing.T) {
	t.Parallel()
	h := connected(t)
	cals, err := h.svc.Calendars(context.Background())
	if err != nil {
		t.Fatalf("Calendars: %v", err)
	}
	got, err := json.Marshal(cals)
	if err != nil {
		t.Fatal(err)
	}
	// Exactly the writer and owner calendars, in Google's order, with only
	// id, summary, primary and accessRole.
	want := `[` +
		`{"id":"household@gmail.com","summary":"household@gmail.com","primary":true,"accessRole":"owner"},` +
		`{"id":"family@group.calendar.google.com","summary":"Family","primary":false,"accessRole":"writer"},` +
		`{"id":"dinners@group.calendar.google.com","summary":"Dinners","primary":false,"accessRole":"owner"}]`
	if string(got) != want {
		t.Errorf("Calendars =\n%s\nwant\n%s", got, want)
	}
	if n := h.google.CalendarCalls(); n != 3 {
		t.Errorf("calendarList called %d times, want 3 (every page)", n)
	}
	if _, ok := h.row(t); !ok {
		t.Error("listing changed the connection")
	}
}

func TestCalendarsOverlappingPages(t *testing.T) {
	t.Parallel()
	h := connected(t)
	// Two to a page, family ends the first page and starts the second
	// again, renamed in between.
	renamed := family
	renamed.Summary = "Family (renamed)"
	h.google.SetCalendars(2, ownPrimary, family, renamed, dinners)
	cals, err := h.svc.Calendars(context.Background())
	if err != nil {
		t.Fatalf("Calendars: %v", err)
	}
	got, err := json.Marshal(cals)
	if err != nil {
		t.Fatal(err)
	}
	// family once, as first listed, in Google's order.
	want := `[` +
		`{"id":"household@gmail.com","summary":"household@gmail.com","primary":true,"accessRole":"owner"},` +
		`{"id":"family@group.calendar.google.com","summary":"Family","primary":false,"accessRole":"writer"},` +
		`{"id":"dinners@group.calendar.google.com","summary":"Dinners","primary":false,"accessRole":"owner"}]`
	if string(got) != want {
		t.Errorf("Calendars =\n%s\nwant\n%s", got, want)
	}
	if n := h.google.CalendarCalls(); n != 2 {
		t.Errorf("calendarList called %d times, want 2 (every page)", n)
	}
}

func TestCalendarsEmpty(t *testing.T) {
	t.Parallel()
	h := connected(t)
	h.google.SetCalendars(0, holidays)
	cals, err := h.svc.Calendars(context.Background())
	if err != nil {
		t.Fatalf("Calendars: %v", err)
	}
	if got, _ := json.Marshal(cals); string(got) != "[]" {
		t.Errorf("Calendars = %s, want []", got)
	}
}

func TestCalendarsNotConnected(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	if _, err := h.svc.Calendars(context.Background()); !errors.Is(err, gcal.ErrNotConnected) {
		t.Errorf("Calendars = %v, want ErrNotConnected", err)
	}
	if _, err := h.svc.SelectCalendar(context.Background(), family.ID); !errors.Is(err, gcal.ErrNotConnected) {
		t.Errorf("SelectCalendar = %v, want ErrNotConnected", err)
	}
	if h.google.TokenCalls() != 0 || h.google.CalendarCalls() != 0 {
		t.Error("Google was called with nothing connected")
	}
}

func TestSelectCalendarStoresIDAndName(t *testing.T) {
	t.Parallel()
	h := connected(t)
	before, _ := h.row(t)
	h.clock = h.clock.Add(time.Hour)

	sel, err := h.svc.SelectCalendar(context.Background(), family.ID)
	if err != nil {
		t.Fatalf("SelectCalendar: %v", err)
	}
	if sel != (gcal.Selection{CalendarID: family.ID, CalendarName: "Family"}) {
		t.Errorf("selection = %+v", sel)
	}
	row, _ := h.row(t)
	if row.CalendarID.String != family.ID || !row.CalendarID.Valid ||
		row.CalendarName.String != "Family" || !row.CalendarName.Valid {
		t.Errorf("stored calendar = %v %v, want %s Family", row.CalendarID, row.CalendarName, family.ID)
	}
	if row.UpdatedAt != "2026-10-07T19:30:00.000Z" {
		t.Errorf("updated_at = %q, want the choice's time", row.UpdatedAt)
	}
	if !bytes.Equal(row.RefreshTokenCt, before.RefreshTokenCt) || row.ConnectedAt != before.ConnectedAt || row.CreatedAt != before.CreatedAt {
		t.Error("choosing a calendar changed the connection")
	}

	// Choosing again replaces the choice.
	if _, err := h.svc.SelectCalendar(context.Background(), ownPrimary.ID); err != nil {
		t.Fatalf("second SelectCalendar: %v", err)
	}
	if row, _ := h.row(t); row.CalendarID.String != ownPrimary.ID || row.CalendarName.String != ownPrimary.Summary {
		t.Errorf("stored calendar = %v %v, want the primary", row.CalendarID, row.CalendarName)
	}
}

func TestSelectCalendarRejectsUnlistedCalendar(t *testing.T) {
	t.Parallel()
	for name, id := range map[string]string{
		"unknown":          "nobody@group.calendar.google.com",
		"reader":           holidays.ID,
		"free/busy reader": busy.ID,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := connected(t)
			if _, err := h.svc.SelectCalendar(context.Background(), dinners.ID); err != nil {
				t.Fatal(err)
			}
			before, _ := h.row(t)
			if _, err := h.svc.SelectCalendar(context.Background(), id); !errors.Is(err, gcal.ErrUnknownCalendar) {
				t.Fatalf("SelectCalendar(%s) = %v, want ErrUnknownCalendar", id, err)
			}
			if row, _ := h.row(t); !reflect.DeepEqual(row, before) {
				t.Errorf("a refused choice changed the row: %+v", row)
			}
		})
	}
}

// TestSelectCalendarNoLongerWritable chooses a calendar, then has Google
// stop listing it as writable, as when it is deleted or the household's
// access drops to reader. It leaves the listing, and choosing it again is
// refused; the stored choice stays until the household chooses another.
func TestSelectCalendarNoLongerWritable(t *testing.T) {
	t.Parallel()
	for name, now := range map[string][]gcaltest.Calendar{
		"deleted":         {ownPrimary},
		"demoted to read": {ownPrimary, {ID: family.ID, Summary: family.Summary, AccessRole: "reader"}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := connected(t)
			if _, err := h.svc.SelectCalendar(context.Background(), family.ID); err != nil {
				t.Fatal(err)
			}
			h.google.SetCalendars(0, now...)
			cals, err := h.svc.Calendars(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(cals) != 1 || cals[0].ID != ownPrimary.ID {
				t.Errorf("Calendars = %+v, want only the primary", cals)
			}
			if _, err := h.svc.SelectCalendar(context.Background(), family.ID); !errors.Is(err, gcal.ErrUnknownCalendar) {
				t.Errorf("choosing it again = %v, want ErrUnknownCalendar", err)
			}
			if row, _ := h.row(t); row.CalendarID.String != family.ID {
				t.Errorf("stored calendar = %v, want the earlier choice kept", row.CalendarID)
			}
		})
	}
}

// TestCalendarsGrantNoLongerAccepted lists calendars after Google stops
// accepting MealQuest's grant. The failure is ErrReconnect; the connection
// is left as it is.
func TestCalendarsGrantNoLongerAccepted(t *testing.T) {
	t.Parallel()
	for name, edit := range map[string]func(*gcaltest.Google){
		"refresh refused":            (*gcaltest.Google).RevokeGrant,
		"calendar list unauthorized": func(g *gcaltest.Google) { g.FailCalendarList(http.StatusUnauthorized) },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := connected(t)
			edit(h.google)
			_, err := h.svc.Calendars(context.Background())
			if !errors.Is(err, gcal.ErrReconnect) {
				t.Fatalf("Calendars = %v, want ErrReconnect", err)
			}
			assertNoSecret(t, "error", err.Error())
			_, err = h.svc.SelectCalendar(context.Background(), family.ID)
			if !errors.Is(err, gcal.ErrReconnect) {
				t.Fatalf("SelectCalendar = %v, want ErrReconnect", err)
			}
			row, ok := h.row(t)
			if !ok || row.CalendarID.Valid {
				t.Errorf("row = %+v, %v: want the connection kept with no calendar", row, ok)
			}
		})
	}
}

func TestCalendarsGoogleFailureIsRedacted(t *testing.T) {
	t.Parallel()
	for name, edit := range map[string]func(*gcaltest.Google){
		"token endpoint": func(g *gcaltest.Google) { g.FailToken(http.StatusServiceUnavailable) },
		"calendar list":  func(g *gcaltest.Google) { g.FailCalendarList(http.StatusForbidden) },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := connected(t)
			edit(h.google)
			_, err := h.svc.Calendars(context.Background())
			var calendar *gcal.CalendarError
			if !errors.As(err, &calendar) {
				t.Fatalf("Calendars = %v, want *CalendarError", err)
			}
			assertNoSecret(t, "error", err.Error())
		})
	}
}

// TestSelectCalendarHoldsTheAccount holds a calendar choice at Google while
// a disconnect starts: the disconnect waits for the choice, so it never
// revokes the token the choice is using.
func TestSelectCalendarHoldsTheAccount(t *testing.T) {
	t.Parallel()
	h := connected(t)
	h.google.SetCalendars(0, family)
	arrived, release := h.google.HoldCalendarList()
	defer release()
	selected := make(chan error, 1)
	go func() {
		_, err := h.svc.SelectCalendar(context.Background(), family.ID)
		selected <- err
	}()
	<-arrived
	disconnected := make(chan error, 1)
	go func() { disconnected <- h.svc.Disconnect(context.Background()) }()
	select {
	case err := <-disconnected:
		t.Fatalf("Disconnect finished (%v) while a calendar choice was at Google", err)
	case <-time.After(200 * time.Millisecond):
	}
	if n := len(h.google.Revoked()); n != 0 {
		t.Fatalf("revoked %d tokens while a calendar choice was at Google", n)
	}
	release()
	if err := <-selected; err != nil {
		t.Errorf("SelectCalendar: %v", err)
	}
	if err := <-disconnected; err != nil {
		t.Errorf("Disconnect: %v", err)
	}
	if _, ok := h.row(t); ok {
		t.Error("connection stored after the disconnect")
	}
}
