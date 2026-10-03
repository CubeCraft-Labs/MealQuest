package auth

import (
	"bytes"
	"context"
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"maps"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

const (
	testClientID     = "mealquest"
	testClientSecret = "client-s3cret"
	testKID          = "test-key"
)

var testPublicURL = &url.URL{Scheme: "https", Host: "meals.example.com"}

// fakeProvider is an OIDC provider serving discovery, JWKS and a token
// endpoint. Each issued code maps to the ID token the token endpoint returns.
type fakeProvider struct {
	t   *testing.T
	srv *httptest.Server
	key *rsa.PrivateKey

	mu    sync.Mutex
	codes map[string]issuedCode
	// discoveryOverrides replace fields of the discovery document.
	discoveryOverrides map[string]any
	// tokenReply, when set, is returned by the token endpoint instead of a
	// token.
	tokenReply *tokenReply
}

type tokenReply struct {
	status int
	body   string
}

type issuedCode struct {
	challenge string
	idToken   string
}

func newFakeProvider(t *testing.T) *fakeProvider {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := &fakeProvider{t: t, key: key, codes: map[string]issuedCode{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", p.discovery)
	mux.HandleFunc("GET /jwks", p.jwks)
	mux.HandleFunc("POST /token", p.token)
	p.srv = httptest.NewServer(mux)
	t.Cleanup(p.srv.Close)
	return p
}

func (p *fakeProvider) issuer() string { return p.srv.URL }

func (p *fakeProvider) discovery(w http.ResponseWriter, _ *http.Request) {
	doc := map[string]any{
		"issuer":                                p.issuer(),
		"authorization_endpoint":                p.issuer() + "/authorize",
		"token_endpoint":                        p.issuer() + "/token",
		"jwks_uri":                              p.issuer() + "/jwks",
		"response_types_supported":              []string{"code"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256", "HS256"},
	}
	p.mu.Lock()
	maps.Copy(doc, p.discoveryOverrides)
	p.mu.Unlock()
	writeJSON(w, doc)
}

func (p *fakeProvider) jwks(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{"keys": []map[string]string{{
		"kty": "RSA",
		"kid": testKID,
		"use": "sig",
		"alg": "RS256",
		"n":   b64(p.key.N.Bytes()),
		"e":   b64(big.NewInt(int64(p.key.E)).Bytes()),
	}}})
}

// token checks client authentication, the code and the PKCE verifier before
// returning the code's ID token.
func (p *fakeProvider) token(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	reply := p.tokenReply
	p.mu.Unlock()
	if reply != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(reply.status)
		io.WriteString(w, reply.body)
		return
	}
	id, secret, ok := r.BasicAuth()
	if !ok || id != testClientID || secret != testClientSecret {
		http.Error(w, `{"error":"invalid_client"}`, http.StatusUnauthorized)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, `{"error":"invalid_request"}`, http.StatusBadRequest)
		return
	}
	p.mu.Lock()
	issued, ok := p.codes[r.PostForm.Get("code")]
	delete(p.codes, r.PostForm.Get("code"))
	p.mu.Unlock()
	if r.PostForm.Get("grant_type") != "authorization_code" || !ok ||
		challengeS256(r.PostForm.Get("code_verifier")) != issued.challenge {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":"invalid_grant"}`)
		return
	}
	writeJSON(w, map[string]any{
		"access_token": "access-token",
		"token_type":   "Bearer",
		"expires_in":   3600,
		"id_token":     issued.idToken,
	})
}

// authorize stands in for the user signing in at the provider: it records a
// code bound to the PKCE challenge and returns it.
func (p *fakeProvider) authorize(challenge, idToken string) string {
	code := rand.Text()
	p.mu.Lock()
	p.codes[code] = issuedCode{challenge: challenge, idToken: idToken}
	p.mu.Unlock()
	return code
}

// claims returns valid ID token claims for nonce.
func (p *fakeProvider) claims(nonce string) map[string]any {
	now := time.Now()
	return map[string]any{
		"iss":                p.issuer(),
		"sub":                "user-123",
		"aud":                testClientID,
		"iat":                now.Unix(),
		"exp":                now.Add(5 * time.Minute).Unix(),
		"nonce":              nonce,
		"email":              "cook@example.com",
		"email_verified":     true,
		"name":               "Home Cook",
		"preferred_username": "cook",
		"groups":             []string{"mealquest-admin"},
	}
}

func (p *fakeProvider) signRS256(claims map[string]any) string {
	return signRS256(p.t, p.key, claims)
}

func signRS256(t *testing.T, key *rsa.PrivateKey, claims map[string]any) string {
	t.Helper()
	signing := jwtSigningInput(t, "RS256", claims)
	digest := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return signing + "." + b64(sig)
}

// signHS256 signs with the client secret, as in an algorithm-confusion attack.
func (p *fakeProvider) signHS256(claims map[string]any) string {
	signing := jwtSigningInput(p.t, "HS256", claims)
	mac := hmac.New(sha256.New, []byte(testClientSecret))
	mac.Write([]byte(signing))
	return signing + "." + b64(mac.Sum(nil))
}

func jwtSigningInput(t *testing.T, alg string, claims map[string]any) string {
	t.Helper()
	header, err := json.Marshal(map[string]string{"alg": alg, "typ": "JWT", "kid": testKID})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return b64(header) + "." + b64(body)
}

func challengeS256(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return b64(sum[:])
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// memorySessions is an in-memory SessionStarter that records the claims it
// was handed.
type memorySessions struct {
	mu     sync.Mutex
	claims []Claims
	err    error
}

func (m *memorySessions) StartSession(w http.ResponseWriter, _ *http.Request, claims Claims) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.claims = append(m.claims, claims)
	http.SetCookie(w, &http.Cookie{Name: "test_session", Value: claims.Subject})
	return nil
}

func (m *memorySessions) started() []Claims {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.claims)
}

type harness struct {
	provider *fakeProvider
	sessions *memorySessions
	router   http.Handler
	logs     *syncBuffer
}

// syncBuffer is a bytes.Buffer safe for concurrent log writes.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	p := newFakeProvider(t)
	sessions := &memorySessions{}
	logs := &syncBuffer{}
	a, err := New(context.Background(), Config{
		Issuer:       p.issuer(),
		ClientID:     testClientID,
		ClientSecret: testClientSecret,
		PublicURL:    testPublicURL,
		SecretKey:    []byte("server-secret-key"),
	}, sessions, slog.New(slog.NewTextHandler(logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	r := chi.NewRouter()
	a.Mount(r)
	return &harness{provider: p, sessions: sessions, router: r, logs: logs}
}

func (h *harness) get(target string, cookies ...*http.Cookie) *http.Response {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)
	return rec.Result()
}

// login is a started login: the authorize URL's query and the login cookie.
type login struct {
	query  url.Values
	cookie *http.Cookie
}

func (h *harness) login(t *testing.T, target string) login {
	t.Helper()
	resp := h.get(target)
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("GET %s status = %d, want 302", target, resp.StatusCode)
	}
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := loc.Scheme+"://"+loc.Host+loc.Path, h.provider.issuer()+"/authorize"; got != want {
		t.Fatalf("redirect to %s, want %s", got, want)
	}
	var cookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == loginCookieName {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatalf("login set no %s cookie", loginCookieName)
	}
	return login{query: loc.Query(), cookie: cookie}
}

// callback completes l at the provider with idToken and calls the callback.
func (h *harness) callback(l login, state, idToken string) *http.Response {
	code := h.provider.authorize(l.query.Get("code_challenge"), idToken)
	q := url.Values{"code": {code}, "state": {state}}
	return h.get(CallbackPath+"?"+q.Encode(), l.cookie)
}

func TestLoginRedirect(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	l := h.login(t, LoginPath+"?return=/plan")

	want := map[string]string{
		"client_id":             testClientID,
		"response_type":         "code",
		"scope":                 "openid profile email groups offline_access",
		"redirect_uri":          "https://meals.example.com/auth/callback",
		"code_challenge_method": "S256",
	}
	for k, v := range want {
		if got := l.query.Get(k); got != v {
			t.Errorf("authorize %s = %q, want %q", k, got, v)
		}
	}
	for _, k := range []string{"state", "nonce", "code_challenge"} {
		if l.query.Get(k) == "" {
			t.Errorf("authorize URL has no %s", k)
		}
	}

	c := l.cookie
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode || c.Path != "/" {
		t.Errorf("login cookie HttpOnly=%v Secure=%v SameSite=%v Path=%q, want HttpOnly, Secure, Lax, /",
			c.HttpOnly, c.Secure, c.SameSite, c.Path)
	}
	if c.MaxAge <= 0 || c.MaxAge > 600 {
		t.Errorf("login cookie MaxAge = %d, want 1-600 seconds", c.MaxAge)
	}
}

func TestLoginStateIsFreshEachTime(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	a, b := h.login(t, LoginPath), h.login(t, LoginPath)
	for _, k := range []string{"state", "nonce", "code_challenge"} {
		if a.query.Get(k) == b.query.Get(k) {
			t.Errorf("%s repeated across logins", k)
		}
	}
}

func TestCallbackSuccess(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	l := h.login(t, LoginPath+"?return="+url.QueryEscape("/plan?week=2026-W40"))

	resp := h.callback(l, l.query.Get("state"), h.provider.signRS256(h.provider.claims(l.query.Get("nonce"))))

	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("callback status = %d, want 303", resp.StatusCode)
	}
	if got := resp.Header.Get("Location"); got != "/plan?week=2026-W40" {
		t.Errorf("callback redirect = %q, want the return path", got)
	}
	started := h.sessions.started()
	want := Claims{
		Subject:           "user-123",
		Email:             "cook@example.com",
		EmailVerified:     true,
		Name:              "Home Cook",
		PreferredUsername: "cook",
		Groups:            []string{"mealquest-admin"},
	}
	if len(started) != 1 || started[0].Subject != want.Subject || started[0].Email != want.Email ||
		started[0].Name != want.Name || started[0].PreferredUsername != want.PreferredUsername ||
		!started[0].EmailVerified || !slices.Equal(started[0].Groups, want.Groups) {
		t.Errorf("session started with %+v, want [%+v]", started, want)
	}
	assertLoginCookieCleared(t, resp)
}

func TestCallbackDefaultReturnPath(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	l := h.login(t, LoginPath)
	resp := h.callback(l, l.query.Get("state"), h.provider.signRS256(h.provider.claims(l.query.Get("nonce"))))
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/" {
		t.Errorf("callback = %d to %q, want 303 to /", resp.StatusCode, resp.Header.Get("Location"))
	}
}

func TestCallbackRejected(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// state returns the state sent to the callback.
		state func(l login) string
		// token returns the ID token the provider issues.
		token      func(p *fakeProvider, l login) string
		wantStatus int
	}{
		{
			name:       "mismatched state",
			state:      func(login) string { return "attacker-state" },
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "wrong nonce",
			token: func(p *fakeProvider, _ login) string {
				return p.signRS256(p.claims("other-nonce"))
			},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "missing nonce",
			token: func(p *fakeProvider, _ login) string {
				c := p.claims("")
				delete(c, "nonce")
				return p.signRS256(c)
			},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "HS256-signed token",
			token: func(p *fakeProvider, l login) string {
				return p.signHS256(p.claims(l.query.Get("nonce")))
			},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "expired token",
			token: func(p *fakeProvider, l login) string {
				c := p.claims(l.query.Get("nonce"))
				c["iat"] = time.Now().Add(-2 * time.Hour).Unix()
				c["exp"] = time.Now().Add(-time.Hour).Unix()
				return p.signRS256(c)
			},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "wrong issuer",
			token: func(p *fakeProvider, l login) string {
				c := p.claims(l.query.Get("nonce"))
				c["iss"] = "https://evil.example.com"
				return p.signRS256(c)
			},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "wrong audience",
			token: func(p *fakeProvider, l login) string {
				c := p.claims(l.query.Get("nonce"))
				c["aud"] = "another-client"
				return p.signRS256(c)
			},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "signed by an unknown key",
			token: func(p *fakeProvider, l login) string {
				key, err := rsa.GenerateKey(rand.Reader, 2048)
				if err != nil {
					p.t.Fatal(err)
				}
				return signRS256(p.t, key, p.claims(l.query.Get("nonce")))
			},
			wantStatus: http.StatusUnauthorized,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			l := h.login(t, LoginPath)
			state := l.query.Get("state")
			if tc.state != nil {
				state = tc.state(l)
			}
			token := h.provider.signRS256(h.provider.claims(l.query.Get("nonce")))
			if tc.token != nil {
				token = tc.token(h.provider, l)
			}

			resp := h.callback(l, state, token)

			if resp.StatusCode != tc.wantStatus {
				t.Errorf("callback status = %d, want %d", resp.StatusCode, tc.wantStatus)
			}
			if n := len(h.sessions.started()); n != 0 {
				t.Errorf("%d sessions started, want none", n)
			}
			assertLoginCookieCleared(t, resp)
		})
	}
}

func TestCallbackWithoutLoginCookie(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	l := h.login(t, LoginPath)
	l.cookie = &http.Cookie{Name: "unrelated", Value: "x"}
	resp := h.callback(l, l.query.Get("state"), h.provider.signRS256(h.provider.claims(l.query.Get("nonce"))))
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("callback status = %d, want 400", resp.StatusCode)
	}
	if n := len(h.sessions.started()); n != 0 {
		t.Errorf("%d sessions started, want none", n)
	}
}

func TestCallbackTamperedLoginCookie(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	l := h.login(t, LoginPath)
	payload, sig, _ := strings.Cut(l.cookie.Value, ".")
	data, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		t.Fatal(err)
	}
	var s loginState
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatal(err)
	}
	s.Return = "/somewhere-else"
	data, err = json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	l.cookie = &http.Cookie{Name: loginCookieName, Value: b64(data) + "." + sig}

	resp := h.callback(l, l.query.Get("state"), h.provider.signRS256(h.provider.claims(l.query.Get("nonce"))))
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("callback status = %d, want 400", resp.StatusCode)
	}
}

// The provider's error and error_description are untrusted text and never
// reach the logs; only the fixed category does.
func TestCallbackProviderError(t *testing.T) {
	t.Parallel()
	const (
		errorMarker       = "provider-error-marker"
		descriptionMarker = "description-marker-" + testClientSecret
	)
	h := newHarness(t)
	l := h.login(t, LoginPath)
	q := url.Values{
		"error":             {errorMarker},
		"error_description": {descriptionMarker},
		"state":             {l.query.Get("state")},
	}
	resp := h.get(CallbackPath+"?"+q.Encode(), l.cookie)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("callback status = %d, want 401", resp.StatusCode)
	}
	assertLoginCookieCleared(t, resp)
	if n := len(h.sessions.started()); n != 0 {
		t.Errorf("started %d sessions, want 0", n)
	}
	logs := h.logs.String()
	for _, marker := range []string{errorMarker, descriptionMarker} {
		if strings.Contains(logs, marker) {
			t.Errorf("logs contain provider text %q: %s", marker, logs)
		}
	}
	if !strings.Contains(logs, "identity provider returned an authorization error") {
		t.Errorf("logs = %q, want the authorization error category", logs)
	}
}

func TestCallbackSessionError(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.sessions.err = errors.New("database down")
	l := h.login(t, LoginPath)
	resp := h.callback(l, l.query.Get("state"), h.provider.signRS256(h.provider.claims(l.query.Get("nonce"))))
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("callback status = %d, want 500", resp.StatusCode)
	}
}

// A failed code exchange is logged by category and status only: the token
// endpoint's body and error_description may echo credentials.
func TestCallbackExchangeFailureDoesNotLogProviderResponse(t *testing.T) {
	t.Parallel()
	const leaked = "leaked-" + testClientSecret
	tests := []struct {
		name     string
		reply    tokenReply
		wantLogs string
	}{
		{
			name:     "error response",
			reply:    tokenReply{http.StatusBadRequest, `{"error":"invalid_grant","error_description":"` + leaked + `"}`},
			wantLogs: "HTTP 400",
		},
		{
			name:     "unparseable error response",
			reply:    tokenReply{http.StatusInternalServerError, leaked},
			wantLogs: "HTTP 500",
		},
		{
			name:     "success without access token",
			reply:    tokenReply{http.StatusOK, `{"id_token":"` + leaked + `"}`},
			wantLogs: "unusable response",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			h.provider.mu.Lock()
			h.provider.tokenReply = &tc.reply
			h.provider.mu.Unlock()
			l := h.login(t, LoginPath)

			resp := h.callback(l, l.query.Get("state"), "unused")

			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("callback status = %d, want 401", resp.StatusCode)
			}
			logs := h.logs.String()
			if strings.Contains(logs, leaked) {
				t.Errorf("logs contain the provider response: %s", logs)
			}
			if !strings.Contains(logs, tc.wantLogs) {
				t.Errorf("logs = %q, want %q", logs, tc.wantLogs)
			}
		})
	}
}

func TestLoginCookieExpires(t *testing.T) {
	t.Parallel()
	c := newLoginCookie([]byte("server-secret-key"))
	now := time.Now()
	rec := httptest.NewRecorder()
	if err := c.set(rec, loginState{State: "s", Nonce: "n", Verifier: "v", Return: "/"}, now); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, CallbackPath, nil)
	req.AddCookie(rec.Result().Cookies()[0])

	if _, err := c.read(req, now.Add(loginCookieTTL-time.Second)); err != nil {
		t.Errorf("read before expiry: %v", err)
	}
	if _, err := c.read(req, now.Add(loginCookieTTL)); err == nil {
		t.Error("read at expiry succeeded")
	}
}

func TestLoginRejectsOffOriginReturn(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	for _, ret := range []string{
		"https://evil.example.com/",
		"http://meals.example.com/plan",
		"//evil.example.com/",
		"/\\evil.example.com",
		"\\\\evil.example.com",
		"javascript:alert(1)",
		"plan",
		"/plan\r\nSet-Cookie: x=y",
	} {
		resp := h.get(LoginPath + "?return=" + url.QueryEscape(ret))
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("return=%q: status = %d, want 400", ret, resp.StatusCode)
		}
		if loc := resp.Header.Get("Location"); loc != "" {
			t.Errorf("return=%q: redirected to %q", ret, loc)
		}
		if len(resp.Cookies()) != 0 {
			t.Errorf("return=%q: set cookies %v", ret, resp.Cookies())
		}
	}
}

func TestNewFailsWhenDiscoveryFails(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	_, err := New(context.Background(), Config{
		Issuer:       srv.URL,
		ClientID:     testClientID,
		ClientSecret: testClientSecret,
		PublicURL:    testPublicURL,
		SecretKey:    []byte("k"),
	}, &memorySessions{}, slog.New(slog.DiscardHandler))
	if err == nil {
		t.Fatal("New succeeded without a discovery document")
	}
}

func TestNewFailsOnIssuerMismatch(t *testing.T) {
	t.Parallel()
	p := newFakeProvider(t)
	// Discovery trims the trailing slash, so this fetches the provider's
	// document, which reports the issuer without it.
	_, err := New(context.Background(), Config{
		Issuer:       p.issuer() + "/",
		ClientID:     testClientID,
		ClientSecret: testClientSecret,
		PublicURL:    testPublicURL,
		SecretKey:    []byte("k"),
	}, &memorySessions{}, slog.New(slog.DiscardHandler))
	if err == nil {
		t.Fatal("New accepted a provider reporting a different issuer")
	}
}

func assertLoginCookieCleared(t *testing.T, resp *http.Response) {
	t.Helper()
	for _, c := range resp.Cookies() {
		if c.Name == loginCookieName && c.MaxAge < 0 {
			return
		}
	}
	t.Errorf("callback did not clear the %s cookie", loginCookieName)
}

func TestNewRejectsRemoteHTTPIssuer(t *testing.T) {
	t.Parallel()
	for _, issuer := range []string{"http://auth.example.com/application/o/mealquest/", "http://192.0.2.10"} {
		_, err := New(context.Background(), Config{
			Issuer:       issuer,
			ClientID:     testClientID,
			ClientSecret: testClientSecret,
			PublicURL:    testPublicURL,
			SecretKey:    []byte("k"),
		}, &memorySessions{}, slog.New(slog.DiscardHandler))
		if err == nil || !strings.Contains(err.Error(), "issuer") {
			t.Errorf("New(issuer %q) error = %v, want issuer rejected", issuer, err)
		}
	}
}

func TestNewRejectsRemoteHTTPEndpoint(t *testing.T) {
	t.Parallel()
	tests := []struct {
		field, name string
	}{
		{"authorization_endpoint", "authorization endpoint"},
		{"token_endpoint", "token endpoint"},
		{"jwks_uri", "JWKS endpoint"},
	}
	for _, tc := range tests {
		t.Run(tc.field, func(t *testing.T) {
			t.Parallel()
			p := newFakeProvider(t)
			p.discoveryOverrides = map[string]any{tc.field: "http://auth.example.com/" + tc.field}
			_, err := New(context.Background(), Config{
				Issuer:       p.issuer(),
				ClientID:     testClientID,
				ClientSecret: testClientSecret,
				PublicURL:    testPublicURL,
				SecretKey:    []byte("k"),
			}, &memorySessions{}, slog.New(slog.DiscardHandler))
			if err == nil || !strings.Contains(err.Error(), tc.name) {
				t.Errorf("New error = %v, want %s rejected", err, tc.name)
			}
		})
	}
}

// A token endpoint on https:// that redirects the code exchange to plain
// http:// must not receive a follow-up request: Go would resend the client
// secret in the Authorization header. The plaintext target is loopback, so
// only the downgrade rule stops it.
func TestCallbackRefusesTokenRedirectToHTTP(t *testing.T) {
	t.Parallel()
	for _, status := range []int{http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			t.Parallel()
			var plaintextHits atomic.Int32
			plaintext := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				plaintextHits.Add(1)
				http.Error(w, "unreachable", http.StatusTeapot)
			}))
			t.Cleanup(plaintext.Close)
			var tlsHits atomic.Int32
			tlsToken := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				tlsHits.Add(1)
				http.Redirect(w, r, plaintext.URL+"/token", status)
			}))
			t.Cleanup(tlsToken.Close)

			p := newFakeProvider(t)
			p.discoveryOverrides = map[string]any{"token_endpoint": tlsToken.URL + "/token"}
			sessions := &memorySessions{}
			logs := &syncBuffer{}
			a, err := New(context.Background(), Config{
				Issuer:       p.issuer(),
				ClientID:     testClientID,
				ClientSecret: testClientSecret,
				PublicURL:    testPublicURL,
				SecretKey:    []byte("server-secret-key"),
			}, sessions, slog.New(slog.NewTextHandler(logs, nil)))
			if err != nil {
				t.Fatal(err)
			}
			// Trust the test server's certificate; the redirect policy stays.
			a.client.Transport = tlsToken.Client().Transport
			r := chi.NewRouter()
			a.Mount(r)
			h := &harness{provider: p, sessions: sessions, router: r, logs: logs}
			l := h.login(t, LoginPath)

			resp := h.callback(l, l.query.Get("state"), "unused")

			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("callback status = %d, want 401", resp.StatusCode)
			}
			if n := tlsHits.Load(); n != 1 {
				t.Errorf("https token endpoint hit %d times, want 1", n)
			}
			if n := plaintextHits.Load(); n != 0 {
				t.Errorf("plaintext endpoint hit %d times, want 0", n)
			}
			if n := len(sessions.started()); n != 0 {
				t.Errorf("started %d sessions, want 0", n)
			}
			if got := logs.String(); !strings.Contains(got, "downgrade") {
				t.Errorf("logs = %q, want the downgrade refusal", got)
			}
		})
	}
}

func TestCheckRedirect(t *testing.T) {
	t.Parallel()
	request := func(raw string) *http.Request {
		req, err := http.NewRequest(http.MethodPost, raw, nil)
		if err != nil {
			t.Fatal(err)
		}
		return req
	}
	tests := []struct {
		from, to string
		ok       bool
	}{
		{"https://auth.example.com/token", "https://auth2.example.com/token", true},
		{"http://127.0.0.1:9000/token", "http://localhost:9001/token", true},
		{"http://127.0.0.1:9000/token", "https://auth.example.com/token", true},
		{"https://auth.example.com/token", "http://auth.example.com/token", false},
		{"https://127.0.0.1:9000/token", "http://127.0.0.1:9001/token", false},
		{"http://127.0.0.1:9000/token", "http://auth.example.com/token", false},
		{"https://auth.example.com/token", "ftp://auth.example.com/token", false},
	}
	for _, tc := range tests {
		err := checkRedirect(request(tc.to), []*http.Request{request(tc.from)})
		if (err == nil) != tc.ok {
			t.Errorf("redirect %s -> %s: error = %v, want ok=%v", tc.from, tc.to, err, tc.ok)
		}
	}
	via := make([]*http.Request, maxRedirects)
	for i := range via {
		via[i] = request("https://auth.example.com/token")
	}
	if err := checkRedirect(request("https://auth.example.com/token"), via); err == nil {
		t.Errorf("redirect after %d hops: error = nil, want limit", maxRedirects)
	}
}
