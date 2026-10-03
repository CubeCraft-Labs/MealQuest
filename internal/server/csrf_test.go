package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/auth"
)

const (
	csrfJSON             = `{"error":{"code":"csrf","message":"missing or invalid CSRF token","retryable":false}}`
	methodNotAllowedJSON = `{"error":{"code":"method_not_allowed","message":"method not allowed","retryable":false}}`
)

func TestCSRFThroughRouter(t *testing.T) {
	t.Parallel()

	router := newTestRouter(t, "dev")
	for _, tc := range []struct {
		name, method, path string
		header             []string // nil sends no CSRF header
		wantStatus         int
		wantBody           string
	}{
		{"POST without header", http.MethodPost, "/api/v1/me", nil, http.StatusForbidden, csrfJSON},
		{"POST with wrong header", http.MethodPost, "/api/v1/me", []string{"wrong-token"}, http.StatusForbidden, csrfJSON},
		{"POST with empty header", http.MethodPost, "/api/v1/me", []string{""}, http.StatusForbidden, csrfJSON},
		{"POST with token prefix", http.MethodPost, "/api/v1/me", []string{"csrf-token"}, http.StatusForbidden, csrfJSON},
		{"PATCH without header", http.MethodPatch, "/api/v1/settings/household", nil, http.StatusForbidden, csrfJSON},
		{"DELETE with wrong header", http.MethodDelete, "/api/v1/me", []string{"wrong-token"}, http.StatusForbidden, csrfJSON},
		// The right token passes the check and reaches the router, which has
		// no POST /api/v1/me, and the settings handler, which refuses the
		// empty body.
		{"POST with right header", http.MethodPost, "/api/v1/me", []string{testSession.CSRFToken}, http.StatusMethodNotAllowed, methodNotAllowedJSON},
		{"PATCH with right header", http.MethodPatch, "/api/v1/settings/household", []string{testSession.CSRFToken}, http.StatusBadRequest, `{"error":{"code":"invalid_request","message":"request body must be a JSON object","retryable":false}}`},
		{"GET without header", http.MethodGet, "/api/v1/me", nil, http.StatusOK, ""},
		// Only GET is exempt; HEAD and OPTIONS need the header too.
		{"HEAD without header", http.MethodHead, "/api/v1/me", nil, http.StatusForbidden, csrfJSON},
		{"OPTIONS without header", http.MethodOptions, "/api/v1/me", nil, http.StatusForbidden, csrfJSON},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(tc.method, tc.path, nil)
			req.AddCookie(validSessionCookie)
			if tc.header != nil {
				req.Header.Set(CSRFHeader, tc.header[0])
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d (body %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantBody != "" && rec.Body.String() != tc.wantBody {
				t.Errorf("body = %s, want %s", rec.Body.String(), tc.wantBody)
			}
		})
	}
}

func TestCSRFUnauthenticatedGets401(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/me", nil)
	rec := httptest.NewRecorder()
	newTestRouter(t, "dev").ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized || rec.Body.String() != unauthenticatedJSON {
		t.Errorf("response = %d %s, want 401 %s", rec.Code, rec.Body.String(), unauthenticatedJSON)
	}
}

func TestRequireCSRF(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, tc := range []struct {
		name       string
		method     string
		session    *auth.Session
		header     string
		wantStatus int
	}{
		{"right header reaches handler", http.MethodPost, &testSession, testSession.CSRFToken, http.StatusNoContent},
		{"wrong header", http.MethodPut, &testSession, "nope", http.StatusForbidden},
		{"GET needs no header", http.MethodGet, &testSession, "", http.StatusNoContent},
		{"HEAD needs the header", http.MethodHead, &testSession, "", http.StatusForbidden},
		{"OPTIONS needs the header", http.MethodOptions, &testSession, "", http.StatusForbidden},
		{"no session in context", http.MethodPost, nil, "", http.StatusInternalServerError},
		{"session without token", http.MethodPost, &auth.Session{Subject: "user-123"}, "", http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			called := false
			h := requireCSRF(logger)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				called = true
				w.WriteHeader(http.StatusNoContent)
			}))
			ctx := context.Background()
			if tc.session != nil {
				ctx = auth.WithSession(ctx, *tc.session)
			}
			req := httptest.NewRequestWithContext(ctx, tc.method, "/api/v1/anything", nil)
			if tc.header != "" {
				req.Header.Set(CSRFHeader, tc.header)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if called != (tc.wantStatus == http.StatusNoContent) {
				t.Errorf("handler called = %v with status %d", called, rec.Code)
			}
		})
	}
}

func TestMe(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	req.AddCookie(validSessionCookie)
	rec := httptest.NewRecorder()
	newTestRouter(t, "dev").ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	want := map[string]any{
		"name":      "Home Cook",
		"email":     "cook@example.com",
		"household": map[string]any{"label": "Our kitchen"},
		"csrfToken": "csrf-token-123",
	}
	gotJSON, _ := json.Marshal(got)
	wantJSON, _ := json.Marshal(want)
	if string(gotJSON) != string(wantJSON) {
		t.Errorf("body = %s, want %s", gotJSON, wantJSON)
	}
}

func TestMeNeedsSession(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	newTestRouter(t, "dev").ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/me", nil))
	if rec.Code != http.StatusUnauthorized || rec.Body.String() != unauthenticatedJSON {
		t.Errorf("response = %d %s, want 401 %s", rec.Code, rec.Body.String(), unauthenticatedJSON)
	}
}
