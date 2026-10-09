package push

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/household"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/notify"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/vapid"
)

const subject = "mailto:kitchen@example.com"

// sentAt is the sender's fake clock.
var sentAt = time.Date(2026, 10, 9, 17, 0, 0, 0, time.UTC)

// device is a browser's push subscription with the private half of its
// keys, so a test can read what was sent to it.
type device struct {
	path string
	key  *ecdh.PrivateKey
	auth []byte
}

func newDevice(t *testing.T, path string) device {
	t.Helper()
	k, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	rand.Read(auth)
	return device{path: path, key: k, auth: auth}
}

// request is one request the push service received.
type request struct {
	path   string
	header http.Header
	body   []byte
}

// pushService is an httptest push service answering each path with its
// status.
type pushService struct {
	srv    *httptest.Server
	status map[string]int

	mu  sync.Mutex
	got []request
}

func newPushService(t *testing.T, status map[string]int) *pushService {
	t.Helper()
	ps := &pushService{status: status}
	ps.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		ps.mu.Lock()
		ps.got = append(ps.got, request{path: r.URL.Path, header: r.Header.Clone(), body: body})
		ps.mu.Unlock()
		code, ok := ps.status[r.URL.Path]
		if !ok {
			t.Errorf("unexpected push to %s", r.URL.Path)
			code = http.StatusBadRequest
		}
		w.WriteHeader(code)
	}))
	t.Cleanup(ps.srv.Close)
	return ps
}

func (ps *pushService) requests() []request {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return append([]request(nil), ps.got...)
}

// newTestSender returns a sender over a migrated store with the devices
// subscribed to ps, and the VAPID key pair it signs with.
func newTestSender(t *testing.T, ps *pushService, devices ...device) (*Sender, *store.Store, vapid.KeyPair) {
	t.Helper()
	svc, st := newTestService(t)
	for _, d := range devices {
		_, err := svc.Subscribe(context.Background(), SubscribeRequest{
			Endpoint: ps.srv.URL + d.path,
			P256dh:   keyEncoding.EncodeToString(d.key.PublicKey().Bytes()),
			Auth:     keyEncoding.EncodeToString(d.auth),
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	pair, err := vapid.Generate()
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewSender(st, slog.New(slog.NewTextHandler(io.Discard, nil)), pair.PublicKey, pair.PrivateKey, subject)
	if err != nil {
		t.Fatal(err)
	}
	s.client = ps.srv.Client()
	s.now = func() time.Time { return sentAt }
	return s, st, pair
}

func message() notify.Message {
	return notify.Message{
		ID:       "01NOTIFICATION",
		EventKey: "plan_ready:2026-10-12",
		Channel:  notify.ChannelPush,
		Payload: notify.Payload{
			Event: notify.PlanReady,
			Title: "This week's plan is ready",
			Body:  "Five dinners on the Draft board.",
			URL:   "/plan",
			Tag:   "plan_ready:2026-10-12",
			Week:  "2026-10-12",
		},
		Attempt: 1,
	}
}

// pushState is a subscription's delivery record.
type pushState struct {
	failures    int
	lastSuccess sql.NullString
}

// states returns the delivery record of each stored subscription by path.
func states(t *testing.T, st *store.Store, ps *pushService) map[string]pushState {
	t.Helper()
	rs, err := st.Reader.Query(`SELECT endpoint, failure_count, last_success_at FROM push_subscriptions`)
	if err != nil {
		t.Fatal(err)
	}
	defer rs.Close()
	got := map[string]pushState{}
	for rs.Next() {
		var ep string
		var s pushState
		if err := rs.Scan(&ep, &s.failures, &s.lastSuccess); err != nil {
			t.Fatal(err)
		}
		got[strings.TrimPrefix(ep, ps.srv.URL)] = s
	}
	if err := rs.Err(); err != nil {
		t.Fatal(err)
	}
	return got
}

// decrypt reads an aes128gcm push message body (RFC 8291) as d.
func decrypt(t *testing.T, d device, body []byte) []byte {
	t.Helper()
	if len(body) < 21 {
		t.Fatalf("body of %d bytes has no header", len(body))
	}
	salt, rs, idLen := body[:16], binary.BigEndian.Uint32(body[16:20]), int(body[20])
	if rs != 4096 || idLen != 65 {
		t.Fatalf("record size %d, key id length %d", rs, idLen)
	}
	asPublic, ciphertext := body[21:21+idLen], body[21+idLen:]
	peer, err := ecdh.P256().NewPublicKey(asPublic)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := d.key.ECDH(peer)
	if err != nil {
		t.Fatal(err)
	}
	info := append(append([]byte("WebPush: info\x00"), d.key.PublicKey().Bytes()...), asPublic...)
	ikm, err := hkdf.Key(sha256.New, secret, d.auth, string(info), 32)
	if err != nil {
		t.Fatal(err)
	}
	cek, err := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		t.Fatal(err)
	}
	nonce, err := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		t.Fatal(err)
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	plain = bytes.TrimRight(plain, "\x00")
	if len(plain) == 0 || plain[len(plain)-1] != 0x02 {
		t.Fatal("no last-record delimiter")
	}
	return plain[:len(plain)-1]
}

// vapidClaims checks the VAPID Authorization header names pair's public key
// and returns its token's claims (the signature is not checked).
func vapidClaims(t *testing.T, header string, pair vapid.KeyPair) map[string]any {
	t.Helper()
	token, key, ok := strings.Cut(strings.TrimPrefix(header, "vapid t="), ", k=")
	if !ok || !strings.HasPrefix(header, "vapid t=") {
		t.Fatalf("Authorization = %q, want vapid t=…, k=…", header)
	}
	if key != pair.PublicKey {
		t.Errorf("k = %q, want the VAPID public key %q", key, pair.PublicKey)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token has %d parts", len(parts))
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims map[string]any
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatal(err)
	}
	return claims
}

func TestSendDeliversToEveryDevice(t *testing.T) {
	phone, tablet := newDevice(t, "/push/phone"), newDevice(t, "/push/tablet")
	ps := newPushService(t, map[string]int{phone.path: http.StatusCreated, tablet.path: http.StatusCreated})
	s, st, pair := newTestSender(t, ps, phone, tablet)

	if err := s.Send(context.Background(), message()); err != nil {
		t.Fatalf("Send: %v", err)
	}

	reqs := ps.requests()
	if len(reqs) != 2 {
		t.Fatalf("push service got %d requests, want 2", len(reqs))
	}
	for _, r := range reqs {
		d := phone
		if r.path == tablet.path {
			d = tablet
		}
		if got := r.header.Get("TTL"); got != "43200" {
			t.Errorf("%s: TTL = %q, want 43200 (12 hours)", r.path, got)
		}
		if got := r.header.Get("Content-Encoding"); got != "aes128gcm" {
			t.Errorf("%s: Content-Encoding = %q", r.path, got)
		}
		claims := vapidClaims(t, r.header.Get("Authorization"), pair)
		if claims["sub"] != subject {
			t.Errorf("%s: VAPID sub = %v, want %s", r.path, claims["sub"], subject)
		}
		if claims["aud"] != ps.srv.URL {
			t.Errorf("%s: VAPID aud = %v, want %s", r.path, claims["aud"], ps.srv.URL)
		}
		var got map[string]string
		if err := json.Unmarshal(decrypt(t, d, r.body), &got); err != nil {
			t.Fatal(err)
		}
		want := map[string]string{
			"title": "This week's plan is ready",
			"body":  "Five dinners on the Draft board.",
			"url":   "/plan",
			"tag":   "plan_ready:2026-10-12",
		}
		if len(got) != len(want) {
			t.Errorf("%s: payload = %v, want %v", r.path, got, want)
		}
		for k, v := range want {
			if got[k] != v {
				t.Errorf("%s: payload %s = %q, want %q", r.path, k, got[k], v)
			}
		}
	}

	want := pushState{lastSuccess: sql.NullString{String: "2026-10-09T17:00:00Z", Valid: true}}
	for path, got := range states(t, st, ps) {
		if got.failures != want.failures || got.lastSuccess.Valid != want.lastSuccess.Valid ||
			!sameInstant(t, got.lastSuccess.String, want.lastSuccess.String) {
			t.Errorf("%s: state = %+v, want %+v", path, got, want)
		}
	}
}

func sameInstant(t *testing.T, a, b string) bool {
	t.Helper()
	ta, err := time.Parse(time.RFC3339Nano, a)
	if err != nil {
		t.Fatalf("last_success_at %q is not RFC 3339: %v", a, err)
	}
	tb, err := time.Parse(time.RFC3339Nano, b)
	if err != nil {
		t.Fatal(err)
	}
	return ta.Equal(tb) && strings.HasSuffix(a, "Z")
}

func TestSendRemovesGoneSubscriptions(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusGone} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			gone := newDevice(t, "/push/gone")
			ps := newPushService(t, map[string]int{gone.path: status})
			s, st, _ := newTestSender(t, ps, gone)

			err := s.Send(context.Background(), message())
			if err == nil {
				t.Fatal("Send with only a gone subscription succeeded, want an error so the outbox retries")
			}
			if strings.Contains(err.Error(), ps.srv.URL) {
				t.Errorf("error %q quotes the endpoint", err)
			}
			if got := states(t, st, ps); len(got) != 0 {
				t.Errorf("subscriptions = %v, want the gone one removed", got)
			}
		})
	}
}

func TestSendSucceedsWhenOneDeviceAccepts(t *testing.T) {
	ok, gone, broken := newDevice(t, "/push/ok"), newDevice(t, "/push/gone"), newDevice(t, "/push/broken")
	ps := newPushService(t, map[string]int{
		ok.path: http.StatusCreated, gone.path: http.StatusGone, broken.path: http.StatusInternalServerError,
	})
	s, st, _ := newTestSender(t, ps, ok, gone, broken)

	if err := s.Send(context.Background(), message()); err != nil {
		t.Fatalf("Send: %v", err)
	}
	got := states(t, st, ps)
	if len(got) != 2 {
		t.Fatalf("subscriptions = %v, want ok and broken kept, gone removed", got)
	}
	if s := got[ok.path]; !s.lastSuccess.Valid || s.failures != 0 {
		t.Errorf("ok = %+v, want last_success_at set and no failures", s)
	}
	if s := got[broken.path]; s.lastSuccess.Valid || s.failures != 1 {
		t.Errorf("broken = %+v, want one failure and no success", s)
	}
}

func TestSendServerErrorIsRetryable(t *testing.T) {
	broken := newDevice(t, "/push/broken")
	ps := newPushService(t, map[string]int{broken.path: http.StatusInternalServerError})
	s, st, _ := newTestSender(t, ps, broken)

	for want := 1; want <= 2; want++ {
		err := s.Send(context.Background(), message())
		if err == nil {
			t.Fatal("Send with only a failing subscription succeeded, want an error so the outbox retries")
		}
		if !strings.Contains(err.Error(), "status 500") || strings.Contains(err.Error(), ps.srv.URL) {
			t.Errorf("error = %q, want the status and no endpoint", err)
		}
		got := states(t, st, ps)[broken.path]
		if got.failures != want || got.lastSuccess.Valid {
			t.Errorf("after send %d: state = %+v, want failure_count %d and no success", want, got, want)
		}
	}
}

func TestSendTransportErrorDoesNotQuoteEndpoint(t *testing.T) {
	d := newDevice(t, "/push/secret-token")
	ps := newPushService(t, map[string]int{})
	s, st, _ := newTestSender(t, ps, d)
	ps.srv.Close()

	err := s.Send(context.Background(), message())
	if err == nil {
		t.Fatal("Send to a closed push service succeeded")
	}
	if strings.Contains(err.Error(), "secret-token") {
		t.Errorf("error %q quotes the endpoint", err)
	}
	if got := states(t, st, ps)[d.path]; got.failures != 1 {
		t.Errorf("state = %+v, want one failure", got)
	}
}

func TestSendWithNoSubscriptionsIsSent(t *testing.T) {
	ps := newPushService(t, map[string]int{})
	s, _, _ := newTestSender(t, ps)

	if err := s.Send(context.Background(), message()); err != nil {
		t.Fatalf("Send with no subscriptions = %v, want nil", err)
	}
	if n := len(ps.requests()); n != 0 {
		t.Errorf("push service got %d requests, want none", n)
	}
}

// TestOutboxDelivery runs the sender under the notify worker and checks the
// outbox row each outcome leaves.
func TestOutboxDelivery(t *testing.T) {
	cases := []struct {
		name       string
		status     map[string]int // by device path; nil for no devices
		wantStatus string
		wantError  bool
	}{
		{"delivered", map[string]int{"/push/ok": http.StatusCreated}, "sent", false},
		{"server error", map[string]int{"/push/broken": http.StatusInternalServerError}, "pending", true},
		{"no subscriptions", nil, "sent", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ps := newPushService(t, tc.status)
			var devices []device
			for path := range tc.status {
				devices = append(devices, newDevice(t, path))
			}
			s, st, _ := newTestSender(t, ps, devices...)
			ctx, cancel := context.WithCancel(context.Background())
			if err := household.SeedNotificationPrefs(ctx, st); err != nil {
				t.Fatal(err)
			}
			if _, err := st.Writer.Exec(`UPDATE notification_prefs SET push = 1, chat = 0 WHERE event = 'plan_ready'`); err != nil {
				t.Fatal(err)
			}
			n, err := notify.NewService(st, slog.New(slog.NewTextHandler(io.Discard, nil)))
			if err != nil {
				t.Fatal(err)
			}
			if err := n.Register(notify.ChannelPush, s); err != nil {
				t.Fatal(err)
			}
			if err := n.Start(ctx); err != nil {
				t.Fatal(err)
			}
			defer func() { cancel(); n.Wait() }()
			if err := n.Emit(ctx, notify.Event{
				Type: notify.PlanReady, Key: "plan_ready:2026-10-12", Title: "This week's plan is ready", URL: "/plan",
			}); err != nil {
				t.Fatal(err)
			}

			var status string
			var attempts int
			var lastError sql.NullString
			deadline := time.Now().Add(10 * time.Second)
			for {
				err := st.Reader.QueryRow(`SELECT status, attempts, last_error FROM notification_outbox
					WHERE event_key = 'plan_ready:2026-10-12' AND channel = 'push'`).Scan(&status, &attempts, &lastError)
				if err != nil {
					t.Fatal(err)
				}
				if attempts > 0 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("the worker did not deliver the notification")
				}
				time.Sleep(10 * time.Millisecond)
			}
			if status != tc.wantStatus || attempts != 1 || lastError.Valid != tc.wantError {
				t.Errorf("outbox row: status %s, attempts %d, last_error %v; want %s, 1, error %v",
					status, attempts, lastError, tc.wantStatus, tc.wantError)
			}
		})
	}
}

func TestNewSenderRefusesBadConfig(t *testing.T) {
	_, st := newTestService(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	pair, err := vapid.Generate()
	if err != nil {
		t.Fatal(err)
	}
	other, err := vapid.Generate()
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]struct{ public, private, subject string }{
		"bad public key":  {"nope", pair.PrivateKey, subject},
		"bad private key": {pair.PublicKey, "nope", subject},
		"mismatched pair": {pair.PublicKey, other.PrivateKey, subject},
		"bad subject":     {pair.PublicKey, pair.PrivateKey, "kitchen@example.com"},
		"empty mailto":    {pair.PublicKey, pair.PrivateKey, "mailto:"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := NewSender(st, logger, c.public, c.private, c.subject)
			if err == nil {
				t.Fatal("NewSender succeeded")
			}
			if strings.Contains(err.Error(), pair.PrivateKey) || strings.Contains(err.Error(), other.PrivateKey) {
				t.Errorf("error %q quotes the private key", err)
			}
		})
	}
	if _, err := NewSender(st, logger, pair.PublicKey, pair.PrivateKey, "https://kitchen.example.com/contact"); err != nil {
		t.Errorf("https subject: %v", err)
	}
}
