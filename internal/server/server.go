// Package server builds the MealQuest HTTP router.
package server

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/aisettings"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/forwarded"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/frontend"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/household"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/jobs"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/recipes"
)

// reservedPrefixes are owned by the API router and are never answered with the
// SPA's index.html.
var reservedPrefixes = []string{"/api", "/auth", "/media", "/health"}

// RouteMounter registers its own routes on the router.
type RouteMounter interface {
	Mount(r chi.Router)
}

// NewRouter returns the HTTP handler for the service. Every request first
// has its client address and scheme resolved, honouring forwarded headers
// only from a peer in trusted (see resolveOrigin). version is reported by
// GET /health; auth mounts the /auth routes; sessions guards /api/v1 and
// /media; settings serves /api/v1/settings/household; search serves
// /api/v1/settings/recipe-search and its /test; ai serves
// /api/v1/settings/ai, its /api-key, /verify, /models and /reset-prompt;
// runner serves
// POST /api/v1/jobs and GET /api/v1/jobs/{id}; photos serves
// GET /media/photos/{id}-{size}.jpg; assets is the web build
// served for every other GET. /health, /auth and the web build are public.
// Requests to /api/v1 other than GET also need the session's CSRF token in CSRFHeader.
func NewRouter(logger *slog.Logger, version string, trusted forwarded.Trusted, auth RouteMounter, sessions SessionLookup, settings *household.SettingsService, search *recipes.SearchSettingsService, ai *aisettings.Service, runner *jobs.Runner, photos PhotoFiles, assets fs.FS) (http.Handler, error) {
	if settings == nil {
		return nil, errors.New("server: nil household settings service")
	}
	if search == nil {
		return nil, errors.New("server: nil recipe search settings service")
	}
	if ai == nil {
		return nil, errors.New("server: nil AI settings service")
	}
	if runner == nil {
		return nil, errors.New("server: nil job runner")
	}
	if photos == nil {
		return nil, errors.New("server: nil photo files")
	}
	notFound := apiErrorHandler(logger, http.StatusNotFound, "not_found", "not found")
	methodNotAllowed := apiErrorHandler(logger, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")

	web, err := frontend.NewHandler(assets, logger, notFound, reservedPrefixes...)
	if err != nil {
		return nil, err
	}

	r := chi.NewRouter()
	r.Use(resolveOrigin(logger, trusted))
	r.MethodNotAllowed(methodNotAllowed)
	r.NotFound(notFound)

	r.Get("/health", health(logger, version))
	auth.Mount(r)
	// The session middleware wraps the whole /api/v1 and /media subrouters
	// rather than being added with Use: chi skips a subrouter's middleware
	// when it has no routes, which would leave it unguarded.
	signedIn := requireSession(logger, sessions)
	csrf := requireCSRF(logger)
	apiV1, media := chi.NewRouter(), chi.NewRouter()
	for _, sub := range []*chi.Mux{apiV1, media} {
		sub.MethodNotAllowed(methodNotAllowed)
		sub.NotFound(notFound)
	}
	apiV1.Get("/me", me(logger))
	apiV1.Get("/settings/household", getHouseholdSettings(logger, settings))
	apiV1.Patch("/settings/household", patchHouseholdSettings(logger, settings))
	apiV1.Get("/settings/recipe-search", getRecipeSearchSettings(logger, search))
	apiV1.Patch("/settings/recipe-search", patchRecipeSearchSettings(logger, search))
	apiV1.Post("/settings/recipe-search/test", postRecipeSearchTest(logger, search))
	apiV1.Get("/settings/ai", getAISettings(logger, ai))
	apiV1.Patch("/settings/ai", patchAISettings(logger, ai))
	apiV1.Put("/settings/ai/api-key", putAIKey(logger, ai))
	apiV1.Post("/settings/ai/verify", postAIVerify(logger, ai))
	apiV1.Get("/settings/ai/models", getAIModels(logger, ai))
	apiV1.Post("/settings/ai/reset-prompt", postAIResetPrompt(logger, ai))
	apiV1.Post("/jobs", postJob(logger, runner))
	apiV1.Get("/jobs/{id}", getJob(logger, runner))
	media.Get("/photos/{name}", getPhoto(logger, photos))
	r.Route("/api", func(r chi.Router) {
		r.MethodNotAllowed(methodNotAllowed)
		r.NotFound(notFound)
		r.Mount("/v1", signedIn(csrf(apiV1)))
	})
	r.Mount("/media", signedIn(media))
	// Catch-all for the PWA. Unknown /auth paths reach the frontend handler,
	// which passes them to notFound rather than the SPA fallback.
	r.Method(http.MethodGet, "/*", web)
	r.Method(http.MethodHead, "/*", web)
	return r, nil
}

type errorBody struct {
	Error apiError `json:"error"`
}

type apiError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

// apiErrorHandler responds with status and a fixed, non-retryable API error.
func apiErrorHandler(logger *slog.Logger, status int, code, message string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeError(w, r, logger, status, code, message)
	}
}

type healthResponse struct {
	Status  string `json:"status"`
	Version string `json:"version"`
}

func health(logger *slog.Logger, version string) http.HandlerFunc {
	body, err := json.Marshal(healthResponse{Status: "ok", Version: version})
	if err != nil {
		panic(err) // two string fields cannot fail to marshal
	}
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write(body); err != nil {
			logger.WarnContext(r.Context(), "write health response", "error", err)
		}
	}
}
