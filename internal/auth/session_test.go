package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/migrations"
)

const (
	testIdle = 30 * 24 * time.Hour
	testMax  = 180 * 24 * time.Hour
)

// testEpoch is the fake clock's starting instant.
var testEpoch = time.Date(2026, 10, 3, 18, 30, 0, 0, time.UTC)

// sessionHarness runs the real login flow against a fake provider with a
// SessionManager over a migrated database and a fake clock.
type sessionHarness struct {
	*harness
	db       *store.Store
	sessions *SessionManager
	auth     *Auth
	clock    time.Time
}

func newSessionHarness(t *testing.T) *sessionHarness {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := store.Migrate(ctx, db.Writer, migrations.FS); err != nil {
		t.Fatal(err)
	}
	sessions, err := NewSessionManager(db, testIdle, testMax)
	if err != nil {
		t.Fatal(err)
	}
	h := &sessionHarness{db: db, sessions: sessions, clock: testEpoch}
	sessions.now = func() time.Time { return h.clock }

	p := newFakeProvider(t)
	logs := &syncBuffer{}
	a, err := New(ctx, Config{
		Issuer:        p.issuer(),
		ClientID:      testClientID,
		ClientSecret:  testClientSecret,
		PublicURL:     testPublicURL,
		SecretKey:     []byte("server-secret-key"),
		AllowedGroups: testAllowedGroups,
	}, sessions, slog.New(slog.NewTextHandler(logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	r := chi.NewRouter()
	a.Mount(r)
	h.harness = &harness{provider: p, router: r, logs: logs}
	h.auth = a
	return h
}

func (h *sessionHarness) advance(d time.Duration) { h.clock = h.clock.Add(d) }

// signIn completes a login and returns the callback response and the
// session cookie it set.
func (h *sessionHarness) signIn(t *testing.T) (*http.Response, *http.Cookie) {
	t.Helper()
	l := h.login(t, LoginPath)
	resp := h.callback(l, l.query.Get("state"), h.provider.signRS256(h.provider.claims(l.query.Get("nonce"))))
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("callback status = %d, want 303", resp.StatusCode)
	}
	for _, c := range resp.Cookies() {
		if c.Name == SessionCookieName {
			return resp, c
		}
	}
	t.Fatalf("callback set no %s cookie", SessionCookieName)
	return nil, nil
}

// use looks up the session cookie c names.
func (h *sessionHarness) use(c *http.Cookie) (Session, error) {
	req := httptest.NewRequest(http.MethodGet, "/api/anything", nil)
	if c != nil {
		req.AddCookie(c)
	}
	return h.sessions.SessionFor(req)
}

type sessionRow struct {
	idHash, subject, email, name, groups, csrf string
	created, lastSeen, expires                 store.Timestamp
}

func (h *sessionHarness) rows(t *testing.T) []sessionRow {
	t.Helper()
	rs, err := h.db.Reader.Query(`SELECT id_hash, subject, email, name, groups, csrf_token,
		created_at, last_seen_at, expires_at FROM sessions`)
	if err != nil {
		t.Fatal(err)
	}
	defer rs.Close()
	var out []sessionRow
	for rs.Next() {
		var r sessionRow
		if err := rs.Scan(&r.idHash, &r.subject, &r.email, &r.name, &r.groups, &r.csrf,
			&r.created, &r.lastSeen, &r.expires); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	if err := rs.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// sessionSetCookie returns the raw Set-Cookie header for the session cookie.
func sessionSetCookie(t *testing.T, resp *http.Response) string {
	t.Helper()
	for _, v := range resp.Header.Values("Set-Cookie") {
		if strings.HasPrefix(v, SessionCookieName+"=") {
			return v
		}
	}
	t.Fatalf("no Set-Cookie for %s in %q", SessionCookieName, resp.Header.Values("Set-Cookie"))
	return ""
}

// assertSessionCookieAttributes checks the attributes the __Host- prefix
// and D-004 require, on the raw header so nothing is lost in parsing.
func assertSessionCookieAttributes(t *testing.T, raw string) {
	t.Helper()
	attrs := map[string]bool{}
	for _, a := range strings.Split(raw, ";")[1:] {
		name, _, _ := strings.Cut(strings.TrimSpace(a), "=")
		attrs[strings.ToLower(name)] = true
	}
	for _, want := range []string{"Path=/", "HttpOnly", "Secure", "SameSite=Lax"} {
		if !slices.Contains(strings.Split(raw, "; "), want) {
			t.Errorf("session Set-Cookie %q lacks %s", raw, want)
		}
	}
	if attrs["domain"] {
		t.Errorf("session Set-Cookie %q has a Domain", raw)
	}
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestCallbackStartsSession(t *testing.T) {
	t.Parallel()
	h := newSessionHarness(t)
	resp, cookie := h.signIn(t)

	assertSessionCookieAttributes(t, sessionSetCookie(t, resp))
	if cookie.MaxAge != int(testMax/time.Second) {
		t.Errorf("session cookie MaxAge = %d, want %d", cookie.MaxAge, int(testMax/time.Second))
	}

	rows := h.rows(t)
	if len(rows) != 1 {
		t.Fatalf("sessions has %d rows, want 1", len(rows))
	}
	r := rows[0]
	if len(cookie.Value) != 43 {
		t.Errorf("session ID %q is %d chars, want 43 (256 bits, base64url)", cookie.Value, len(cookie.Value))
	}
	if r.idHash != sha256Hex(cookie.Value) {
		t.Errorf("id_hash = %q, want the SHA-256 of the cookie value", r.idHash)
	}
	if r.subject != "user-123" || r.email != "cook@example.com" || r.name != "Home Cook" ||
		r.groups != `["mealquest-admin"]` {
		t.Errorf("session row = %+v, want the ID token claims", r)
	}
	if len(r.csrf) != 43 || r.csrf == cookie.Value {
		t.Errorf("csrf_token = %q, want a separate 256-bit token", r.csrf)
	}
	if !r.created.Equal(testEpoch) || !r.lastSeen.Equal(testEpoch) || !r.expires.Equal(testEpoch.Add(testIdle)) {
		t.Errorf("created, last_seen, expires = %v, %v, %v, want %v, %v, %v",
			r.created, r.lastSeen, r.expires, testEpoch, testEpoch, testEpoch.Add(testIdle))
	}

	s, err := h.use(cookie)
	if err != nil {
		t.Fatalf("SessionFor = %v, want the new session", err)
	}
	if s.Subject != "user-123" || s.CSRFToken != r.csrf || !slices.Equal(s.Groups, []string{"mealquest-admin"}) {
		t.Errorf("SessionFor = %+v", s)
	}
}

func TestStartSessionStoresOnlyTheHash(t *testing.T) {
	t.Parallel()
	h := newSessionHarness(t)
	_, cookie := h.signIn(t)
	var n int
	err := h.db.Reader.QueryRow(`SELECT count(*) FROM sessions WHERE
		id_hash = ?1 OR subject = ?1 OR email = ?1 OR name = ?1 OR groups = ?1 OR csrf_token = ?1`,
		cookie.Value).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Error("the raw session ID is stored in the sessions table")
	}
}

func TestSessionLastSeenSlides(t *testing.T) {
	t.Parallel()
	h := newSessionHarness(t)
	_, cookie := h.signIn(t)

	// Used every 20 days, the session outlives several idle periods.
	for range 4 {
		h.advance(20 * 24 * time.Hour)
		s, err := h.use(cookie)
		if err != nil {
			t.Fatalf("at %v SessionFor = %v, want the session", h.clock, err)
		}
		if !s.LastSeenAt.Equal(h.clock) || !s.ExpiresAt.Equal(h.clock.Add(testIdle)) {
			t.Errorf("at %v session last seen %v, expires %v, want %v, %v",
				h.clock, s.LastSeenAt, s.ExpiresAt, h.clock, h.clock.Add(testIdle))
		}
		r := h.rows(t)[0]
		if !r.lastSeen.Equal(h.clock) || !r.expires.Equal(h.clock.Add(testIdle)) || !r.created.Equal(testEpoch) {
			t.Errorf("at %v row created %v, last seen %v, expires %v", h.clock, r.created, r.lastSeen, r.expires)
		}
	}
}

func TestSessionIdleExpiry(t *testing.T) {
	t.Parallel()
	h := newSessionHarness(t)
	_, cookie := h.signIn(t)

	h.advance(testIdle - time.Millisecond)
	if _, err := h.use(cookie); err != nil {
		t.Fatalf("just inside the idle limit SessionFor = %v, want the session", err)
	}
	h.advance(testIdle)
	if _, err := h.use(cookie); !errors.Is(err, ErrNoSession) {
		t.Fatalf("after idle SessionFor = %v, want ErrNoSession", err)
	}
	if n := len(h.rows(t)); n != 0 {
		t.Errorf("expired session left %d rows, want 0", n)
	}
	// Turning the clock back cannot revive it.
	h.clock = testEpoch
	if _, err := h.use(cookie); !errors.Is(err, ErrNoSession) {
		t.Errorf("after deletion SessionFor = %v, want ErrNoSession", err)
	}
}

func TestSessionAbsoluteExpiry(t *testing.T) {
	t.Parallel()
	h := newSessionHarness(t)
	_, cookie := h.signIn(t)

	for h.clock.Before(testEpoch.Add(testMax - 20*24*time.Hour)) {
		h.advance(20 * 24 * time.Hour)
		if _, err := h.use(cookie); err != nil {
			t.Fatalf("at %v SessionFor = %v, want the session", h.clock, err)
		}
	}
	// Last used 20 days before the absolute limit: the idle window would run
	// past it, so expiry is capped there.
	if r := h.rows(t)[0]; !r.expires.Equal(testEpoch.Add(testMax)) {
		t.Errorf("expires_at = %v, want the absolute limit %v", r.expires, testEpoch.Add(testMax))
	}

	h.clock = testEpoch.Add(testMax - time.Millisecond)
	if _, err := h.use(cookie); err != nil {
		t.Fatalf("just inside the absolute limit SessionFor = %v, want the session", err)
	}
	h.clock = testEpoch.Add(testMax)
	if _, err := h.use(cookie); !errors.Is(err, ErrNoSession) {
		t.Fatalf("at the absolute limit, used 1ms ago, SessionFor = %v, want ErrNoSession", err)
	}
	if n := len(h.rows(t)); n != 0 {
		t.Errorf("expired session left %d rows, want 0", n)
	}
}

// A max with fractional seconds rounds the cookie Max-Age up, so the browser
// keeps the cookie until the server-side absolute limit.
func TestSessionCookieOutlivesFractionalMax(t *testing.T) {
	t.Parallel()
	h := newSessionHarness(t)
	h.sessions.idle, h.sessions.max = 1500*time.Millisecond, 2500*time.Millisecond
	_, cookie := h.signIn(t)
	if cookie.MaxAge != 3 {
		t.Errorf("session cookie MaxAge = %d, want 3", cookie.MaxAge)
	}
	if r := h.rows(t)[0]; !r.expires.Equal(testEpoch.Add(1500 * time.Millisecond)) {
		t.Errorf("expires_at = %v, want %v", r.expires, testEpoch.Add(1500*time.Millisecond))
	}

	h.advance(time.Second)
	if _, err := h.use(cookie); err != nil {
		t.Fatalf("at 1s SessionFor = %v, want the session", err)
	}
	h.clock = testEpoch.Add(2500*time.Millisecond - time.Millisecond)
	if _, err := h.use(cookie); err != nil {
		t.Fatalf("just inside the absolute limit SessionFor = %v, want the session", err)
	}
	h.clock = testEpoch.Add(2500 * time.Millisecond)
	if _, err := h.use(cookie); !errors.Is(err, ErrNoSession) {
		t.Errorf("at the absolute limit SessionFor = %v, want ErrNoSession", err)
	}
}

// A lifetime near the largest accepted duration still yields a positive,
// rounded-up cookie Max-Age rather than overflowing.
func TestSessionCookieMaxAgeNearLargestMax(t *testing.T) {
	t.Parallel()
	largest, err := time.ParseDuration("2562047h47m16.854s")
	if err != nil {
		t.Fatal(err)
	}
	h := newSessionHarness(t)
	h.sessions.max = largest
	_, cookie := h.signIn(t)
	if want := 9223372037; cookie.MaxAge != want {
		t.Errorf("session cookie MaxAge = %d, want %d", cookie.MaxAge, want)
	}
}

// A lifetime shortened by configuration applies to existing sessions even
// though their stored expires_at is later.
func TestSessionLifetimeChecksUseCurrentConfig(t *testing.T) {
	t.Parallel()
	h := newSessionHarness(t)
	_, cookie := h.signIn(t)
	h.sessions.idle = 24 * time.Hour
	h.advance(24 * time.Hour)
	if _, err := h.use(cookie); !errors.Is(err, ErrNoSession) {
		t.Errorf("past the shortened idle SessionFor = %v, want ErrNoSession", err)
	}

	_, cookie = h.signIn(t)
	h.sessions.idle, h.sessions.max = 24*time.Hour, 48*time.Hour
	for range 3 {
		h.advance(12 * time.Hour)
		if _, err := h.use(cookie); err != nil {
			t.Fatalf("SessionFor = %v, want the session", err)
		}
	}
	h.advance(12 * time.Hour)
	if _, err := h.use(cookie); !errors.Is(err, ErrNoSession) {
		t.Errorf("past the shortened max SessionFor = %v, want ErrNoSession", err)
	}

	// Used recently and with expires_at still ahead, but older than a max
	// shortened after that use: only the absolute check ends it.
	h.sessions.idle, h.sessions.max = testIdle, testMax
	_, cookie = h.signIn(t)
	h.advance(10 * 24 * time.Hour)
	if _, err := h.use(cookie); err != nil {
		t.Fatalf("SessionFor = %v, want the session", err)
	}
	h.sessions.max = 10 * 24 * time.Hour
	h.advance(time.Hour)
	if _, err := h.use(cookie); !errors.Is(err, ErrNoSession) {
		t.Errorf("past a max shortened after the last use SessionFor = %v, want ErrNoSession", err)
	}
}

func TestSessionForRejectsBadCookies(t *testing.T) {
	t.Parallel()
	h := newSessionHarness(t)
	h.signIn(t)
	for name, c := range map[string]*http.Cookie{
		"missing":      nil,
		"empty":        {Name: SessionCookieName, Value: ""},
		"not base64":   {Name: SessionCookieName, Value: strings.Repeat("!", 43)},
		"short":        {Name: SessionCookieName, Value: "AAAA"},
		"unknown ID":   {Name: SessionCookieName, Value: randomToken()},
		"other cookie": {Name: "mealquest_session", Value: randomToken()},
	} {
		if _, err := h.use(c); !errors.Is(err, ErrNoSession) {
			t.Errorf("%s: SessionFor = %v, want ErrNoSession", name, err)
		}
	}
	if n := len(h.rows(t)); n != 1 {
		t.Errorf("sessions has %d rows, want the signed-in session only", n)
	}
}

func (h *sessionHarness) logout(cookies ...*http.Cookie) *http.Response {
	req := httptest.NewRequest(http.MethodPost, LogoutPath, nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)
	return rec.Result()
}

func TestLogout(t *testing.T) {
	t.Parallel()
	h := newSessionHarness(t)
	_, cookie := h.signIn(t)
	_, other := h.signIn(t)

	resp := h.logout(cookie)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("logout status = %d, want 303", resp.StatusCode)
	}
	want := h.provider.issuer() + "/end-session/"
	if got := resp.Header.Get("Location"); got != want || h.auth.EndSessionURL() != want {
		t.Errorf("logout redirect = %q, EndSessionURL = %q, want %q", got, h.auth.EndSessionURL(), want)
	}
	raw := sessionSetCookie(t, resp)
	assertSessionCookieAttributes(t, raw)
	if !strings.HasPrefix(raw, SessionCookieName+"=;") || !strings.Contains(raw, "Max-Age=0") {
		t.Errorf("logout Set-Cookie = %q, want the cookie cleared", raw)
	}

	rows := h.rows(t)
	if len(rows) != 1 || rows[0].idHash != sha256Hex(other.Value) {
		t.Errorf("after logout sessions = %+v, want only the other session", rows)
	}
	if _, err := h.use(cookie); !errors.Is(err, ErrNoSession) {
		t.Errorf("logged-out cookie: SessionFor = %v, want ErrNoSession", err)
	}
	if _, err := h.use(other); err != nil {
		t.Errorf("other session: SessionFor = %v, want it untouched", err)
	}
}

func TestLogoutWithoutSession(t *testing.T) {
	t.Parallel()
	h := newSessionHarness(t)
	resp := h.logout()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != h.provider.issuer()+"/end-session/" {
		t.Errorf("logout = %d to %q, want 303 to the end session endpoint", resp.StatusCode, resp.Header.Get("Location"))
	}
	assertSessionCookieAttributes(t, sessionSetCookie(t, resp))
}

func TestLogoutIsPostOnly(t *testing.T) {
	t.Parallel()
	h := newSessionHarness(t)
	_, cookie := h.signIn(t)
	if resp := h.get(LogoutPath, cookie); resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET %s status = %d, want 405", LogoutPath, resp.StatusCode)
	}
	if _, err := h.use(cookie); err != nil {
		t.Errorf("after GET %s SessionFor = %v, want the session", LogoutPath, err)
	}
}

func TestLogoutSessionError(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.sessions.err = errors.New("database down")
	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, LogoutPath, nil))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("logout status = %d, want 500", rec.Code)
	}
}

func TestNewRequiresEndSessionEndpoint(t *testing.T) {
	t.Parallel()
	p := newFakeProvider(t)
	p.discoveryOverrides = map[string]any{"end_session_endpoint": nil}
	_, err := New(context.Background(), Config{
		Issuer:        p.issuer(),
		ClientID:      testClientID,
		ClientSecret:  testClientSecret,
		PublicURL:     testPublicURL,
		SecretKey:     []byte("k"),
		AllowedGroups: testAllowedGroups,
	}, &memorySessions{}, slog.New(slog.DiscardHandler))
	if err == nil || !strings.Contains(err.Error(), "end session endpoint") {
		t.Errorf("New error = %v, want the missing end session endpoint named", err)
	}
}

func TestNewSessionManagerValidates(t *testing.T) {
	t.Parallel()
	db := &store.Store{}
	for _, tc := range []struct {
		name      string
		db        *store.Store
		idle, max time.Duration
	}{
		{"nil store", nil, time.Hour, time.Hour},
		{"zero idle", db, 0, time.Hour},
		{"negative max", db, time.Hour, -time.Hour},
		{"max below idle", db, 2 * time.Hour, time.Hour},
	} {
		if _, err := NewSessionManager(tc.db, tc.idle, tc.max); err == nil {
			t.Errorf("%s: NewSessionManager succeeded", tc.name)
		}
	}
}
