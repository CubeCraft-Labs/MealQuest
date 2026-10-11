package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"testing/fstest"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/auth"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/forwarded"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/migrations"
)

// newDiscoveryProvider serves the OIDC discovery document auth.New needs;
// login only redirects to its authorization endpoint.
func newDiscoveryProvider(t *testing.T) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/openid-configuration" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                srv.URL,
			"authorization_endpoint":                srv.URL + "/authorize",
			"token_endpoint":                        srv.URL + "/token",
			"jwks_uri":                              srv.URL + "/jwks",
			"userinfo_endpoint":                     srv.URL + "/userinfo",
			"end_session_endpoint":                  srv.URL + "/end-session/",
			"response_types_supported":              []string{"code"},
			"subject_types_supported":               []string{"public"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// newLoginRouter wires the production router as cmd/server does: the real
// auth flow and session manager, trusting Traefik only.
func newLoginRouter(t *testing.T, issuer string) http.Handler {
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
	sessions, err := auth.NewSessionManager(db, 720*time.Hour, 4320*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(forwarded.LogHandler(slog.NewTextHandler(io.Discard, nil)))
	flow, err := auth.New(ctx, auth.Config{
		Issuer:        issuer,
		ClientID:      "mealquest",
		ClientSecret:  "client-s3cret",
		PublicURL:     &url.URL{Scheme: "https", Host: "meals.example.com"},
		SecretKey:     []byte("server-secret-key"),
		AllowedGroups: []string{"mealquest-admin"},
	}, sessions, logger)
	if err != nil {
		t.Fatal(err)
	}
	trusted, err := forwarded.ParseTrusted(traefik)
	if err != nil {
		t.Fatal(err)
	}
	r, err := NewRouter(logger, "dev", trusted, flow, sessions, newTestSettings(t),
		newTestSearch(t), newTestAI(t), newTestChat(t), newTestDinners(t), newTestSuggest(t), newTestPlan(t), newTestShopping(t), newTestGoogle(t), newTestPush(t), newTestIdeas(t), newTestHome(t), newTestRunner(t), newTestPhotos(t), fstest.MapFS{"index.html": {Data: []byte(fixtureIndex)}})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// TestLoginThroughRouter starts a login through the production router. The
// redirect URI and the Secure login cookie come from MEALQUEST_PUBLIC_URL
// whatever the direct peer: forwarded headers from an untrusted peer are
// ignored but never fail sign-in.
func TestLoginThroughRouter(t *testing.T) {
	t.Parallel()
	provider := newDiscoveryProvider(t)
	router := newLoginRouter(t, provider.URL)
	// Traefik reaches the service over plain http on its internal address;
	// the host headers must not leak into the redirect URI.
	headers := map[string]string{
		forwarded.HeaderFor:   "203.0.113.7",
		forwarded.HeaderProto: "https",
		"X-Forwarded-Host":    "evil.example.com",
	}
	tests := []struct {
		name string
		peer string
	}{
		{"trusted proxy", traefik + ":41000"},
		{"untrusted peer", "172.17.0.1:5000"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, originRequest("http://mealquest:8080"+auth.LoginPath, tt.peer, headers))
			resp := rec.Result()
			if resp.StatusCode != http.StatusFound {
				t.Fatalf("login status = %d, want 302", resp.StatusCode)
			}
			loc, err := url.Parse(resp.Header.Get("Location"))
			if err != nil {
				t.Fatal(err)
			}
			if got, want := loc.Scheme+"://"+loc.Host+loc.Path, provider.URL+"/authorize"; got != want {
				t.Errorf("redirect to %s, want %s", got, want)
			}
			if got, want := loc.Query().Get("redirect_uri"), "https://meals.example.com/auth/callback"; got != want {
				t.Errorf("redirect_uri = %q, want %q from the public URL", got, want)
			}
			cookies := resp.Cookies()
			if len(cookies) != 1 || cookies[0].Name != "__Host-mealquest_login" || !cookies[0].Secure || !cookies[0].HttpOnly {
				t.Errorf("login cookies = %v, want one Secure, HttpOnly __Host-mealquest_login", cookies)
			}
		})
	}
}
