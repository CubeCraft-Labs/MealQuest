package server

import (
	"errors"
	"log/slog"
	"net/http"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/chat"
)

// chatWebhookBody is the PUT /settings/chat-webhook body. Both fields are
// required.
type chatWebhookBody struct {
	URL        *string `json:"url"`
	SpaceLabel *string `json:"space_label"`
}

// getChatWebhook answers the stored webhook's settings, which never hold
// its URL.
func getChatWebhook(logger *slog.Logger, svc *chat.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		settings, err := svc.Get(r.Context())
		if err != nil {
			logger.ErrorContext(r.Context(), "get chat webhook", "error", err)
			writeError(w, r, logger, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		writeJSON(w, r, logger, http.StatusOK, settings)
	}
}

// putChatWebhook stores the webhook URL sealed, with its space label, and
// answers the settings as GET does.
func putChatWebhook(logger *slog.Logger, svc *chat.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := decodePatch[chatWebhookBody](http.MaxBytesReader(w, r.Body, maxSettingsBody))
		if err != nil {
			writeError(w, r, logger, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		if body.URL == nil || body.SpaceLabel == nil {
			writeError(w, r, logger, http.StatusBadRequest, "invalid_request", "url and space_label are required")
			return
		}
		settings, err := svc.Put(r.Context(), *body.URL, *body.SpaceLabel)
		var invalid *chat.ValidationError
		if errors.As(err, &invalid) {
			writeError(w, r, logger, http.StatusBadRequest, "invalid_webhook", invalid.Error())
			return
		}
		if err != nil {
			logger.ErrorContext(r.Context(), "save chat webhook", "error", err)
			writeError(w, r, logger, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		writeJSON(w, r, logger, http.StatusOK, settings)
	}
}

// postChatWebhookTest sends chat.TestText to the stored webhook and answers
// 200 with the status Google Chat answered, whatever it was, and the
// recorded outcome. No webhook stored, or one replaced during the test, is
// 409; no answer from Google Chat is 502.
func postChatWebhookTest(logger *slog.Logger, svc *chat.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		result, err := svc.Test(r.Context())
		switch {
		case errors.Is(err, chat.ErrNotConfigured):
			writeError(w, r, logger, http.StatusConflict, "not_configured", "no Google Chat webhook is configured")
		case errors.Is(err, chat.ErrWebhookChanged):
			writeError(w, r, logger, http.StatusConflict, "webhook_changed", "the Google Chat webhook changed during the test; test it again")
		case err != nil && r.Context().Err() != nil:
			logger.WarnContext(r.Context(), "chat webhook test interrupted", "error", err)
		case errors.Is(err, chat.ErrUnreachable):
			logger.WarnContext(r.Context(), "chat webhook test failed", "error", err)
			writeError(w, r, logger, http.StatusBadGateway, "chat_unreachable", "Google Chat did not answer the test message")
		case err != nil:
			logger.ErrorContext(r.Context(), "chat webhook test", "error", err)
			writeError(w, r, logger, http.StatusInternalServerError, "internal", "internal error")
		default:
			writeJSON(w, r, logger, http.StatusOK, result)
		}
	}
}
