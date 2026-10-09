// Package notifytest builds notify services for tests of the features that
// emit notifications, and reads back what they stored.
package notifytest

import (
	"encoding/json"
	"io"
	"log/slog"
	"testing"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/household"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/notify"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

// New returns a notify service over st, with st's notification preferences
// seeded, so every event type goes to push and plan_ready and list_ready to
// Google Chat too. Its worker is not started: what is emitted stays pending.
func New(t *testing.T, st *store.Store) *notify.Service {
	t.Helper()
	if err := household.SeedNotificationPrefs(t.Context(), st); err != nil {
		t.Fatal(err)
	}
	svc, err := notify.NewService(st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

// Row is one stored notification.
type Row struct {
	Key     string
	Channel notify.Channel
	Payload notify.Payload
}

// Rows returns every notification stored in st whose event is of type ev,
// oldest first.
func Rows(t *testing.T, st *store.Store, ev notify.EventType) []Row {
	t.Helper()
	rs, err := st.Reader.QueryContext(t.Context(),
		`SELECT event_key, channel, payload FROM notification_outbox WHERE event_key LIKE ? ORDER BY created_at, channel`,
		string(ev)+":%")
	if err != nil {
		t.Fatal(err)
	}
	defer rs.Close()
	var out []Row
	for rs.Next() {
		var r Row
		var payload string
		if err := rs.Scan(&r.Key, &r.Channel, &payload); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(payload), &r.Payload); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	if err := rs.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}
