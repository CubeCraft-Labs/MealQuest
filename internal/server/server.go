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
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/chat"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/dinners"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/forwarded"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/frontend"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/gcal"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/household"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/jobs"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/plan"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/push"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/recipes"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/shopping"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/suggest"
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
// chatSvc serves GET and PUT /api/v1/settings/chat-webhook and its /test;
// dinnerSvc serves GET and POST /api/v1/dinners, PATCH and DELETE
// /api/v1/dinners/{id}, POST /api/v1/dinners/{id}/favorite, GET
// /api/v1/dinners/{id}/recipe-candidates and POST /api/v1/dinners/{id}/ground;
// suggestSvc serves POST /api/v1/suggestions/batches, GET
// /api/v1/suggestions/deck and POST /api/v1/suggestions/{id}/accept and
// /decline; planSvc serves GET /api/v1/plans/{weekStart}, POST
// /api/v1/plans/{weekStart}/draft, PUT
// /api/v1/plans/{weekStart}/days/{date}, GET /api/v1/rotation-reviews and
// POST /api/v1/dinners/{id}/rotation-review, and POST
// /api/v1/plans/{weekStart}/push-calendar with shopSvc, which makes the
// pushed week's Supply run; shopSvc serves GET /api/v1/shopping/archive,
// GET /api/v1/shopping/{weekStart},
// POST /api/v1/shopping/{weekStart}/generate and PATCH
// /api/v1/shopping/items/{id}; googleSvc serves GET
// /api/v1/integrations/google and its /connect, /callback and /calendars,
// PUT /api/v1/integrations/google/calendar and DELETE
// /api/v1/integrations/google; pushSvc serves GET
// /api/v1/push/vapid-public-key and POST and DELETE
// /api/v1/push/subscriptions; runner serves
// POST /api/v1/jobs and GET /api/v1/jobs/{id}; photos serves
// GET /media/photos/{id}-{size}.jpg; assets is the web build
// served for every other GET. /health, /auth and the web build are public.
// Requests to /api/v1 other than GET also need the session's CSRF token in CSRFHeader.
func NewRouter(logger *slog.Logger, version string, trusted forwarded.Trusted, auth RouteMounter, sessions SessionLookup, settings *household.SettingsService, search *recipes.SearchSettingsService, ai *aisettings.Service, chatSvc *chat.Service, dinnerSvc *dinners.Service, suggestSvc *suggest.Service, planSvc *plan.Service, shopSvc *shopping.Service, googleSvc *gcal.Service, pushSvc *push.Service, runner *jobs.Runner, photos PhotoFiles, assets fs.FS) (http.Handler, error) {
	if settings == nil {
		return nil, errors.New("server: nil household settings service")
	}
	if search == nil {
		return nil, errors.New("server: nil recipe search settings service")
	}
	if ai == nil {
		return nil, errors.New("server: nil AI settings service")
	}
	if chatSvc == nil {
		return nil, errors.New("server: nil Google Chat service")
	}
	if dinnerSvc == nil {
		return nil, errors.New("server: nil dinners service")
	}
	if suggestSvc == nil {
		return nil, errors.New("server: nil suggestions service")
	}
	if planSvc == nil {
		return nil, errors.New("server: nil plan service")
	}
	if shopSvc == nil {
		return nil, errors.New("server: nil shopping service")
	}
	if googleSvc == nil {
		return nil, errors.New("server: nil Google Calendar service")
	}
	if pushSvc == nil {
		return nil, errors.New("server: nil push service")
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
	apiV1.Get("/settings/chat-webhook", getChatWebhook(logger, chatSvc))
	apiV1.Put("/settings/chat-webhook", putChatWebhook(logger, chatSvc))
	apiV1.Post("/settings/chat-webhook/test", postChatWebhookTest(logger, chatSvc))
	apiV1.Get("/dinners", getDinners(logger, dinnerSvc))
	apiV1.Post("/dinners", postDinner(logger, dinnerSvc))
	apiV1.Patch("/dinners/{id}", patchDinner(logger, dinnerSvc))
	apiV1.Delete("/dinners/{id}", deleteDinner(logger, dinnerSvc))
	apiV1.Post("/dinners/{id}/favorite", postDinnerFavorite(logger, dinnerSvc))
	apiV1.Get("/dinners/{id}/recipe-candidates", getDinnerCandidates(logger, dinnerSvc))
	apiV1.Post("/dinners/{id}/ground", postDinnerGround(logger, dinnerSvc))
	apiV1.Post("/suggestions/batches", postSuggestionBatch(logger, suggestSvc))
	apiV1.Get("/suggestions/deck", getSuggestionDeck(logger, suggestSvc))
	apiV1.Post("/suggestions/{id}/accept", postSuggestionDecision(logger, suggestSvc, true))
	apiV1.Post("/suggestions/{id}/decline", postSuggestionDecision(logger, suggestSvc, false))
	apiV1.Get("/plans/{weekStart}", getPlan(logger, planSvc))
	apiV1.Post("/plans/{weekStart}/draft", postPlanDraft(logger, planSvc))
	apiV1.Put("/plans/{weekStart}/days/{date}", putPlanDay(logger, planSvc))
	apiV1.Post("/plans/{weekStart}/push-calendar", postPlanPush(logger, planSvc, shopSvc))
	apiV1.Get("/shopping/archive", getSupplyArchive(logger, shopSvc))
	apiV1.Get("/shopping/{weekStart}", getSupplyRun(logger, shopSvc))
	apiV1.Post("/shopping/{weekStart}/generate", postSupplyRunGenerate(logger, shopSvc))
	apiV1.Patch("/shopping/items/{id}", patchShoppingItem(logger, shopSvc))
	apiV1.Get("/rotation-reviews", getRotationReviews(logger, planSvc))
	apiV1.Post("/dinners/{id}/rotation-review", postRotationReview(logger, planSvc))
	// connect and callback are top-level browser navigations, so they are
	// GETs: the Lax session cookie rides both and CSRF applies to neither.
	// The callback's state is bound to the session that started connect.
	apiV1.Get("/integrations/google/connect", getGoogleConnect(logger, googleSvc))
	apiV1.Get("/integrations/google/callback", getGoogleCallback(logger, googleSvc))
	apiV1.Get("/integrations/google", getGoogle(logger, googleSvc))
	apiV1.Delete("/integrations/google", deleteGoogle(logger, googleSvc))
	apiV1.Get("/integrations/google/calendars", getGoogleCalendars(logger, googleSvc))
	apiV1.Put("/integrations/google/calendar", putGoogleCalendar(logger, googleSvc))
	apiV1.Get("/push/vapid-public-key", getVAPIDPublicKey(logger, pushSvc))
	apiV1.Post("/push/subscriptions", postPushSubscription(logger, pushSvc))
	apiV1.Delete("/push/subscriptions", deletePushSubscription(logger, pushSvc))
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
