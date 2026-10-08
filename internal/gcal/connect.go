// Package gcal connects the household's Google account for Push to Calendar.
// The service is a confidential OAuth web client: the authorization code,
// tokens and client secret stay on the server. The refresh token is stored
// sealed by secretbox in integrations_google and is never logged or
// returned.
package gcal

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/endpoints"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/auth"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/secretbox"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

// CallbackPath is where Google returns the browser after consent. It is
// served under the signed-in /api/v1 router, so the redirect URI registered
// with Google is MEALQUEST_PUBLIC_URL's origin followed by this path.
const CallbackPath = "/api/v1/integrations/google/callback"

// Scopes requested at connect: create and change the events MealQuest pushes,
// and list the calendars the household can push to.
var Scopes = []string{
	"https://www.googleapis.com/auth/calendar.events",
	"https://www.googleapis.com/auth/calendar.calendarlist.readonly",
}

// GoogleRevokeURL is Google's OAuth token revocation endpoint.
const GoogleRevokeURL = "https://oauth2.googleapis.com/revoke"

// GoogleCalendarAPI is the base URL of Google's Calendar API v3.
const GoogleCalendarAPI = "https://www.googleapis.com/calendar/v3"

// GoogleEndpoint is Google's OAuth authorization and token endpoint.
var GoogleEndpoint = oauth2.Endpoint{
	AuthURL:   endpoints.Google.AuthURL,
	TokenURL:  endpoints.Google.TokenURL,
	AuthStyle: oauth2.AuthStyleInParams,
}

const (
	connectCookieName = "__Host-mealquest_google"
	connectCookieTTL  = 10 * time.Minute
	// connectCookieKeyLabel separates the connect cookie's signing key from
	// the login cookie's and every other key derived from the server secret.
	connectCookieKeyLabel = "mealquest google connect cookie v1"
	// googleTimeout bounds every request to Google.
	googleTimeout = 10 * time.Second
	// maxRevokeBody caps how much of a revoke response is read.
	maxRevokeBody = 4 << 10
)

var (
	// ErrBadState means the callback's state is missing, does not match the
	// connect cookie, or the cookie is missing, expired or belongs to another
	// session.
	ErrBadState = errors.New("gcal: connect state is missing, expired or does not match this session")
	// ErrDenied means Google returned an authorization error, usually because
	// the household declined consent.
	ErrDenied = errors.New("gcal: Google did not grant access")
	// ErrNoCode means the callback carries no authorization code.
	ErrNoCode = errors.New("gcal: callback has no authorization code")
	// ErrNoRefreshToken means Google granted access without a refresh token,
	// so MealQuest could not push later; the household must connect again.
	ErrNoRefreshToken = errors.New("gcal: Google returned no refresh token")
	// ErrScopes means the household did not grant every scope in Scopes.
	ErrScopes = errors.New("gcal: Google did not grant every Calendar permission")
	// ErrNotConnected means no Google account is connected.
	ErrNotConnected = errors.New("gcal: Google Calendar is not connected")
)

// Config is what NewService needs to talk to Google.
type Config struct {
	ClientID     string
	ClientSecret string
	// PublicURL is the service's public URL; its origin prefixes CallbackPath
	// to form the redirect URI.
	PublicURL *url.URL
	// SecretKey is the server key the connect cookie's signing key derives
	// from.
	SecretKey []byte
	// Endpoint is Google's authorization and token endpoint; production uses
	// GoogleEndpoint and tests a fake.
	Endpoint oauth2.Endpoint
	// RevokeURL is Google's revocation endpoint; production uses
	// GoogleRevokeURL and tests a fake.
	RevokeURL string
	// CalendarAPI is the Calendar API's base URL; production uses
	// GoogleCalendarAPI and tests a fake.
	CalendarAPI string
}

// Service connects and disconnects the household's Google account and
// chooses the calendar MealQuest pushes to.
// MealQuest runs as one process, so the service's in-memory lock and pending
// states see every request.
type Service struct {
	st        *store.Store
	box       *secretbox.Box
	oauth     oauth2.Config
	revokeURL string
	// calendarAPI is the Calendar API's base URL.
	calendarAPI string
	client      *http.Client
	cookie      auth.FlowCookie
	// now is the clock; tests replace it.
	now func() time.Time

	// account serializes every use of and change to the connected account —
	// a connect's exchange and store, a disconnect's read, revoke and delete,
	// a calendar listing's read and Calendar API calls, a calendar choice's
	// listing and store — across the requests to Google and the database
	// write. Google revokes the whole grant, so a revoke overlapping a
	// connect would also kill the token the connect stores.
	account sync.Mutex

	// pending holds the state of every connect started and not yet
	// completed, with when it expires. Complete consumes a state once, so a
	// replayed cookie finds it gone.
	pendingMu sync.Mutex
	pending   map[string]time.Time
}

// NewService returns the service over st, sealing refresh tokens with box.
func NewService(st *store.Store, box *secretbox.Box, cfg Config) (*Service, error) {
	switch {
	case st == nil:
		return nil, errors.New("gcal: nil store")
	case box == nil:
		return nil, errors.New("gcal: nil secret box")
	case cfg.ClientID == "", cfg.ClientSecret == "":
		return nil, errors.New("gcal: client ID and client secret are required")
	case cfg.PublicURL == nil:
		return nil, errors.New("gcal: public URL is required")
	case len(cfg.SecretKey) == 0:
		return nil, errors.New("gcal: secret key is required")
	case cfg.Endpoint.AuthURL == "", cfg.Endpoint.TokenURL == "", cfg.RevokeURL == "", cfg.CalendarAPI == "":
		return nil, errors.New("gcal: authorization, token, revoke and Calendar API endpoints are required")
	}
	return &Service{
		st:  st,
		box: box,
		oauth: oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			Endpoint:     cfg.Endpoint,
			RedirectURL:  cfg.PublicURL.Scheme + "://" + cfg.PublicURL.Host + CallbackPath,
			Scopes:       slices.Clone(Scopes),
		},
		revokeURL:   cfg.RevokeURL,
		calendarAPI: cfg.CalendarAPI,
		client:      &http.Client{Timeout: googleTimeout, CheckRedirect: auth.CheckRedirect},
		cookie:      auth.NewFlowCookie(connectCookieName, connectCookieKeyLabel, connectCookieTTL, cfg.SecretKey),
		now:         time.Now,
		pending:     map[string]time.Time{},
	}, nil
}

// connectState travels from Connect to Complete in the signed connect
// cookie.
type connectState struct {
	State    string `json:"s"`
	Verifier string `json:"v"`
	// Session binds the state to the session that started the connect: the
	// SHA-256 of its CSRF token, which is unique to the session and never
	// changes during it.
	Session string `json:"b"`
	auth.FlowExpiry
}

// sessionBinding is the value connectState.Session must hold for s.
func sessionBinding(s auth.Session) string {
	sum := sha256.Sum256([]byte(s.CSRFToken))
	return hex.EncodeToString(sum[:])
}

// Connect sets the connect cookie on w, bound to session, and returns
// Google's consent URL: offline access, consent always prompted so Google
// always issues a refresh token, and PKCE (S256).
func (s *Service) Connect(w http.ResponseWriter, session auth.Session) (string, error) {
	if session.CSRFToken == "" {
		return "", errors.New("gcal: connect needs a session")
	}
	state := connectState{
		State:    rand.Text(),
		Verifier: oauth2.GenerateVerifier(),
		Session:  sessionBinding(session),
	}
	now := s.now()
	if err := s.cookie.Set(w, &state, now); err != nil {
		return "", fmt.Errorf("gcal: set connect cookie: %w", err)
	}
	s.addPending(state.State, now)
	return s.oauth.AuthCodeURL(state.State,
		oauth2.AccessTypeOffline,
		oauth2.ApprovalForce,
		oauth2.S256ChallengeOption(state.Verifier),
	), nil
}

// Complete finishes the connect Google redirected r back with. It clears the
// connect cookie, checks the state against it and session, consumes the
// state, exchanges the code and stores the refresh token, sealed, with
// connected_at now. A reconnect replaces the stored token and keeps the
// chosen calendar.
func (s *Service) Complete(w http.ResponseWriter, r *http.Request, session auth.Session) error {
	ctx := r.Context()
	s.cookie.Clear(w)

	var state connectState
	if err := s.cookie.Read(r, &state, s.now()); err != nil {
		return fmt.Errorf("%w: %v", ErrBadState, err)
	}
	q := r.URL.Query()
	if subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(state.State)) != 1 {
		return fmt.Errorf("%w: state mismatch", ErrBadState)
	}
	if subtle.ConstantTimeCompare([]byte(sessionBinding(session)), []byte(state.Session)) != 1 {
		return fmt.Errorf("%w: started by another session", ErrBadState)
	}
	// A copy of the cookie verifies until it expires, so the state is
	// single-use only because it is consumed here.
	if !s.consumePending(state.State) {
		return fmt.Errorf("%w: already used", ErrBadState)
	}
	if q.Get("error") != "" {
		// error and error_description are Google-supplied text; neither is
		// kept.
		return ErrDenied
	}
	code := q.Get("code")
	if code == "" {
		return ErrNoCode
	}

	s.account.Lock()
	defer s.account.Unlock()
	token, err := s.oauth.Exchange(context.WithValue(ctx, oauth2.HTTPClient, s.client), code,
		oauth2.VerifierOption(state.Verifier))
	if err != nil {
		return fmt.Errorf("gcal: %w", auth.ExchangeError(err))
	}
	if token.RefreshToken == "" {
		return ErrNoRefreshToken
	}
	if err := checkScopes(token); err != nil {
		return err
	}
	sealed, err := s.box.Encrypt([]byte(token.RefreshToken))
	if err != nil {
		return fmt.Errorf("gcal: seal refresh token: %w", err)
	}
	return s.store(ctx, sealed)
}

// addPending records state, started at now, as pending until the connect
// cookie expires, and forgets expired ones.
func (s *Service) addPending(state string, now time.Time) {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	for k, expires := range s.pending {
		if !now.Before(expires) {
			delete(s.pending, k)
		}
	}
	s.pending[state] = now.Add(connectCookieTTL)
}

// consumePending removes state from the pending states, reporting whether
// it was there.
func (s *Service) consumePending(state string) bool {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	_, ok := s.pending[state]
	delete(s.pending, state)
	return ok
}

// checkScopes fails with ErrScopes unless token's granted scopes include
// every scope in Scopes. Google lets the household untick a permission on
// the consent screen and reports what was granted in "scope".
func checkScopes(token *oauth2.Token) error {
	granted, _ := token.Extra("scope").(string)
	have := strings.Fields(granted)
	for _, want := range Scopes {
		if !slices.Contains(have, want) {
			return ErrScopes
		}
	}
	return nil
}

// store saves sealed as the refresh token with connected_at now, clearing
// any earlier sync error.
func (s *Service) store(ctx context.Context, sealed []byte) error {
	return s.st.WithTx(ctx, func(tx *sql.Tx) error {
		q := db.New(tx)
		row, err := q.GetIntegrationsGoogle(ctx)
		exists := err == nil
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("gcal: read Google integration: %w", err)
		}
		now := store.Timestamp{Time: s.now().UTC().Truncate(time.Millisecond)}.String()
		created := now
		if exists {
			created = row.CreatedAt
		}
		if _, err := q.UpsertIntegrationsGoogle(ctx, db.UpsertIntegrationsGoogleParams{
			RefreshTokenCt: sealed,
			CalendarID:     row.CalendarID,
			CalendarName:   row.CalendarName,
			ConnectedAt:    now,
			CreatedAt:      created,
			UpdatedAt:      now,
		}); err != nil {
			return fmt.Errorf("gcal: save Google integration: %w", err)
		}
		return nil
	})
}

// Disconnect revokes the stored refresh token at Google, then deletes the
// connection. A token Google no longer knows (the household already removed
// MealQuest's access in their Google account) counts as revoked. Any other
// revoke failure keeps the connection so the household can retry.
func (s *Service) Disconnect(ctx context.Context) error {
	s.account.Lock()
	defer s.account.Unlock()
	row, err := db.New(s.st.Reader).GetIntegrationsGoogle(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotConnected
	}
	if err != nil {
		return fmt.Errorf("gcal: read Google integration: %w", err)
	}
	token, err := s.box.Decrypt(row.RefreshTokenCt)
	if err != nil {
		return fmt.Errorf("gcal: open refresh token: %w", err)
	}
	if err := s.revoke(ctx, string(token)); err != nil {
		return err
	}
	if err := db.New(s.st.Writer).DeleteIntegrationsGoogle(ctx); err != nil {
		return fmt.Errorf("gcal: delete Google integration: %w", err)
	}
	return nil
}

// RevokeError is a revoke Google refused or that failed in transit. Its text
// never holds the token or Google's response body.
type RevokeError struct {
	msg string
}

func (e *RevokeError) Error() string { return e.msg }

// revoke revokes token at Google. HTTP 200 and HTTP 400 invalid_token (the
// token is already revoked or expired) both leave it unusable.
func (s *Service) revoke(ctx context.Context, token string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.revokeURL,
		strings.NewReader(url.Values{"token": {token}}.Encode()))
	if err != nil {
		return fmt.Errorf("gcal: build revoke request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.client.Do(req)
	if err != nil {
		var transport *url.Error
		if errors.As(err, &transport) {
			// The URL is dropped: after a refused redirect it is the
			// Google-supplied Location.
			return &RevokeError{msg: fmt.Sprintf("gcal: revoke request failed: %s: %v", transport.Op, transport.Err)}
		}
		return &RevokeError{msg: "gcal: revoke request failed"}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxRevokeBody))
	if err != nil {
		return &RevokeError{msg: "gcal: read revoke response failed"}
	}
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	if resp.StatusCode == http.StatusBadRequest {
		var reply struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(body, &reply) == nil && reply.Error == "invalid_token" {
			return nil
		}
	}
	return &RevokeError{msg: fmt.Sprintf("gcal: Google refused the revoke (HTTP %d)", resp.StatusCode)}
}
