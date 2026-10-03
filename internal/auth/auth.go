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
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-chi/chi/v5"
	"golang.org/x/oauth2"
)

// Routes owned by this package.
const (
	LoginPath    = "/auth/login"
	CallbackPath = "/auth/callback"
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

// SessionStarter is the session layer's entry point. StartSession is called
// once per successful login with claims from a verified ID token; it must set
// the session cookie on w. A returned error aborts the login with 500.
type SessionStarter interface {
	StartSession(w http.ResponseWriter, r *http.Request, claims Claims) error
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
}

// Auth serves the login and callback routes.
type Auth struct {
	oauth    oauth2.Config
	verifier *oidc.IDTokenVerifier
	client   *http.Client
	cookie   loginCookie
	sessions SessionStarter
	logger   *slog.Logger
}

// New discovers the provider at cfg.Issuer and returns the login flow. It
// fails if discovery fails or the issuer the provider reports differs.
func New(ctx context.Context, cfg Config, sessions SessionStarter, logger *slog.Logger) (*Auth, error) {
	switch {
	case cfg.Issuer == "", cfg.ClientID == "", cfg.ClientSecret == "":
		return nil, errors.New("auth: issuer, client ID and client secret are required")
	case cfg.PublicURL == nil:
		return nil, errors.New("auth: public URL is required")
	case len(cfg.SecretKey) == 0:
		return nil, errors.New("auth: secret key is required")
	case sessions == nil:
		return nil, errors.New("auth: session starter is required")
	}

	client := &http.Client{Timeout: providerTimeout}
	provider, err := oidc.NewProvider(oidc.ClientContext(ctx, client), cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("auth: OIDC discovery: %w", err)
	}
	endpoint := provider.Endpoint()
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
		client:   client,
		cookie:   newLoginCookie(cfg.SecretKey),
		sessions: sessions,
		logger:   logger,
	}, nil
}

// Mount registers GET LoginPath and GET CallbackPath on r.
func (a *Auth) Mount(r chi.Router) {
	r.Get(LoginPath, a.login)
	r.Get(CallbackPath, a.callback)
}

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
// exchanges the code, verifies the ID token and nonce, then starts a session.
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
	if e := q.Get("error"); e != "" {
		a.logger.WarnContext(ctx, "identity provider refused sign-in", "error", e, "description", q.Get("error_description"))
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
	if err := a.sessions.StartSession(w, r, claims); err != nil {
		a.logger.ErrorContext(ctx, "start session", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, state.Return, http.StatusSeeOther)
}

// verify exchanges code for tokens and returns the verified ID token claims.
// The verifier enforces RS256, issuer, audience and expiry; nonce is checked
// here.
func (a *Auth) verify(ctx context.Context, code string, state loginState) (Claims, error) {
	token, err := a.oauth.Exchange(context.WithValue(ctx, oauth2.HTTPClient, a.client), code,
		oauth2.VerifierOption(state.Verifier))
	if err != nil {
		return Claims{}, fmt.Errorf("exchange code: %w", err)
	}
	raw, ok := token.Extra("id_token").(string)
	if !ok || raw == "" {
		return Claims{}, errors.New("token response has no id_token")
	}
	idToken, err := a.verifier.Verify(ctx, raw)
	if err != nil {
		return Claims{}, fmt.Errorf("verify ID token: %w", err)
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
	return claims, nil
}
