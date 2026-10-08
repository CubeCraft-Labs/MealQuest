package server

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/gcal"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/plan"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

// maxPlanBody bounds a POST /plans/{weekStart}/draft or PUT
// /plans/{weekStart}/days/{date} body.
const maxPlanBody = 4 << 10

// planDraft is the POST /plans/{weekStart}/draft body. confirm is needed
// to draft over a plan pushed to Calendar.
type planDraft struct {
	Confirm *bool `json:"confirm"`
}

// planDaySwap is the PUT /plans/{weekStart}/days/{date} body.
type planDaySwap struct {
	DinnerID *string `json:"dinner_id"`
}

// getPlan answers GET /plans/{weekStart} with the week's plan.
func getPlan(logger *slog.Logger, svc *plan.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		week, ok := weekStartOrFail(w, r, logger)
		if !ok {
			return
		}
		p, err := svc.Get(r.Context(), week)
		if err != nil {
			writePlanError(w, r, logger, "read plan", err)
			return
		}
		writeJSON(w, r, logger, http.StatusOK, p)
	}
}

// postPlanDraft enqueues the draft of the week and answers 202 with the
// jobId that GET /jobs/{id} reports on, without waiting for the job. When
// a draft of the week is already queued or running, it answers 202 with
// that job instead, or 409 when this draft is confirmed and that one is
// not.
func postPlanDraft(logger *slog.Logger, svc *plan.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		week, ok := weekStartOrFail(w, r, logger)
		if !ok {
			return
		}
		body, err := decodePatch[planDraft](http.MaxBytesReader(w, r.Body, maxPlanBody))
		if err != nil {
			writeError(w, r, logger, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		d, err := svc.StartDraft(r.Context(), week, body.Confirm != nil && *body.Confirm)
		if err != nil {
			writePlanError(w, r, logger, "start plan draft", err)
			return
		}
		writeJSON(w, r, logger, http.StatusAccepted, d)
	}
}

// putPlanDay swaps the dinner of one day of the week's plan and answers
// with the plan as swapped.
func putPlanDay(logger *slog.Logger, svc *plan.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		week, ok := weekStartOrFail(w, r, logger)
		if !ok {
			return
		}
		body, err := decodePatch[planDaySwap](http.MaxBytesReader(w, r.Body, maxPlanBody))
		if err != nil {
			writeError(w, r, logger, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		if body.DinnerID == nil {
			writeError(w, r, logger, http.StatusBadRequest, "invalid_request", "dinner_id: required")
			return
		}
		p, err := svc.Swap(r.Context(), week, chi.URLParam(r, "date"), *body.DinnerID)
		if err != nil {
			writePlanError(w, r, logger, "swap plan day", err)
			return
		}
		writeJSON(w, r, logger, http.StatusOK, p)
	}
}

// postPlanPush pushes the week's plan to the chosen Google calendar
// (Push to Calendar) and answers with the plan as synced.
func postPlanPush(logger *slog.Logger, svc *plan.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		week, ok := weekStartOrFail(w, r, logger)
		if !ok {
			return
		}
		p, err := svc.Push(r.Context(), week)
		if err != nil {
			writePlanPushError(w, r, logger, err)
			return
		}
		writeJSON(w, r, logger, http.StatusOK, p)
	}
}

// writePlanPushError maps a Push to Calendar error to its API error: 409
// not_connected when Google Calendar is not connected, no_calendar when no
// calendar is chosen, calendar_unavailable when the chosen calendar was
// deleted or is no longer writable, and plan_changed when the plan was
// edited during the push; a Google failure as writeGoogleFailure maps it
// (409 google_reconnect_required, 503 google_unavailable or 502
// google_failed). A failed push leaves the plan's status as it was. Any other
// error is the plan's, as writePlanError maps it.
func writePlanPushError(w http.ResponseWriter, r *http.Request, logger *slog.Logger, err error) {
	var calendar *gcal.CalendarError
	switch {
	case errors.Is(err, gcal.ErrNotConnected):
		writeError(w, r, logger, http.StatusConflict, "not_connected", "Google Calendar is not connected")
	case errors.Is(err, gcal.ErrNoCalendar):
		writeError(w, r, logger, http.StatusConflict, "no_calendar",
			"choose the calendar to push dinners to in Settings, then push again")
	case errors.Is(err, gcal.ErrCalendarUnavailable):
		logger.WarnContext(r.Context(), "push plan to Calendar", "error", err)
		writeError(w, r, logger, http.StatusConflict, "calendar_unavailable",
			"the chosen calendar was deleted or MealQuest can no longer add dinners to it; choose another calendar in Settings")
	case errors.Is(err, plan.ErrPlanChanged):
		writeError(w, r, logger, http.StatusConflict, "plan_changed", err.Error())
	case errors.Is(err, gcal.ErrReconnect), errors.As(err, &calendar):
		writeGoogleFailure(w, r, logger, "push plan to Calendar", err, "Google Calendar could not be reached; push again")
	default:
		writePlanError(w, r, logger, "push plan to Calendar", err)
	}
}

// weekStartOrFail parses the {weekStart} path parameter, or answers 400
// when it is not a Monday date.
func weekStartOrFail(w http.ResponseWriter, r *http.Request, logger *slog.Logger) (store.LocalDate, bool) {
	week, err := plan.ParseWeekStart(chi.URLParam(r, "weekStart"))
	if err != nil {
		writeError(w, r, logger, http.StatusBadRequest, "invalid_week", err.Error())
		return store.LocalDate{}, false
	}
	return week, true
}

// writePlanError maps a plan.Service error to its API error: a week with
// no plan or an unknown dinner is 404, a date outside the week is 400, and
// drafting over a synced plan without confirm, a confirmed draft while an
// unconfirmed one runs, planning a dinner that is neither a Regular nor in
// the Quest log, or one already on another day of the week, is 409.
func writePlanError(w http.ResponseWriter, r *http.Request, logger *slog.Logger, action string, err error) {
	var day *plan.InvalidDayError
	var week *plan.InvalidWeekError
	var notPlannable *plan.DinnerNotPlannableError
	switch {
	case errors.As(err, &day):
		writeError(w, r, logger, http.StatusBadRequest, "invalid_date", day.Error())
	case errors.As(err, &week):
		writeError(w, r, logger, http.StatusBadRequest, "invalid_week", week.Error())
	case errors.As(err, &notPlannable):
		writeError(w, r, logger, http.StatusConflict, "dinner_not_plannable", notPlannable.Error())
	case errors.Is(err, plan.ErrDinnerAlreadyPlanned):
		writeError(w, r, logger, http.StatusConflict, "dinner_already_planned", err.Error())
	case errors.Is(err, plan.ErrConfirmRequired):
		writeError(w, r, logger, http.StatusConflict, "confirm_required", err.Error())
	case errors.Is(err, plan.ErrDraftRunning):
		writeError(w, r, logger, http.StatusConflict, "draft_running", err.Error())
	case errors.Is(err, plan.ErrNotFound):
		writeError(w, r, logger, http.StatusNotFound, "not_found", "plan not found")
	case errors.Is(err, plan.ErrDinnerNotFound):
		writeError(w, r, logger, http.StatusNotFound, "not_found", "dinner not found")
	default:
		logger.ErrorContext(r.Context(), action, "error", err)
		writeError(w, r, logger, http.StatusInternalServerError, "internal", "internal error")
	}
}
