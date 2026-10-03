package server

import (
	"errors"
	"log/slog"
	"net/http"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/forwarded"
)

// resolveOrigin resolves every request's client address and scheme with
// forwarded.Resolve and puts them in the context (read them with
// forwarded.ClientIP and forwarded.Scheme). Forwarded headers are honoured
// only from a peer in trusted. A malformed forwarded header from a trusted
// peer gets 400 bad_request.
func resolveOrigin(logger *slog.Logger, trusted forwarded.Trusted) func(http.Handler) http.Handler {
	badRequest := apiErrorHandler(logger, http.StatusBadRequest, "bad_request", "invalid forwarded headers")
	internal := apiErrorHandler(logger, http.StatusInternalServerError, "internal", "internal error")
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			o, err := forwarded.Resolve(r, trusted)
			switch {
			case errors.Is(err, forwarded.ErrInvalidHeader):
				logger.WarnContext(r.Context(), "rejected forwarded headers", "peer", r.RemoteAddr, "error", err)
				badRequest(w, r)
			case err != nil:
				logger.ErrorContext(r.Context(), "resolve request origin", "error", err)
				internal(w, r)
			default:
				next.ServeHTTP(w, r.WithContext(forwarded.NewContext(r.Context(), o)))
			}
		})
	}
}
