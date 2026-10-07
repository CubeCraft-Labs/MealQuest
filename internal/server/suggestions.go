package server

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/suggest"
)

type deck struct {
	Suggestions []suggest.Suggestion `json:"suggestions"`
}

// postSuggestionBatch starts a manual suggestion batch and answers 202 with
// its batch_id and the job_id that GET /jobs/{id} reports on, without
// waiting for the job. When a batch is already pending or running it
// answers 202 with that batch instead of starting another.
func postSuggestionBatch(logger *slog.Logger, svc *suggest.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, err := svc.StartBatch(r.Context(), suggest.TriggerManual)
		if err != nil {
			writeSuggestionError(w, r, logger, "start suggestion batch", err)
			return
		}
		writeJSON(w, r, logger, http.StatusAccepted, b)
	}
}

// getSuggestionDeck answers GET /suggestions/deck with the pending
// suggestions, oldest first.
func getSuggestionDeck(logger *slog.Logger, svc *suggest.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		list, err := svc.Deck(r.Context())
		if err != nil {
			writeSuggestionError(w, r, logger, "list suggestion deck", err)
			return
		}
		writeJSON(w, r, logger, http.StatusOK, deck{Suggestions: list})
	}
}

// postSuggestionDecision records Try it (accept) or Pass (decline) on a
// suggestion, recording the session's actor as its dinner's updated_by.
func postSuggestionDecision(logger *slog.Logger, svc *suggest.Service, accept bool) http.HandlerFunc {
	decide, action := svc.Decline, "decline suggestion"
	if accept {
		decide, action = svc.Accept, "accept suggestion"
	}
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := actorOrFail(w, r, logger)
		if !ok {
			return
		}
		d, err := decide(r.Context(), chi.URLParam(r, "id"), actor.Subject)
		if err != nil {
			writeSuggestionError(w, r, logger, action, err)
			return
		}
		writeJSON(w, r, logger, http.StatusOK, d)
	}
}

// writeSuggestionError maps a suggest.Service error to its API error: an
// unknown suggestion is 404, and a decision on one already decided the
// other way, or whose dinner left the deck, is 409.
func writeSuggestionError(w http.ResponseWriter, r *http.Request, logger *slog.Logger, action string, err error) {
	var decided *suggest.DecidedError
	switch {
	case errors.As(err, &decided):
		writeError(w, r, logger, http.StatusConflict, "suggestion_decided", decided.Error())
	case errors.Is(err, suggest.ErrDinnerNotSuggested):
		writeError(w, r, logger, http.StatusConflict, "dinner_not_suggested", err.Error())
	case errors.Is(err, suggest.ErrNotFound):
		writeError(w, r, logger, http.StatusNotFound, "not_found", "suggestion not found")
	default:
		logger.ErrorContext(r.Context(), action, "error", err)
		writeError(w, r, logger, http.StatusInternalServerError, "internal", "internal error")
	}
}
