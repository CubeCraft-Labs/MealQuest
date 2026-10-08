package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/gcal"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/gcal/gcaltest"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/secretbox"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

const (
	googleConnectPath   = "/api/v1/integrations/google/connect"
	googleCallbackPath  = "/api/v1/integrations/google/callback"
	googlePath          = "/api/v1/integrations/google"
	googleCalendarsPath = "/api/v1/integrations/google/calendars"
	googleCalendarPath  = "/api/v1/integrations/google/calendar"
)

// googleEnv is a router whose Google Calendar service talks to a fake
// Google, with every log line it writes.
type googleEnv struct {
	h      http.Handler
	google *gcaltest.Google
	st     *store.Store
	box    *secretbox.Box
	logs   *bytes.Buffer
	// seen collects every response body and header, to check none holds a
	// secret.
	seen strings.Builder
}

func newGoogleEnv(t *testing.T) *googleEnv {
	t.Helper()
	pub, err := url.Parse("https://mealquest.example")
	if err != nil {
		t.Fatal(err)
	}
	e := &googleEnv{
		google: gcaltest.New(t, "https://mealquest.example"+gcal.CallbackPath),
		st:     newTestStore(t),
		box:    newTestBox(t),
		logs:   &bytes.Buffer{},
	}
	svc, err := gcal.NewService(e.st, e.box, gcal.Config{
		ClientID:     gcaltest.ClientID,
		ClientSecret: gcaltest.ClientSecret,
		PublicURL:    pub,
		SecretKey:    []byte("server-secret-key"),
		Endpoint:     e.google.Endpoint(),
		RevokeURL:    e.google.RevokeURL(),
		CalendarAPI:  e.google.CalendarAPI(),
	})
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewJSONHandler(e.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	e.h, err = NewRouter(logger, "dev", nil, stubAuth{}, stubSessions{}, newTestSettings(t), newTestSearch(t), newTestAI(t),
		newTestDinners(t), newTestSuggest(t), newTestPlan(t), svc, newTestRunner(t), newTestPhotos(t),
		fstest.MapFS{"index.html": {Data: []byte(fixtureIndex)}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, secret := range []string{gcaltest.RefreshToken, gcaltest.AccessToken, gcaltest.ClientSecret} {
			if strings.Contains(e.seen.String(), secret) {
				t.Errorf("a response holds a secret: %s", e.seen.String())
			}
			if strings.Contains(e.logs.String(), secret) {
				t.Errorf("the logs hold a secret: %s", e.logs.String())
			}
		}
	})
	return e
}

func (e *googleEnv) serve(req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	e.seen.WriteString(rec.Body.String())
	for k, vs := range rec.Header() {
		e.seen.WriteString(k + ": " + strings.Join(vs, ", ") + "\n")
	}
	return rec
}

// connect starts a connect as the test session and returns Google's consent
// URL and the connect cookie.
func (e *googleEnv) connect(t *testing.T) (*url.URL, *http.Cookie) {
	t.Helper()
	rec := e.serve(signedIn(http.MethodGet, googleConnectPath, nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("connect = %d %s, want 302", rec.Code, rec.Body)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("connect Cache-Control = %q, want no-store", cc)
	}
	consent, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	var cookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == "__Host-mealquest_google" {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("connect set no connect cookie")
	}
	return consent, cookie
}

// callback returns to the callback as the test session with the connect
// cookie and query.
func (e *googleEnv) callback(cookie *http.Cookie, query url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, googleCallbackPath+"?"+query.Encode(), nil)
	req.AddCookie(validSessionCookie)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	return e.serve(req)
}

func (e *googleEnv) consented(consent *url.URL) url.Values {
	q := consent.Query()
	return url.Values{"state": {q.Get("state")}, "code": {e.google.Authorize(q.Get("code_challenge"))}}
}

func (e *googleEnv) stored(t *testing.T) bool {
	t.Helper()
	_, err := db.New(e.st.Reader).GetIntegrationsGoogle(context.Background())
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	if err != nil {
		t.Fatal(err)
	}
	return true
}

func errorBodyCode(t *testing.T, rec *httptest.ResponseRecorder) (code, message string) {
	t.Helper()
	var body struct {
		Error apiError `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return body.Error.Code, body.Error.Message
}

func TestGoogleConnectAndDisconnect(t *testing.T) {
	t.Parallel()
	e := newGoogleEnv(t)

	consent, cookie := e.connect(t)
	q := consent.Query()
	if consent.Host != strings.TrimPrefix(e.google.Server.URL, "http://") || consent.Path != "/auth" {
		t.Errorf("consent URL = %s, want the fake's /auth", consent)
	}
	if q.Get("access_type") != "offline" || q.Get("prompt") != "consent" ||
		q.Get("scope") != strings.Join(gcal.Scopes, " ") ||
		q.Get("redirect_uri") != "https://mealquest.example"+gcal.CallbackPath {
		t.Errorf("consent query = %v", q)
	}

	rec := e.callback(cookie, e.consented(consent))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/settings/calendar" {
		t.Fatalf("callback = %d Location %q %s, want 303 to /settings/calendar", rec.Code, rec.Header().Get("Location"), rec.Body)
	}
	row, err := db.New(e.st.Reader).GetIntegrationsGoogle(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if plain, err := e.box.Decrypt(row.RefreshTokenCt); err != nil || string(plain) != gcaltest.RefreshToken {
		t.Errorf("stored token opens to %q, %v", plain, err)
	}
	if row.ConnectedAt == "" {
		t.Error("connected_at not set")
	}

	// DELETE needs the CSRF header like every other write.
	req := httptest.NewRequest(http.MethodDelete, googlePath, nil)
	req.AddCookie(validSessionCookie)
	if rec := e.serve(req); rec.Code != http.StatusForbidden {
		t.Errorf("DELETE without CSRF = %d, want 403", rec.Code)
	}
	if len(e.google.Revoked()) != 0 || !e.stored(t) {
		t.Fatal("a DELETE without CSRF revoked or cleared the connection")
	}

	rec = e.serve(signedIn(http.MethodDelete, googlePath, nil))
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Fatalf("DELETE = %d %q, want 204 with no body", rec.Code, rec.Body)
	}
	if got := e.google.Revoked(); !slices.Equal(got, []string{gcaltest.RefreshToken}) {
		t.Errorf("revoked %q, want the refresh token", got)
	}
	if e.stored(t) {
		t.Error("connection still stored after DELETE")
	}

	rec = e.serve(signedIn(http.MethodDelete, googlePath, nil))
	if code, _ := errorBodyCode(t, rec); rec.Code != http.StatusConflict || code != "not_connected" {
		t.Errorf("second DELETE = %d %s, want 409 not_connected", rec.Code, rec.Body)
	}
}

func TestGoogleCallbackBadState(t *testing.T) {
	t.Parallel()
	e := newGoogleEnv(t)
	consent, cookie := e.connect(t)
	good := e.consented(consent)

	for name, tc := range map[string]struct {
		cookie *http.Cookie
		query  url.Values
	}{
		"missing state":  {cookie, url.Values{"code": good["code"]}},
		"wrong state":    {cookie, url.Values{"state": {"forged"}, "code": good["code"]}},
		"missing cookie": {nil, good},
	} {
		rec := e.callback(tc.cookie, tc.query)
		if code, _ := errorBodyCode(t, rec); rec.Code != http.StatusBadRequest || code != "invalid_state" {
			t.Errorf("%s: callback = %d %s, want 400 invalid_state", name, rec.Code, rec.Body)
		}
	}
	if e.google.TokenCalls() != 0 {
		t.Error("a code was exchanged despite a bad state")
	}
	if e.stored(t) {
		t.Error("a connection was stored")
	}
}

func TestGoogleCallbackWithoutRefreshTokenAsksToReconnect(t *testing.T) {
	t.Parallel()
	e := newGoogleEnv(t)
	e.google.OmitRefreshToken()
	consent, cookie := e.connect(t)
	rec := e.callback(cookie, e.consented(consent))
	code, message := errorBodyCode(t, rec)
	if rec.Code != http.StatusBadGateway || code != "reconnect_required" || !strings.Contains(message, "connect Google Calendar again") {
		t.Errorf("callback = %d %s, want 502 reconnect_required asking to connect again", rec.Code, rec.Body)
	}
	if e.stored(t) {
		t.Error("a connection was stored without a refresh token")
	}
}

func TestGoogleCallbackFailuresAreRedacted(t *testing.T) {
	t.Parallel()
	e := newGoogleEnv(t)
	e.google.FailToken(http.StatusBadRequest)
	consent, cookie := e.connect(t)
	rec := e.callback(cookie, e.consented(consent))
	if code, _ := errorBodyCode(t, rec); rec.Code != http.StatusBadGateway || code != "google_failed" {
		t.Errorf("callback = %d %s, want 502 google_failed", rec.Code, rec.Body)
	}
	// newGoogleEnv's cleanup checks the response and logs hold no secret.
}

func TestGoogleRevokeFailureKeepsConnection(t *testing.T) {
	t.Parallel()
	e := newGoogleEnv(t)
	consent, cookie := e.connect(t)
	if rec := e.callback(cookie, e.consented(consent)); rec.Code != http.StatusSeeOther {
		t.Fatalf("callback = %d %s", rec.Code, rec.Body)
	}
	e.google.AnswerRevoke(http.StatusServiceUnavailable, "")
	rec := e.serve(signedIn(http.MethodDelete, googlePath, nil))
	if code, _ := errorBodyCode(t, rec); rec.Code != http.StatusBadGateway || code != "revoke_failed" {
		t.Errorf("DELETE = %d %s, want 502 revoke_failed", rec.Code, rec.Body)
	}
	if !e.stored(t) {
		t.Error("connection cleared although Google did not revoke")
	}
}

func TestGoogleRoutesRequireSession(t *testing.T) {
	t.Parallel()
	e := newGoogleEnv(t)
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, googleConnectPath},
		{http.MethodGet, googleCallbackPath + "?state=s&code=c"},
		{http.MethodDelete, googlePath},
		{http.MethodGet, googleCalendarsPath},
		{http.MethodPut, googleCalendarPath},
	} {
		rec := e.serve(httptest.NewRequest(c.method, c.path, nil))
		if rec.Code != http.StatusUnauthorized || rec.Body.String() != unauthenticatedJSON {
			t.Errorf("%s %s signed out = %d %s, want 401", c.method, c.path, rec.Code, rec.Body)
		}
	}
	if e.google.TokenCalls() != 0 || len(e.google.Revoked()) != 0 || e.google.CalendarCalls() != 0 {
		t.Error("a signed-out request reached Google")
	}
}

// connected connects the test session's Google account and has the fake
// list an owned primary calendar, a writable shared one and a read-only one.
func (e *googleEnv) connected(t *testing.T) {
	t.Helper()
	consent, cookie := e.connect(t)
	if rec := e.callback(cookie, e.consented(consent)); rec.Code != http.StatusSeeOther {
		t.Fatalf("callback = %d %s", rec.Code, rec.Body)
	}
	e.google.SetCalendars(0,
		gcaltest.Calendar{ID: "household@gmail.com", Summary: "household@gmail.com", Primary: true, AccessRole: "owner"},
		gcaltest.Calendar{ID: "family@group.calendar.google.com", Summary: "Family", AccessRole: "writer", Description: "private notes"},
		gcaltest.Calendar{ID: "holidays@group.v.calendar.google.com", Summary: "Holidays", AccessRole: "reader"},
	)
}

func chooseCalendar(id string) *http.Request {
	return signedIn(http.MethodPut, googleCalendarPath, strings.NewReader(`{"calendar_id":"`+id+`"}`))
}

func TestGoogleCalendarsListAndChoose(t *testing.T) {
	t.Parallel()
	e := newGoogleEnv(t)
	e.connected(t)

	rec := e.serve(signedIn(http.MethodGet, googleCalendarsPath, nil))
	want := `[{"id":"household@gmail.com","summary":"household@gmail.com","primary":true,"accessRole":"owner"},` +
		`{"id":"family@group.calendar.google.com","summary":"Family","primary":false,"accessRole":"writer"}]`
	if rec.Code != http.StatusOK || rec.Body.String() != want {
		t.Fatalf("GET calendars = %d %s, want 200 %s", rec.Code, rec.Body, want)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}

	// PUT needs the CSRF header like every other write.
	req := httptest.NewRequest(http.MethodPut, googleCalendarPath, strings.NewReader(`{"calendar_id":"family@group.calendar.google.com"}`))
	req.AddCookie(validSessionCookie)
	if rec := e.serve(req); rec.Code != http.StatusForbidden {
		t.Errorf("PUT without CSRF = %d, want 403", rec.Code)
	}
	if row, _ := db.New(e.st.Reader).GetIntegrationsGoogle(context.Background()); row.CalendarID.Valid {
		t.Fatal("a PUT without CSRF chose a calendar")
	}

	rec = e.serve(chooseCalendar("family@group.calendar.google.com"))
	if want := `{"calendar_id":"family@group.calendar.google.com","calendar_name":"Family"}`; rec.Code != http.StatusOK || rec.Body.String() != want {
		t.Fatalf("PUT calendar = %d %s, want 200 %s", rec.Code, rec.Body, want)
	}
	row, err := db.New(e.st.Reader).GetIntegrationsGoogle(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if row.CalendarID.String != "family@group.calendar.google.com" || row.CalendarName.String != "Family" {
		t.Errorf("stored calendar = %v %v", row.CalendarID, row.CalendarName)
	}

	for _, id := range []string{"holidays@group.v.calendar.google.com", "nobody@group.calendar.google.com"} {
		rec := e.serve(chooseCalendar(id))
		if code, _ := errorBodyCode(t, rec); rec.Code != http.StatusUnprocessableEntity || code != "unknown_calendar" {
			t.Errorf("PUT %s = %d %s, want 422 unknown_calendar", id, rec.Code, rec.Body)
		}
	}
	if row, _ := db.New(e.st.Reader).GetIntegrationsGoogle(context.Background()); row.CalendarID.String != "family@group.calendar.google.com" {
		t.Errorf("a refused choice replaced the stored calendar with %v", row.CalendarID)
	}
}

func TestGoogleCalendarChoiceBadRequest(t *testing.T) {
	t.Parallel()
	e := newGoogleEnv(t)
	e.connected(t)
	calls := e.google.CalendarCalls()
	for _, body := range []string{``, `{}`, `{"calendar_id":""}`, `{"calendar_id":null}`, `{"calendar_id":7}`,
		`{"calendar_id":"family@group.calendar.google.com","calendar_name":"Mine"}`} {
		rec := e.serve(signedIn(http.MethodPut, googleCalendarPath, strings.NewReader(body)))
		if code, _ := errorBodyCode(t, rec); rec.Code != http.StatusBadRequest || code != "invalid_request" {
			t.Errorf("PUT %q = %d %s, want 400 invalid_request", body, rec.Code, rec.Body)
		}
	}
	if e.google.CalendarCalls() != calls {
		t.Error("a bad request reached Google")
	}
}

func TestGoogleCalendarsNotConnected(t *testing.T) {
	t.Parallel()
	e := newGoogleEnv(t)
	for _, req := range []*http.Request{
		signedIn(http.MethodGet, googleCalendarsPath, nil),
		chooseCalendar("family@group.calendar.google.com"),
	} {
		rec := e.serve(req)
		code, message := errorBodyCode(t, rec)
		if rec.Code != http.StatusConflict || code != "not_connected" || message != "Google Calendar is not connected" {
			t.Errorf("%s %s = %d %s, want 409 not_connected", req.Method, req.URL.Path, rec.Code, rec.Body)
		}
	}
	if e.google.TokenCalls() != 0 || e.google.CalendarCalls() != 0 {
		t.Error("Google was called with nothing connected")
	}
}

// TestGoogleCalendarsGrantRevoked lists and chooses after the household
// removed MealQuest's access in their Google account: 502
// reconnect_required, and the connection is kept for the household to
// reconnect or disconnect.
func TestGoogleCalendarsGrantRevoked(t *testing.T) {
	t.Parallel()
	e := newGoogleEnv(t)
	e.connected(t)
	e.google.RevokeGrant()
	for _, req := range []*http.Request{
		signedIn(http.MethodGet, googleCalendarsPath, nil),
		chooseCalendar("family@group.calendar.google.com"),
	} {
		rec := e.serve(req)
		code, message := errorBodyCode(t, rec)
		if rec.Code != http.StatusBadGateway || code != "reconnect_required" || !strings.Contains(message, "connect Google Calendar again") {
			t.Errorf("%s %s = %d %s, want 502 reconnect_required", req.Method, req.URL.Path, rec.Code, rec.Body)
		}
	}
	if !e.stored(t) {
		t.Error("the connection was cleared")
	}
	// newGoogleEnv's cleanup checks the responses and logs hold no secret.
}

func TestGoogleCalendarsFailureIsRedacted(t *testing.T) {
	t.Parallel()
	e := newGoogleEnv(t)
	e.connected(t)
	e.google.FailCalendarList(http.StatusInternalServerError)
	rec := e.serve(signedIn(http.MethodGet, googleCalendarsPath, nil))
	if code, _ := errorBodyCode(t, rec); rec.Code != http.StatusBadGateway || code != "google_failed" {
		t.Errorf("GET calendars = %d %s, want 502 google_failed", rec.Code, rec.Body)
	}
	if !strings.Contains(e.logs.String(), "HTTP 500") {
		t.Errorf("logs = %s, want the failure's status", e.logs)
	}
	// newGoogleEnv's cleanup checks the response and logs hold no secret.
}
