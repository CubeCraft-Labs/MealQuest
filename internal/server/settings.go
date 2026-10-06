package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/auth"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/household"
)

// maxSettingsBody bounds a PATCH /settings/household, /settings/ai or
// /settings/recipe-search, or PUT /settings/ai/api-key, request body.
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

// patchHouseholdSettings records the session's actor subject as updated_by.
func patchHouseholdSettings(logger *slog.Logger, svc *household.SettingsService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := auth.ActorFrom(r.Context())
		if !ok {
			logger.ErrorContext(r.Context(), "PATCH /api/v1/settings/household without a session")
			writeError(w, r, logger, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		patch, err := decodePatch[settingsPatch](http.MaxBytesReader(w, r.Body, maxSettingsBody))
		if err != nil {
			writeError(w, r, logger, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		settings, err := svc.Update(r.Context(), household.SettingsPatch(patch), actor.Subject)
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

// decodePatch reads a single JSON object into a patch of type T, refusing
// unknown fields, null values and trailing data.
func decodePatch[T any](body io.Reader) (T, error) {
	var patch T
	data, err := io.ReadAll(body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return patch, fmt.Errorf("request body exceeds %d bytes", tooLarge.Limit)
		}
		return patch, errors.New("cannot read request body")
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		return patch, errors.New("request body must be a JSON object")
	}
	for name, raw := range fields {
		if bytes.Equal(raw, []byte("null")) {
			return patch, fmt.Errorf("%s: must not be null", name)
		}
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&patch); err != nil {
		var typeErr *json.UnmarshalTypeError
		if errors.As(err, &typeErr) {
			return patch, fmt.Errorf("%s: want a JSON %s", typeErr.Field, typeErr.Type)
		}
		return patch, fmt.Errorf("invalid settings: %v", err)
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
	writeAPIError(w, r, logger, status, apiError{Code: code, Message: message})
}

// writeAPIError responds with status and e.
func writeAPIError(w http.ResponseWriter, r *http.Request, logger *slog.Logger, status int, e apiError) {
	body, err := json.Marshal(errorBody{Error: e})
	if err != nil {
		panic(err) // string and bool fields cannot fail to marshal
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if _, err := w.Write(body); err != nil {
		logger.WarnContext(r.Context(), "write error response", "code", e.Code, "error", err)
	}
}
