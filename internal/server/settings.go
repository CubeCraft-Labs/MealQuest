package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/actor"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/household"
)

// maxSettingsBody bounds a PATCH /settings/household request body.
const maxSettingsBody = 16 << 10

// settingsPatch is the PATCH /settings/household body. Every field is
// optional; unknown fields and null values are refused.
type settingsPatch struct {
	Timezone         *string `json:"timezone"`
	DinnerTime       *string `json:"dinner_time"`
	ReminderTime     *string `json:"reminder_time"`
	ReviewTime       *string `json:"review_time"`
	PlanDraftDow     *int    `json:"plan_draft_dow"`
	PlanDraftTime    *string `json:"plan_draft_time"`
	SuggestionsDow   *int    `json:"suggestions_dow"`
	SuggestionsTime  *string `json:"suggestions_time"`
	ChatAutosendDow  *int    `json:"chat_autosend_dow"`
	ChatAutosendTime *string `json:"chat_autosend_time"`
}

func getHouseholdSettings(logger *slog.Logger, svc *household.SettingsService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		settings, err := svc.Get(r.Context())
		if err != nil {
			logger.ErrorContext(r.Context(), "get household settings", "error", err)
			writeError(w, r, logger, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		writeJSON(w, r, logger, http.StatusOK, settings)
	}
}

func patchHouseholdSettings(logger *slog.Logger, svc *household.SettingsService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		patch, err := decodeSettingsPatch(http.MaxBytesReader(w, r.Body, maxSettingsBody))
		if err != nil {
			writeError(w, r, logger, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		settings, err := svc.Update(r.Context(), household.SettingsPatch(patch), actor.From(r.Context()))
		var invalid *household.ValidationError
		if errors.As(err, &invalid) {
			writeError(w, r, logger, http.StatusBadRequest, "invalid_settings", invalid.Error())
			return
		}
		if err != nil {
			logger.ErrorContext(r.Context(), "update household settings", "error", err)
			writeError(w, r, logger, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		writeJSON(w, r, logger, http.StatusOK, settings)
	}
}

// decodeSettingsPatch reads a single JSON object, refusing unknown fields,
// null values and trailing data.
func decodeSettingsPatch(body io.Reader) (settingsPatch, error) {
	data, err := io.ReadAll(body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return settingsPatch{}, fmt.Errorf("request body exceeds %d bytes", tooLarge.Limit)
		}
		return settingsPatch{}, errors.New("cannot read request body")
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		return settingsPatch{}, errors.New("request body must be a JSON object")
	}
	for name, raw := range fields {
		if bytes.Equal(raw, []byte("null")) {
			return settingsPatch{}, fmt.Errorf("%s: must not be null", name)
		}
	}

	var patch settingsPatch
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&patch); err != nil {
		var typeErr *json.UnmarshalTypeError
		if errors.As(err, &typeErr) {
			return settingsPatch{}, fmt.Errorf("%s: want a JSON %s", typeErr.Field, typeErr.Type)
		}
		return settingsPatch{}, fmt.Errorf("invalid settings: %v", err)
	}
	return patch, nil
}

func writeJSON(w http.ResponseWriter, r *http.Request, logger *slog.Logger, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		logger.ErrorContext(r.Context(), "encode response", "error", err)
		writeError(w, r, logger, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if _, err := w.Write(body); err != nil {
		logger.WarnContext(r.Context(), "write response", "error", err)
	}
}

// writeError responds with status and a non-retryable API error.
func writeError(w http.ResponseWriter, r *http.Request, logger *slog.Logger, status int, code, message string) {
	body, err := json.Marshal(errorBody{Error: apiError{Code: code, Message: message}})
	if err != nil {
		panic(err) // string and bool fields cannot fail to marshal
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if _, err := w.Write(body); err != nil {
		logger.WarnContext(r.Context(), "write error response", "code", code, "error", err)
	}
}
