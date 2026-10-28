package gcal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"time"
)

// maxEventBody caps how much of an events response is read.
const maxEventBody = 64 << 10

var (
	// ErrNoCalendar means Google Calendar is connected but no calendar is
	// chosen to push to.
	ErrNoCalendar = errors.New("gcal: no calendar is chosen for Push to Calendar")
	// ErrCalendarUnavailable means the chosen calendar was deleted or the
	// connected account can no longer add events to it. Such a calendar
	// cannot be chosen again; the household must choose another.
	ErrCalendarUnavailable = errors.New("gcal: the chosen calendar was deleted or MealQuest can no longer add dinners to it")
)

// Google's rule for a client-chosen event id: base32hex characters
// (lowercase a-v and 0-9), 5 to 1024 of them.
var eventIDChars = regexp.MustCompile(`^[a-v0-9]+$`)

const (
	minEventID = 5
	maxEventID = 1024
)

// Event is one event to write to the chosen calendar.
type Event struct {
	// ID is the event's id, chosen by the caller so that writing the same
	// event again updates it; it must satisfy Google's id rule.
	ID          string
	Summary     string
	Description string
	Start, End  time.Time
	// TimeZone is the IANA zone the event is in, such as America/New_York.
	TimeZone string
	// Remove deletes the event instead of writing it. A missing event on a
	// calendar that is still writable is success. The same HTTP 404 from a
	// deleted or unavailable calendar is ErrCalendarUnavailable, so a
	// delete-only push is not recorded against a calendar that is gone.
	// Summary, description and times are ignored.
	Remove bool
}

func (e Event) validate() error {
	if len(e.ID) < minEventID || len(e.ID) > maxEventID || !eventIDChars.MatchString(e.ID) {
		return fmt.Errorf("gcal: event id %q is not 5 to 1024 base32hex characters", e.ID)
	}
	if e.Remove {
		return nil
	}
	switch {
	case e.Summary == "":
		return fmt.Errorf("gcal: event %s has no summary", e.ID)
	case !e.End.After(e.Start):
		return fmt.Errorf("gcal: event %s does not end after it starts", e.ID)
	case e.TimeZone == "":
		return fmt.Errorf("gcal: event %s has no time zone", e.ID)
	}
	return nil
}

// eventBody is an event as the Calendar API takes it.
type eventBody struct {
	ID          string    `json:"id"`
	Status      string    `json:"status"`
	Summary     string    `json:"summary"`
	Description string    `json:"description"`
	Start       eventTime `json:"start"`
	End         eventTime `json:"end"`
}

type eventTime struct {
	DateTime string `json:"dateTime"`
	TimeZone string `json:"timeZone"`
}

// PushEvents writes events, in order, to the chosen calendar: each is
// inserted with its id, and an id Google already has (HTTP 409, which
// includes an event deleted in Calendar) is updated in place instead, so
// writing an event again never adds a second one. It stops at the first
// failure; the events before it stay written, and writing them all again
// is safe. Nothing connected is ErrNotConnected, a grant already recorded
// as refused is ErrReconnect whatever calendar is chosen, no calendar
// chosen is ErrNoCalendar, a chosen calendar that was deleted or is no longer
// writable is ErrCalendarUnavailable, a grant Google no longer accepts is
// ErrReconnect (recorded as reconnect required, see recordRefusal), and any
// other Google failure is a *CalendarError, retryable when Google answered
// every try with HTTP 429 or 5xx.
//
// Once every event is written, PushEvents returns record(), which the
// caller uses to store what it pushed. record runs under the same hold of
// the account as the writes, so the chosen calendar cannot change between
// them; it is not run when a write fails.
func (s *Service) PushEvents(ctx context.Context, events []Event, record func() error) error {
	if len(events) == 0 {
		return errors.New("gcal: no events to push")
	}
	for _, e := range events {
		if err := e.validate(); err != nil {
			return err
		}
	}
	s.account.Lock()
	defer s.account.Unlock()
	return s.recordRefusal(ctx, s.pushEvents(ctx, events, record))
}

// pushEvents is PushEvents once the events are valid. The caller holds
// s.account. A refused grant is reported before no calendar chosen
// (grantedConnection): choosing a calendar cannot fix it, connecting again
// can.
func (s *Service) pushEvents(ctx context.Context, events []Event, record func() error) error {
	row, err := s.grantedConnection(ctx)
	if err != nil {
		return err
	}
	if !row.CalendarID.Valid || row.CalendarID.String == "" {
		return ErrNoCalendar
	}
	access, err := s.accessToken(ctx, row)
	if err != nil {
		return err
	}
	for _, e := range events {
		if err := s.writeEvent(ctx, access, row.CalendarID.String, e); err != nil {
			return err
		}
	}
	return record()
}

// writeEvent inserts e on calendarID, the chosen calendar, and updates it
// when Google already has its id. A removal deletes the event. One the
// calendar does not have is already gone. A 404 is also what Google
// returns when the calendar itself was deleted, so that 404 succeeds only
// when calendarID is still writable.
func (s *Service) writeEvent(ctx context.Context, access, calendarID string, e Event) error {
	base := s.calendarAPI + "/calendars/" + url.PathEscape(calendarID) + "/events"
	if e.Remove {
		status, err := s.eventRequest(ctx, access, http.MethodDelete, base+"/"+url.PathEscape(e.ID), nil)
		if err != nil || status != http.StatusNotFound {
			return err
		}
		return s.calendarStillWritable(ctx, access, calendarID)
	}
	body, err := json.Marshal(eventBody{
		ID:          e.ID,
		Status:      "confirmed",
		Summary:     e.Summary,
		Description: e.Description,
		Start:       eventTime{DateTime: e.Start.Format(time.RFC3339), TimeZone: e.TimeZone},
		End:         eventTime{DateTime: e.End.Format(time.RFC3339), TimeZone: e.TimeZone},
	})
	if err != nil {
		return fmt.Errorf("gcal: encode event %s: %w", e.ID, err)
	}
	status, err := s.eventRequest(ctx, access, http.MethodPost, base, body)
	if err != nil {
		return err
	}
	if status != http.StatusConflict {
		return nil
	}
	// The status "confirmed" in the body restores an event deleted in
	// Calendar, which Google keeps as cancelled under the same id.
	_, err = s.eventRequest(ctx, access, http.MethodPut, base+"/"+url.PathEscape(e.ID), body)
	return err
}

// eventRequest sends an event insert (POST), update (PUT) or delete,
// retrying a transient failure (send). It returns HTTP 200, 204 or 410 for
// a delete, 404 for a delete the caller must distinguish (a missing event
// or a missing calendar), or 409 for an insert of an id Google already has.
// Every other answer is an error. An insert Google took but answered with
// a transient failure is answered 409 when retried, and updated.
func (s *Service) eventRequest(ctx context.Context, access, method, target string, body []byte) (int, error) {
	what := "event insert"
	switch method {
	case http.MethodPut:
		what = "event update"
	case http.MethodDelete:
		what = "event delete"
	}
	status, reply, err := s.send(ctx, what, maxEventBody, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+access)
		req.Header.Set("Content-Type", "application/json")
		return req, nil
	})
	if err != nil {
		return 0, err
	}
	switch {
	case status == http.StatusOK:
		return http.StatusOK, nil
	case status == http.StatusNoContent && method == http.MethodDelete:
		return status, nil
	case method == http.MethodDelete && status == http.StatusGone:
		// 410 is an event Google has already deleted. The calendar
		// answered, so this says nothing is wrong with the calendar.
		return status, nil
	case method == http.MethodDelete && status == http.StatusNotFound:
		// 404 is the event missing, or the calendar deleted or hidden.
		// Google uses the same status for both. The caller tells them
		// apart.
		return status, nil
	case status == http.StatusConflict && method == http.MethodPost:
		return http.StatusConflict, nil
	case status == http.StatusUnauthorized:
		// The access token was just refreshed from the grant.
		return 0, ErrReconnect
	case status == http.StatusNotFound && method == http.MethodPost,
		status == http.StatusForbidden && errorReason(reply) == "requiredAccessLevel":
		// An insert into a deleted calendar, or one the account no longer
		// sees, is not found; a write to one it can only read is
		// requiredAccessLevel. An update not found is the event missing
		// from the calendar after Google reported its id taken, which
		// says nothing about the calendar.
		return 0, ErrCalendarUnavailable
	}
	return 0, &CalendarError{msg: fmt.Sprintf("gcal: Google refused the %s (HTTP %d)", what, status)}
}

// errorReason is the reason of the first error in a Calendar API error
// body, or "" when there is none. Only the reason is read; the rest of
// Google's text is never kept.
func errorReason(body []byte) string {
	var reply struct {
		Error struct {
			Errors []struct {
				Reason string `json:"reason"`
			} `json:"errors"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &reply) != nil || len(reply.Error.Errors) == 0 {
		return ""
	}
	return reply.Error.Errors[0].Reason
}

// calendarStillWritable reports whether calendarID is still a calendar the
// connected account can add events to. A delete that answered 404 is the
// event already gone when it is, and ErrCalendarUnavailable when the
// calendar was deleted or is no longer writable. The caller holds s.account
// and access is the token the delete just used.
func (s *Service) calendarStillWritable(ctx context.Context, access, calendarID string) error {
	pageToken := ""
	for range maxCalendarPages {
		page, err := s.calendarPage(ctx, access, pageToken)
		if err != nil {
			return err
		}
		for _, item := range page.Items {
			if item.ID == "" {
				return &CalendarError{msg: "gcal: Google listed a calendar without an id"}
			}
			if item.ID != calendarID {
				continue
			}
			if item.AccessRole == "writer" || item.AccessRole == "owner" {
				return nil
			}
			return ErrCalendarUnavailable
		}
		if page.NextPageToken == "" {
			return ErrCalendarUnavailable
		}
		pageToken = page.NextPageToken
	}
	return &CalendarError{msg: fmt.Sprintf("gcal: Google listed more than %d pages of calendars", maxCalendarPages)}
}
