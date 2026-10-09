package server

import (
	"errors"
	"log/slog"
	"net/http"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ideas"
)

// maxIdeasBody bounds a POST /ideas/from-ingredients or /ideas/quick body.
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

// ideasQuick is the POST /ideas/quick body. Both fields are required:
// exclude is [] on a first page.
type ideasQuick struct {
	Kind    *string  `json:"kind"`
	Exclude []string `json:"exclude"`
}

// postIdeasQuick starts finding a page of Quick dinner ideas of a kind,
// crockpot or under30, none named in exclude, and answers 202 with the
// jobId that GET /jobs/{id} reports on, without waiting for the job. A
// missing or unknown kind, or an exclude list the household must correct,
// is 400.
func postIdeasQuick(logger *slog.Logger, svc *ideas.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := decodePatch[ideasQuick](http.MaxBytesReader(w, r.Body, maxIdeasBody))
		if err != nil {
			writeError(w, r, logger, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		switch {
		case body.Kind == nil:
			writeError(w, r, logger, http.StatusBadRequest, "invalid_request", "kind: required")
			return
		case body.Exclude == nil:
			writeError(w, r, logger, http.StatusBadRequest, "invalid_request", "exclude: required")
			return
		}
		started, err := svc.StartQuick(r.Context(), *body.Kind, body.Exclude)
		var invalid *ideas.InvalidError
		switch {
		case errors.As(err, &invalid):
			writeError(w, r, logger, http.StatusBadRequest, "invalid_request", invalid.Message)
			return
		case err != nil:
			logger.ErrorContext(r.Context(), "start quick ideas", "error", err)
			writeError(w, r, logger, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		writeJSON(w, r, logger, http.StatusAccepted, started)
	}
}
