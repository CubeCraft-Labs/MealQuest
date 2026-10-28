// Package gcaltest provides a fake Google OAuth server and Calendar API for
// tests of the Google Calendar connection: a token endpoint that checks the
// client, the redirect URI and PKCE, issues a distinct refresh token per
// exchange and trades a live refresh token for an access token; a revoke
// endpoint that, like Google's, revokes the whole grant: every token issued
// so far; a calendarList endpoint that requires a live grant's access
// token; and events insert and update endpoints that, like Google's, keep
// each calendar's events by id, answer an insert of an id the calendar
// already has (even a cancelled event's) with HTTP 409, and refuse a
// calendar not listed (404) or not writable (403 requiredAccessLevel). It
// never touches the network beyond its own httptest server.
package gcaltest

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"golang.org/x/oauth2"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/auth"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/gcal"
)

// Fixed credentials and tokens the fake issues and expects. The first
// exchange issues RefreshToken; exchange n after it issues RefreshToken
// followed by "-n", so every issued refresh token contains RefreshToken. Every
// exchange and refresh issues AccessToken.
const (
	ClientID     = "mealquest-test.apps.googleusercontent.com"
	ClientSecret = "google-client-s3cret-4f1c"
	RefreshToken = "1//google-refresh-t0ken-9a8b7c"
	AccessToken  = "ya29.google-access-t0ken-5d6e"
)

// Google is the fake. Its exported fields script the next responses; set
// them before the request they affect.
type Google struct {
	Server *httptest.Server
	// RedirectURL is the redirect URI the token endpoint requires.
	RedirectURL string

	mu sync.Mutex
	// omitRefresh leaves refresh_token out of the token response.
	omitRefresh bool
	// scope is the token response's granted scopes.
	scope string
	// tokenStatus, when not 200, fails the token endpoint with a body that
	// quotes the refresh token and client secret.
	tokenStatus int
	// revokeStatus and revokeBody answer the revoke endpoint; revokeBody
	// quotes the token when revokeStatus is not 200 or 400.
	revokeStatus int
	revokeBody   string
	codes        map[string]string // code -> PKCE challenge
	tokenCalls   int
	revoked      []string
	// issued is every refresh token issued, in order; dead is those a
	// revoke has invalidated.
	issued []string
	dead   map[string]bool
	// tokenHold, revokeHold and calendarHold, when set, stop the next
	// request to that endpoint.
	tokenHold, revokeHold, calendarHold *hold
	// calendars is what the calendarList endpoint lists, pageSize entries
	// to a page (all of them when 0).
	calendars []Calendar
	pageSize  int
	// calendarStatus, when not 200, fails the calendarList endpoint with a
	// body that quotes the access token: every request when calendarFails
	// is 0, else the next calendarFails requests.
	calendarStatus int
	calendarFails  int
	calendarCalls  int
	// rejectAccess makes the Calendar API answer HTTP 401 to every access
	// token, while the grant stays live at the token endpoint.
	rejectAccess bool
	// events holds each calendar's events by id.
	events map[string]map[string]Event
	// inserts and updates count the events created and updated.
	inserts, updates int
	// eventWrites counts the requests that reached the events endpoints;
	// failWrite, when not 0, fails the requests that make it that count up
	// to failLast with failStatus and a body that quotes the access token.
	eventWrites, failWrite, failLast, failStatus int
	// eventHold, when set, stops the next events request.
	eventHold *hold
}

// Event is an event as the fake keeps it: what MealQuest sent.
type Event struct {
	ID          string    `json:"id"`
	Status      string    `json:"status"`
	Summary     string    `json:"summary"`
	Description string    `json:"description"`
	Start       EventTime `json:"start"`
	End         EventTime `json:"end"`
}

// EventTime is an event's start or end.
type EventTime struct {
	DateTime string `json:"dateTime"`
	TimeZone string `json:"timeZone"`
}

// Calendar is one calendarList entry. The fake answers each with Google's
// other fields too, so tests can check that nothing beyond id, summary,
// primary and accessRole reaches the household.
type Calendar struct {
	ID              string
	Summary         string
	Primary         bool
	AccessRole      string
	Description     string
	SummaryOverride string
}

// hold stops one request: arrived is closed when it arrives, and it goes on
// once release is closed.
type hold struct {
	arrived, release chan struct{}
}

// New starts a fake Google that requires redirectURL at its token endpoint.
// It grants every scope in gcal.Scopes with a refresh token by default.
func New(t *testing.T, redirectURL string) *Google {
	t.Helper()
	g := &Google{
		RedirectURL:    redirectURL,
		scope:          strings.Join(gcal.Scopes, " "),
		tokenStatus:    http.StatusOK,
		revokeStatus:   http.StatusOK,
		calendarStatus: http.StatusOK,
		codes:          map[string]string{},
		dead:           map[string]bool{},
		events:         map[string]map[string]Event{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /token", g.token)
	mux.HandleFunc("POST /revoke", g.revoke)
	mux.HandleFunc("GET /calendar/v3/users/me/calendarList", g.calendarList)
	mux.HandleFunc("POST /calendar/v3/calendars/{calendar}/events", g.insertEvent)
	mux.HandleFunc("PUT /calendar/v3/calendars/{calendar}/events/{event}", g.updateEvent)
	mux.HandleFunc("DELETE /calendar/v3/calendars/{calendar}/events/{event}", g.deleteEvent)
	g.Server = httptest.NewServer(mux)
	t.Cleanup(g.Server.Close)
	return g
}

// Endpoint is the fake's authorization and token endpoint, with Google's
// client authentication style.
func (g *Google) Endpoint() oauth2.Endpoint {
	return oauth2.Endpoint{
		AuthURL:   g.Server.URL + "/auth",
		TokenURL:  g.Server.URL + "/token",
		AuthStyle: gcal.GoogleEndpoint.AuthStyle,
	}
}

// RevokeURL is the fake's revocation endpoint.
func (g *Google) RevokeURL() string { return g.Server.URL + "/revoke" }

// CalendarAPI is the fake's Calendar API base URL.
func (g *Google) CalendarAPI() string { return g.Server.URL + "/calendar/v3" }

// Authorize plays the consent screen for an authorization request with PKCE
// challenge and returns the code Google would send back.
func (g *Google) Authorize(challenge string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	code := fmt.Sprintf("code-%d", len(g.codes)+1)
	g.codes[code] = challenge
	return code
}

// Connect connects svc to the fake as a household completing the consent
// screen does. svc must use the fake's endpoints and redirect URL.
func (g *Google) Connect(t *testing.T, svc *gcal.Service) {
	t.Helper()
	session := auth.Session{Subject: "gcaltest", CSRFToken: "gcaltest-session"}
	started := httptest.NewRecorder()
	raw, err := svc.Connect(started, session)
	if err != nil {
		t.Fatal(err)
	}
	consent, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	q := url.Values{
		"state": {consent.Query().Get("state")},
		"code":  {g.Authorize(consent.Query().Get("code_challenge"))},
	}
	req := httptest.NewRequest(http.MethodGet, gcal.CallbackPath+"?"+q.Encode(), nil)
	for _, c := range started.Result().Cookies() {
		req.AddCookie(c)
	}
	if err := svc.Complete(httptest.NewRecorder(), req, session); err != nil {
		t.Fatalf("connect: %v", err)
	}
}

// OmitRefreshToken makes the token endpoint answer without a refresh token,
// as Google does when it reuses an earlier grant.
func (g *Google) OmitRefreshToken() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.omitRefresh = true
}

// GrantScope sets the scopes the token endpoint reports as granted.
func (g *Google) GrantScope(scope string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.scope = scope
}

// FailToken makes the token endpoint answer status with a body that quotes
// the refresh token and client secret.
func (g *Google) FailToken(status int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.tokenStatus = status
}

// AnswerRevoke makes the revoke endpoint answer status with body. A body of
// "" with a status other than 200 quotes the token it was sent.
func (g *Google) AnswerRevoke(status int, body string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.revokeStatus, g.revokeBody = status, body
}

// RevokeGrant revokes every token issued so far, as the household removing
// MealQuest's access in their Google account does.
func (g *Google) RevokeGrant() {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, t := range g.issued {
		g.dead[t] = true
	}
}

// SetCalendars makes the calendarList endpoint list cals, pageSize to a
// page (all on one page when 0).
func (g *Google) SetCalendars(pageSize int, cals ...Calendar) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calendars, g.pageSize = slices.Clone(cals), pageSize
}

// FailCalendarList makes the calendarList endpoint answer status with a
// body that quotes the access token.
func (g *Google) FailCalendarList(status int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calendarStatus, g.calendarFails = status, 0
}

// FailCalendarListNext makes the next n calendarList requests answer status
// as FailCalendarList does, and later ones answer normally.
func (g *Google) FailCalendarListNext(n, status int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calendarStatus, g.calendarFails = status, n
}

// RejectAccessToken makes the Calendar API answer HTTP 401 to every access
// token while refreshing the grant still succeeds, as Google does when it
// refuses the access token it just issued.
func (g *Google) RejectAccessToken() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.rejectAccess = true
}

// AcceptAccessToken undoes RejectAccessToken, as Google does for the grant
// of a later connection.
func (g *Google) AcceptAccessToken() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.rejectAccess = false
}

// CalendarCalls is how many requests reached the calendarList endpoint.
func (g *Google) CalendarCalls() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.calendarCalls
}

// HoldCalendarList stops the next calendarList request before it is
// answered. It returns a channel closed when the request arrives and the
// function that lets it go on.
func (g *Google) HoldCalendarList() (arrived <-chan struct{}, release func()) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calendarHold = newHold()
	return g.calendarHold.arrived, g.calendarHold.releaseFunc()
}

// Events returns the events of calendar, by id.
func (g *Google) Events(calendar string) map[string]Event {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := map[string]Event{}
	for id, e := range g.events[calendar] {
		out[id] = e
	}
	return out
}

// EventWrites returns how many events were inserted and how many updated.
func (g *Google) EventWrites() (inserts, updates int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.inserts, g.updates
}

// ResetEventWrites zeroes the insert and update counts.
func (g *Google) ResetEventWrites() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.inserts, g.updates = 0, 0
}

// CancelEvent marks event id of calendar cancelled, as deleting it in
// Google Calendar does: Google keeps the id.
func (g *Google) CancelEvent(calendar, id string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	e := g.events[calendar][id]
	e.Status = "cancelled"
	g.events[calendar][id] = e
}

// FailEventWrite makes the nth request to the events endpoints from now
// (1 for the next) answer status with a body that quotes the access token.
func (g *Google) FailEventWrite(n, status int) {
	g.FailEventWrites(n, 1, status)
}

// FailEventWrites makes count requests to the events endpoints in a row,
// from the nth from now (1 for the next), answer status with a body that
// quotes the access token.
func (g *Google) FailEventWrites(n, count, status int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.failWrite, g.failLast, g.failStatus = g.eventWrites+n, g.eventWrites+n+count-1, status
}

// EventRequests is how many requests reached the events endpoints.
func (g *Google) EventRequests() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.eventWrites
}

// HoldEvents stops the next request to the events endpoints before it is
// answered. It returns a channel closed when the request arrives and the
// function that lets it go on.
func (g *Google) HoldEvents() (arrived <-chan struct{}, release func()) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.eventHold = newHold()
	return g.eventHold.arrived, g.eventHold.releaseFunc()
}

// TokenCalls is how many requests, exchanges and refreshes, reached the
// token endpoint.
func (g *Google) TokenCalls() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.tokenCalls
}

// Revoked lists the tokens the revoke endpoint received, in order.
func (g *Google) Revoked() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.revoked...)
}

// Live reports whether token was issued by the fake and no revoke has
// invalidated it since.
func (g *Google) Live(token string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Contains(g.issued, token) && !g.dead[token]
}

// HoldToken stops the next token request after its refresh token is issued
// and before it is answered, so a revoke can land in between. It returns a
// channel closed when the request arrives and the function that lets it go
// on.
func (g *Google) HoldToken() (arrived <-chan struct{}, release func()) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.tokenHold = newHold()
	return g.tokenHold.arrived, g.tokenHold.releaseFunc()
}

// HoldRevoke stops the next revoke request before Google acts on it. It
// returns a channel closed when the request arrives and the function that
// lets it go on.
func (g *Google) HoldRevoke() (arrived <-chan struct{}, release func()) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.revokeHold = newHold()
	return g.revokeHold.arrived, g.revokeHold.releaseFunc()
}

func newHold() *hold {
	return &hold{arrived: make(chan struct{}), release: make(chan struct{})}
}

func (h *hold) releaseFunc() func() {
	var once sync.Once
	return func() { once.Do(func() { close(h.release) }) }
}

// wait stops the request at h, if any, without holding g.mu.
func (g *Google) wait(h *hold) {
	if h == nil {
		return
	}
	g.mu.Unlock()
	close(h.arrived)
	<-h.release
	g.mu.Lock()
}

func (g *Google) token(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.tokenCalls++
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	f := r.PostForm
	if f.Get("grant_type") == "refresh_token" {
		g.refresh(w, f)
		return
	}
	challenge, known := g.codes[f.Get("code")]
	delete(g.codes, f.Get("code"))
	switch {
	case f.Get("grant_type") != "authorization_code":
		oauthError(w, http.StatusBadRequest, "unsupported_grant_type")
		return
	case f.Get("client_id") != ClientID || f.Get("client_secret") != ClientSecret:
		oauthError(w, http.StatusUnauthorized, "invalid_client")
		return
	case !known, f.Get("redirect_uri") != g.RedirectURL, s256(f.Get("code_verifier")) != challenge:
		oauthError(w, http.StatusBadRequest, "invalid_grant")
		return
	}
	if g.tokenStatus != http.StatusOK {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(g.tokenStatus)
		fmt.Fprintf(w, `{"error":"invalid_grant","error_description":"echo %s %s"}`, RefreshToken, ClientSecret)
		return
	}
	refresh := ""
	if !g.omitRefresh {
		issue := RefreshToken
		if n := len(g.issued); n > 0 {
			issue = fmt.Sprintf("%s-%d", RefreshToken, n+1)
		}
		g.issued = append(g.issued, issue)
		refresh = fmt.Sprintf(`"refresh_token":%q,`, issue)
	}
	h := g.tokenHold
	g.tokenHold = nil
	g.wait(h)
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"access_token":%q,%s"expires_in":3599,"scope":%q,"token_type":"Bearer"}`,
		AccessToken, refresh, g.scope)
}

// refresh answers a refresh_token grant: an access token for a live refresh
// token, invalid_grant quoting the token for any other.
func (g *Google) refresh(w http.ResponseWriter, f url.Values) {
	token := f.Get("refresh_token")
	switch {
	case f.Get("client_id") != ClientID || f.Get("client_secret") != ClientSecret:
		oauthError(w, http.StatusUnauthorized, "invalid_client")
		return
	case !slices.Contains(g.issued, token) || g.dead[token]:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error":"invalid_grant","error_description":"Token has been expired or revoked: %s"}`, token)
		return
	case g.tokenStatus != http.StatusOK:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(g.tokenStatus)
		fmt.Fprintf(w, `{"error":"server_error","error_description":"echo %s %s"}`, token, ClientSecret)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"access_token":%q,"expires_in":3599,"scope":%q,"token_type":"Bearer"}`, AccessToken, g.scope)
}

// calendarList answers like Google's calendarList.list: the scripted
// calendars, a page at a time, to a live grant's access token.
func (g *Google) calendarList(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calendarCalls++
	h := g.calendarHold
	g.calendarHold = nil
	g.wait(h)
	auth := r.Header.Get("Authorization")
	if auth != "Bearer "+AccessToken || !g.grantLive() || g.rejectAccess {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprintf(w, `{"error":{"code":401,"message":"Invalid Credentials: %s"}}`, auth)
		return
	}
	if status := g.calendarStatus; status != http.StatusOK {
		if g.calendarFails > 0 {
			g.calendarFails--
			if g.calendarFails == 0 {
				g.calendarStatus = http.StatusOK
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		fmt.Fprintf(w, `{"error":{"code":%d,"message":"echo %s"}}`, status, auth)
		return
	}
	start := 0
	if pt := r.URL.Query().Get("pageToken"); pt != "" {
		n, err := strconv.Atoi(strings.TrimPrefix(pt, "page-"))
		if err != nil || n <= 0 || n > len(g.calendars) {
			http.Error(w, "bad pageToken", http.StatusBadRequest)
			return
		}
		start = n
	}
	end := len(g.calendars)
	if g.pageSize > 0 {
		end = min(start+g.pageSize, end)
	}
	items := []map[string]any{}
	for _, c := range g.calendars[start:end] {
		item := map[string]any{
			"kind":            "calendar#calendarListEntry",
			"etag":            `"1700000000000000"`,
			"id":              c.ID,
			"summary":         c.Summary,
			"description":     c.Description,
			"timeZone":        "America/New_York",
			"colorId":         "14",
			"backgroundColor": "#9fe1e7",
			"foregroundColor": "#000000",
			"selected":        true,
			"accessRole":      c.AccessRole,
			"defaultReminders": []map[string]any{
				{"method": "popup", "minutes": 30},
			},
			"conferenceProperties": map[string]any{"allowedConferenceSolutionTypes": []string{"hangoutsMeet"}},
		}
		if c.Primary {
			item["primary"] = true
		}
		if c.SummaryOverride != "" {
			item["summaryOverride"] = c.SummaryOverride
		}
		items = append(items, item)
	}
	page := map[string]any{"kind": "calendar#calendarList", "etag": `"p33k"`, "items": items}
	if end < len(g.calendars) {
		page["nextPageToken"] = fmt.Sprintf("page-%d", end)
	} else {
		page["nextSyncToken"] = "sync-token"
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(page); err != nil {
		panic(err)
	}
}

// insertEvent answers like Google's events.insert with a client-chosen id.
func (g *Google) insertEvent(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	cal, ok := g.eventCalendar(w, r)
	if !ok {
		return
	}
	var e Event
	if err := json.NewDecoder(r.Body).Decode(&e); err != nil || e.ID == "" {
		http.Error(w, "bad event", http.StatusBadRequest)
		return
	}
	if _, exists := g.events[cal][e.ID]; exists {
		apiError(w, http.StatusConflict, "duplicate", "The requested identifier already exists.")
		return
	}
	if g.events[cal] == nil {
		g.events[cal] = map[string]Event{}
	}
	g.events[cal][e.ID] = e
	g.inserts++
	writeEvent(w, e)
}

// updateEvent answers like Google's events.update.
func (g *Google) updateEvent(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	cal, ok := g.eventCalendar(w, r)
	if !ok {
		return
	}
	id := r.PathValue("event")
	if _, exists := g.events[cal][id]; !exists {
		apiError(w, http.StatusNotFound, "notFound", "Not Found")
		return
	}
	var e Event
	if err := json.NewDecoder(r.Body).Decode(&e); err != nil || e.ID != id {
		http.Error(w, "bad event", http.StatusBadRequest)
		return
	}
	g.events[cal][id] = e
	g.updates++
	writeEvent(w, e)
}

// deleteEvent answers like Google's events.delete: 204, or 404 when the
// event is not on the calendar.
func (g *Google) deleteEvent(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	cal, ok := g.eventCalendar(w, r)
	if !ok {
		return
	}
	id := r.PathValue("event")
	if _, exists := g.events[cal][id]; !exists {
		apiError(w, http.StatusNotFound, "notFound", "Not Found")
		return
	}
	delete(g.events[cal], id)
	w.WriteHeader(http.StatusNoContent)
}

// eventCalendar runs the checks every events request shares: the hold, a
// live grant's access token, a scripted failure, and a listed, writable
// calendar, which it returns. When a check fails it answers and returns
// false. The caller holds g.mu.
func (g *Google) eventCalendar(w http.ResponseWriter, r *http.Request) (string, bool) {
	g.eventWrites++
	h := g.eventHold
	g.eventHold = nil
	g.wait(h)
	auth := r.Header.Get("Authorization")
	if auth != "Bearer "+AccessToken || !g.grantLive() || g.rejectAccess {
		apiError(w, http.StatusUnauthorized, "authError", "Invalid Credentials: "+auth)
		return "", false
	}
	if g.failWrite != 0 && g.eventWrites >= g.failWrite && g.eventWrites <= g.failLast {
		apiError(w, g.failStatus, "backendError", "echo "+auth)
		return "", false
	}
	cal := r.PathValue("calendar")
	i := slices.IndexFunc(g.calendars, func(c Calendar) bool { return c.ID == cal })
	switch {
	case i < 0:
		apiError(w, http.StatusNotFound, "notFound", "Not Found")
		return "", false
	case g.calendars[i].AccessRole != "writer" && g.calendars[i].AccessRole != "owner":
		apiError(w, http.StatusForbidden, "requiredAccessLevel", "You need to have writer access to this calendar.")
		return "", false
	}
	return cal, true
}

// writeEvent answers with e as Google does, with fields of its own.
func writeEvent(w http.ResponseWriter, e Event) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"kind": "calendar#event", "etag": `"3300000000000000"`, "id": e.ID, "status": e.Status,
		"htmlLink": "https://www.google.com/calendar/event?eid=" + e.ID,
		"summary":  e.Summary, "description": e.Description, "start": e.Start, "end": e.End,
		"iCalUID": e.ID + "@google.com", "sequence": 0,
	}); err != nil {
		panic(err)
	}
}

// apiError answers a Calendar API error with reason and message.
func apiError(w http.ResponseWriter, status int, reason, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{
		"code": status, "message": message,
		"errors": []map[string]any{{"domain": "global", "reason": reason, "message": message}},
	}}); err != nil {
		panic(err)
	}
}

// grantLive reports whether any refresh token issued is still live. The
// caller holds g.mu.
func (g *Google) grantLive() bool {
	for _, t := range g.issued {
		if !g.dead[t] {
			return true
		}
	}
	return false
}

func (g *Google) revoke(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
		http.Error(w, "want a form", http.StatusUnsupportedMediaType)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	h := g.revokeHold
	g.revokeHold = nil
	g.wait(h)
	token := r.PostForm.Get("token")
	g.revoked = append(g.revoked, token)
	if g.revokeStatus == http.StatusOK && slices.Contains(g.issued, token) {
		// Google revokes the grant, not just the token: every token issued
		// to it so far stops working.
		for _, t := range g.issued {
			g.dead[t] = true
		}
	}
	body := g.revokeBody
	if body == "" && g.revokeStatus != http.StatusOK {
		body = "cannot revoke " + token
	}
	w.WriteHeader(g.revokeStatus)
	fmt.Fprint(w, body)
}

func oauthError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	fmt.Fprintf(w, `{"error":%q}`, code)
}

// s256 is the PKCE S256 challenge for verifier.
func s256(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
