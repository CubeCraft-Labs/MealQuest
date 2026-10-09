package notify

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/household"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/migrations"
)

// start is the fake clock's first instant.
var start = time.Date(2026, 10, 9, 15, 0, 0, 0, time.UTC)

// clock is a controllable clock.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

// fakeSender records the messages it is given and fails while fail is set.
type fakeSender struct {
	mu   sync.Mutex
	fail error
	got  []Message
}

func (f *fakeSender) Send(_ context.Context, m Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = append(f.got, m)
	return f.fail
}

func (f *fakeSender) messages() []Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Message(nil), f.got...)
}

func newTestService(t *testing.T) (*Service, *store.Store, *clock) {
	t.Helper()
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
	if err := household.SeedNotificationPrefs(ctx, st); err != nil {
		t.Fatal(err)
	}
	svc, err := NewService(st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	c := &clock{t: start}
	svc.now = c.now
	return svc, st, c
}

func setPref(t *testing.T, st *store.Store, ev EventType, push, chat bool) {
	t.Helper()
	res, err := st.Writer.Exec(`UPDATE notification_prefs SET push = ?, chat = ? WHERE event = ?`, push, chat, string(ev))
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		t.Fatalf("no notification preference for %s", ev)
	}
}

// outbox returns the stored rows of key by channel.
func outbox(t *testing.T, st *store.Store, key string) map[Channel]db.NotificationOutbox {
	t.Helper()
	rs, err := st.Reader.Query(`SELECT id, event_key, channel, payload, status, attempts, next_attempt_at, last_error, created_at, updated_at
		FROM notification_outbox WHERE event_key = ?`, key)
	if err != nil {
		t.Fatal(err)
	}
	defer rs.Close()
	got := map[Channel]db.NotificationOutbox{}
	for rs.Next() {
		var r db.NotificationOutbox
		if err := rs.Scan(&r.ID, &r.EventKey, &r.Channel, &r.Payload, &r.Status, &r.Attempts, &r.NextAttemptAt, &r.LastError, &r.CreatedAt, &r.UpdatedAt); err != nil {
			t.Fatal(err)
		}
		if _, dup := got[Channel(r.Channel)]; dup {
			t.Fatalf("two %s rows for %s", r.Channel, key)
		}
		got[Channel(r.Channel)] = r
	}
	if err := rs.Err(); err != nil {
		t.Fatal(err)
	}
	return got
}

func onlyRow(t *testing.T, st *store.Store, key string, ch Channel) db.NotificationOutbox {
	t.Helper()
	rows := outbox(t, st, key)
	r, ok := rows[ch]
	if !ok || len(rows) != 1 {
		t.Fatalf("rows of %s = %v, want one %s row", key, rows, ch)
	}
	return r
}

func planReady(key string) Event {
	return Event{
		Type:  PlanReady,
		Key:   key,
		Title: "This week's plan is ready",
		Body:  "Five dinners on the Draft board.",
		URL:   "/plan",
		Week:  mustDate("2026-10-12"),
	}
}

func mustDate(s string) store.LocalDate {
	d, err := store.ParseLocalDate(s)
	if err != nil {
		panic(err)
	}
	return d
}

func TestEmitRespectsPrefsMatrix(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		push, chat bool
		want       []Channel
	}{
		{push: false, chat: false, want: nil},
		{push: true, chat: false, want: []Channel{ChannelPush}},
		{push: false, chat: true, want: []Channel{ChannelChat}},
		{push: true, chat: true, want: []Channel{ChannelPush, ChannelChat}},
	} {
		t.Run(fmt.Sprintf("push=%v,chat=%v", tc.push, tc.chat), func(t *testing.T) {
			svc, st, _ := newTestService(t)
			for ev := range eventTypes {
				setPref(t, st, ev, tc.push, tc.chat)
				key := string(ev) + ":2026-10-12"
				if err := svc.Emit(ctx, Event{Type: ev, Key: key, Title: "Title", Body: "Body", URL: "/"}); err != nil {
					t.Fatal(err)
				}
				rows := outbox(t, st, key)
				if len(rows) != len(tc.want) {
					t.Fatalf("%s: %d rows, want channels %v", ev, len(rows), tc.want)
				}
				for _, ch := range tc.want {
					r, ok := rows[ch]
					if !ok {
						t.Fatalf("%s: no %s row", ev, ch)
					}
					now := timestamp(start)
					if r.Status != "pending" || r.Attempts != 0 || r.NextAttemptAt.String != now || r.CreatedAt != now || r.LastError.Valid {
						t.Errorf("%s %s row = %+v, want pending, due now", ev, ch, r)
					}
				}
			}
		})
	}
}

func TestEmitStoresPayload(t *testing.T) {
	svc, st, _ := newTestService(t)
	setPref(t, st, PlanReady, true, true)
	if err := svc.Emit(context.Background(), planReady("plan_ready:2026-10-12")); err != nil {
		t.Fatal(err)
	}
	want := `{"event":"plan_ready","title":"This week's plan is ready","body":"Five dinners on the Draft board.","url":"/plan","tag":"plan_ready:2026-10-12","week":"2026-10-12"}`
	for ch, r := range outbox(t, st, "plan_ready:2026-10-12") {
		if r.Payload != want {
			t.Errorf("%s payload = %s, want %s", ch, r.Payload, want)
		}
	}

	// An event about no week omits it.
	if err := svc.Emit(context.Background(), Event{Type: NewSuggestions, Key: "new_suggestions:batch1", Title: "New Side quests", URL: "/suggestions"}); err != nil {
		t.Fatal(err)
	}
	r := onlyRow(t, st, "new_suggestions:batch1", ChannelPush)
	if want := `{"event":"new_suggestions","title":"New Side quests","body":"","url":"/suggestions","tag":"new_suggestions:batch1"}`; r.Payload != want {
		t.Errorf("payload = %s, want %s", r.Payload, want)
	}
}

func TestEmitIgnoresDuplicateKey(t *testing.T) {
	ctx := context.Background()
	svc, st, c := newTestService(t)
	setPref(t, st, PlanReady, true, false)
	const key = "plan_ready:2026-10-12"
	if err := svc.Emit(ctx, planReady(key)); err != nil {
		t.Fatal(err)
	}
	first := onlyRow(t, st, key, ChannelPush)

	c.set(start.Add(time.Minute))
	again := planReady(key)
	again.Title = "A different title"
	if err := svc.Emit(ctx, again); err != nil {
		t.Fatalf("duplicate emit: %v", err)
	}
	if got := onlyRow(t, st, key, ChannelPush); got != first {
		t.Fatalf("duplicate emit changed the row:\n got %+v\nwant %+v", got, first)
	}

	// Once sent, a duplicate still leaves the row as it is.
	sender := &fakeSender{}
	if err := svc.Register(ChannelPush, sender); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.deliverDue(ctx); err != nil {
		t.Fatal(err)
	}
	sent := onlyRow(t, st, key, ChannelPush)
	if sent.Status != "sent" {
		t.Fatalf("status = %s, want sent", sent.Status)
	}
	c.set(start.Add(2 * time.Minute))
	if err := svc.Emit(ctx, again); err != nil {
		t.Fatalf("emit after sent: %v", err)
	}
	if got := onlyRow(t, st, key, ChannelPush); got != sent {
		t.Fatalf("emit after sent changed the row:\n got %+v\nwant %+v", got, sent)
	}
	if _, err := svc.deliverDue(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(sender.messages()); n != 1 {
		t.Fatalf("%d deliveries, want 1", n)
	}
}

func TestEmitRejectsInvalidEvents(t *testing.T) {
	svc, st, _ := newTestService(t)
	valid := planReady("plan_ready:2026-10-12")
	for name, mutate := range map[string]func(*Event){
		"unknown type":         func(e *Event) { e.Type = "dinner_burnt" },
		"empty key":            func(e *Event) { e.Key = "" },
		"key without id":       func(e *Event) { e.Key = "plan_ready:" },
		"key of another type":  func(e *Event) { e.Key = "list_ready:2026-10-12" },
		"blank title":          func(e *Event) { e.Title = "  " },
		"empty url":            func(e *Event) { e.URL = "" },
		"relative url":         func(e *Event) { e.URL = "plan" },
		"absolute url":         func(e *Event) { e.URL = "https://evil.example/plan" },
		"protocol-relative":    func(e *Event) { e.URL = "//evil.example/plan" },
		"backslash url":        func(e *Event) { e.URL = "/\\evil.example" },
		"url with a tab":       func(e *Event) { e.URL = "/\t/evil.example" },
		"url with a bad query": func(e *Event) { e.URL = "/plan%zz" },
	} {
		t.Run(name, func(t *testing.T) {
			ev := valid
			mutate(&ev)
			if err := svc.Emit(context.Background(), ev); !errors.Is(err, ErrInvalidEvent) {
				t.Fatalf("Emit = %v, want ErrInvalidEvent", err)
			}
		})
	}
	var n int
	if err := st.Reader.QueryRow(`SELECT COUNT(*) FROM notification_outbox`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("%d rows stored, want 0", n)
	}
}

func TestEmitWithoutPrefIsAnError(t *testing.T) {
	svc, st, _ := newTestService(t)
	if _, err := st.Writer.Exec(`DELETE FROM notification_prefs WHERE event = 'plan_ready'`); err != nil {
		t.Fatal(err)
	}
	err := svc.Emit(context.Background(), planReady("plan_ready:2026-10-12"))
	if err == nil || errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("Emit = %v, want a missing-preference error", err)
	}
}

func TestDeliveryMarkedSent(t *testing.T) {
	ctx := context.Background()
	svc, st, c := newTestService(t)
	setPref(t, st, PlanReady, true, true)
	push, chat := &fakeSender{}, &fakeSender{}
	if err := svc.Register(ChannelPush, push); err != nil {
		t.Fatal(err)
	}
	if err := svc.Register(ChannelChat, chat); err != nil {
		t.Fatal(err)
	}
	const key = "plan_ready:2026-10-12"
	if err := svc.Emit(ctx, planReady(key)); err != nil {
		t.Fatal(err)
	}
	sentAt := start.Add(3 * time.Second)
	c.set(sentAt)
	wait, err := svc.deliverDue(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if wait != -1 {
		t.Errorf("wait = %v, want -1 with nothing pending", wait)
	}

	rows := outbox(t, st, key)
	wantPayload := Payload{Event: PlanReady, Title: "This week's plan is ready", Body: "Five dinners on the Draft board.", URL: "/plan", Tag: key, Week: "2026-10-12"}
	for ch, sender := range map[Channel]*fakeSender{ChannelPush: push, ChannelChat: chat} {
		r := rows[ch]
		if r.Status != "sent" || r.Attempts != 1 || r.NextAttemptAt.Valid || r.UpdatedAt != timestamp(sentAt) || r.LastError.Valid {
			t.Errorf("%s row = %+v, want sent at %s after 1 attempt", ch, r, timestamp(sentAt))
		}
		got := sender.messages()
		want := Message{ID: r.ID, EventKey: key, Channel: ch, Payload: wantPayload, Attempt: 1}
		if len(got) != 1 || got[0] != want {
			t.Errorf("%s sender got %+v, want [%+v]", ch, got, want)
		}
	}
}

func TestRetryBackoffSchedule(t *testing.T) {
	ctx := context.Background()
	svc, st, c := newTestService(t)
	setPref(t, st, PlanReady, true, false)
	sender := &fakeSender{fail: errors.New("push service unavailable")}
	if err := svc.Register(ChannelPush, sender); err != nil {
		t.Fatal(err)
	}
	const key = "plan_ready:2026-10-12"
	if err := svc.Emit(ctx, planReady(key)); err != nil {
		t.Fatal(err)
	}

	now := start
	for i, wantDelay := range []time.Duration{
		time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 16 * time.Minute,
		32 * time.Minute, time.Hour, time.Hour, time.Hour,
	} {
		c.set(now)
		wait, err := svc.deliverDue(ctx)
		if err != nil {
			t.Fatal(err)
		}
		r := onlyRow(t, st, key, ChannelPush)
		wantNext := now.Add(wantDelay)
		if r.Status != "pending" || r.Attempts != int64(i+1) || r.NextAttemptAt.String != timestamp(wantNext) ||
			r.LastError.String != "push service unavailable" {
			t.Fatalf("after failure %d row = %+v, want pending, attempts %d, next %s", i+1, r, i+1, timestamp(wantNext))
		}
		if wait != wantDelay {
			t.Fatalf("after failure %d wait = %v, want %v", i+1, wait, wantDelay)
		}

		// Not due a moment before the retry time.
		c.set(wantNext.Add(-time.Millisecond))
		if _, err := svc.deliverDue(ctx); err != nil {
			t.Fatal(err)
		}
		if n := len(sender.messages()); n != i+1 {
			t.Fatalf("%d deliveries before the retry was due, want %d", n, i+1)
		}
		now = wantNext
	}
	for i, m := range sender.messages() {
		if m.Attempt != int64(i+1) {
			t.Errorf("delivery %d has Attempt %d", i+1, m.Attempt)
		}
	}
}

func TestRetryThenSent(t *testing.T) {
	ctx := context.Background()
	svc, st, c := newTestService(t)
	setPref(t, st, ListReady, false, true)
	sender := &fakeSender{fail: errors.New("chat returned 503")}
	if err := svc.Register(ChannelChat, sender); err != nil {
		t.Fatal(err)
	}
	const key = "list_ready:2026-10-12"
	if err := svc.Emit(ctx, Event{Type: ListReady, Key: key, Title: "Supply run ready", URL: "/supply-run"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.deliverDue(ctx); err != nil {
		t.Fatal(err)
	}
	sender.mu.Lock()
	sender.fail = nil
	sender.mu.Unlock()
	c.set(start.Add(time.Minute))
	if _, err := svc.deliverDue(ctx); err != nil {
		t.Fatal(err)
	}
	r := onlyRow(t, st, key, ChannelChat)
	if r.Status != "sent" || r.Attempts != 2 || r.UpdatedAt != timestamp(start.Add(time.Minute)) {
		t.Fatalf("row = %+v, want sent on attempt 2", r)
	}
}

func TestUndeliverableFailsAtOnce(t *testing.T) {
	ctx := context.Background()
	svc, st, _ := newTestService(t)
	setPref(t, st, PlanReady, false, true)
	sender := &fakeSender{fail: fmt.Errorf("%w: chat refused the message (status 400)", ErrUndeliverable)}
	if err := svc.Register(ChannelChat, sender); err != nil {
		t.Fatal(err)
	}
	const key = "plan_ready:2026-10-12"
	if err := svc.Emit(ctx, planReady(key)); err != nil {
		t.Fatal(err)
	}
	wait, err := svc.deliverDue(ctx)
	if err != nil {
		t.Fatal(err)
	}
	r := onlyRow(t, st, key, ChannelChat)
	if r.Status != "failed" || r.Attempts != 1 || r.LastError.String != sender.fail.Error() {
		t.Fatalf("row = %+v, want failed after one attempt with the sender's error", r)
	}
	if wait != -1 || len(sender.messages()) != 1 {
		t.Fatalf("wait = %v after %d deliveries, want nothing pending after one", wait, len(sender.messages()))
	}
}

func TestGiveUpAfter24Hours(t *testing.T) {
	ctx := context.Background()
	svc, st, c := newTestService(t)
	setPref(t, st, DayOfReminder, true, false)
	sender := &fakeSender{fail: errors.New("push service unavailable")}
	if err := svc.Register(ChannelPush, sender); err != nil {
		t.Fatal(err)
	}
	const key = "day_of_reminder:2026-10-09"
	if err := svc.Emit(ctx, Event{Type: DayOfReminder, Key: key, Title: "Tonight: tacos", URL: "/"}); err != nil {
		t.Fatal(err)
	}

	// Deliver each retry as it falls due until the row leaves pending.
	deadline := start.Add(24 * time.Hour)
	var times []time.Time
	for range 100 {
		r := onlyRow(t, st, key, ChannelPush)
		if r.Status != "pending" {
			break
		}
		due, err := parseTimestamp(r.NextAttemptAt.String)
		if err != nil {
			t.Fatal(err)
		}
		if due.After(deadline) {
			t.Fatalf("retry due %s, after the 24 h deadline %s", due, deadline)
		}
		c.set(due)
		times = append(times, due)
		if _, err := svc.deliverDue(ctx); err != nil {
			t.Fatal(err)
		}
	}

	// Attempts at 0, 1, 3, 7, 15, 31 and 63 minutes, then hourly to 1383
	// minutes (22 more): 29 in all. The row is due once more at the
	// deadline, where it is failed without being sent.
	const wantAttempts = 29
	r := onlyRow(t, st, key, ChannelPush)
	if r.Status != "failed" || r.Attempts != wantAttempts || r.NextAttemptAt.Valid ||
		r.LastError.String != "not delivered within 24 hours of creation; last error: push service unavailable" ||
		r.UpdatedAt != timestamp(deadline) {
		t.Fatalf("row = %+v, want failed at %s after %d attempts", r, timestamp(deadline), wantAttempts)
	}
	if len(times) != wantAttempts+1 || !times[len(times)-1].Equal(deadline) || !times[len(times)-2].Equal(start.Add(1383*time.Minute)) {
		t.Fatalf("due at %v, want %d attempts ending 1383 min after creation, then the deadline", times, wantAttempts)
	}
	if n := len(sender.messages()); n != wantAttempts {
		t.Fatalf("%d deliveries, want %d", n, wantAttempts)
	}

	// A failed row is never delivered again.
	c.set(deadline.Add(48 * time.Hour))
	if _, err := svc.deliverDue(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(sender.messages()); n != wantAttempts {
		t.Fatalf("%d deliveries, want %d", n, wantAttempts)
	}
}

// TestOverdueRowIsNeverDelivered covers a row first claimed 24 hours or
// more after its creation, as when the service was down: it is failed
// without calling the sender, whether the sender would succeed or fail.
func TestOverdueRowIsNeverDelivered(t *testing.T) {
	for _, tc := range []struct {
		name string
		at   time.Duration
		fail error
	}{
		{"succeeding sender at 24 h", 24 * time.Hour, nil},
		{"succeeding sender after 24 h", 25 * time.Hour, nil},
		{"failing sender at 24 h", 24 * time.Hour, errors.New("gone")},
		{"failing sender after 24 h", 25 * time.Hour, errors.New("gone")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			svc, st, c := newTestService(t)
			setPref(t, st, PlanReady, true, false)
			sender := &fakeSender{fail: tc.fail}
			if err := svc.Register(ChannelPush, sender); err != nil {
				t.Fatal(err)
			}
			const key = "plan_ready:2026-10-12"
			if err := svc.Emit(ctx, planReady(key)); err != nil {
				t.Fatal(err)
			}
			c.set(start.Add(tc.at))
			if _, err := svc.deliverDue(ctx); err != nil {
				t.Fatal(err)
			}
			if n := len(sender.messages()); n != 0 {
				t.Fatalf("%d deliveries of an overdue row, want 0", n)
			}
			r := onlyRow(t, st, key, ChannelPush)
			if r.Status != "failed" || r.Attempts != 0 || r.NextAttemptAt.Valid ||
				r.LastError.String != "not delivered within 24 hours of creation" || r.UpdatedAt != timestamp(start.Add(tc.at)) {
				t.Fatalf("row = %+v, want failed without an attempt", r)
			}
		})
	}
}

// TestRowJustBeforeDeadlineIsDelivered pins the boundary from the other
// side: a row claimed just under 24 hours after its creation is sent.
func TestRowJustBeforeDeadlineIsDelivered(t *testing.T) {
	ctx := context.Background()
	svc, st, c := newTestService(t)
	setPref(t, st, PlanReady, true, false)
	sender := &fakeSender{}
	if err := svc.Register(ChannelPush, sender); err != nil {
		t.Fatal(err)
	}
	const key = "plan_ready:2026-10-12"
	if err := svc.Emit(ctx, planReady(key)); err != nil {
		t.Fatal(err)
	}
	c.set(start.Add(24*time.Hour - time.Millisecond))
	if _, err := svc.deliverDue(ctx); err != nil {
		t.Fatal(err)
	}
	if r := onlyRow(t, st, key, ChannelPush); r.Status != "sent" || r.Attempts != 1 || len(sender.messages()) != 1 {
		t.Fatalf("row = %+v after %d deliveries, want sent once", r, len(sender.messages()))
	}
}

func TestChannelWithoutSenderFails(t *testing.T) {
	ctx := context.Background()
	svc, st, _ := newTestService(t)
	setPref(t, st, PlanReady, true, true)
	push := &fakeSender{}
	if err := svc.Register(ChannelPush, push); err != nil {
		t.Fatal(err)
	}
	const key = "plan_ready:2026-10-12"
	if err := svc.Emit(ctx, planReady(key)); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.deliverDue(ctx); err != nil {
		t.Fatal(err)
	}
	rows := outbox(t, st, key)
	if r := rows[ChannelChat]; r.Status != "failed" || r.Attempts != 0 || r.LastError.String != "no sender registered for channel chat" {
		t.Fatalf("chat row = %+v, want failed with no sender", r)
	}
	if r := rows[ChannelPush]; r.Status != "sent" {
		t.Fatalf("push row = %+v, want sent", r)
	}
}

func TestLeasedRowIsNotClaimedAgain(t *testing.T) {
	ctx := context.Background()
	svc, st, c := newTestService(t)
	setPref(t, st, PlanReady, true, false)
	sender := &fakeSender{}
	if err := svc.Register(ChannelPush, sender); err != nil {
		t.Fatal(err)
	}
	const key = "plan_ready:2026-10-12"
	if err := svc.Emit(ctx, planReady(key)); err != nil {
		t.Fatal(err)
	}
	// Another tick claims the row and stops before recording the delivery.
	lease := start.Add(leaseFor)
	if _, err := db.New(st.Writer).ClaimDueNotification(ctx, db.ClaimDueNotificationParams{
		LeaseUntil: sql.NullString{String: timestamp(lease), Valid: true}, Now: timestamp(start),
	}); err != nil {
		t.Fatal(err)
	}
	c.set(lease.Add(-time.Millisecond))
	if _, err := svc.deliverDue(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(sender.messages()); n != 0 {
		t.Fatalf("%d deliveries of a leased row, want 0", n)
	}
	// Once the lease ends the row is delivered.
	c.set(lease)
	if _, err := svc.deliverDue(ctx); err != nil {
		t.Fatal(err)
	}
	if r := onlyRow(t, st, key, ChannelPush); r.Status != "sent" || r.Attempts != 1 || len(sender.messages()) != 1 {
		t.Fatalf("row = %+v after %d deliveries, want sent once", r, len(sender.messages()))
	}
}

// blockingSender holds every Send until release is closed.
type blockingSender struct {
	fakeSender
	entered chan struct{}
	release chan struct{}
}

func (b *blockingSender) Send(ctx context.Context, m Message) error {
	b.entered <- struct{}{}
	<-b.release
	return b.fakeSender.Send(ctx, m)
}

func TestConcurrentTicksDeliverOnce(t *testing.T) {
	ctx := context.Background()
	svc, st, _ := newTestService(t)
	setPref(t, st, PlanReady, true, false)
	sender := &blockingSender{entered: make(chan struct{}, 10), release: make(chan struct{})}
	if err := svc.Register(ChannelPush, sender); err != nil {
		t.Fatal(err)
	}
	const key = "plan_ready:2026-10-12"
	if err := svc.Emit(ctx, planReady(key)); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	first := make(chan error, 1)
	wg.Go(func() {
		_, err := svc.deliverDue(ctx)
		first <- err
	})
	<-sender.entered // the first tick holds the row
	if _, err := svc.deliverDue(ctx); err != nil {
		t.Fatal(err)
	}
	close(sender.release)
	wg.Wait()
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if n := len(sender.messages()); n != 1 {
		t.Fatalf("%d deliveries, want 1", n)
	}
	if r := onlyRow(t, st, key, ChannelPush); r.Status != "sent" {
		t.Fatalf("row = %+v, want sent", r)
	}
}

func TestRetryDelay(t *testing.T) {
	for attempt, want := range map[int64]time.Duration{
		1: time.Minute, 2: 2 * time.Minute, 3: 4 * time.Minute, 6: 32 * time.Minute,
		7: time.Hour, 8: time.Hour, 1000: time.Hour,
	} {
		if got := retryDelay(attempt); got != want {
			t.Errorf("retryDelay(%d) = %v, want %v", attempt, got, want)
		}
	}
}

func TestRegister(t *testing.T) {
	svc, _, _ := newTestService(t)
	if err := svc.Register("email", &fakeSender{}); err == nil {
		t.Error("registered an unknown channel")
	}
	if err := svc.Register(ChannelPush, nil); err == nil {
		t.Error("registered a nil sender")
	}
	if err := svc.Register(ChannelPush, &fakeSender{}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Register(ChannelPush, &fakeSender{}); err == nil {
		t.Error("registered a second push sender")
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := svc.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := svc.Register(ChannelChat, &fakeSender{}); err == nil {
		t.Error("registered a sender after Start")
	}
	if err := svc.Start(ctx); err == nil {
		t.Error("started twice")
	}
	cancel()
	svc.Wait()
}

// TestWorkerDeliversEmittedEvent runs the worker on the real clock: Emit
// wakes it, it delivers, and it stops when its context is cancelled.
func TestWorkerDeliversEmittedEvent(t *testing.T) {
	svc, st, _ := newTestService(t)
	svc.now = time.Now
	setPref(t, st, PlanReady, true, false)
	sender := &fakeSender{}
	if err := svc.Register(ChannelPush, sender); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := svc.Start(ctx); err != nil {
		t.Fatal(err)
	}
	const key = "plan_ready:2026-10-12"
	if err := svc.Emit(context.Background(), planReady(key)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for onlyRow(t, st, key, ChannelPush).Status != "sent" {
		if time.Now().After(deadline) {
			t.Fatal("worker did not deliver the notification")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	svc.Wait()
	if n := len(sender.messages()); n != 1 {
		t.Fatalf("%d deliveries, want 1", n)
	}
}
