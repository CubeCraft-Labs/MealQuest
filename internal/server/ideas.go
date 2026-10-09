package server

import (
	"errors"
	"log/slog"
	"net/http"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ideas"
)

// maxIdeasBody bounds a POST /ideas/from-ingredients body.
const maxIdeasBody = 16 << 10

// ideasFromIngredients is the POST /ideas/from-ingredients body. Both
// fields are required.
type ideasFromIngredients struct {
	Ingredients []string `json:"ingredients"`
	QuickOnly   *bool    `json:"quick_only"`
}

// postIdeasFromIngredients starts finding ideas from the ingredients on
// hand and answers 202 with the jobId that GET /jobs/{id} reports on,
// without waiting for the job. An ingredient list the household must
// correct is 400.
func postIdeasFromIngredients(logger *slog.Logger, svc *ideas.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := decodePatch[ideasFromIngredients](http.MaxBytesReader(w, r.Body, maxIdeasBody))
		if err != nil {
			writeError(w, r, logger, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		switch {
		case body.Ingredients == nil:
			writeError(w, r, logger, http.StatusBadRequest, "invalid_request", "ingredients: required")
			return
		case body.QuickOnly == nil:
			writeError(w, r, logger, http.StatusBadRequest, "invalid_request", "quick_only: required")
			return
		}
		started, err := svc.StartFromIngredients(r.Context(), body.Ingredients, *body.QuickOnly)
		var invalid *ideas.InvalidError
		switch {
		case errors.As(err, &invalid):
			writeError(w, r, logger, http.StatusBadRequest, "invalid_request", invalid.Message)
			return
		case err != nil:
			logger.ErrorContext(r.Context(), "start ideas from ingredients", "error", err)
			writeError(w, r, logger, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		writeJSON(w, r, logger, http.StatusAccepted, started)
	}
}
