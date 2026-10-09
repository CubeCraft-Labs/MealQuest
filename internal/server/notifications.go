package server

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/auth"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/household"
)

// notificationsBody is the PUT /settings/notifications body: every event's
// channels and the reminder time, all required. Unknown fields and null
// values are refused.
type notificationsBody struct {
	Prefs        *[]notificationPrefBody `json:"prefs"`
	ReminderTime *string                 `json:"reminder_time"`
}

type notificationPrefBody struct {
	Event *string `json:"event"`
	Push  *bool   `json:"push"`
	Chat  *bool   `json:"chat"`
}

// settings is the body as household notification settings, or why it is
// incomplete.
func (b notificationsBody) settings() (household.NotificationSettings, error) {
	if b.Prefs == nil || b.ReminderTime == nil {
		return household.NotificationSettings{}, errors.New("prefs and reminder_time are required")
	}
	prefs := make([]household.NotificationPref, len(*b.Prefs))
	for i, p := range *b.Prefs {
		if p.Event == nil || p.Push == nil || p.Chat == nil {
			return household.NotificationSettings{}, fmt.Errorf("prefs[%d]: event, push and chat are required", i)
		}
		prefs[i] = household.NotificationPref{Event: *p.Event, Push: *p.Push, Chat: *p.Chat}
	}
	return household.NotificationSettings{Prefs: prefs, ReminderTime: *b.ReminderTime}, nil
}

func getNotificationSettings(logger *slog.Logger, svc *household.SettingsService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		settings, err := svc.Notifications(r.Context())
		if err != nil {
			logger.ErrorContext(r.Context(), "get notification settings", "error", err)
			writeError(w, r, logger, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		writeJSON(w, r, logger, http.StatusOK, settings)
	}
}

// putNotificationSettings replaces the notification preferences and the
// reminder time, recording the session's actor subject as updated_by.
func putNotificationSettings(logger *slog.Logger, svc *household.SettingsService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := auth.ActorFrom(r.Context())
		if !ok {
			logger.ErrorContext(r.Context(), "PUT /api/v1/settings/notifications without a session")
			writeError(w, r, logger, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		body, err := decodePatch[notificationsBody](http.MaxBytesReader(w, r.Body, maxSettingsBody))
		if err != nil {
			writeError(w, r, logger, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		want, err := body.settings()
		if err != nil {
			writeError(w, r, logger, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		settings, err := svc.UpdateNotifications(r.Context(), want, actor.Subject)
		var invalid *household.ValidationError
		if errors.As(err, &invalid) {
			writeError(w, r, logger, http.StatusBadRequest, "invalid_settings", invalid.Error())
			return
		}
		if err != nil {
			logger.ErrorContext(r.Context(), "update notification settings", "error", err)
			writeError(w, r, logger, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		writeJSON(w, r, logger, http.StatusOK, settings)
	}
}
