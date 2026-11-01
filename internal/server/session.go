package server

import (
	"errors"
	"log/slog"
	"net/http"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/auth"
)

// SessionLookup finds the live session a request carries; it returns
// auth.ErrNoSession when there is none. *auth.SessionManager implements it.
type SessionLookup interface {
	SessionFor(r *http.Request) (auth.Session, error)
}

// RequireSession is the session middleware. It passes requests with a
// live session to next with the session in the context (read it with
// auth.SessionFrom or auth.ActorFrom); others get 401 unauthenticated.
func RequireSession(logger *slog.Logger, sessions SessionLookup) func(http.Handler) http.Handler {
	unauthenticated := apiErrorHandler(logger, http.StatusUnauthorized, "unauthenticated", "sign in required")
	internal := apiErrorHandler(logger, http.StatusInternalServerError, "internal", "internal error")
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			s, err := sessions.SessionFor(r)
			switch {
			case errors.Is(err, auth.ErrNoSession):
				unauthenticated(w, r)
			case err != nil:
				logger.ErrorContext(r.Context(), "look up session", "error", err)
				internal(w, r)
			default:
				next.ServeHTTP(w, r.WithContext(auth.WithSession(r.Context(), s)))
			}
		})
	}
}
