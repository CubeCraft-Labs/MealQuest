package server

import (
	"errors"
	"log/slog"
	"net/http"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/recipes"
)

// searchTestResponse is the POST /settings/recipe-search/test body.
type searchTestResponse struct {
	Query string `json:"query"`
	Count int    `json:"count"`
}

func getRecipeSearchSettings(logger *slog.Logger, svc *recipes.SearchSettingsService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		settings, err := svc.Get(r.Context())
		if err != nil {
			logger.ErrorContext(r.Context(), "get recipe search settings", "error", err)
			writeError(w, r, logger, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		writeJSON(w, r, logger, http.StatusOK, settings)
	}
}

// patchRecipeSearchSettings takes the Brave API key in api_key; the
// response, like GET's, never holds it.
func patchRecipeSearchSettings(logger *slog.Logger, svc *recipes.SearchSettingsService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		patch, err := decodePatch[recipes.SearchSettingsPatch](http.MaxBytesReader(w, r.Body, maxSettingsBody))
		if err != nil {
			writeError(w, r, logger, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		settings, err := svc.Update(r.Context(), patch)
		var invalid *recipes.SearchSettingsError
		if errors.As(err, &invalid) {
			writeError(w, r, logger, http.StatusBadRequest, "invalid_settings", invalid.Error())
			return
		}
		if err != nil {
			logger.ErrorContext(r.Context(), "update recipe search settings", "error", err)
			writeError(w, r, logger, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		writeJSON(w, r, logger, http.StatusOK, settings)
	}
}

// postRecipeSearchTest searches for recipes.SearchTestQuery with the saved
// settings. Recipe search not configured is 409; a search that fails is
// 502 with the provider's error, which never holds the API key.
func postRecipeSearchTest(logger *slog.Logger, svc *recipes.SearchSettingsService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		count, err := svc.TestSearch(r.Context())
		if errors.Is(err, recipes.ErrSearchNotConfigured) {
			writeError(w, r, logger, http.StatusConflict, "not_configured", "recipe search is not configured")
			return
		}
		if err != nil {
			logger.WarnContext(r.Context(), "recipe search test failed", "error", err)
			writeError(w, r, logger, http.StatusBadGateway, "search_failed", err.Error())
			return
		}
		writeJSON(w, r, logger, http.StatusOK, searchTestResponse{Query: recipes.SearchTestQuery, Count: count})
	}
}
