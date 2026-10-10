package server

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/dinners"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ideas"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/plan"
)

// maxIdeasBody bounds a POST /ideas/from-ingredients or /ideas/quick body.
const maxIdeasBody = 16 << 10

// ideasFromIngredients is the POST /ideas/from-ingredients body. Both
// fields are required.
type ideasFromIngredients struct {
	Ingredients []string `json:"ingredients"`
	QuickOnly   *bool    `json:"quick_only"`
}

// postIdeasFromIngredients starts finding ideas from the ingredients on
// hand and answers 202 with the jobId that GET /jobs/{id} reports on,
// without waiting for the job. An ingredient list the household must
// correct is 400.
func postIdeasFromIngredients(logger *slog.Logger, svc *ideas.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := decodePatch[ideasFromIngredients](http.MaxBytesReader(w, r.Body, maxIdeasBody))
		if err != nil {
			writeError(w, r, logger, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		switch {
		case body.Ingredients == nil:
			writeError(w, r, logger, http.StatusBadRequest, "invalid_request", "ingredients: required")
			return
		case body.QuickOnly == nil:
			writeError(w, r, logger, http.StatusBadRequest, "invalid_request", "quick_only: required")
			return
		}
		started, err := svc.StartFromIngredients(r.Context(), body.Ingredients, *body.QuickOnly)
		var invalid *ideas.InvalidError
		switch {
		case errors.As(err, &invalid):
			writeError(w, r, logger, http.StatusBadRequest, "invalid_request", invalid.Message)
			return
		case err != nil:
			logger.ErrorContext(r.Context(), "start ideas from ingredients", "error", err)
			writeError(w, r, logger, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		writeJSON(w, r, logger, http.StatusAccepted, started)
	}
}

// ideasQuick is the POST /ideas/quick body. Both fields are required:
// exclude is [] on a first page.
type ideasQuick struct {
	Kind    *string  `json:"kind"`
	Exclude []string `json:"exclude"`
}

// postIdeasQuick starts finding a page of Quick dinner ideas of a kind,
// crockpot or under30, none named in exclude, and answers 202 with the
// jobId that GET /jobs/{id} reports on, without waiting for the job. A
// missing or unknown kind, or an exclude list the household must correct,
// is 400.
func postIdeasQuick(logger *slog.Logger, svc *ideas.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := decodePatch[ideasQuick](http.MaxBytesReader(w, r.Body, maxIdeasBody))
		if err != nil {
			writeError(w, r, logger, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		switch {
		case body.Kind == nil:
			writeError(w, r, logger, http.StatusBadRequest, "invalid_request", "kind: required")
			return
		case body.Exclude == nil:
			writeError(w, r, logger, http.StatusBadRequest, "invalid_request", "exclude: required")
			return
		}
		started, err := svc.StartQuick(r.Context(), *body.Kind, body.Exclude)
		var invalid *ideas.InvalidError
		switch {
		case errors.As(err, &invalid):
			writeError(w, r, logger, http.StatusBadRequest, "invalid_request", invalid.Message)
			return
		case err != nil:
			logger.ErrorContext(r.Context(), "start quick ideas", "error", err)
			writeError(w, r, logger, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		writeJSON(w, r, logger, http.StatusAccepted, started)
	}
}

// chosenIdeaOrFail returns the idea {i} of ideas job {jobId}, or answers
// 400 invalid_request when i is not an index (0, 1, ...), 404 not_found
// for an idea no succeeded ideas job shows, or 409 ideas_not_ready while
// the job is still finding ideas.
func chosenIdeaOrFail(w http.ResponseWriter, r *http.Request, logger *slog.Logger, svc *ideas.Service) (ideas.Chosen, bool) {
	raw := chi.URLParam(r, "i")
	i, err := strconv.Atoi(raw)
	if err != nil || i < 0 || strconv.Itoa(i) != raw {
		writeError(w, r, logger, http.StatusBadRequest, "invalid_request", "i: must be an idea's index, 0 or more")
		return ideas.Chosen{}, false
	}
	idea, err := svc.Idea(r.Context(), chi.URLParam(r, "jobId"), i)
	switch {
	case errors.Is(err, ideas.ErrIdeaNotFound):
		writeError(w, r, logger, http.StatusNotFound, "not_found", "idea not found")
		return ideas.Chosen{}, false
	case errors.Is(err, ideas.ErrIdeasNotReady):
		writeError(w, r, logger, http.StatusConflict, "ideas_not_ready", err.Error())
		return ideas.Chosen{}, false
	case err != nil:
		logger.ErrorContext(r.Context(), "read idea", "error", err)
		writeError(w, r, logger, http.StatusInternalServerError, "internal", "internal error")
		return ideas.Chosen{}, false
	}
	return idea, true
}

// postIdeaQuestLog adds idea {i} of ideas job {jobId} to the Quest log,
// recording the session's actor as updated_by, as a dinner grounded in
// the recipe the idea was grounded in, with origin ingredients or quick.
// It answers 201 with the dinner made, or 200 with the dinner the idea
// repeats (see dinners.Service.AddToQuestLog), so asking again answers the
// same dinner. A repeated dinner awaiting the household's choice is 409
// duplicate_dinner.
func postIdeaQuestLog(logger *slog.Logger, ideasSvc *ideas.Service, dinnerSvc *dinners.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := actorOrFail(w, r, logger)
		if !ok {
			return
		}
		idea, ok := chosenIdeaOrFail(w, r, logger, ideasSvc)
		if !ok {
			return
		}
		d, created, err := dinnerSvc.AddToQuestLog(r.Context(), dinners.NewQuestLog(idea), actor.Subject)
		if err != nil {
			writeDinnerError(w, r, logger, "add idea to Quest log", err)
			return
		}
		status := http.StatusOK
		if created {
			status = http.StatusCreated
		}
		writeJSON(w, r, logger, status, d)
	}
}

// cookedTonight is the POST /ideas/{jobId}/items/{i}/cook-tonight answer:
// the idea's dinner and the week's plan with it tonight.
type cookedTonight struct {
	Dinner dinners.Dinner `json:"dinner"`
	Plan   plan.Plan      `json:"plan"`
}

// postIdeaCookTonight plans idea {i} of ideas job {jobId} tonight: today,
// in the household's zone, of the plan of today's week. The idea's dinner
// is added or found as postIdeaQuestLog does, then swapped onto today
// through plan.Service.Swap, exactly as a swap on the Draft board, so the
// plan is marked edited and a synced plan becomes changed. It answers 200
// with the dinner and the plan as swapped; asking again plans the same
// dinner again.
//
// When today's week has no plan it answers 409 no_plan and changes
// nothing: no dinner is added and no plan is made, since a plan holds all
// seven days; the household plans the week first. The week is always
// today's own, so no other day is ever written. A dinner already on
// another day of the week is 409 dinner_already_planned, after the dinner
// is added or found.
func postIdeaCookTonight(logger *slog.Logger, ideasSvc *ideas.Service, dinnerSvc *dinners.Service, planSvc *plan.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := actorOrFail(w, r, logger)
		if !ok {
			return
		}
		idea, ok := chosenIdeaOrFail(w, r, logger, ideasSvc)
		if !ok {
			return
		}
		week, today, err := planSvc.Tonight(r.Context())
		if errors.Is(err, plan.ErrNotFound) {
			writeNoPlan(w, r, logger)
			return
		}
		if err != nil {
			writePlanError(w, r, logger, "find tonight's plan", err)
			return
		}
		d, _, err := dinnerSvc.AddToQuestLog(r.Context(), dinners.NewQuestLog(idea), actor.Subject)
		if err != nil {
			writeDinnerError(w, r, logger, "add idea to cook tonight", err)
			return
		}
		p, err := planSvc.Swap(r.Context(), week, today.String(), d.ID)
		if errors.Is(err, plan.ErrNotFound) {
			// Plans are never deleted, so the plan Tonight found is still
			// there; this is said loudly rather than answered as success.
			logger.ErrorContext(r.Context(), "plan of tonight's week gone before the swap", "week", week.String())
			writeNoPlan(w, r, logger)
			return
		}
		if err != nil {
			writePlanError(w, r, logger, "cook idea tonight", err)
			return
		}
		writeJSON(w, r, logger, http.StatusOK, cookedTonight{Dinner: d, Plan: p})
	}
}

// writeNoPlan answers 409 no_plan: today's week has no plan to put
// tonight's dinner on, so the household plans the week first.
func writeNoPlan(w http.ResponseWriter, r *http.Request, logger *slog.Logger) {
	writeError(w, r, logger, http.StatusConflict, "no_plan", "this week has no plan yet; plan this week first")
}
