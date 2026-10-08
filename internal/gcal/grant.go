package gcal

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

// ReconnectReason is the last_sync_error stored when Google refuses the
// stored grant.
const ReconnectReason = "Google access was revoked or expired — reconnect"

const (
	// maxRetries is how many times a request Google answers with a
	// transient failure (HTTP 429 or 5xx) is sent again.
	maxRetries = 2
	// firstRetryDelay is the wait before the first retry; each later retry
	// waits twice as long as the one before.
	firstRetryDelay = 500 * time.Millisecond
)

// The states Status reports.
const (
	// StatusNotConnected: no Google account is connected.
	StatusNotConnected = "not_connected"
	// StatusConnected: a Google account is connected and its grant was last
	// accepted.
	StatusConnected = "connected"
	// StatusReconnectRequired: Google refused the stored grant, so it was
	// cleared; only connecting again restores Push to Calendar.
	StatusReconnectRequired = "reconnect_required"
)

// Status is the Google Calendar connection as the household sees it. It
// never holds a token.
type Status struct {
	State         string  `json:"status"`
	CalendarID    *string `json:"calendar_id"`
	CalendarName  *string `json:"calendar_name"`
	ConnectedAt   *string `json:"connected_at"`
	LastSyncError *string `json:"last_sync_error"`
}

// Status reports the connection: not connected, connected, or reconnect
// required, with the chosen calendar, when it was connected and the last
// sync error. It is one read of one row, so it takes no lock.
func (s *Service) Status(ctx context.Context) (Status, error) {
	row, err := s.connection(ctx)
	if errors.Is(err, ErrNotConnected) {
		return Status{State: StatusNotConnected}, nil
	}
	if err != nil {
		return Status{}, err
	}
	st := Status{
		State:         StatusConnected,
		CalendarID:    nullable(row.CalendarID),
		CalendarName:  nullable(row.CalendarName),
		ConnectedAt:   &row.ConnectedAt,
		LastSyncError: nullable(row.LastSyncError),
	}
	if row.RefreshTokenCt == nil {
		st.State = StatusReconnectRequired
	}
	return st, nil
}

func nullable(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	return &v.String
}

// recordRefusal returns err, first recording, when it is ErrReconnect, that
// Google refuses the stored grant: the refresh token is cleared and
// last_sync_error set to ReconnectReason, so Status reports reconnect
// required and no request offers Google the refused token again.
//
// The chosen calendar and every stored calendar event id are kept, and
// forgetEvents is deliberately not run: the events MealQuest pushed are
// still on the chosen calendar, so a stored calendar_event_id still names
// an event on the chosen calendar. Reconnecting keeps the choice (Complete),
// and the next push updates those events in place. The caller holds
// s.account, which every push and calendar choice also holds, so the
// refusal is recorded between them, never during one.
func (s *Service) recordRefusal(ctx context.Context, err error) error {
	if !errors.Is(err, ErrReconnect) {
		return err
	}
	cleared := s.st.WithTx(ctx, func(tx *sql.Tx) error {
		n, err := db.New(tx).ClearIntegrationsGoogleToken(ctx, db.ClearIntegrationsGoogleTokenParams{
			LastSyncError: sql.NullString{String: ReconnectReason, Valid: true},
			UpdatedAt:     store.Timestamp{Time: s.now().UTC().Truncate(time.Millisecond)}.String(),
		})
		if err != nil {
			return err
		}
		if n == 0 {
			// The lock is held since the connection was read, so only a
			// change outside this service gets here.
			return ErrNotConnected
		}
		return nil
	})
	if cleared != nil {
		return fmt.Errorf("gcal: record that Google refused the grant: %w", cleared)
	}
	return err
}

// transient reports whether Google's HTTP status is a failure worth
// retrying: rate limited (429) or a server error (5xx).
func transient(status int) bool {
	return status == http.StatusTooManyRequests || status >= http.StatusInternalServerError
}

// backoff waits before retry number attempt (1 for the first), or until ctx
// ends.
func (s *Service) backoff(ctx context.Context, attempt int) error {
	t := time.NewTimer(s.retryDelay << (attempt - 1))
	defer t.Stop()
	select {
	case <-ctx.Done():
		return fmt.Errorf("gcal: wait to retry: %w", ctx.Err())
	case <-t.C:
		return nil
	}
}

// send sends the request newRequest builds, and sends it again, up to
// maxRetries times, while Google answers it with a transient failure. It
// returns the final answer's status and up to limit bytes of its body. A
// transient failure on the last try is a retryable *CalendarError; what
// names the request in errors, which never hold the URL, a token or
// Google's response body.
func (s *Service) send(ctx context.Context, what string, limit int64, newRequest func() (*http.Request, error)) (int, []byte, error) {
	for attempt := 0; ; attempt++ {
		if attempt > 0 {
			if err := s.backoff(ctx, attempt); err != nil {
				return 0, nil, err
			}
		}
		req, err := newRequest()
		if err != nil {
			return 0, nil, fmt.Errorf("gcal: build %s request: %w", what, err)
		}
		resp, err := s.client.Do(req)
		if err != nil {
			var transport *url.Error
			if errors.As(err, &transport) {
				// The URL is dropped: after a refused redirect it is the
				// Google-supplied Location.
				return 0, nil, &CalendarError{msg: fmt.Sprintf("gcal: %s request failed: %s: %v", what, transport.Op, transport.Err)}
			}
			return 0, nil, &CalendarError{msg: fmt.Sprintf("gcal: %s request failed", what)}
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, limit))
		resp.Body.Close()
		if err != nil {
			return 0, nil, &CalendarError{msg: fmt.Sprintf("gcal: read %s response failed", what)}
		}
		if !transient(resp.StatusCode) {
			return resp.StatusCode, body, nil
		}
		if attempt == maxRetries {
			return 0, nil, &CalendarError{retryable: true,
				msg: fmt.Sprintf("gcal: Google could not take the %s (HTTP %d) in %d tries", what, resp.StatusCode, maxRetries+1)}
		}
	}
}
