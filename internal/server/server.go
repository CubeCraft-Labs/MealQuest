// Package server builds the MealQuest HTTP router.
package server

import (
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/frontend"
)

// reservedPrefixes are owned by the API router and are never answered with the
// SPA's index.html.
var reservedPrefixes = []string{"/api", "/auth", "/media", "/health"}

// RouteMounter registers its own routes on the router.
type RouteMounter interface {
	Mount(r chi.Router)
}

// NewRouter returns the HTTP handler for the service. version is reported by
// GET /health; auth mounts the /auth routes; assets is the web build served
// for every other GET.
func NewRouter(logger *slog.Logger, version string, auth RouteMounter, assets fs.FS) (http.Handler, error) {
	notFound := apiErrorHandler(logger, http.StatusNotFound, "not_found", "not found")
	methodNotAllowed := apiErrorHandler(logger, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")

	web, err := frontend.NewHandler(assets, logger, notFound, reservedPrefixes...)
	if err != nil {
		return nil, err
	}

	r := chi.NewRouter()
	r.MethodNotAllowed(methodNotAllowed)
	r.NotFound(notFound)

	r.Get("/health", health(logger, version))
	auth.Mount(r)
	r.Route("/api", func(r chi.Router) {
		r.MethodNotAllowed(methodNotAllowed)
		r.NotFound(notFound)
	})
	// Catch-all for the PWA. Unknown /auth paths and /media (no routes yet)
	// reach the frontend handler, which passes them to notFound rather than
	// the SPA fallback.
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
	body, err := json.Marshal(errorBody{Error: apiError{
		Code:    code,
		Message: message,
	}})
	if err != nil {
		panic(err) // fixed string and bool fields cannot fail to marshal
	}
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if _, err := w.Write(body); err != nil {
			logger.WarnContext(r.Context(), "write error response", "code", code, "error", err)
		}
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
