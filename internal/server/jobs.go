package server

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/jobs"
)

// getJob serves GET /api/v1/jobs/{id}: the job's id, kind, status, latest
// result and error.
func getJob(logger *slog.Logger, runner *jobs.Runner) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		job, err := runner.Get(r.Context(), chi.URLParam(r, "id"))
		if errors.Is(err, jobs.ErrNotFound) {
			writeError(w, r, logger, http.StatusNotFound, "not_found", "job not found")
			return
		}
		if err != nil {
			logger.ErrorContext(r.Context(), "get job", "error", err)
			writeError(w, r, logger, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		writeJSON(w, r, logger, http.StatusOK, job)
	}
}
