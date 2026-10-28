package chat

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/household"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/notify"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

// recordingSender is a notify.Sender that records what the worker delivers.
type recordingSender struct {
	mu  sync.Mutex
	got []notify.Message
}

func (s *recordingSender) Send(_ context.Context, m notify.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.got = append(s.got, m)
	return nil
}

func (s *recordingSender) messages() []notify.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]notify.Message(nil), s.got...)
}

// planDinnerOn stores dinnerID, named name, on date so a reminder can be
// checked against that plan day.
func planDinnerOn(t *testing.T, st *store.Store, dinnerID, name, date string) {
	t.Helper()
	ctx := t.Context()
	if _, err := st.Writer.ExecContext(ctx, `INSERT INTO dinners (id, name, normalized_name, status, origin, is_favorite, grounding_status, created_at, updated_at)
		VALUES (?, ?, ?, 'regular', 'regular', 0, 'grounded', 'c', 'u')`, dinnerID, name, name); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Writer.ExecContext(ctx, `INSERT INTO week_plans (id, week_start, status, created_at, updated_at)
		VALUES ('plan-1', '2026-10-12', 'draft', 'c', 'u')`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Writer.ExecContext(ctx, `INSERT INTO plan_days (id, plan_id, date, dinner_id, is_new, created_at, updated_at)
		VALUES ('day-1', 'plan-1', ?, ?, 0, 'c', 'u')`, date, dinnerID); err != nil {
		t.Fatal(err)
	}
}

func clearDinnerOn(t *testing.T, st *store.Store, date string) {
	t.Helper()
	res, err := st.Writer.ExecContext(t.Context(), `UPDATE plan_days SET dinner_id = NULL WHERE date = ?`, date)
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		t.Fatalf("cleared %d plan days on %s, want 1", n, date)
	}
}

// deliverReminder starts the outbox worker and waits until the push row of
// key is sent or failed.
func deliverReminder(t *testing.T, svc *notify.Service, st *store.Store, sender *recordingSender, key string) (status, lastError, payload string) {
	t.Helper()
	if err := svc.Register(notify.ChannelPush, sender); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	if err := svc.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		svc.Wait()
	})
	deadline := time.Now().Add(5 * time.Second)
	for {
		var last sql.NullString
		err := st.Reader.QueryRowContext(t.Context(),
			`SELECT status, last_error, payload FROM notification_outbox WHERE event_key = ? AND channel = 'push'`, key).
			Scan(&status, &last, &payload)
		if err != nil {
			t.Fatal(err)
		}
		if last.Valid {
			lastError = last.String
		}
		if status == "sent" || status == "failed" {
			return status, lastError, payload
		}
		if time.Now().After(deadline) {
			t.Fatalf("notification %s still %s", key, status)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func newReminderService(t *testing.T) (*notify.Service, *store.Store) {
	t.Helper()
	st := newTestStore(t)
	if err := household.SeedNotificationPrefs(t.Context(), st); err != nil {
		t.Fatal(err)
	}
	svc, err := notify.NewService(st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return svc, st
}

func queuedObject(t *testing.T, payload string) map[string]any {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal([]byte(payload), &got); err != nil {
		t.Fatal(err)
	}
	return got
}

// TestQueuedReminderCarriesTheDinnerID is MQ18-09. A day-of reminder and a
// crock-pot start reminder carry the planned dinner's id even when they
// offer no recipe, and a queued reminder is not delivered once that dinner
// has left its date. The same reminders are delivered while the dinner stays
// on the date. A manual night has no dinner and is still delivered.
func TestQueuedReminderCarriesTheDinnerID(t *testing.T) {
	const (
		dinnerID = "01J9CHILI"
		name     = "Beef Chili"
		date     = "2026-10-14"
	)
	day := mustDate(t, date)
	startBy := time.Date(2026, 10, 14, 9, 30, 0, 0, time.FixedZone("CDT", -5*60*60))
	noRecipe := []struct {
		name string
		ev   func() notify.Event
	}{
		{"day-of reminder", func() notify.Event { return DayOfReminder(day, dinnerID, name, false) }},
		{"start reminder", func() notify.Event { return StartReminder(day, dinnerID, name, startBy, false) }},
	}

	for _, tc := range noRecipe {
		t.Run(tc.name+" is suppressed when its dinner left the date", func(t *testing.T) {
			svc, st := newReminderService(t)
			planDinnerOn(t, st, dinnerID, name, date)
			ev := tc.ev()
			if err := svc.Emit(t.Context(), ev); err != nil {
				t.Fatal(err)
			}
			clearDinnerOn(t, st, date)
			sender := &recordingSender{}
			status, lastError, payload := deliverReminder(t, svc, st, sender, ev.Key)
			if n := len(sender.messages()); n != 0 || status != "failed" {
				t.Fatalf("delivered %d messages, status %s error %q, want the reminder suppressed", n, status, lastError)
			}
			if lastError != "suppressed: the dinner is no longer planned for this date" {
				t.Errorf("last_error = %q, want the dinner-left suppression", lastError)
			}
			got := queuedObject(t, payload)
			if got["planned_dinner_id"] != dinnerID {
				t.Errorf("queued planned_dinner_id = %v, want %s", got["planned_dinner_id"], dinnerID)
			}
			if _, offered := got["dinner"]; offered || got["url"] != "/plan?week=2026-10-12" {
				t.Errorf("queued payload offers a recipe: url %v dinner %v", got["url"], got["dinner"])
			}
		})

		t.Run(tc.name+" is delivered when its dinner stays on the date", func(t *testing.T) {
			svc, st := newReminderService(t)
			planDinnerOn(t, st, dinnerID, name, date)
			ev := tc.ev()
			if err := svc.Emit(t.Context(), ev); err != nil {
				t.Fatal(err)
			}
			sender := &recordingSender{}
			status, lastError, payload := deliverReminder(t, svc, st, sender, ev.Key)
			gotMsg := sender.messages()
			if status != "sent" || len(gotMsg) != 1 {
				t.Fatalf("status %s error %q messages %d, want the reminder delivered", status, lastError, len(gotMsg))
			}
			if gotMsg[0].Payload.Dinner != nil || gotMsg[0].Payload.URL != "/plan?week=2026-10-12" {
				t.Errorf("delivered payload offers a recipe: %+v", gotMsg[0].Payload)
			}
			got := queuedObject(t, payload)
			if got["planned_dinner_id"] != dinnerID {
				t.Errorf("queued planned_dinner_id = %v, want %s", got["planned_dinner_id"], dinnerID)
			}
			if _, offered := got["dinner"]; offered {
				t.Errorf("queued payload names a recipe dinner: %v", got["dinner"])
			}
		})
	}

	t.Run("manual night reminder is delivered", func(t *testing.T) {
		svc, st := newReminderService(t)
		ev := ManualNightReminder(day, "mummy dogs")
		if err := svc.Emit(t.Context(), ev); err != nil {
			t.Fatal(err)
		}
		sender := &recordingSender{}
		status, lastError, payload := deliverReminder(t, svc, st, sender, ev.Key)
		gotMsg := sender.messages()
		if status != "sent" || len(gotMsg) != 1 {
			t.Fatalf("status %s error %q messages %d, want the manual night delivered", status, lastError, len(gotMsg))
		}
		if gotMsg[0].Payload.Title != "Tonight: mummy dogs" || gotMsg[0].Payload.Dinner != nil || gotMsg[0].Payload.URL != "/plan?week=2026-10-12" {
			t.Errorf("delivered payload = %+v, want the label, the week's plan and no dinner", gotMsg[0].Payload)
		}
		got := queuedObject(t, payload)
		if _, ok := got["planned_dinner_id"]; ok || got["dinner"] != nil {
			t.Errorf("manual night payload = %v, want no dinner id", got)
		}
	})
}
