package server

import (
	"crypto/subtle"
	"log/slog"
	"net/http"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/auth"
)

// CSRFHeader carries the session's CSRF token, which the client reads from
// GET /api/v1/me.
const CSRFHeader = "X-MealQuest-CSRF"

// requireCSRF passes GET, HEAD and OPTIONS requests to next; every other
// request must carry CSRFHeader equal to its session's CSRF token or get 403
// csrf. It runs behind requireSession, which puts the session in the context.
func requireCSRF(logger *slog.Logger) func(http.Handler) http.Handler {
	forbidden := apiErrorHandler(logger, http.StatusForbidden, "csrf", "missing or invalid CSRF token")
	internal := apiErrorHandler(logger, http.StatusInternalServerError, "internal", "internal error")
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions:
				next.ServeHTTP(w, r)
				return
			}
			s, ok := auth.SessionFrom(r.Context())
			if !ok || s.CSRFToken == "" {
				// An empty token would match a missing header.
				logger.ErrorContext(r.Context(), "csrf check without a session token", "session", ok)
				internal(w, r)
				return
			}
			if subtle.ConstantTimeCompare([]byte(r.Header.Get(CSRFHeader)), []byte(s.CSRFToken)) != 1 {
				forbidden(w, r)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
