package gcal

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"golang.org/x/oauth2"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

const (
	// calendarPageSize is the calendarList page size: Google's maximum.
	calendarPageSize = 250
	// maxCalendarPages caps how many calendarList pages a listing reads.
	maxCalendarPages = 20
	// maxCalendarBody caps how much of one calendarList page is read.
	maxCalendarBody = 1 << 20
)

var (
	// ErrReconnect means Google no longer accepts the stored grant: the
	// household revoked MealQuest's access in their Google account, or the
	// refresh token expired. Only connecting again restores it.
	ErrReconnect = errors.New("gcal: Google no longer accepts MealQuest's access; connect Google Calendar again")
	// ErrUnknownCalendar means the calendar chosen is not one the connected
	// account can push to.
	ErrUnknownCalendar = errors.New("gcal: not a calendar MealQuest can add dinners to")
)

// Calendar is one calendar the connected account can push to.
type Calendar struct {
	ID         string `json:"id"`
	Summary    string `json:"summary"`
	Primary    bool   `json:"primary"`
	AccessRole string `json:"accessRole"`
}

// Selection is the calendar MealQuest pushes to.
type Selection struct {
	CalendarID   string `json:"calendar_id"`
	CalendarName string `json:"calendar_name"`
}

// CalendarError is a Calendar API or token refresh request Google refused
// or that failed in transit. Its text never holds a token or Google's
// response body.
type CalendarError struct {
	msg string
}

func (e *CalendarError) Error() string { return e.msg }

// Calendars lists the connected account's calendars it can push to: those
// it is a writer or owner of, in Google's order.
func (s *Service) Calendars(ctx context.Context) ([]Calendar, error) {
	s.account.Lock()
	defer s.account.Unlock()
	return s.calendars(ctx)
}

// SelectCalendar makes id the calendar MealQuest pushes to, storing its id
// and name. id must be among Calendars at the time of the call; anything
// else, including a calendar chosen earlier that has since been deleted or
// is no longer writable, is ErrUnknownCalendar and leaves the stored
// choice as it was.
func (s *Service) SelectCalendar(ctx context.Context, id string) (Selection, error) {
	s.account.Lock()
	defer s.account.Unlock()
	cals, err := s.calendars(ctx)
	if err != nil {
		return Selection{}, err
	}
	for _, c := range cals {
		if c.ID != id {
			continue
		}
		sel := Selection{CalendarID: c.ID, CalendarName: c.Summary}
		n, err := db.New(s.st.Writer).SetIntegrationsGoogleCalendar(ctx, db.SetIntegrationsGoogleCalendarParams{
			CalendarID:   sql.NullString{String: sel.CalendarID, Valid: true},
			CalendarName: sql.NullString{String: sel.CalendarName, Valid: true},
			UpdatedAt:    store.Timestamp{Time: s.now().UTC().Truncate(time.Millisecond)}.String(),
		})
		if err != nil {
			return Selection{}, fmt.Errorf("gcal: save calendar: %w", err)
		}
		if n != 1 {
			// The lock is held since the connection was read, so only a
			// change outside this service gets here.
			return Selection{}, ErrNotConnected
		}
		return sel, nil
	}
	return Selection{}, ErrUnknownCalendar
}

// calendars lists the writable calendars, each once, with the stored refresh
// token. The caller holds s.account.
func (s *Service) calendars(ctx context.Context) ([]Calendar, error) {
	access, err := s.accessToken(ctx)
	if err != nil {
		return nil, err
	}
	cals := []Calendar{}
	// listed holds the ids in cals: pages can overlap, and a calendar is
	// listed once, as Google first lists it.
	listed := map[string]bool{}
	pageToken := ""
	for range maxCalendarPages {
		page, err := s.calendarPage(ctx, access, pageToken)
		if err != nil {
			return nil, err
		}
		for _, item := range page.Items {
			if item.ID == "" {
				return nil, &CalendarError{msg: "gcal: Google listed a calendar without an id"}
			}
			if (item.AccessRole == "writer" || item.AccessRole == "owner") && !listed[item.ID] {
				listed[item.ID] = true
				cals = append(cals, Calendar{ID: item.ID, Summary: item.Summary, Primary: item.Primary, AccessRole: item.AccessRole})
			}
		}
		if page.NextPageToken == "" {
			return cals, nil
		}
		pageToken = page.NextPageToken
	}
	return nil, &CalendarError{msg: fmt.Sprintf("gcal: Google listed more than %d pages of calendars", maxCalendarPages)}
}

// accessToken trades the stored refresh token for an access token. A grant
// Google reports as invalid_grant is ErrReconnect.
func (s *Service) accessToken(ctx context.Context) (string, error) {
	row, err := db.New(s.st.Reader).GetIntegrationsGoogle(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotConnected
	}
	if err != nil {
		return "", fmt.Errorf("gcal: read Google integration: %w", err)
	}
	refresh, err := s.box.Decrypt(row.RefreshTokenCt)
	if err != nil {
		return "", fmt.Errorf("gcal: open refresh token: %w", err)
	}
	token, err := s.oauth.TokenSource(context.WithValue(ctx, oauth2.HTTPClient, s.client),
		&oauth2.Token{RefreshToken: string(refresh)}).Token()
	if err != nil {
		var retrieve *oauth2.RetrieveError
		var transport *url.Error
		switch {
		case errors.As(err, &retrieve) && retrieve.ErrorCode == "invalid_grant":
			return "", ErrReconnect
		case errors.As(err, &retrieve):
			return "", &CalendarError{msg: fmt.Sprintf("gcal: Google refused the token refresh (HTTP %d)", retrieve.Response.StatusCode)}
		case errors.As(err, &transport):
			// The URL is dropped: after a refused redirect it is the
			// Google-supplied Location.
			return "", &CalendarError{msg: fmt.Sprintf("gcal: token refresh failed: %s: %v", transport.Op, transport.Err)}
		default:
			return "", &CalendarError{msg: "gcal: token refresh returned an unusable response"}
		}
	}
	return token.AccessToken, nil
}

// calendarListPage is the part of a calendarList page MealQuest reads.
type calendarListPage struct {
	Items []struct {
		ID         string `json:"id"`
		Summary    string `json:"summary"`
		Primary    bool   `json:"primary"`
		AccessRole string `json:"accessRole"`
	} `json:"items"`
	NextPageToken string `json:"nextPageToken"`
}

// calendarPage fetches the calendarList page at pageToken ("" for the
// first) with access. HTTP 401 means Google no longer accepts the grant.
func (s *Service) calendarPage(ctx context.Context, access, pageToken string) (calendarListPage, error) {
	q := url.Values{"maxResults": {fmt.Sprint(calendarPageSize)}}
	if pageToken != "" {
		q.Set("pageToken", pageToken)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.calendarAPI+"/users/me/calendarList?"+q.Encode(), nil)
	if err != nil {
		return calendarListPage{}, fmt.Errorf("gcal: build calendar list request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+access)
	resp, err := s.client.Do(req)
	if err != nil {
		var transport *url.Error
		if errors.As(err, &transport) {
			return calendarListPage{}, &CalendarError{msg: fmt.Sprintf("gcal: calendar list request failed: %s: %v", transport.Op, transport.Err)}
		}
		return calendarListPage{}, &CalendarError{msg: "gcal: calendar list request failed"}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxCalendarBody+1))
	if err != nil {
		return calendarListPage{}, &CalendarError{msg: "gcal: read calendar list failed"}
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return calendarListPage{}, ErrReconnect
	case resp.StatusCode != http.StatusOK:
		return calendarListPage{}, &CalendarError{msg: fmt.Sprintf("gcal: Google refused the calendar list (HTTP %d)", resp.StatusCode)}
	case len(body) > maxCalendarBody:
		return calendarListPage{}, &CalendarError{msg: fmt.Sprintf("gcal: calendar list page exceeds %d bytes", maxCalendarBody)}
	}
	var page calendarListPage
	if err := json.Unmarshal(body, &page); err != nil {
		return calendarListPage{}, &CalendarError{msg: "gcal: Google returned an unreadable calendar list"}
	}
	return page, nil
}
