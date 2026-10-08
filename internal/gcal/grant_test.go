package gcal_test

import (
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

// refusals are the ways Google refuses the stored grant: invalid_grant on
// the token refresh, and HTTP 401 from the Calendar API to the access token
// just refreshed.
var refusals = map[string]func(*gcaltest.Google){
	"invalid_grant":       (*gcaltest.Google).RevokeGrant,
	"401 after a refresh": (*gcaltest.Google).RejectAccessToken,
}

// calls are the Calendar calls that use the stored grant.
var calls = map[string]func(*harness) error{
	"list calendars": func(h *harness) error {
		_, err := h.svc.Calendars(context.Background())
		return err
	},
	"choose a calendar": func(h *harness) error {
		_, err := h.svc.SelectCalendar(context.Background(), family.ID)
		return err
	},
	"push events": func(h *harness) error {
		return h.svc.PushEvents(context.Background(), []gcal.Event{event("abcde")}, func() error {
			return errors.New("record ran after a refused grant")
		})
	},
}

// TestRefusedGrantRequiresReconnect refuses the grant on every Calendar
// call: the call fails with ErrReconnect, the refresh token is cleared,
// last_sync_error says why, the chosen calendar is kept and Status reports
// reconnect required.
func TestRefusedGrantRequiresReconnect(t *testing.T) {
	t.Parallel()
	for refusal, refuse := range refusals {
		for call, do := range calls {
			t.Run(refusal+"/"+call, func(t *testing.T) {
				t.Parallel()
				h := chosen(t)
				refuse(h.google)
				h.clock = h.clock.Add(time.Hour)
				err := do(h)
				if !errors.Is(err, gcal.ErrReconnect) {
					t.Fatalf("%s = %v, want ErrReconnect", call, err)
				}
				assertNoSecret(t, "error", err.Error())
				row, ok := h.row(t)
				switch {
				case !ok:
					t.Fatal("a refused grant forgot the connection")
				case row.RefreshTokenCt != nil:
					t.Error("refresh token kept after Google refused it")
				case row.LastSyncError.String != "Google access was revoked or expired — reconnect":
					t.Errorf("last_sync_error = %v", row.LastSyncError)
				case row.CalendarID.String != dinners.ID || row.CalendarName.String != dinners.Summary:
					t.Errorf("calendar = %v %v, want %s kept", row.CalendarID, row.CalendarName, dinners.ID)
				case row.UpdatedAt != "2026-10-07T19:30:00.000Z":
					t.Errorf("updated_at = %s, want the refusal's time", row.UpdatedAt)
				}
				st, err := h.svc.Status(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				if st.State != gcal.StatusReconnectRequired || st.LastSyncError == nil || *st.LastSyncError != gcal.ReconnectReason {
					t.Errorf("Status = %+v, want reconnect_required with the reason", st)
				}

				// The cleared grant is never offered to Google again, by
				// any call.
				tokens, cals, events := h.google.TokenCalls(), h.google.CalendarCalls(), h.google.EventRequests()
				for again, do := range calls {
					if err := do(h); !errors.Is(err, gcal.ErrReconnect) {
						t.Errorf("%s after the refusal = %v, want ErrReconnect", again, err)
					}
				}
				if h.google.TokenCalls() != tokens || h.google.CalendarCalls() != cals || h.google.EventRequests() != events {
					t.Error("a call reached Google after the grant was refused")
				}
			})
		}
	}
}

// TestRefusedGrantBeforeACalendarIsChosen refuses the grant while no
// calendar is chosen, on each call that can record it then (listing and
// choosing): every Calendar call, push included, is ErrReconnect rather than
// ErrNoCalendar, since only connecting again restores the grant. After
// connecting again and choosing a calendar, a push succeeds.
func TestRefusedGrantBeforeACalendarIsChosen(t *testing.T) {
	t.Parallel()
	for refusal, refuse := range refusals {
		for _, call := range []string{"list calendars", "choose a calendar"} {
			t.Run(refusal+"/"+call, func(t *testing.T) {
				t.Parallel()
				h := connected(t)
				refuse(h.google)
				if err := calls[call](h); !errors.Is(err, gcal.ErrReconnect) {
					t.Fatalf("%s = %v, want ErrReconnect", call, err)
				}
				if row, _ := h.row(t); row.RefreshTokenCt != nil || row.CalendarID.Valid {
					t.Fatalf("row = %+v, want the grant cleared and no calendar chosen", row)
				}
				tokens, cals, events := h.google.TokenCalls(), h.google.CalendarCalls(), h.google.EventRequests()
				for again, do := range calls {
					if err := do(h); !errors.Is(err, gcal.ErrReconnect) {
						t.Errorf("%s after the refusal = %v, want ErrReconnect", again, err)
					}
				}
				if h.google.TokenCalls() != tokens || h.google.CalendarCalls() != cals || h.google.EventRequests() != events {
					t.Error("a call reached Google after the grant was refused")
				}

				h.google.AcceptAccessToken()
				c := h.connect(t, session)
				if _, err := h.callback(c, session, h.consented(c)); err != nil {
					t.Fatalf("Complete: %v", err)
				}
				if _, err := h.svc.SelectCalendar(context.Background(), dinners.ID); err != nil {
					t.Fatalf("SelectCalendar after connecting again: %v", err)
				}
				if err := h.svc.PushEvents(context.Background(), []gcal.Event{event("abcde")}, noRecord); err != nil {
					t.Errorf("PushEvents after connecting again: %v", err)
				}
			})
		}
	}
}

// TestReconnectAfterRefusal connects again after Google refused the grant:
// the new token is stored, the reason cleared and the calendar kept.
func TestReconnectAfterRefusal(t *testing.T) {
	t.Parallel()
	h := chosen(t)
	h.google.RevokeGrant()
	if _, err := h.svc.Calendars(context.Background()); !errors.Is(err, gcal.ErrReconnect) {
		t.Fatalf("Calendars = %v, want ErrReconnect", err)
	}
	c := h.connect(t, session)
	if _, err := h.callback(c, session, h.consented(c)); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	st, err := h.svc.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.State != gcal.StatusConnected || st.LastSyncError != nil || st.CalendarID == nil || *st.CalendarID != dinners.ID {
		t.Errorf("Status = %+v, want connected to %s with no error", st, dinners.ID)
	}
	if err := h.svc.PushEvents(context.Background(), []gcal.Event{event("abcde")}, noRecord); err != nil {
		t.Errorf("PushEvents after connecting again: %v", err)
	}
}

// TestDisconnectAfterRefusal disconnects a connection whose grant Google
// refused: there is nothing to revoke, and the connection is deleted.
func TestDisconnectAfterRefusal(t *testing.T) {
	t.Parallel()
	h := chosen(t)
	h.google.RevokeGrant()
	if _, err := h.svc.Calendars(context.Background()); !errors.Is(err, gcal.ErrReconnect) {
		t.Fatalf("Calendars = %v, want ErrReconnect", err)
	}
	if err := h.svc.Disconnect(context.Background()); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	if n := len(h.google.Revoked()); n != 0 {
		t.Errorf("revoked %d tokens; a refused grant has none to revoke", n)
	}
	if _, ok := h.row(t); ok {
		t.Error("connection kept after disconnecting")
	}
}

// transients are the failures Google asks clients to retry.
var transients = []int{http.StatusServiceUnavailable, http.StatusInternalServerError, http.StatusTooManyRequests}

// TestTransientFailureIsRetried fails each Google request three times in a
// row: it is tried three times (two retries), then fails as retryable, and
// nothing is disconnected or cleared.
func TestTransientFailureIsRetried(t *testing.T) {
	t.Parallel()
	for _, status := range transients {
		for name, tc := range map[string]struct {
			fail  func(*gcaltest.Google)
			do    func(*harness) error
			tries func(*gcaltest.Google) int
		}{
			"token refresh": {
				fail:  func(g *gcaltest.Google) { g.FailToken(status) },
				do:    calls["list calendars"],
				tries: (*gcaltest.Google).TokenCalls,
			},
			"calendar list": {
				fail:  func(g *gcaltest.Google) { g.FailCalendarList(status) },
				do:    calls["choose a calendar"],
				tries: (*gcaltest.Google).CalendarCalls,
			},
			"event write": {
				fail:  func(g *gcaltest.Google) { g.FailEventWrites(1, 3, status) },
				do:    calls["push events"],
				tries: (*gcaltest.Google).EventRequests,
			},
		} {
			t.Run(name+"/"+http.StatusText(status), func(t *testing.T) {
				t.Parallel()
				h := chosen(t)
				before, _ := h.row(t)
				tried := tc.tries(h.google)
				tc.fail(h.google)
				err := tc.do(h)
				var calendar *gcal.CalendarError
				if !errors.As(err, &calendar) || !calendar.Retryable() {
					t.Fatalf("err = %v, want a retryable *CalendarError", err)
				}
				assertNoSecret(t, "error", err.Error())
				if n := tc.tries(h.google) - tried; n != 3 {
					t.Errorf("tried %d times, want 3", n)
				}
				after, ok := h.row(t)
				if !ok || !reflect.DeepEqual(before, after) {
					t.Errorf("connection = %+v, %v; want it unchanged %+v", after, ok, before)
				}
				if st, err := h.svc.Status(context.Background()); err != nil || st.State != gcal.StatusConnected {
					t.Errorf("Status = %+v, %v; want connected", st, err)
				}
			})
		}
	}
}

// TestTransientFailureRecovers fails each Google request twice: the second
// retry succeeds.
func TestTransientFailureRecovers(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		fail func(*gcaltest.Google)
		do   func(*harness) error
	}{
		"calendar list": {
			fail: func(g *gcaltest.Google) { g.FailCalendarListNext(2, http.StatusServiceUnavailable) },
			do:   calls["choose a calendar"],
		},
		"event write": {
			fail: func(g *gcaltest.Google) { g.FailEventWrites(1, 2, http.StatusServiceUnavailable) },
			do: func(h *harness) error {
				return h.svc.PushEvents(context.Background(), []gcal.Event{event("abcde")}, noRecord)
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := chosen(t)
			tc.fail(h.google)
			if err := tc.do(h); err != nil {
				t.Fatalf("err = %v, want the second retry to succeed", err)
			}
		})
	}
}

// TestTransientFailureBacksOff checks the waits between tries: the first
// retry waits the retry delay, the second twice that.
func TestTransientFailureBacksOff(t *testing.T) {
	t.Parallel()
	h := chosen(t)
	gcal.SetRetryDelay(h.svc, 50*time.Millisecond)
	h.google.FailCalendarList(http.StatusServiceUnavailable)
	start := time.Now()
	if _, err := h.svc.Calendars(context.Background()); err == nil {
		t.Fatal("Calendars succeeded")
	}
	if took := time.Since(start); took < 150*time.Millisecond {
		t.Errorf("three tries took %s, want at least 150ms of waits", took)
	}
}

func TestStatus(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	st, err := h.svc.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := statusJSON(t, st); got != `{"status":"not_connected","calendar_id":null,"calendar_name":null,"connected_at":null,"last_sync_error":null}` {
		t.Errorf("not connected: Status = %s", got)
	}
	c := h.connect(t, session)
	if _, err := h.callback(c, session, h.consented(c)); err != nil {
		t.Fatal(err)
	}
	st, err = h.svc.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := statusJSON(t, st); got != `{"status":"connected","calendar_id":null,"calendar_name":null,"connected_at":"2026-10-07T18:30:00.000Z","last_sync_error":null}` {
		t.Errorf("connected: Status = %s", got)
	}
	h.google.SetCalendars(0, dinners)
	if _, err := h.svc.SelectCalendar(context.Background(), dinners.ID); err != nil {
		t.Fatal(err)
	}
	h.google.RevokeGrant()
	if _, err := h.svc.Calendars(context.Background()); !errors.Is(err, gcal.ErrReconnect) {
		t.Fatalf("Calendars = %v, want ErrReconnect", err)
	}
	st, err = h.svc.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := `{"status":"reconnect_required","calendar_id":"dinners@group.calendar.google.com","calendar_name":"Dinners",` +
		`"connected_at":"2026-10-07T18:30:00.000Z","last_sync_error":"Google access was revoked or expired — reconnect"}`
	if got := statusJSON(t, st); got != want {
		t.Errorf("reconnect required: Status =\n%s\nwant\n%s", got, want)
	}
}

func statusJSON(t *testing.T, st gcal.Status) string {
	t.Helper()
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
