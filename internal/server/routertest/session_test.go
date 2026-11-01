package routertest

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/auth"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/server"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/server/srvtest"
)

const internalJSON = `{"error":{"code":"internal","message":"internal error","retryable":false}}`

func TestProtectedPathsRequireSession(t *testing.T) {
	t.Parallel()

	router := srvtest.NewTestRouter(t, "dev")
	for _, tc := range []struct {
		method, path string
		cookie       *http.Cookie
	}{
		{http.MethodGet, "/api/v1/settings/household", nil},
		{http.MethodPatch, "/api/v1/settings/household", nil},
		{http.MethodGet, "/api/v1", nil},
		{http.MethodGet, "/media/photo.jpg", nil},
		{http.MethodGet, "/api/v1/settings/household", &http.Cookie{Name: auth.SessionCookieName, Value: "unknown"}},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(tc.method, tc.path, nil)
			if tc.cookie != nil {
				req.AddCookie(tc.cookie)
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401", rec.Code)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", ct)
			}
			if got := rec.Body.String(); got != srvtest.UnauthenticatedJSON {
				t.Errorf("body = %s, want %s", got, srvtest.UnauthenticatedJSON)
			}
		})
	}
}

func TestPublicPathsNeedNoSession(t *testing.T) {
	t.Parallel()

	router := srvtest.NewTestRouter(t, "dev")
	for _, path := range []string{"/health", "/auth/login", "/", "/assets/index-AbC123.js", "/quests/42"} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
			if rec.Code != http.StatusOK {
				t.Errorf("GET %s without a cookie = %d, want 200", path, rec.Code)
			}
		})
	}
}

func TestSessionLookupFailure(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/settings/household", nil)
	req.AddCookie(srvtest.BrokenSessionCookie)
	rec := httptest.NewRecorder()
	srvtest.NewTestRouter(t, "dev").ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError || rec.Body.String() != internalJSON {
		t.Errorf("response = %d %s, want 500 %s", rec.Code, rec.Body.String(), internalJSON)
	}
}

func TestRequireSessionAttachesActor(t *testing.T) {
	t.Parallel()

	var got auth.Actor
	var ok bool
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok = auth.ActorFrom(r.Context())
		w.WriteHeader(http.StatusNoContent)
	})
	h := server.RequireSession(slog.New(slog.NewTextHandler(io.Discard, nil)), srvtest.StubSessions{})(next)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/settings/household", nil)
	req.AddCookie(srvtest.ValidSessionCookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want the handler's 204", rec.Code)
	}
	if !ok || got.Subject != "user-123" || got.Email != "cook@example.com" || got.Name != "Home Cook" ||
		!slices.Equal(got.Groups, []string{"mealquest-admin"}) {
		t.Errorf("actor = %+v (ok=%v), want the session's household member", got, ok)
	}
}

func TestRequireSessionStopsUnauthenticated(t *testing.T) {
	t.Parallel()

	called := false
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })
	h := server.RequireSession(slog.New(slog.NewTextHandler(io.Discard, nil)), srvtest.StubSessions{})(next)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/settings/household", nil))

	if called || rec.Code != http.StatusUnauthorized {
		t.Errorf("handler called = %v, status = %d; want not called and 401", called, rec.Code)
	}
}
