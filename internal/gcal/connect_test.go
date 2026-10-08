package gcal_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/auth"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/gcal"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/gcal/gcaltest"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/secretbox"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/migrations"
)

const publicURL = "https://meals.example.com/app"

var (
	redirectURL = "https://meals.example.com" + gcal.CallbackPath
	session     = auth.Session{Subject: "user-1", CSRFToken: "csrf-session-one"}
	other       = auth.Session{Subject: "user-1", CSRFToken: "csrf-session-two"}
)

type harness struct {
	svc    *gcal.Service
	google *gcaltest.Google
	st     *store.Store
	box    *secretbox.Box
	clock  time.Time
}

func newHarness(t *testing.T) *harness {
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
	key := make([]byte, secretbox.KeySize)
	rand.Read(key)
	box, err := secretbox.New(base64.StdEncoding.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}
	pub, err := url.Parse(publicURL)
	if err != nil {
		t.Fatal(err)
	}
	google := gcaltest.New(t, redirectURL)
	svc, err := gcal.NewService(st, box, gcal.Config{
		ClientID:     gcaltest.ClientID,
		ClientSecret: gcaltest.ClientSecret,
		PublicURL:    pub,
		SecretKey:    []byte("server-secret-key"),
		Endpoint:     google.Endpoint(),
		RevokeURL:    google.RevokeURL(),
	})
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{svc: svc, google: google, st: st, box: box, clock: time.Date(2026, 10, 7, 18, 30, 0, 0, time.UTC)}
	gcal.SetClock(svc, func() time.Time { return h.clock })
	return h
}

// connect is a started connect: Google's consent URL and the connect
// cookie.
type connect struct {
	consent *url.URL
	cookie  *http.Cookie
}

func (h *harness) connect(t *testing.T, s auth.Session) connect {
	t.Helper()
	rec := httptest.NewRecorder()
	raw, err := h.svc.Connect(rec, s)
	if err != nil {
		t.Fatal(err)
	}
	consent, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("Connect set %d cookies, want 1", len(cookies))
	}
	return connect{consent: consent, cookie: cookies[0]}
}

// callback completes c as session s with query, returning the error and the
// recorder Complete wrote to.
func (h *harness) callback(c connect, s auth.Session, query url.Values) (*httptest.ResponseRecorder, error) {
	req := httptest.NewRequest(http.MethodGet, gcal.CallbackPath+"?"+query.Encode(), nil)
	if c.cookie != nil {
		req.AddCookie(c.cookie)
	}
	rec := httptest.NewRecorder()
	return rec, h.svc.Complete(rec, req, s)
}

// consented is the query Google sends back after consent to c.
func (h *harness) consented(c connect) url.Values {
	q := c.consent.Query()
	return url.Values{"state": {q.Get("state")}, "code": {h.google.Authorize(q.Get("code_challenge"))}}
}

func (h *harness) row(t *testing.T) (db.IntegrationsGoogle, bool) {
	t.Helper()
	row, err := db.New(h.st.Reader).GetIntegrationsGoogle(context.Background())
	if errors.Is(err, sql.ErrNoRows) {
		return db.IntegrationsGoogle{}, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return row, true
}

func TestConnectRedirectsToConsent(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.connect(t, session)

	if got, want := c.consent.Scheme+"://"+c.consent.Host+c.consent.Path, h.google.Endpoint().AuthURL; got != want {
		t.Errorf("consent URL = %s, want %s", got, want)
	}
	q := c.consent.Query()
	for name, want := range map[string]string{
		"client_id":             gcaltest.ClientID,
		"redirect_uri":          redirectURL,
		"response_type":         "code",
		"access_type":           "offline",
		"prompt":                "consent",
		"code_challenge_method": "S256",
	} {
		if got := q.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if got := strings.Fields(q.Get("scope")); !slices.Equal(got, []string{
		"https://www.googleapis.com/auth/calendar.events",
		"https://www.googleapis.com/auth/calendar.calendarlist.readonly",
	}) {
		t.Errorf("scope = %q", q.Get("scope"))
	}
	if q.Get("state") == "" || q.Get("code_challenge") == "" {
		t.Errorf("state %q, code_challenge %q: want both set", q.Get("state"), q.Get("code_challenge"))
	}
	if q.Get("client_secret") != "" || strings.Contains(c.consent.String(), gcaltest.ClientSecret) {
		t.Error("consent URL carries the client secret")
	}

	ck := c.cookie
	if ck.Name != "__Host-mealquest_google" || ck.Path != "/" || !ck.Secure || !ck.HttpOnly ||
		ck.SameSite != http.SameSiteLaxMode || ck.MaxAge != 600 || ck.Domain != "" {
		t.Errorf("connect cookie = %+v, want __Host-, Path=/, Secure, HttpOnly, Lax, 10 minutes", ck)
	}
	// Two connects never share a state.
	if again := h.connect(t, session); again.consent.Query().Get("state") == q.Get("state") {
		t.Error("two connects share a state")
	}
}

func TestCompleteStoresSealedRefreshToken(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.connect(t, session)
	rec, err := h.callback(c, session, h.consented(c))
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	row, ok := h.row(t)
	if !ok {
		t.Fatal("no integration stored")
	}
	if bytes.Contains(row.RefreshTokenCt, []byte(gcaltest.RefreshToken)) {
		t.Error("refresh token stored in plain text")
	}
	plain, err := h.box.Decrypt(row.RefreshTokenCt)
	if err != nil || string(plain) != gcaltest.RefreshToken {
		t.Errorf("stored token opens to %q, %v; want the refresh token", plain, err)
	}
	if want := "2026-10-07T18:30:00.000Z"; row.ConnectedAt != want || row.CreatedAt != want || row.UpdatedAt != want {
		t.Errorf("connected_at %q created_at %q updated_at %q, want %s", row.ConnectedAt, row.CreatedAt, row.UpdatedAt, want)
	}
	if row.CalendarID.Valid || row.CalendarName.Valid || row.LastSyncError.Valid {
		t.Errorf("new connection has calendar or sync error: %+v", row)
	}
	assertCookieCleared(t, rec)
	assertNoSecret(t, "response", rec.Body.String()+headerText(rec.Header()))
}

func TestReconnectReplacesTokenAndKeepsCalendar(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	ctx := context.Background()
	if _, err := db.New(h.st.Writer).UpsertIntegrationsGoogle(ctx, db.UpsertIntegrationsGoogleParams{
		RefreshTokenCt: []byte("old sealed token"),
		CalendarID:     sql.NullString{String: "family@group.calendar.google.com", Valid: true},
		CalendarName:   sql.NullString{String: "Family", Valid: true},
		ConnectedAt:    "2026-09-01T00:00:00.000Z",
		LastSyncError:  sql.NullString{String: "token revoked", Valid: true},
		CreatedAt:      "2026-09-01T00:00:00.000Z",
		UpdatedAt:      "2026-09-01T00:00:00.000Z",
	}); err != nil {
		t.Fatal(err)
	}

	c := h.connect(t, session)
	if _, err := h.callback(c, session, h.consented(c)); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	row, _ := h.row(t)
	if plain, err := h.box.Decrypt(row.RefreshTokenCt); err != nil || string(plain) != gcaltest.RefreshToken {
		t.Errorf("token after reconnect opens to %q, %v", plain, err)
	}
	if row.CalendarID.String != "family@group.calendar.google.com" || row.CalendarName.String != "Family" {
		t.Errorf("calendar after reconnect = %v %v, want it kept", row.CalendarID, row.CalendarName)
	}
	if row.LastSyncError.Valid {
		t.Errorf("last_sync_error = %q, want it cleared", row.LastSyncError.String)
	}
	if row.CreatedAt != "2026-09-01T00:00:00.000Z" || row.ConnectedAt != "2026-10-07T18:30:00.000Z" {
		t.Errorf("created_at %q connected_at %q", row.CreatedAt, row.ConnectedAt)
	}
}

func TestCompleteRefusesBadState(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		// edit changes the connect, the callback's session or its query.
		edit func(t *testing.T, h *harness, c *connect, s *auth.Session, q url.Values)
	}{
		{"missing state", func(_ *testing.T, _ *harness, _ *connect, _ *auth.Session, q url.Values) { q.Del("state") }},
		{"wrong state", func(_ *testing.T, _ *harness, _ *connect, _ *auth.Session, q url.Values) { q.Set("state", "forged") }},
		{"no connect cookie", func(_ *testing.T, _ *harness, c *connect, _ *auth.Session, _ url.Values) { c.cookie = nil }},
		{"tampered cookie", func(_ *testing.T, _ *harness, c *connect, _ *auth.Session, _ url.Values) {
			c.cookie = &http.Cookie{Name: c.cookie.Name, Value: "x" + c.cookie.Value}
		}},
		{"another session", func(_ *testing.T, _ *harness, _ *connect, s *auth.Session, _ url.Values) { *s = other }},
		{"expired", func(_ *testing.T, h *harness, _ *connect, _ *auth.Session, _ url.Values) {
			h.clock = h.clock.Add(10 * time.Minute)
		}},
		{"state of another connect", func(t *testing.T, h *harness, _ *connect, _ *auth.Session, q url.Values) {
			q.Set("state", h.connect(t, session).consent.Query().Get("state"))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			c := h.connect(t, session)
			s := session
			q := h.consented(c)
			tc.edit(t, h, &c, &s, q)

			rec, err := h.callback(c, s, q)
			if !errors.Is(err, gcal.ErrBadState) {
				t.Fatalf("Complete error = %v, want ErrBadState", err)
			}
			if h.google.TokenCalls() != 0 {
				t.Error("the code was exchanged despite the bad state")
			}
			if _, ok := h.row(t); ok {
				t.Error("an integration was stored")
			}
			assertCookieCleared(t, rec)
		})
	}
}

// TestCompleteRefusesReplayedState presents a connect's cookie and state a
// second time, with a second valid code from the same consent URL, as an
// attacker holding a copy of the cookie could. The server refuses it,
// whatever the browser did with the cleared cookie.
func TestCompleteRefusesReplayedState(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.connect(t, session)
	if _, err := h.callback(c, session, h.consented(c)); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	stored, _ := h.row(t)

	_, err := h.callback(c, session, h.consented(c))
	if !errors.Is(err, gcal.ErrBadState) {
		t.Fatalf("replayed Complete error = %v, want ErrBadState", err)
	}
	if got := h.google.TokenCalls(); got != 1 {
		t.Errorf("token endpoint called %d times, want once: the replayed code was exchanged", got)
	}
	if row, _ := h.row(t); !bytes.Equal(row.RefreshTokenCt, stored.RefreshTokenCt) {
		t.Error("the replay replaced the stored token")
	}
}

func TestCompleteRefusesMissingRefreshToken(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.google.OmitRefreshToken()
	c := h.connect(t, session)
	_, err := h.callback(c, session, h.consented(c))
	if !errors.Is(err, gcal.ErrNoRefreshToken) {
		t.Fatalf("Complete error = %v, want ErrNoRefreshToken", err)
	}
	if _, ok := h.row(t); ok {
		t.Error("an integration was stored without a refresh token")
	}
	assertNoSecret(t, "error", err.Error())
}

func TestCompleteRefusesMissingScope(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.google.GrantScope("https://www.googleapis.com/auth/calendar.calendarlist.readonly")
	c := h.connect(t, session)
	_, err := h.callback(c, session, h.consented(c))
	if !errors.Is(err, gcal.ErrScopes) {
		t.Fatalf("Complete error = %v, want ErrScopes", err)
	}
	if _, ok := h.row(t); ok {
		t.Error("an integration was stored without calendar.events")
	}
}

func TestCompleteDenied(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.connect(t, session)
	q := url.Values{"state": {c.consent.Query().Get("state")}, "error": {"access_denied"}}
	if _, err := h.callback(c, session, q); !errors.Is(err, gcal.ErrDenied) {
		t.Fatalf("Complete error = %v, want ErrDenied", err)
	}
	if h.google.TokenCalls() != 0 {
		t.Error("token endpoint called after a denial")
	}
}

func TestCompleteExchangeFailureIsRedacted(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.google.FailToken(http.StatusBadRequest)
	c := h.connect(t, session)
	_, err := h.callback(c, session, h.consented(c))
	if err == nil {
		t.Fatal("Complete succeeded despite the token endpoint failing")
	}
	if !strings.Contains(err.Error(), "HTTP 400") {
		t.Errorf("error = %v, want the status", err)
	}
	assertNoSecret(t, "error", err.Error())
	if _, ok := h.row(t); ok {
		t.Error("an integration was stored")
	}
}

func TestDisconnectRevokesAndClears(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.connect(t, session)
	if _, err := h.callback(c, session, h.consented(c)); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.Disconnect(context.Background()); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	if got := h.google.Revoked(); !slices.Equal(got, []string{gcaltest.RefreshToken}) {
		t.Errorf("revoked %q, want the refresh token", got)
	}
	if _, ok := h.row(t); ok {
		t.Error("integration still stored after disconnect")
	}
	if err := h.svc.Disconnect(context.Background()); !errors.Is(err, gcal.ErrNotConnected) {
		t.Errorf("second Disconnect = %v, want ErrNotConnected", err)
	}
}

// TestDisconnectRacingReconnect drives a disconnect and a reconnect into
// each other at Google, with distinct tokens. However they interleave, the
// end state is consistent: disconnected, or connected with a token Google
// has not revoked.
func TestDisconnectRacingReconnect(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		// hold stops the request the other operation must not slip past.
		hold func(*gcaltest.Google) (<-chan struct{}, func())
		// first is held at Google while second runs.
		first, second func(h *harness, c connect, q url.Values) error
	}{
		{
			// The disconnect has read the stored token and is revoking it
			// when the reconnect would store a new one.
			name:   "reconnect during revoke",
			hold:   (*gcaltest.Google).HoldRevoke,
			first:  disconnect,
			second: reconnect,
		},
		{
			// The reconnect's new token is issued but not yet stored when
			// the disconnect would revoke the old one.
			name:   "disconnect during exchange",
			hold:   (*gcaltest.Google).HoldToken,
			first:  reconnect,
			second: disconnect,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			first := h.connect(t, session)
			if _, err := h.callback(first, session, h.consented(first)); err != nil {
				t.Fatal(err)
			}
			c := h.connect(t, session)
			q := h.consented(c)

			arrived, release := tc.hold(h.google)
			defer release()
			firstDone := make(chan error, 1)
			go func() { firstDone <- tc.first(h, c, q) }()
			<-arrived
			secondDone := make(chan error, 1)
			go func() { secondDone <- tc.second(h, c, q) }()
			// Let the second operation run as far as it can while the first
			// is held at Google: to the end when nothing serializes them.
			select {
			case err := <-secondDone:
				secondDone <- err
			case <-time.After(500 * time.Millisecond):
			}
			release()
			for _, done := range []chan error{firstDone, secondDone} {
				if err := <-done; err != nil {
					t.Logf("operation: %v", err)
				}
			}

			row, ok := h.row(t)
			if !ok {
				return
			}
			plain, err := h.box.Decrypt(row.RefreshTokenCt)
			if err != nil {
				t.Fatal(err)
			}
			if !h.google.Live(string(plain)) {
				t.Errorf("stored as connected with a revoked token (Google revoked %d tokens)", len(h.google.Revoked()))
			}
		})
	}
}

func disconnect(h *harness, _ connect, _ url.Values) error {
	return h.svc.Disconnect(context.Background())
}

func reconnect(h *harness, c connect, q url.Values) error {
	_, err := h.callback(c, session, q)
	return err
}

func TestDisconnectTokenAlreadyRevokedAtGoogle(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.connect(t, session)
	if _, err := h.callback(c, session, h.consented(c)); err != nil {
		t.Fatal(err)
	}
	h.google.AnswerRevoke(http.StatusBadRequest, `{"error":"invalid_token","error_description":"Token expired or revoked"}`)
	if err := h.svc.Disconnect(context.Background()); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	if _, ok := h.row(t); ok {
		t.Error("integration still stored")
	}
}

func TestDisconnectRevokeFailureKeepsConnection(t *testing.T) {
	t.Parallel()
	for _, status := range []int{http.StatusBadRequest, http.StatusServiceUnavailable} {
		h := newHarness(t)
		c := h.connect(t, session)
		if _, err := h.callback(c, session, h.consented(c)); err != nil {
			t.Fatal(err)
		}
		h.google.AnswerRevoke(status, "")
		err := h.svc.Disconnect(context.Background())
		var revoke *gcal.RevokeError
		if !errors.As(err, &revoke) {
			t.Fatalf("HTTP %d: Disconnect error = %v, want *RevokeError", status, err)
		}
		assertNoSecret(t, "error", err.Error())
		if _, ok := h.row(t); !ok {
			t.Errorf("HTTP %d: connection cleared although Google did not revoke", status)
		}
	}
}

func TestDisconnectNotConnected(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	if err := h.svc.Disconnect(context.Background()); !errors.Is(err, gcal.ErrNotConnected) {
		t.Fatalf("Disconnect = %v, want ErrNotConnected", err)
	}
	if len(h.google.Revoked()) != 0 {
		t.Error("revoke called with nothing connected")
	}
}

func TestNewServiceRequiresConfig(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	pub, _ := url.Parse(publicURL)
	valid := gcal.Config{
		ClientID: "id", ClientSecret: "secret", PublicURL: pub, SecretKey: []byte("k"),
		Endpoint: gcal.GoogleEndpoint, RevokeURL: gcal.GoogleRevokeURL,
	}
	if _, err := gcal.NewService(h.st, h.box, valid); err != nil {
		t.Fatalf("valid config: %v", err)
	}
	for name, edit := range map[string]func(*gcal.Config){
		"client ID":     func(c *gcal.Config) { c.ClientID = "" },
		"client secret": func(c *gcal.Config) { c.ClientSecret = "" },
		"public URL":    func(c *gcal.Config) { c.PublicURL = nil },
		"secret key":    func(c *gcal.Config) { c.SecretKey = nil },
		"token URL":     func(c *gcal.Config) { c.Endpoint.TokenURL = "" },
		"revoke URL":    func(c *gcal.Config) { c.RevokeURL = "" },
	} {
		cfg := valid
		edit(&cfg)
		if _, err := gcal.NewService(h.st, h.box, cfg); err == nil {
			t.Errorf("NewService accepted a config without the %s", name)
		}
	}
}

func assertCookieCleared(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == "__Host-mealquest_google" && c.MaxAge < 0 {
			return
		}
	}
	t.Error("connect cookie not cleared")
}

// assertNoSecret fails when text holds the refresh token, the access token
// or the client secret.
func assertNoSecret(t *testing.T, what, text string) {
	t.Helper()
	for _, secret := range []string{gcaltest.RefreshToken, gcaltest.AccessToken, gcaltest.ClientSecret} {
		if strings.Contains(text, secret) {
			t.Errorf("%s contains a secret: %s", what, text)
		}
	}
}

func headerText(h http.Header) string {
	var b strings.Builder
	for k, vs := range h {
		b.WriteString(k + ": " + strings.Join(vs, ", ") + "\n")
	}
	return b.String()
}
