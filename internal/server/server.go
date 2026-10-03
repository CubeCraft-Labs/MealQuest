// Package server builds the MealQuest HTTP router.
package server

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
)

// NewRouter returns the HTTP handler for the service. version is reported by
// GET /health.
func NewRouter(logger *slog.Logger, version string) http.Handler {
	r := chi.NewRouter()
	r.Get("/health", health(logger, version))
	r.MethodNotAllowed(apiErrorHandler(logger, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed"))
	r.NotFound(apiErrorHandler(logger, http.StatusNotFound, "not_found", "not found"))
	return r
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
