package server

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/auth"
)

const (
	unauthenticatedJSON = `{"error":{"code":"unauthenticated","message":"sign in required","retryable":false}}`
	internalJSON        = `{"error":{"code":"internal","message":"internal error","retryable":false}}`
)

var (
	validSessionCookie  = &http.Cookie{Name: auth.SessionCookieName, Value: "valid"}
	brokenSessionCookie = &http.Cookie{Name: auth.SessionCookieName, Value: "broken"}
	testSession         = auth.Session{
		Subject:   "user-123",
		Email:     "cook@example.com",
		Name:      "Home Cook",
		Groups:    []string{"mealquest-admin"},
		CSRFToken: "csrf-token-123",
	}
)

// stubSessions knows one valid session cookie; the broken cookie fails the
// lookup as a database error would.
type stubSessions struct{}

func (stubSessions) SessionFor(r *http.Request) (auth.Session, error) {
	c, err := r.Cookie(auth.SessionCookieName)
	switch {
	case err != nil:
		return auth.Session{}, auth.ErrNoSession
	case c.Value == validSessionCookie.Value:
		return testSession, nil
	case c.Value == brokenSessionCookie.Value:
		return auth.Session{}, errors.New("database down")
	default:
		return auth.Session{}, auth.ErrNoSession
	}
}

func TestProtectedPathsRequireSession(t *testing.T) {
	t.Parallel()

	router := newTestRouter(t, "dev")
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
			if got := rec.Body.String(); got != unauthenticatedJSON {
				t.Errorf("body = %s, want %s", got, unauthenticatedJSON)
			}
		})
	}
}

func TestPublicPathsNeedNoSession(t *testing.T) {
	t.Parallel()

	router := newTestRouter(t, "dev")
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
	req.AddCookie(brokenSessionCookie)
	rec := httptest.NewRecorder()
	newTestRouter(t, "dev").ServeHTTP(rec, req)

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
	h := requireSession(slog.New(slog.NewTextHandler(io.Discard, nil)), stubSessions{})(next)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/settings/household", nil)
	req.AddCookie(validSessionCookie)
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
	h := requireSession(slog.New(slog.NewTextHandler(io.Discard, nil)), stubSessions{})(next)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/settings/household", nil))

	if called || rec.Code != http.StatusUnauthorized {
		t.Errorf("handler called = %v, status = %d; want not called and 401", called, rec.Code)
	}
}
