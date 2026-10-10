package server

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/home"
)

// getHome answers GET /home with the Home screen at the household's local
// "now".
func getHome(logger *slog.Logger, svc *home.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h, err := svc.Get(r.Context())
		if err != nil {
			logger.ErrorContext(r.Context(), "read home", "error", err)
			writeError(w, r, logger, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		writeJSON(w, r, logger, http.StatusOK, h)
	}
}

// updatesBody is the GET /updates answer.
type updatesBody struct {
	Updates []home.Update `json:"updates"`
}

// getUpdates answers GET /updates with the Recent updates: the last
// home.RecentLimit distinct notification events, newest first.
func getUpdates(logger *slog.Logger, svc *home.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		updates, err := svc.Updates(r.Context())
		if err != nil {
			logger.ErrorContext(r.Context(), "read recent updates", "error", err)
			writeError(w, r, logger, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		writeJSON(w, r, logger, http.StatusOK, updatesBody{Updates: updates})
	}
}

// maxUpdatesSeenBody bounds a POST /updates/seen request body.
const maxUpdatesSeenBody = 1 << 10

// updatesSeenRequest is the POST /updates/seen body: through is the
// created_at of the newest update the household was shown.
type updatesSeenRequest struct {
	Through *string `json:"through"`
}

// postUpdatesSeen answers POST /updates/seen by recording that the
// household has seen its updates through the body's through, and answers
// 204. A through before the one already recorded changes nothing.
func postUpdatesSeen(logger *slog.Logger, svc *home.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		req, err := decodePatch[updatesSeenRequest](http.MaxBytesReader(w, r.Body, maxUpdatesSeenBody))
		if err != nil {
			writeError(w, r, logger, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		if req.Through == nil {
			writeError(w, r, logger, http.StatusBadRequest, "invalid_request", "through: required")
			return
		}
		through, err := time.Parse(time.RFC3339Nano, *req.Through)
		if err != nil {
			writeError(w, r, logger, http.StatusBadRequest, "invalid_request", "through: want an RFC 3339 timestamp")
			return
		}
		err = svc.MarkUpdatesSeen(r.Context(), through)
		if errors.Is(err, home.ErrSeenInFuture) {
			writeError(w, r, logger, http.StatusBadRequest, "invalid_request", "through: must not be after now")
			return
		}
		if err != nil {
			logger.ErrorContext(r.Context(), "mark recent updates seen", "error", err)
			writeError(w, r, logger, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
