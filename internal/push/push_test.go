package push

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/vapid"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/migrations"
)

const endpoint = "https://fcm.googleapis.com/fcm/send/abc123"

func newTestService(t *testing.T) (*Service, *store.Store) {
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
	pair, err := vapid.Generate()
	if err != nil {
		t.Fatal(err)
	}
	svc, err := NewService(st, pair.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return svc, st
}

// browserKeys returns a p256dh and auth as a browser's
// PushSubscription.toJSON() gives them.
func browserKeys(t *testing.T) (string, string) {
	t.Helper()
	k, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	rand.Read(auth)
	return base64.RawURLEncoding.EncodeToString(k.PublicKey().Bytes()), base64.RawURLEncoding.EncodeToString(auth)
}

type storedRow struct {
	id, p256dh, auth, createdAt, updatedAt string
	ua                                     *string
	failures                               int
}

func rows(t *testing.T, st *store.Store) map[string]storedRow {
	t.Helper()
	rs, err := st.Reader.Query(`SELECT endpoint, id, p256dh, auth, user_agent, failure_count, created_at, updated_at FROM push_subscriptions`)
	if err != nil {
		t.Fatal(err)
	}
	defer rs.Close()
	got := map[string]storedRow{}
	for rs.Next() {
		var ep string
		var r storedRow
		if err := rs.Scan(&ep, &r.id, &r.p256dh, &r.auth, &r.ua, &r.failures, &r.createdAt, &r.updatedAt); err != nil {
			t.Fatal(err)
		}
		got[ep] = r
	}
	if err := rs.Err(); err != nil {
		t.Fatal(err)
	}
	return got
}

func TestNewServiceRefusesBadPublicKey(t *testing.T) {
	t.Parallel()
	_, st := newTestService(t)
	if _, err := NewService(st, "not-a-key"); err == nil {
		t.Error("NewService accepted a malformed public key")
	}
	if _, err := NewService(nil, ""); err == nil {
		t.Error("NewService accepted a nil store")
	}
}

func TestSubscribeUpsertsByEndpoint(t *testing.T) {
	t.Parallel()
	svc, st := newTestService(t)
	ctx := context.Background()
	p1, a1 := browserKeys(t)
	ua := "Mozilla/5.0 (iPad)"

	first, err := svc.Subscribe(ctx, SubscribeRequest{Endpoint: endpoint, P256dh: p1, Auth: a1, UserAgent: &ua})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == "" || first.Endpoint != endpoint || first.UserAgent == nil || *first.UserAgent != ua || first.CreatedAt != first.UpdatedAt {
		t.Errorf("first Subscribe = %+v", first)
	}
	if _, err := st.Writer.Exec(`UPDATE push_subscriptions SET failure_count = 3`); err != nil {
		t.Fatal(err)
	}

	// The same endpoint again, with the same keys and then rotated ones,
	// updates the one row.
	again, err := svc.Subscribe(ctx, SubscribeRequest{Endpoint: endpoint, P256dh: p1, Auth: a1, UserAgent: &ua})
	if err != nil {
		t.Fatal(err)
	}
	p2, a2 := browserKeys(t)
	rotated, err := svc.Subscribe(ctx, SubscribeRequest{Endpoint: endpoint, P256dh: p2, Auth: a2})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []Subscription{again, rotated} {
		if s.ID != first.ID || s.CreatedAt != first.CreatedAt {
			t.Errorf("resubscribe = %+v, want id %s and created_at %s kept", s, first.ID, first.CreatedAt)
		}
	}
	if rotated.UserAgent != nil {
		t.Errorf("resubscribe without user_agent kept %q", *rotated.UserAgent)
	}
	got := rows(t, st)
	if len(got) != 1 {
		t.Fatalf("rows = %d, want 1: %+v", len(got), got)
	}
	r := got[endpoint]
	if r.p256dh != p2 || r.auth != a2 || r.ua != nil || r.failures != 0 || r.updatedAt < r.createdAt {
		t.Errorf("stored row = %+v, want rotated keys, no user agent, failures cleared", r)
	}

	// A second browser is a second row.
	other := endpoint + "-other"
	if _, err := svc.Subscribe(ctx, SubscribeRequest{Endpoint: other, P256dh: p1, Auth: a1}); err != nil {
		t.Fatal(err)
	}
	if got := rows(t, st); len(got) != 2 {
		t.Errorf("rows after a second endpoint = %d, want 2", len(got))
	}
}

func TestUnsubscribe(t *testing.T) {
	t.Parallel()
	svc, st := newTestService(t)
	ctx := context.Background()
	p, a := browserKeys(t)
	for _, ep := range []string{endpoint, endpoint + "-keep"} {
		if _, err := svc.Subscribe(ctx, SubscribeRequest{Endpoint: ep, P256dh: p, Auth: a}); err != nil {
			t.Fatal(err)
		}
	}

	removed, err := svc.Unsubscribe(ctx, endpoint)
	if err != nil || !removed {
		t.Errorf("Unsubscribe = %v, %v; want true, nil", removed, err)
	}
	got := rows(t, st)
	if _, ok := got[endpoint]; ok || len(got) != 1 {
		t.Errorf("rows after Unsubscribe = %+v, want only the other endpoint", got)
	}
	removed, err = svc.Unsubscribe(ctx, endpoint)
	if err != nil || removed {
		t.Errorf("Unsubscribe of an endpoint not stored = %v, %v; want false, nil", removed, err)
	}
	var invalid *ValidationError
	if _, err := svc.Unsubscribe(ctx, "http://push.example.com/x"); !errors.As(err, &invalid) {
		t.Errorf("Unsubscribe of an http endpoint = %v, want *ValidationError", err)
	}
}

func TestSubscribeValidates(t *testing.T) {
	t.Parallel()
	svc, st := newTestService(t)
	p, a := browserKeys(t)
	pair, err := vapid.Generate()
	if err != nil {
		t.Fatal(err)
	}
	blank, long := "  ", strings.Repeat("x", MaxUserAgentBytes+1)
	offCurve := make([]byte, 65)
	offCurve[0] = 0x04
	raw := base64.RawURLEncoding.EncodeToString

	for _, tc := range []struct {
		name  string
		req   SubscribeRequest
		field string
	}{
		{"missing endpoint", SubscribeRequest{P256dh: p, Auth: a}, "endpoint"},
		{"http endpoint", SubscribeRequest{Endpoint: "http://push.example.com/x", P256dh: p, Auth: a}, "endpoint"},
		{"relative endpoint", SubscribeRequest{Endpoint: "/fcm/send/x", P256dh: p, Auth: a}, "endpoint"},
		{"endpoint with credentials", SubscribeRequest{Endpoint: "https://u:p@push.example.com/x", P256dh: p, Auth: a}, "endpoint"},
		{"endpoint too long", SubscribeRequest{Endpoint: "https://push.example.com/" + strings.Repeat("x", MaxEndpointBytes), P256dh: p, Auth: a}, "endpoint"},
		{"missing p256dh", SubscribeRequest{Endpoint: endpoint, Auth: a}, "keys.p256dh"},
		{"padded p256dh", SubscribeRequest{Endpoint: endpoint, P256dh: p + "=", Auth: a}, "keys.p256dh"},
		{"short p256dh", SubscribeRequest{Endpoint: endpoint, P256dh: a, Auth: a}, "keys.p256dh"},
		{"p256dh off the curve", SubscribeRequest{Endpoint: endpoint, P256dh: raw(offCurve), Auth: a}, "keys.p256dh"},
		{"private key as p256dh", SubscribeRequest{Endpoint: endpoint, P256dh: pair.PrivateKey, Auth: a}, "keys.p256dh"},
		{"missing auth", SubscribeRequest{Endpoint: endpoint, P256dh: p}, "keys.auth"},
		{"long auth", SubscribeRequest{Endpoint: endpoint, P256dh: p, Auth: raw(make([]byte, 32))}, "keys.auth"},
		{"blank user agent", SubscribeRequest{Endpoint: endpoint, P256dh: p, Auth: a, UserAgent: &blank}, "user_agent"},
		{"long user agent", SubscribeRequest{Endpoint: endpoint, P256dh: p, Auth: a, UserAgent: &long}, "user_agent"},
	} {
		_, err := svc.Subscribe(context.Background(), tc.req)
		var invalid *ValidationError
		if !errors.As(err, &invalid) || len(invalid.Problems) != 1 || invalid.Problems[0].Field != tc.field {
			t.Errorf("%s: Subscribe error = %v, want one problem for %s", tc.name, err, tc.field)
		}
	}
	if got := rows(t, st); len(got) != 0 {
		t.Errorf("invalid requests stored %d rows", len(got))
	}
}
