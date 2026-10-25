package push

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"testing"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/chat"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/household"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/notify"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

// deliveredDir holds the push messages these tests deliver, byte for byte;
// web/src/push/notification.test.ts shows and taps the same files, so the
// service worker is tested on exactly what the sender sends.
const deliveredDir = "../../web/src/push/delivered/"

// deliverReminder emits the day-of reminder of Tacos, planned on Wed 14 Oct
// 2026, while Tacos is a Regular, runs between (which may archive it) and
// returns the push message the device then receives from the outbox worker.
func deliverReminder(t *testing.T, between func(st *store.Store)) []byte {
	t.Helper()
	phone := newDevice(t, "/push/phone")
	ps := newPushService(t, map[string]int{phone.path: http.StatusCreated})
	s, st, _ := newTestSender(t, ps, phone)
	ctx, cancel := context.WithCancel(context.Background())
	if err := household.SeedNotificationPrefs(ctx, st); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Writer.Exec(`UPDATE notification_prefs SET push = 1, chat = 0 WHERE event = 'day_of_reminder'`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Writer.Exec(`INSERT INTO dinners (id, name, normalized_name, status, origin, is_favorite, grounding_status, created_at, updated_at)
		VALUES ('01TACOS', 'Tacos', 'tacos', 'regular', 'regular', 0, 'grounded', 'c', 'u')`); err != nil {
		t.Fatal(err)
	}
	n, err := notify.NewService(st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if err := n.Register(notify.ChannelPush, s); err != nil {
		t.Fatal(err)
	}
	date, err := store.ParseLocalDate("2026-10-14")
	if err != nil {
		t.Fatal(err)
	}
	// Stored while Tacos is viewable, before the worker runs.
	if err := n.Emit(ctx, chat.DayOfReminder(date, "01TACOS", "Tacos", true)); err != nil {
		t.Fatal(err)
	}
	between(st)
	if err := n.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); n.Wait() }()

	deadline := time.Now().Add(10 * time.Second)
	for len(ps.requests()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the worker did not deliver the reminder")
		}
		time.Sleep(10 * time.Millisecond)
	}
	reqs := ps.requests()
	if len(reqs) != 1 {
		t.Fatalf("push service got %d requests, want 1", len(reqs))
	}
	return decrypt(t, phone, reqs[0].body)
}

// checkDelivered compares got with the delivered file name.
func checkDelivered(t *testing.T, got []byte, name string) {
	t.Helper()
	want, err := os.ReadFile(deliveredDir + name)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, bytes.TrimSpace(want)) {
		t.Errorf("delivered push message =\n%s\nwant (%s)\n%s", got, name, want)
	}
}

func TestDeliveredReminderOfAViewableDinnerNamesItsDinner(t *testing.T) {
	got := deliverReminder(t, func(*store.Store) {})
	checkDelivered(t, got, "reminder-viewable.json")
}

func TestDeliveredReminderOfADinnerArchivedAfterQueueingOffersNoRecipe(t *testing.T) {
	got := deliverReminder(t, func(st *store.Store) {
		if _, err := st.Writer.Exec(`UPDATE dinners SET status = 'archived' WHERE id = '01TACOS'`); err != nil {
			t.Fatal(err)
		}
	})
	checkDelivered(t, got, "reminder-archived.json")
}
