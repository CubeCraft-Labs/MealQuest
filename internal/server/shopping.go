package server

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/chat"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/household"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/shopping"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

// maxShoppingItemBody bounds a PATCH /shopping/items/{id} body.
const maxShoppingItemBody = 1 << 10

// maxSendChatBody bounds a POST /shopping/{weekStart}/send-chat body.
const maxSendChatBody = 1 << 10

// supplyRun is a Supply run as the API shows it. label is the week label,
// which stands in for title until the AI writes one.
type supplyRun struct {
	WeekStart    string  `json:"weekStart"`
	Label        string  `json:"label"`
	Title        *string `json:"title"`
	Status       string  `json:"status"`
	SentToChatAt *string `json:"sent_to_chat_at"`
	shopping.Counts
	Groups []supplyRunGroup `json:"groups"`
}

// supplyRunGroup is the items of one plan day's dinner. day is the plan
// day's weekday, such as "Monday".
type supplyRunGroup struct {
	Dinner supplyRunDinner `json:"dinner"`
	Date   string          `json:"date"`
	Day    string          `json:"day"`
	Items  []supplyRunItem `json:"items"`
}

type supplyRunDinner struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// supplyRunItem is one ingredient to buy; qty is null when the recipe
// gives none, and needed is false once it is marked "have it".
type supplyRunItem struct {
	ID     string  `json:"id"`
	Name   string  `json:"name"`
	Qty    *string `json:"qty"`
	Needed bool    `json:"needed"`
}

// generatedSupplyRun is the POST /shopping/{weekStart}/generate answer:
// the Supply run as made, and the plan days with nothing to shop for.
type generatedSupplyRun struct {
	supplyRun
	Skipped []shopping.SkippedDay `json:"skipped"`
}

// sendChatRequest is the POST /shopping/{weekStart}/send-chat body:
// resend posts a week already sent again.
type sendChatRequest struct {
	Resend bool `json:"resend"`
}

// shoppingItemPatch is the PATCH /shopping/items/{id} body.
type shoppingItemPatch struct {
	Needed *bool `json:"needed"`
}

// toggledItem is the PATCH /shopping/items/{id} answer: the item's flag
// and its Supply run's counts after the change.
type toggledItem struct {
	ID     string `json:"id"`
	Needed bool   `json:"needed"`
	shopping.Counts
}

// getSupplyRun answers GET /shopping/{weekStart} with the week's Supply
// run.
func getSupplyRun(logger *slog.Logger, svc *shopping.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		week, ok := weekStartOrFail(w, r, logger)
		if !ok {
			return
		}
		l, err := svc.Get(r.Context(), week)
		if err != nil {
			writeShoppingError(w, r, logger, "read Supply run", err)
			return
		}
		out, err := supplyRunOf(week, l)
		if err != nil {
			writeShoppingError(w, r, logger, "read Supply run", err)
			return
		}
		writeJSON(w, r, logger, http.StatusOK, out)
	}
}

// postSupplyRunGenerate makes, or remakes, the week's Supply run from its
// plan and answers with it and the plan days it skipped.
func postSupplyRunGenerate(logger *slog.Logger, svc *shopping.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		week, ok := weekStartOrFail(w, r, logger)
		if !ok {
			return
		}
		g, err := svc.Generate(r.Context(), week)
		if err != nil {
			writeShoppingError(w, r, logger, "generate Supply run", err)
			return
		}
		out, err := supplyRunOf(week, g.List)
		if err != nil {
			writeShoppingError(w, r, logger, "generate Supply run", err)
			return
		}
		skipped := g.Skipped
		if skipped == nil {
			skipped = []shopping.SkippedDay{}
		}
		writeJSON(w, r, logger, http.StatusOK, generatedSupplyRun{supplyRun: out, Skipped: skipped})
	}
}

// postSupplyRunSendChat posts the week's Supply run to Google Chat and
// answers with it, sent_to_chat_at recorded. Its body is a JSON object,
// {} or {"resend": true}: a week already sent is answered as it is,
// without a second post, unless resend is true. No webhook stored is 409
// not_configured; Google Chat not answering is 502 chat_unreachable,
// retryable, and refusing the message 502 chat_refused, retryable only for
// 429 or 5xx.
func postSupplyRunSendChat(logger *slog.Logger, svc *shopping.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		week, ok := weekStartOrFail(w, r, logger)
		if !ok {
			return
		}
		body, err := decodePatch[sendChatRequest](http.MaxBytesReader(w, r.Body, maxSendChatBody))
		if err != nil {
			writeError(w, r, logger, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		l, err := svc.SendToChat(r.Context(), week, body.Resend)
		var refused *chat.RefusedError
		switch {
		case errors.Is(err, chat.ErrNotConfigured):
			writeError(w, r, logger, http.StatusConflict, "not_configured", "no Google Chat webhook is configured; add one in Settings")
			return
		case errors.Is(err, chat.ErrUnreachable):
			logger.WarnContext(r.Context(), "send Supply run to Google Chat", "error", err)
			writeAPIError(w, r, logger, http.StatusBadGateway, apiError{Code: "chat_unreachable", Message: "Google Chat did not answer", Retryable: true})
			return
		case errors.As(err, &refused):
			logger.WarnContext(r.Context(), "send Supply run to Google Chat", "error", err)
			writeAPIError(w, r, logger, http.StatusBadGateway, apiError{Code: "chat_refused", Message: err.Error(), Retryable: refused.Retryable()})
			return
		case err != nil:
			writeShoppingError(w, r, logger, "send Supply run to Google Chat", err)
			return
		}
		out, err := supplyRunOf(week, l)
		if err != nil {
			writeShoppingError(w, r, logger, "send Supply run to Google Chat", err)
			return
		}
		writeJSON(w, r, logger, http.StatusOK, out)
	}
}

// patchShoppingItem marks a Supply run item needed or "have it" and
// answers with its Supply run's new counts.
func patchShoppingItem(logger *slog.Logger, svc *shopping.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := decodePatch[shoppingItemPatch](http.MaxBytesReader(w, r.Body, maxShoppingItemBody))
		if err != nil {
			writeError(w, r, logger, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		if body.Needed == nil {
			writeError(w, r, logger, http.StatusBadRequest, "invalid_request", "needed: required")
			return
		}
		id := chi.URLParam(r, "id")
		c, err := svc.SetNeeded(r.Context(), id, *body.Needed)
		if err != nil {
			writeShoppingError(w, r, logger, "mark Supply run item", err)
			return
		}
		writeJSON(w, r, logger, http.StatusOK, toggledItem{ID: id, Needed: *body.Needed, Counts: c})
	}
}

// supplyArchive is the GET /shopping/archive answer: the Supply archive's
// periods, newest first, as grouped by by.
type supplyArchive struct {
	By      string          `json:"by"`
	Periods []archivePeriod `json:"periods"`
}

// archivePeriod is one heading of the Supply archive and its posts,
// newest first.
type archivePeriod struct {
	Key     string        `json:"key"`
	Heading string        `json:"heading"`
	Posts   []archivePost `json:"posts"`
}

// archivePost is one week's Supply run in the Supply archive. label is
// the week label, which stands in for title while it is null, and
// onHandCount counts the items marked "have it".
type archivePost struct {
	WeekStart   string   `json:"weekStart"`
	Label       string   `json:"label"`
	Title       *string  `json:"title"`
	DinnerCount int      `json:"dinnerCount"`
	ItemCount   int      `json:"itemCount"`
	OnHandCount int      `json:"onHandCount"`
	Dinners     []string `json:"dinners"`
	Current     bool     `json:"current"`
}

// getSupplyArchive answers GET /shopping/archive?by=week|month|year with
// the Supply archive grouped by that period.
func getSupplyArchive(logger *slog.Logger, svc *shopping.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		by := r.URL.Query().Get("by")
		periods, err := svc.Archive(r.Context(), by)
		if errors.Is(err, shopping.ErrInvalidPeriod) {
			writeError(w, r, logger, http.StatusBadRequest, "invalid_request", shopping.ErrInvalidPeriod.Error())
			return
		}
		if err != nil {
			writeShoppingError(w, r, logger, "read Supply archive", err)
			return
		}
		out := supplyArchive{By: by, Periods: make([]archivePeriod, len(periods))}
		for i, p := range periods {
			posts := make([]archivePost, len(p.Posts))
			for j, post := range p.Posts {
				posts[j] = archivePost{
					WeekStart:   post.WeekStart,
					Label:       post.Label,
					Title:       post.Title,
					DinnerCount: len(post.Dinners),
					ItemCount:   post.Items,
					OnHandCount: post.OnHand,
					Dinners:     post.Dinners,
					Current:     post.Current,
				}
			}
			out.Periods[i] = archivePeriod{Key: p.Key, Heading: p.Heading, Posts: posts}
		}
		writeJSON(w, r, logger, http.StatusOK, out)
	}
}

// supplyRunOf is l, the Supply run of the week starting week, as the API
// shows it.
func supplyRunOf(week store.LocalDate, l shopping.List) (supplyRun, error) {
	label, err := household.WeekLabel(week)
	if err != nil {
		return supplyRun{}, err
	}
	groups := make([]supplyRunGroup, len(l.Groups))
	for i, g := range l.Groups {
		date, err := time.Parse(time.DateOnly, g.Date)
		if err != nil {
			return supplyRun{}, fmt.Errorf("group %s date: %w", g.ID, err)
		}
		items := make([]supplyRunItem, len(g.Items))
		for j, it := range g.Items {
			items[j] = supplyRunItem{ID: it.ID, Name: it.Name, Qty: it.Qty, Needed: it.Needed}
		}
		groups[i] = supplyRunGroup{
			Dinner: supplyRunDinner{ID: g.DinnerID, Name: g.DinnerName},
			Date:   g.Date,
			Day:    date.Weekday().String(),
			Items:  items,
		}
	}
	return supplyRun{
		WeekStart:    l.WeekStart,
		Label:        label,
		Title:        l.Title,
		Status:       l.Status,
		SentToChatAt: l.SentToChatAt,
		Counts:       l.Counts(),
		Groups:       groups,
	}, nil
}

// writeShoppingError maps a shopping.Service error to its API error: a
// week with no Supply run or no plan, or an unknown item, is 404, and a
// change to a Supply run in the Supply archive is 409 archived.
func writeShoppingError(w http.ResponseWriter, r *http.Request, logger *slog.Logger, action string, err error) {
	switch {
	case errors.Is(err, shopping.ErrListNotFound):
		writeError(w, r, logger, http.StatusNotFound, "not_found", "Supply run not found")
	case errors.Is(err, shopping.ErrItemNotFound):
		writeError(w, r, logger, http.StatusNotFound, "not_found", "Supply run item not found")
	case errors.Is(err, shopping.ErrPlanNotFound):
		writeError(w, r, logger, http.StatusNotFound, "not_found", "plan not found")
	case errors.Is(err, shopping.ErrFrozen):
		writeError(w, r, logger, http.StatusConflict, "archived", err.Error())
	default:
		logger.ErrorContext(r.Context(), action, "error", err)
		writeError(w, r, logger, http.StatusInternalServerError, "internal", "internal error")
	}
}
