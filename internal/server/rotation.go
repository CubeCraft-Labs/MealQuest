package server

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/plan"
)

// maxRotationReviewBody bounds a POST /dinners/{id}/rotation-review body.
const maxRotationReviewBody = 1 << 10

// rotationDecision is the POST /dinners/{id}/rotation-review body:
// keep, drop ("Not for us") or not_made ("We didn't make it").
type rotationDecision struct {
	Decision *string `json:"decision"`
}

type rotationReviewList struct {
	Reviews []plan.Review `json:"reviews"`
}

// getRotationReviews answers GET /rotation-reviews with the pending
// Rotation checks, oldest plan date first.
func getRotationReviews(logger *slog.Logger, svc *plan.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		list, err := svc.PendingReviews(r.Context())
		if err != nil {
			writeRotationError(w, r, logger, "list Rotation checks", err)
			return
		}
		writeJSON(w, r, logger, http.StatusOK, rotationReviewList{Reviews: list})
	}
}

// postRotationReview applies the household's answer to "Keep it in
// rotation?" for a tried dinner, recording the session's actor as
// updated_by, and answers with the Rotation check as decided.
func postRotationReview(logger *slog.Logger, svc *plan.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := actorOrFail(w, r, logger)
		if !ok {
			return
		}
		body, err := decodePatch[rotationDecision](http.MaxBytesReader(w, r.Body, maxRotationReviewBody))
		if err != nil {
			writeError(w, r, logger, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		if body.Decision == nil {
			writeError(w, r, logger, http.StatusBadRequest, "invalid_request", "decision: required")
			return
		}
		d, err := svc.DecideReview(r.Context(), chi.URLParam(r, "id"), *body.Decision, actor.Subject)
		if err != nil {
			writeRotationError(w, r, logger, "decide Rotation check", err)
			return
		}
		writeJSON(w, r, logger, http.StatusOK, d)
	}
}

// writeRotationError maps a Rotation check error to its API error: a
// decision other than keep, drop or not_made is 400, an unknown dinner or
// a dinner never checked 404, and a dinner whose checks are all decided
// (such as a second decision) or no longer tried 409.
func writeRotationError(w http.ResponseWriter, r *http.Request, logger *slog.Logger, action string, err error) {
	var invalid *plan.InvalidDecisionError
	switch {
	case errors.As(err, &invalid):
		writeError(w, r, logger, http.StatusBadRequest, "invalid_decision", invalid.Error())
	case errors.Is(err, plan.ErrDinnerNotFound):
		writeError(w, r, logger, http.StatusNotFound, "not_found", "dinner not found")
	case errors.Is(err, plan.ErrReviewNotFound):
		writeError(w, r, logger, http.StatusNotFound, "not_found", "rotation check not found")
	case errors.Is(err, plan.ErrNoPendingReview):
		writeError(w, r, logger, http.StatusConflict, "no_pending_review", err.Error())
	case errors.Is(err, plan.ErrDinnerNotTried):
		writeError(w, r, logger, http.StatusConflict, "dinner_not_tried", err.Error())
	default:
		logger.ErrorContext(r.Context(), action, "error", err)
		writeError(w, r, logger, http.StatusInternalServerError, "internal", "internal error")
	}
}
