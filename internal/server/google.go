package server

import (
	"errors"
	"log/slog"
	"net/http"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/auth"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/gcal"
)

// googleConnectedPath is where a completed connect sends the browser: the
// Google Calendar settings screen.
const googleConnectedPath = "/settings/calendar"

// getGoogleConnect sends the browser to Google's consent screen with a state
// bound to its session. The household opens it as a top-level navigation;
// the session cookie is SameSite=Lax, so it rides along.
func getGoogleConnect(logger *slog.Logger, svc *gcal.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		session, ok := auth.SessionFrom(r.Context())
		if !ok {
			logger.ErrorContext(r.Context(), "Google connect without a session")
			writeError(w, r, logger, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		consent, err := svc.Connect(w, session)
		if err != nil {
			logger.ErrorContext(r.Context(), "start Google connect", "error", err)
			writeError(w, r, logger, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		http.Redirect(w, r, consent, http.StatusFound)
	}
}

// getGoogleCallback completes the connect Google redirects back with, then
// sends the browser to the Google Calendar settings screen. Every failure is
// an API error; none holds a token or Google's response text.
func getGoogleCallback(logger *slog.Logger, svc *gcal.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		ctx := r.Context()
		session, ok := auth.SessionFrom(ctx)
		if !ok {
			logger.ErrorContext(ctx, "Google callback without a session")
			writeError(w, r, logger, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		err := svc.Complete(w, r, session)
		switch {
		case err == nil:
			http.Redirect(w, r, googleConnectedPath, http.StatusSeeOther)
		case errors.Is(err, gcal.ErrBadState):
			logger.WarnContext(ctx, "Google callback refused", "error", err)
			writeError(w, r, logger, http.StatusBadRequest, "invalid_state",
				"this Google Calendar connection expired or was not started here; connect again")
		case errors.Is(err, gcal.ErrDenied):
			logger.WarnContext(ctx, "Google returned an authorization error")
			writeError(w, r, logger, http.StatusForbidden, "google_denied",
				"Google did not grant access to your calendar; connect again to retry")
		case errors.Is(err, gcal.ErrNoCode):
			writeError(w, r, logger, http.StatusBadRequest, "invalid_request", "missing authorization code")
		case errors.Is(err, gcal.ErrNoRefreshToken):
			logger.WarnContext(ctx, "Google connect refused", "error", err)
			writeError(w, r, logger, http.StatusBadGateway, "reconnect_required",
				"Google did not grant lasting access to your calendar; connect Google Calendar again")
		case errors.Is(err, gcal.ErrScopes):
			logger.WarnContext(ctx, "Google connect refused", "error", err)
			writeError(w, r, logger, http.StatusForbidden, "scopes_missing",
				"allow MealQuest to see your calendars and manage their events, then connect Google Calendar again")
		default:
			logger.WarnContext(ctx, "Google connect failed", "error", err)
			writeError(w, r, logger, http.StatusBadGateway, "google_failed",
				"Google Calendar could not be connected; connect again")
		}
	}
}

// deleteGoogle revokes the connected account's access at Google and forgets
// it: 204 on success, 409 not_connected when nothing is connected, 502 when
// Google could not revoke (the connection is kept, so it can be retried).
func deleteGoogle(logger *slog.Logger, svc *gcal.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		err := svc.Disconnect(r.Context())
		var revoke *gcal.RevokeError
		switch {
		case err == nil:
			w.WriteHeader(http.StatusNoContent)
		case errors.Is(err, gcal.ErrNotConnected):
			writeError(w, r, logger, http.StatusConflict, "not_connected", "Google Calendar is not connected")
		case errors.As(err, &revoke):
			logger.WarnContext(r.Context(), "revoke Google access", "error", err)
			writeError(w, r, logger, http.StatusBadGateway, "revoke_failed",
				"Google could not revoke MealQuest's access; try again")
		default:
			logger.ErrorContext(r.Context(), "disconnect Google Calendar", "error", err)
			writeError(w, r, logger, http.StatusInternalServerError, "internal", "internal error")
		}
	}
}
