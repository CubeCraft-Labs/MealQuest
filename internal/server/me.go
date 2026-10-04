package server

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/auth"
)

// householdLabel is the single shared household identity.
const householdLabel = "Our kitchen"

type meResponse struct {
	Name      string        `json:"name"`
	Email     string        `json:"email"`
	Household householdInfo `json:"household"`
	CSRFToken string        `json:"csrfToken"`
}

type householdInfo struct {
	Label string `json:"label"`
}

// me answers GET /api/v1/me with the signed-in member, the household and the
// session's CSRF token for CSRFHeader.
func me(logger *slog.Logger) http.HandlerFunc {
	internal := apiErrorHandler(logger, http.StatusInternalServerError, "internal", "internal error")
	return func(w http.ResponseWriter, r *http.Request) {
		s, ok := auth.SessionFrom(r.Context())
		if !ok {
			logger.ErrorContext(r.Context(), "GET /api/v1/me without a session")
			internal(w, r)
			return
		}
		body, err := json.Marshal(meResponse{
			Name:      s.Name,
			Email:     s.Email,
			Household: householdInfo{Label: householdLabel},
			CSRFToken: s.CSRFToken,
		})
		if err != nil {
			panic(err) // string fields cannot fail to marshal
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write(body); err != nil {
			logger.WarnContext(r.Context(), "write me response", "error", err)
		}
	}
}
