// Package auth signs the household in with Authentik over OpenID Connect.
// The service is a confidential client: the authorization code, tokens and
// client secret stay on the server and never reach JavaScript.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"sync/atomic"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-chi/chi/v5"
	"golang.org/x/oauth2"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/secureurl"
)

// Routes owned by this package.
const (
	LoginPath    = "/auth/login"
	CallbackPath = "/auth/callback"
	LogoutPath   = "/auth/logout"
)

// providerTimeout bounds every request to the identity provider.
const providerTimeout = 10 * time.Second

// scopes requested at login.
var scopes = []string{oidc.ScopeOpenID, "profile", "email", "groups", oidc.ScopeOfflineAccess}

// Claims are the verified ID token claims handed to the session layer.
type Claims struct {
	Subject           string   `json:"sub"`
	Email             string   `json:"email"`
	EmailVerified     bool     `json:"email_verified"`
	Name              string   `json:"name"`
	PreferredUsername string   `json:"preferred_username"`
	Groups            []string `json:"groups"`
}

// Sessions is the session layer. StartSession is called once per successful
// login with claims from a verified ID token; it must set the session cookie
// on w. EndSession is called on logout; it must end the session r carries,
// if any, and clear the cookie on w. A returned error fails the request with
// 500.
type Sessions interface {
	StartSession(w http.ResponseWriter, r *http.Request, claims Claims) error
	EndSession(w http.ResponseWriter, r *http.Request) error
}

// Config is what New needs to reach the identity provider.
type Config struct {
	// Issuer is the exact issuer URL; discovery runs against it.
	Issuer       string
	ClientID     string
	ClientSecret string
	// PublicURL is the service's public URL; its origin prefixes CallbackPath
	// to form the redirect URI.
	PublicURL *url.URL
	// SecretKey is the server key the login cookie's signing key derives from.
	SecretKey []byte
	// AllowedGroups are the household's groups; a sign-in is refused unless
	// the account is in at least one of them.
	AllowedGroups []string
}

// Auth serves the login and callback routes.
type Auth struct {
	oauth    oauth2.Config
	verifier *oidc.IDTokenVerifier
	provider *oidc.Provider
	client   *http.Client
	cookie   loginCookie
	sessions Sessions
	allowed  []string
	// endSessionURL is the provider's end_session_endpoint; logout ends
	// there so the provider's own session ends too.
	endSessionURL string
	logger        *slog.Logger
}

// New discovers the provider at cfg.Issuer and returns the login flow. It
// fails if discovery fails or the issuer the provider reports differs.
func New(ctx context.Context, cfg Config, sessions Sessions, logger *slog.Logger) (*Auth, error) {
	switch {
	case cfg.Issuer == "", cfg.ClientID == "", cfg.ClientSecret == "":
		return nil, errors.New("auth: issuer, client ID and client secret are required")
	case cfg.PublicURL == nil:
		return nil, errors.New("auth: public URL is required")
	case len(cfg.SecretKey) == 0:
		return nil, errors.New("auth: secret key is required")
	case sessions == nil:
		return nil, errors.New("auth: session layer is required")
	case len(cfg.AllowedGroups) == 0:
		return nil, errors.New("auth: at least one allowed group is required")
	}

	if err := checkEndpoint("issuer", cfg.Issuer); err != nil {
		return nil, err
	}

	client := &http.Client{Timeout: providerTimeout, CheckRedirect: checkRedirect}
	discovery := *client
	recorder := &statusRecorder{next: http.DefaultTransport}
	discovery.Transport = recorder
	provider, err := oidc.NewProvider(oidc.ClientContext(ctx, &discovery), cfg.Issuer)
	if err != nil {
		return nil, discoveryError(err, int(recorder.status.Load()))
	}
	endpoint := provider.Endpoint()
	var discovered struct {
		JWKSURI       string `json:"jwks_uri"`
		EndSessionURI string `json:"end_session_endpoint"`
	}
	if err := provider.Claims(&discovered); err != nil {
		return nil, fmt.Errorf("auth: OIDC discovery: %w", err)
	}
	// The token endpoint receives the client secret and the key set decides
	// which tokens verify, so every endpoint used must pass the same rule.
	for _, e := range []struct{ name, raw string }{
		{"authorization endpoint", endpoint.AuthURL},
		{"token endpoint", endpoint.TokenURL},
		{"JWKS endpoint", discovered.JWKSURI},
		{"end session endpoint", discovered.EndSessionURI},
		// Groups come from userinfo when the ID token carries none, and the
		// access token is sent there.
		{"userinfo endpoint", provider.UserInfoEndpoint()},
	} {
		if err := checkEndpoint(e.name, e.raw); err != nil {
			return nil, err
		}
	}
	// client_secret_basic, the OIDC default; no auto-detection retries.
	endpoint.AuthStyle = oauth2.AuthStyleInHeader

	return &Auth{
		oauth: oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			Endpoint:     endpoint,
			RedirectURL:  cfg.PublicURL.Scheme + "://" + cfg.PublicURL.Host + CallbackPath,
			Scopes:       scopes,
		},
		verifier: provider.Verifier(&oidc.Config{
			ClientID:             cfg.ClientID,
			SupportedSigningAlgs: []string{oidc.RS256},
		}),
		provider:      provider,
		client:        client,
		allowed:       slices.Clone(cfg.AllowedGroups),
		cookie:        newLoginCookie(cfg.SecretKey),
		sessions:      sessions,
		endSessionURL: discovered.EndSessionURI,
		logger:        logger,
	}, nil
}

// statusRecorder records the status of the last response it carries.
type statusRecorder struct {
	next   http.RoundTripper
	status atomic.Int32
}

func (s *statusRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := s.next.RoundTrip(req)
	if err == nil {
		s.status.Store(int32(resp.StatusCode))
	}
	return resp, err
}

// discoveryError reports a failed discovery. go-oidc puts the provider's
// response body in its error for a non-200 discovery response, so that
// error is replaced by the status alone. Transport errors carry no response
// content and keep their cause.
func discoveryError(err error, status int) error {
	var urlErr *url.Error
	if !errors.As(err, &urlErr) && status != 0 && status != http.StatusOK {
		return fmt.Errorf("auth: OIDC discovery: provider answered HTTP %d", status)
	}
	return fmt.Errorf("auth: OIDC discovery: %w", err)
}

// checkEndpoint applies secureurl.Check to the provider URL raw, naming the
// endpoint in the error.
func checkEndpoint(name, raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("auth: OIDC %s %q is not a valid URL", name, raw)
	}
	if err := secureurl.Check(u); err != nil {
		return fmt.Errorf("auth: OIDC %s %q: %w", name, raw, err)
	}
	return nil
}

// maxRedirects matches net/http's default redirect limit.
const maxRedirects = 10

// checkRedirect is the provider client's redirect policy. On a 307 or 308 to
// the same host Go resends the body and Authorization header, which carry the
// code and client secret, so every target must pass secureurl.Check and an
// https:// request never follows a redirect to plain http://, even loopback.
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return fmt.Errorf("stopped after %d redirects", maxRedirects)
	}
	if err := secureurl.Check(req.URL); err != nil {
		return fmt.Errorf("redirect refused: %w", err)
	}
	if via[len(via)-1].URL.Scheme == "https" && req.URL.Scheme != "https" {
		return errors.New("redirect refused: https:// to plain http:// downgrade")
	}
	return nil
}

// Mount registers GET LoginPath, GET CallbackPath and POST LogoutPath on r.
func (a *Auth) Mount(r chi.Router) {
	r.Get(LoginPath, a.login)
	r.Get(CallbackPath, a.callback)
	r.Post(LogoutPath, a.logout)
}

// EndSessionURL is the provider's end_session_endpoint.
func (a *Auth) EndSessionURL() string { return a.endSessionURL }

// login starts the authorization code flow with PKCE (S256). The optional
// "return" query parameter must be a same-origin path; anything else fails
// the login with 400.
func (a *Auth) login(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")

	returnTo, err := returnPath(r.URL.Query().Get("return"))
	if err != nil {
		http.Error(w, "invalid return path", http.StatusBadRequest)
		return
	}
	state := loginState{
		State:    rand.Text(),
		Nonce:    rand.Text(),
		Verifier: oauth2.GenerateVerifier(),
		Return:   returnTo,
	}
	if err := a.cookie.set(w, state, time.Now()); err != nil {
		a.logger.ErrorContext(r.Context(), "set login cookie", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, a.oauth.AuthCodeURL(state.State,
		oidc.Nonce(state.Nonce),
		oauth2.S256ChallengeOption(state.Verifier),
	), http.StatusFound)
}

// callback completes the flow: it checks state against the login cookie,
// exchanges the code, verifies the ID token and nonce, then starts a session
// if the account is in an allowed group. Membership is checked on every
// login, so an account removed from the group is refused at its next one.
func (a *Auth) callback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	ctx := r.Context()
	// The login cookie is single-use.
	a.cookie.clear(w)

	state, err := a.cookie.read(r, time.Now())
	if err != nil {
		a.logger.WarnContext(ctx, "login callback without a valid login cookie", "error", err)
		http.Error(w, "sign-in expired; start again", http.StatusBadRequest)
		return
	}
	q := r.URL.Query()
	if subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(state.State)) != 1 {
		a.logger.WarnContext(ctx, "login callback state mismatch")
		http.Error(w, "sign-in state mismatch; start again", http.StatusBadRequest)
		return
	}
	if q.Get("error") != "" {
		// error and error_description are provider-supplied text; only the
		// fixed category is logged.
		a.logger.WarnContext(ctx, "identity provider returned an authorization error")
		http.Error(w, "sign-in was refused", http.StatusUnauthorized)
		return
	}
	code := q.Get("code")
	if code == "" {
		http.Error(w, "missing authorization code", http.StatusBadRequest)
		return
	}

	claims, err := a.verify(ctx, code, state)
	if err != nil {
		a.logger.WarnContext(ctx, "sign-in failed", "error", err)
		http.Error(w, "sign-in failed", http.StatusUnauthorized)
		return
	}
	if !a.member(claims.Groups) {
		if claims.Groups == nil {
			a.logger.WarnContext(ctx, "sign-in refused: no groups claim in the ID token or userinfo", "subject", claims.Subject)
		} else {
			a.logger.WarnContext(ctx, "sign-in refused: not in an allowed group", "subject", claims.Subject)
		}
		// A session this browser already holds ends too, so a refused
		// sign-in never leaves it signed in.
		if err := a.sessions.EndSession(w, r); err != nil {
			a.logger.ErrorContext(ctx, "end session", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		a.refuse(w, r)
		return
	}
	if err := a.sessions.StartSession(w, r, claims); err != nil {
		a.logger.ErrorContext(ctx, "start session", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, state.Return, http.StatusSeeOther)
}

// logout ends the session, clears the cookie and sends the browser to the
// provider's end_session_endpoint to end the provider session as well.
func (a *Auth) logout(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if err := a.sessions.EndSession(w, r); err != nil {
		a.logger.ErrorContext(r.Context(), "end session", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, a.endSessionURL, http.StatusSeeOther)
}

// verify exchanges code for tokens and returns the verified ID token claims.
// The verifier enforces RS256, issuer, audience and expiry; nonce is checked
// here. When the ID token has no groups claim, Groups comes from userinfo;
// it stays nil when neither has one.
func (a *Auth) verify(ctx context.Context, code string, state loginState) (Claims, error) {
	token, err := a.oauth.Exchange(context.WithValue(ctx, oauth2.HTTPClient, a.client), code,
		oauth2.VerifierOption(state.Verifier))
	if err != nil {
		return Claims{}, exchangeError(err)
	}
	raw, ok := token.Extra("id_token").(string)
	if !ok || raw == "" {
		return Claims{}, errors.New("token response has no id_token")
	}
	idToken, err := a.verifier.Verify(ctx, raw)
	if err != nil {
		return Claims{}, verifyError(err)
	}
	if subtle.ConstantTimeCompare([]byte(idToken.Nonce), []byte(state.Nonce)) != 1 {
		return Claims{}, errors.New("ID token nonce mismatch")
	}
	var claims Claims
	if err := idToken.Claims(&claims); err != nil {
		return Claims{}, fmt.Errorf("decode ID token claims: %w", err)
	}
	if claims.Subject == "" {
		return Claims{}, errors.New("ID token has no subject")
	}
	if claims.Groups == nil {
		groups, err := a.userinfoGroups(ctx, token, claims.Subject)
		if err != nil {
			return Claims{}, err
		}
		claims.Groups = groups
	}
	return claims, nil
}

// userinfoGroups returns the groups claim from the userinfo endpoint, nil if
// it has none. The userinfo subject must match the ID token's subject.
func (a *Auth) userinfoGroups(ctx context.Context, token *oauth2.Token, subject string) ([]string, error) {
	info, err := a.provider.UserInfo(oidc.ClientContext(ctx, a.client), oauth2.StaticTokenSource(token))
	if err != nil {
		return nil, userinfoError(err)
	}
	if info.Subject != subject {
		return nil, errors.New("userinfo subject does not match the ID token")
	}
	var claims struct {
		Groups []string `json:"groups"`
	}
	if err := info.Claims(&claims); err != nil {
		return nil, fmt.Errorf("decode userinfo claims: %w", err)
	}
	return claims.Groups, nil
}

// userinfoError reduces a failed userinfo request to a fixed category, like
// exchangeError: go-oidc's error text can carry the response body.
func userinfoError(err error) error {
	var transport *url.Error
	if errors.As(err, &transport) {
		return fmt.Errorf("userinfo: request failed: %s: %w", transport.Op, transport.Err)
	}
	return errors.New("userinfo: endpoint returned an unusable response")
}

// verifyError reduces a failed ID token verification to a fixed category.
// go-oidc's error text can carry the JWKS endpoint's response body or
// token-supplied values, so only the expiry of an expired token is kept.
func verifyError(err error) error {
	var expired *oidc.TokenExpiredError
	if errors.As(err, &expired) {
		return fmt.Errorf("verify ID token: token expired at %s", expired.Expiry.UTC().Format(time.RFC3339))
	}
	return errors.New("verify ID token: token or provider key set rejected")
}

// member reports whether groups contains an allowed group.
func (a *Auth) member(groups []string) bool {
	return slices.ContainsFunc(groups, func(g string) bool { return slices.Contains(a.allowed, g) })
}

// exchangeError reduces a failed code exchange to a fixed category and safe
// metadata. The oauth2 error text can carry the token endpoint's response
// body or error_description, which may echo credentials, so it is dropped.
func exchangeError(err error) error {
	var retrieve *oauth2.RetrieveError
	var transport *url.Error
	switch {
	case errors.As(err, &retrieve):
		return fmt.Errorf("exchange code: token endpoint rejected the request (HTTP %d)", retrieve.Response.StatusCode)
	case errors.As(err, &transport):
		// Request failures never carry the response. The URL is dropped: after
		// a refused redirect it is the provider-supplied Location.
		return fmt.Errorf("exchange code: token request failed: %s: %w", transport.Op, transport.Err)
	default:
		return errors.New("exchange code: token endpoint returned an unusable response")
	}
}
