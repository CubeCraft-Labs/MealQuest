package gcal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
}

func (e Event) validate() error {
	switch {
	case len(e.ID) < minEventID, len(e.ID) > maxEventID, !eventIDChars.MatchString(e.ID):
		return fmt.Errorf("gcal: event id %q is not 5 to 1024 base32hex characters", e.ID)
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
// is safe. Nothing connected is ErrNotConnected, no calendar chosen is
// ErrNoCalendar, a chosen calendar that was deleted or is no longer
// writable is ErrCalendarUnavailable, a grant Google no longer accepts is
// ErrReconnect, and any other Google failure is a *CalendarError.
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
	row, err := s.connection(ctx)
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
	base := s.calendarAPI + "/calendars/" + url.PathEscape(row.CalendarID.String) + "/events"
	for _, e := range events {
		if err := s.writeEvent(ctx, access, base, e); err != nil {
			return err
		}
	}
	return record()
}

// writeEvent inserts e at base, the chosen calendar's events collection,
// and updates it when Google already has its id.
func (s *Service) writeEvent(ctx context.Context, access, base string, e Event) error {
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

// eventRequest sends an event insert (POST) or update (PUT) and returns
// HTTP 200 or, for an insert, 409; every other answer is an error.
func (s *Service) eventRequest(ctx context.Context, access, method, target string, body []byte) (int, error) {
	what := "event insert"
	if method == http.MethodPut {
		what = "event update"
	}
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("gcal: build %s request: %w", what, err)
	}
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		var transport *url.Error
		if errors.As(err, &transport) {
			// The URL is dropped: after a refused redirect it is the
			// Google-supplied Location.
			return 0, &CalendarError{msg: fmt.Sprintf("gcal: %s request failed: %s: %v", what, transport.Op, transport.Err)}
		}
		return 0, &CalendarError{msg: fmt.Sprintf("gcal: %s request failed", what)}
	}
	defer resp.Body.Close()
	reply, err := io.ReadAll(io.LimitReader(resp.Body, maxEventBody))
	if err != nil {
		return 0, &CalendarError{msg: fmt.Sprintf("gcal: read %s response failed", what)}
	}
	switch {
	case resp.StatusCode == http.StatusOK:
		return http.StatusOK, nil
	case resp.StatusCode == http.StatusConflict && method == http.MethodPost:
		return http.StatusConflict, nil
	case resp.StatusCode == http.StatusUnauthorized:
		return 0, ErrReconnect
	case resp.StatusCode == http.StatusNotFound && method == http.MethodPost,
		resp.StatusCode == http.StatusForbidden && errorReason(reply) == "requiredAccessLevel":
		// An insert into a deleted calendar, or one the account no longer
		// sees, is not found; a write to one it can only read is
		// requiredAccessLevel. An update not found is the event missing
		// from the calendar after Google reported its id taken, which
		// says nothing about the calendar.
		return 0, ErrCalendarUnavailable
	}
	return 0, &CalendarError{msg: fmt.Sprintf("gcal: Google refused the %s (HTTP %d)", what, resp.StatusCode)}
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
