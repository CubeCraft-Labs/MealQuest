package gcal_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/gcal"
)

// chosen is a harness connected with dinners chosen as the calendar to
// push to.
func chosen(t *testing.T) *harness {
	t.Helper()
	h := connected(t)
	if _, err := h.svc.SelectCalendar(context.Background(), dinners.ID); err != nil {
		t.Fatal(err)
	}
	return h
}

// noRecord is a PushEvents record that stores nothing.
func noRecord() error { return nil }

// event is a valid event with id.
func event(id string) gcal.Event {
	start := time.Date(2026, 10, 12, 21, 30, 0, 0, time.UTC)
	return gcal.Event{ID: id, Summary: "Dinner: Beef Chili", Description: "Beef Chili",
		Start: start, End: start.Add(time.Hour), TimeZone: "America/New_York"}
}

func TestPushEventsInsertsThenUpdates(t *testing.T) {
	t.Parallel()
	h := chosen(t)
	if err := h.svc.PushEvents(context.Background(), []gcal.Event{event("abcde0"), event("abcde1")}, noRecord); err != nil {
		t.Fatalf("PushEvents: %v", err)
	}
	renamed := event("abcde0")
	renamed.Summary = "Dinner: Pot Roast"
	if err := h.svc.PushEvents(context.Background(), []gcal.Event{renamed}, noRecord); err != nil {
		t.Fatalf("PushEvents again: %v", err)
	}
	if inserts, updates := h.google.EventWrites(); inserts != 2 || updates != 1 {
		t.Errorf("%d inserts and %d updates, want 2 and 1", inserts, updates)
	}
	events := h.google.Events(dinners.ID)
	if len(events) != 2 || events["abcde0"].Summary != "Dinner: Pot Roast" {
		t.Errorf("events = %+v", events)
	}
	if got := events["abcde1"].Start.DateTime; got != "2026-10-12T21:30:00Z" {
		t.Errorf("start = %s, want the instant as given", got)
	}
}

func TestPushEventsRefusesInvalidEvents(t *testing.T) {
	t.Parallel()
	h := chosen(t)
	before := h.google.TokenCalls()
	for name, edit := range map[string]func(*gcal.Event){
		"uppercase id":      func(e *gcal.Event) { e.ID = "ABCDE" },
		"id outside a-v":    func(e *gcal.Event) { e.ID = "abcdw" },
		"id too short":      func(e *gcal.Event) { e.ID = "abcd" },
		"id too long":       func(e *gcal.Event) { e.ID = strings.Repeat("a", 1025) },
		"no summary":        func(e *gcal.Event) { e.Summary = "" },
		"ends as it starts": func(e *gcal.Event) { e.End = e.Start },
		"no time zone":      func(e *gcal.Event) { e.TimeZone = "" },
	} {
		ev := event("abcde")
		edit(&ev)
		if err := h.svc.PushEvents(context.Background(), []gcal.Event{event("abcde0"), ev}, noRecord); err == nil {
			t.Errorf("%s: PushEvents succeeded", name)
		}
	}
	if err := h.svc.PushEvents(context.Background(), nil, noRecord); err == nil {
		t.Error("PushEvents of no events succeeded")
	}
	if err := h.svc.PushEvents(context.Background(), []gcal.Event{event(strings.Repeat("v", 1024))}, noRecord); err != nil {
		t.Errorf("PushEvents of a 1024-character id: %v", err)
	}
	if inserts, _ := h.google.EventWrites(); inserts != 1 {
		t.Errorf("%d events inserted, want only the valid one", inserts)
	}
	if n := h.google.TokenCalls() - before; n != 1 {
		t.Errorf("token endpoint called %d times, want once, for the valid push", n)
	}
}

func TestPushEventsGrantNoLongerAccepted(t *testing.T) {
	t.Parallel()
	h := chosen(t)
	h.google.FailEventWrite(1, http.StatusUnauthorized)
	err := h.svc.PushEvents(context.Background(), []gcal.Event{event("abcde")}, func() error {
		t.Error("record ran after a failed write")
		return nil
	})
	if !errors.Is(err, gcal.ErrReconnect) {
		t.Fatalf("PushEvents = %v, want ErrReconnect", err)
	}
	assertNoSecret(t, "error", err.Error())
	if _, ok := h.row(t); !ok {
		t.Error("a refused grant forgot the connection")
	}
}

func TestPushEventsGoogleFailureIsRedacted(t *testing.T) {
	t.Parallel()
	for name, n := range map[string]int{"insert": 1, "update": 2} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := chosen(t)
			if name == "update" {
				if err := h.svc.PushEvents(context.Background(), []gcal.Event{event("abcde")}, noRecord); err != nil {
					t.Fatal(err)
				}
			}
			// The fake's failure body quotes the access token.
			h.google.FailEventWrite(n, http.StatusInternalServerError)
			err := h.svc.PushEvents(context.Background(), []gcal.Event{event("abcde")}, noRecord)
			var calendar *gcal.CalendarError
			if !errors.As(err, &calendar) {
				t.Fatalf("PushEvents = %v, want *CalendarError", err)
			}
			if !strings.Contains(err.Error(), "event "+name) || !strings.Contains(err.Error(), "HTTP 500") {
				t.Errorf("error = %q, want the failed %s and its status", err, name)
			}
			assertNoSecret(t, "error", err.Error())
		})
	}
}

// TestPushEventsHoldsTheAccount holds an event write at Google while a
// disconnect starts: the disconnect waits for the push, so it never
// revokes the token the push is using.
func TestPushEventsHoldsTheAccount(t *testing.T) {
	t.Parallel()
	h := chosen(t)
	arrived, release := h.google.HoldEvents()
	defer release()
	pushed := make(chan error, 1)
	go func() { pushed <- h.svc.PushEvents(context.Background(), []gcal.Event{event("abcde")}, noRecord) }()
	<-arrived
	disconnected := make(chan error, 1)
	go func() { disconnected <- h.svc.Disconnect(context.Background()) }()
	select {
	case err := <-disconnected:
		t.Fatalf("Disconnect finished (%v) while a push was at Google", err)
	case <-time.After(200 * time.Millisecond):
	}
	if n := len(h.google.Revoked()); n != 0 {
		t.Fatalf("revoked %d tokens while a push was at Google", n)
	}
	release()
	if err := <-pushed; err != nil {
		t.Errorf("PushEvents: %v", err)
	}
	if err := <-disconnected; err != nil {
		t.Errorf("Disconnect: %v", err)
	}
}

// TestPushEventsRecordsWhileTheCalendarIsChosen chooses another calendar
// while a push records: the choice waits for the record, so a push never
// records events on a calendar no longer chosen.
func TestPushEventsRecordsWhileTheCalendarIsChosen(t *testing.T) {
	t.Parallel()
	h := chosen(t)
	selected := make(chan error, 1)
	err := h.svc.PushEvents(context.Background(), []gcal.Event{event("abcde")}, func() error {
		go func() {
			_, err := h.svc.SelectCalendar(context.Background(), family.ID)
			selected <- err
		}()
		select {
		case err := <-selected:
			t.Errorf("SelectCalendar finished (%v) while a push was recording", err)
		case <-time.After(200 * time.Millisecond):
		}
		return nil
	})
	if err != nil {
		t.Fatalf("PushEvents: %v", err)
	}
	if err := <-selected; err != nil {
		t.Errorf("SelectCalendar: %v", err)
	}
}
