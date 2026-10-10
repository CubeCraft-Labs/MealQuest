package server

import (
	"log/slog"
	"net/http"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/home"
)

// getHome answers GET /home with the Home screen at the household's local
// "now".
func getHome(logger *slog.Logger, svc *home.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h, err := svc.Get(r.Context())
		if err != nil {
			logger.ErrorContext(r.Context(), "read home", "error", err)
			writeError(w, r, logger, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		writeJSON(w, r, logger, http.StatusOK, h)
	}
}
