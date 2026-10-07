package server

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/auth"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/dinners"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/recipes"
)

// maxDinnerBody bounds a POST or PATCH /dinners request body.
const maxDinnerBody = 16 << 10

// maxGroundBody bounds a POST /dinners/{id}/ground body, which may carry a
// manual recipe of dinners.MaxManualLines lines of each kind.
const maxGroundBody = 512 << 10

// dinnerCreate is the POST /dinners body: a name, with or without a
// recipe_url.
type dinnerCreate struct {
	Name      *string `json:"name"`
	RecipeURL *string `json:"recipe_url"`
}

// dinnerPatch is the PATCH /dinners/{id} body. Every field is optional;
// unknown fields and null values are refused.
type dinnerPatch struct {
	Name         *string `json:"name"`
	Method       *string `json:"method"`
	MinutesTotal *int    `json:"minutes_total"`
	Difficulty   *string `json:"difficulty"`
}

type dinnerList struct {
	Dinners []dinners.Dinner `json:"dinners"`
}

// dinnerGround is the POST /dinners/{id}/ground body: exactly one of
// candidate_id, recipe_url and manual.
type dinnerGround struct {
	CandidateID *string             `json:"candidate_id"`
	RecipeURL   *string             `json:"recipe_url"`
	Manual      *dinnerManualRecipe `json:"manual"`
}

type dinnerManualRecipe struct {
	Ingredients []string `json:"ingredients"`
	Steps       []string `json:"steps"`
	Minutes     *int     `json:"minutes"`
	Method      *string  `json:"method"`
}

type candidateList struct {
	Candidates []dinners.Candidate `json:"candidates"`
}

// getDinners answers GET /dinners?status=regular|quest_log.
func getDinners(logger *slog.Logger, svc *dinners.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		list, err := svc.List(r.Context(), r.URL.Query().Get("status"))
		if err != nil {
			writeDinnerError(w, r, logger, "list dinners", err)
			return
		}
		writeJSON(w, r, logger, http.StatusOK, dinnerList{Dinners: list})
	}
}

// postDinner adds a Regular, recording the session's actor as updated_by,
// and answers 201 with it and its grounding_job_id without waiting for the
// job. When the job cannot be enqueued it still answers 201, with the dinner
// grounding_status failed and grounding_job_id null.
func postDinner(logger *slog.Logger, svc *dinners.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := actorOrFail(w, r, logger)
		if !ok {
			return
		}
		body, err := decodePatch[dinnerCreate](http.MaxBytesReader(w, r.Body, maxDinnerBody))
		if err != nil {
			writeError(w, r, logger, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		d, err := svc.CreateRegular(r.Context(), dinners.NewRegular(body), actor.Subject)
		if err != nil {
			writeDinnerError(w, r, logger, "create dinner", err)
			return
		}
		writeJSON(w, r, logger, http.StatusCreated, d)
	}
}

// patchDinner edits a dinner, recording the session's actor as updated_by.
func patchDinner(logger *slog.Logger, svc *dinners.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := actorOrFail(w, r, logger)
		if !ok {
			return
		}
		patch, err := decodePatch[dinnerPatch](http.MaxBytesReader(w, r.Body, maxDinnerBody))
		if err != nil {
			writeError(w, r, logger, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		d, err := svc.Update(r.Context(), chi.URLParam(r, "id"), dinners.Patch(patch), actor.Subject)
		if err != nil {
			writeDinnerError(w, r, logger, "update dinner", err)
			return
		}
		writeJSON(w, r, logger, http.StatusOK, d)
	}
}

// getDinnerCandidates answers GET /dinners/{id}/recipe-candidates with up
// to dinners.MaxCandidates recipe pages for the dinner's name.
func getDinnerCandidates(logger *slog.Logger, svc *dinners.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		list, err := svc.Candidates(r.Context(), chi.URLParam(r, "id"))
		if err != nil {
			writeDinnerError(w, r, logger, "list recipe candidates", err)
			return
		}
		writeJSON(w, r, logger, http.StatusOK, candidateList{Candidates: list})
	}
}

// postDinnerGround grounds a dinner by hand in a candidate, a pasted link
// or a recipe typed in, recording the session's actor as updated_by.
func postDinnerGround(logger *slog.Logger, svc *dinners.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := actorOrFail(w, r, logger)
		if !ok {
			return
		}
		body, err := decodePatch[dinnerGround](http.MaxBytesReader(w, r.Body, maxGroundBody))
		if err != nil {
			writeError(w, r, logger, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		fix := dinners.Fix{CandidateID: body.CandidateID, RecipeURL: body.RecipeURL}
		if body.Manual != nil {
			m := dinners.ManualRecipe(*body.Manual)
			fix.Manual = &m
		}
		d, err := svc.Ground(r.Context(), chi.URLParam(r, "id"), fix, actor.Subject)
		if err != nil {
			writeDinnerError(w, r, logger, "ground dinner", err)
			return
		}
		writeJSON(w, r, logger, http.StatusOK, d)
	}
}

// deleteDinner archives a dinner; the row is never deleted.
func deleteDinner(logger *slog.Logger, svc *dinners.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := actorOrFail(w, r, logger)
		if !ok {
			return
		}
		d, err := svc.Archive(r.Context(), chi.URLParam(r, "id"), actor.Subject)
		if err != nil {
			writeDinnerError(w, r, logger, "archive dinner", err)
			return
		}
		writeJSON(w, r, logger, http.StatusOK, d)
	}
}

// postDinnerFavorite toggles a dinner's is_favorite.
func postDinnerFavorite(logger *slog.Logger, svc *dinners.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := actorOrFail(w, r, logger)
		if !ok {
			return
		}
		d, err := svc.ToggleFavorite(r.Context(), chi.URLParam(r, "id"), actor.Subject)
		if err != nil {
			writeDinnerError(w, r, logger, "toggle dinner favorite", err)
			return
		}
		writeJSON(w, r, logger, http.StatusOK, d)
	}
}

// actorOrFail returns the session's actor, or answers 500 when the request
// has none, which behind requireSession is a wiring bug.
func actorOrFail(w http.ResponseWriter, r *http.Request, logger *slog.Logger) (auth.Actor, bool) {
	actor, ok := auth.ActorFrom(r.Context())
	if !ok {
		logger.ErrorContext(r.Context(), r.Method+" "+r.URL.Path+" without a session")
		writeError(w, r, logger, http.StatusInternalServerError, "internal", "internal error")
	}
	return actor, ok
}

// writeDinnerError maps a dinners.Service error to its API error. A page
// that cannot ground the dinner is 422 ungroundable; recipe search not set
// up is 409 not_configured; a search or model failure is 502; grounding by
// hand that runs past dinners.GroundTimeout is 504 ground_timeout; grounding
// an archived dinner is 409 dinner_archived and one changed while grounding
// 409 ground_conflict. Any other grounding failure, by candidate, link or
// manual recipe, wraps dinners.ErrGroundFailed and is 500, logged by reason
// and dinner id only.
func writeDinnerError(w http.ResponseWriter, r *http.Request, logger *slog.Logger, action string, err error) {
	var invalid *dinners.ValidationError
	var dup *dinners.DuplicateError
	var ungroundable *recipes.ErrUngroundable
	switch {
	case errors.As(err, &ungroundable):
		// The reasons quote dependency errors, so they are neither returned
		// nor logged.
		writeError(w, r, logger, http.StatusUnprocessableEntity, "ungroundable", "no usable recipe was found at that link")
	case errors.Is(err, recipes.ErrSearchNotConfigured):
		writeError(w, r, logger, http.StatusConflict, "not_configured", "recipe search is not configured")
	case errors.Is(err, recipes.ErrSearchFailed):
		// The error may quote the search provider's response, so only the
		// reason is logged.
		logger.WarnContext(r.Context(), action, "reason", "search_failed")
		writeError(w, r, logger, http.StatusBadGateway, "search_failed", "recipe search failed")
	case errors.Is(err, recipes.ErrNormalizeFailed):
		logger.WarnContext(r.Context(), action, "reason", "normalize_recipe failed")
		writeError(w, r, logger, http.StatusBadGateway, "ai_failed", "the AI provider failed to read the recipe")
	case errors.Is(err, dinners.ErrGroundFailed):
		// The error may quote the pasted link, which can carry a personal
		// token, and dependency errors, so only the reason and dinner are
		// logged.
		logger.ErrorContext(r.Context(), action, "reason", "ground_failed", "dinner_id", chi.URLParam(r, "id"))
		writeError(w, r, logger, http.StatusInternalServerError, "internal", "internal error")
	case errors.Is(err, dinners.ErrArchived):
		writeError(w, r, logger, http.StatusConflict, "dinner_archived", "the dinner is archived")
	case errors.Is(err, dinners.ErrGroundConflict):
		writeError(w, r, logger, http.StatusConflict, "ground_conflict", "the dinner changed while its recipe was grounding; reload and try again")
	case errors.Is(err, dinners.ErrGroundTimeout):
		logger.WarnContext(r.Context(), action, "reason", "ground_timeout")
		writeError(w, r, logger, http.StatusGatewayTimeout, "ground_timeout", "grounding the recipe took too long; try again")
	case errors.As(err, &invalid):
		writeError(w, r, logger, http.StatusBadRequest, "invalid_dinner", invalid.Error())
	case errors.As(err, &dup):
		writeError(w, r, logger, http.StatusConflict, "duplicate_dinner", dup.Error())
	case errors.Is(err, dinners.ErrNotFound):
		writeError(w, r, logger, http.StatusNotFound, "not_found", "dinner not found")
	default:
		logger.ErrorContext(r.Context(), action, "error", err)
		writeError(w, r, logger, http.StatusInternalServerError, "internal", "internal error")
	}
}
