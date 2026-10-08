// Package gcaltest provides a fake Google OAuth server for tests of the
// Google Calendar connection: a token endpoint that checks the client, the
// redirect URI and PKCE, and a revoke endpoint that records what it revoked.
// It never touches the network beyond its own httptest server.
package gcaltest

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"golang.org/x/oauth2"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/gcal"
)

// Fixed credentials and tokens the fake issues and expects.
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
}

// New starts a fake Google that requires redirectURL at its token endpoint.
// It grants every scope in gcal.Scopes with a refresh token by default.
func New(t *testing.T, redirectURL string) *Google {
	t.Helper()
	g := &Google{
		RedirectURL:  redirectURL,
		scope:        strings.Join(gcal.Scopes, " "),
		tokenStatus:  http.StatusOK,
		revokeStatus: http.StatusOK,
		codes:        map[string]string{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /token", g.token)
	mux.HandleFunc("POST /revoke", g.revoke)
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

// Authorize plays the consent screen for an authorization request with PKCE
// challenge and returns the code Google would send back.
func (g *Google) Authorize(challenge string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	code := fmt.Sprintf("code-%d", len(g.codes)+1)
	g.codes[code] = challenge
	return code
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

// TokenCalls is how many requests reached the token endpoint.
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

func (g *Google) token(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.tokenCalls++
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	f := r.PostForm
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
		refresh = fmt.Sprintf(`"refresh_token":%q,`, RefreshToken)
	}
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"access_token":%q,%s"expires_in":3599,"scope":%q,"token_type":"Bearer"}`,
		AccessToken, refresh, g.scope)
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
	token := r.PostForm.Get("token")
	g.revoked = append(g.revoked, token)
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
