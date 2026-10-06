package server

import (
	"errors"
	"log/slog"
	"net/http"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/aisettings"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/llm"
)

// apiKeyBody is the PUT /settings/ai/api-key body. An empty api_key removes
// the stored key.
type apiKeyBody struct {
	APIKey *string `json:"api_key"`
}

// modelsResponse is the GET /settings/ai/models body.
type modelsResponse struct {
	Models []aisettings.Model `json:"models"`
}

// No response from these handlers holds the API key: the settings carry
// only its last four characters, and provider errors are redacted by
// package aisettings.

func getAISettings(logger *slog.Logger, svc *aisettings.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		settings, err := svc.Get(r.Context())
		if err != nil {
			logger.ErrorContext(r.Context(), "get AI settings", "error", err)
			writeError(w, r, logger, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		writeJSON(w, r, logger, http.StatusOK, settings)
	}
}

func patchAISettings(logger *slog.Logger, svc *aisettings.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		patch, err := decodePatch[aisettings.Patch](http.MaxBytesReader(w, r.Body, maxSettingsBody))
		if err != nil {
			writeError(w, r, logger, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		writeAISettings(w, r, logger, "update AI settings")(svc.Update(r.Context(), patch))
	}
}

// putAIKey takes the API key in api_key; the response, like GET's, holds
// only its last four characters.
func putAIKey(logger *slog.Logger, svc *aisettings.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := decodePatch[apiKeyBody](http.MaxBytesReader(w, r.Body, maxSettingsBody))
		if err == nil && body.APIKey == nil {
			err = errors.New("api_key: is required")
		}
		if err != nil {
			writeError(w, r, logger, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		writeAISettings(w, r, logger, "store AI API key")(svc.SetAPIKey(r.Context(), *body.APIKey))
	}
}

func postAIResetPrompt(logger *slog.Logger, svc *aisettings.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeAISettings(w, r, logger, "reset AI system prompt")(svc.ResetPrompt(r.Context()))
	}
}

// postAIVerify checks the stored key with the provider and returns the
// settings with key_verified_at, or the failure (see writeAIFailure).
func postAIVerify(logger *slog.Logger, svc *aisettings.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		settings, err := svc.Verify(r.Context())
		if err != nil {
			writeAIFailure(w, r, logger, "verify AI key", err)
			return
		}
		writeJSON(w, r, logger, http.StatusOK, settings)
	}
}

// getAIModels returns the provider's live model list, or the failure (see
// writeAIFailure).
func getAIModels(logger *slog.Logger, svc *aisettings.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		models, err := svc.Models(r.Context())
		if err != nil {
			writeAIFailure(w, r, logger, "list AI models", err)
			return
		}
		writeJSON(w, r, logger, http.StatusOK, modelsResponse{Models: models})
	}
}

// writeAISettings returns a func writing a Service result: the settings,
// 400 invalid_settings for a *aisettings.ValidationError, or 500.
func writeAISettings(w http.ResponseWriter, r *http.Request, logger *slog.Logger, action string) func(aisettings.Settings, error) {
	return func(settings aisettings.Settings, err error) {
		var invalid *aisettings.ValidationError
		switch {
		case errors.As(err, &invalid):
			writeError(w, r, logger, http.StatusBadRequest, "invalid_settings", invalid.Error())
		case err != nil:
			logger.ErrorContext(r.Context(), action, "error", err)
			writeError(w, r, logger, http.StatusInternalServerError, "internal", "internal error")
		default:
			writeJSON(w, r, logger, http.StatusOK, settings)
		}
	}
}

// writeAIFailure writes a Verify or Models error: 409 not_configured when
// the settings cannot build a provider, 409 settings_changed when they
// changed during verification, and for a failed provider call a 502 (504
// for a timeout) whose code names the llm typed error: auth_failed,
// rate_limited, timeout, bad_output or provider_error. Rate limits and
// timeouts are retryable. Anything else is 500.
func writeAIFailure(w http.ResponseWriter, r *http.Request, logger *slog.Logger, action string, err error) {
	switch {
	case errors.Is(err, aisettings.ErrNotConfigured):
		writeError(w, r, logger, http.StatusConflict, "not_configured", err.Error())
	case errors.Is(err, aisettings.ErrChanged):
		writeError(w, r, logger, http.StatusConflict, "settings_changed", err.Error())
	case errors.Is(err, aisettings.ErrProviderFailed):
		logger.WarnContext(r.Context(), action+" failed", "error", err)
		status, e := http.StatusBadGateway, apiError{Code: "provider_error", Message: err.Error()}
		switch {
		case errors.Is(err, llm.ErrAuth):
			e.Code = "auth_failed"
		case errors.Is(err, llm.ErrRateLimited):
			e.Code, e.Retryable = "rate_limited", true
		case errors.Is(err, llm.ErrTimeout):
			status, e.Code, e.Retryable = http.StatusGatewayTimeout, "timeout", true
		case errors.Is(err, llm.ErrBadOutput):
			e.Code = "bad_output"
		}
		writeAPIError(w, r, logger, status, e)
	default:
		logger.ErrorContext(r.Context(), action, "error", err)
		writeError(w, r, logger, http.StatusInternalServerError, "internal", "internal error")
	}
}
