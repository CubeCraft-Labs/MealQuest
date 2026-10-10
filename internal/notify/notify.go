// Package notify decides who is told what and delivers it reliably. It is
// the only package that reads notification_prefs to choose channels.
//
// Emit is the one way to notify the household: it reads the preference row
// of the event's type and, for each channel switched on, stores one pending
// row in notification_outbox keyed by (event key, channel). The event key is
// the caller's idempotency key, so emitting the same event twice stores
// nothing the second time and never touches a row already stored, whatever
// its status. EmitTx does the same inside the caller's transaction, so an
// event is stored with the change it is about; the caller then calls Wake.
// EmitPush stores an event for the push channel alone, for a test of push
// delivery that must not reach Google Chat.
//
// The worker started by Start delivers due rows through the Sender
// registered for their channel. A delivered row is marked sent. A failed
// delivery is retried after a backoff of one minute doubling up to one hour,
// until 24 hours after the row was created; the delivery that fails at or
// after that point, or with an error wrapping ErrUndeliverable, marks it
// failed, with the error in last_error.
package notify

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

// EventType is what a notification is about. Each has one row in
// notification_prefs saying which channels it goes to.
type EventType string

// The event types, as notification_prefs.event names them.
const (
	PlanReady      EventType = "plan_ready"
	ListReady      EventType = "list_ready"
	DayOfReminder  EventType = "day_of_reminder"
	NewSuggestions EventType = "new_suggestions"
	RotationReview EventType = "rotation_review"
)

// eventTypes is every EventType.
var eventTypes = map[EventType]bool{
	PlanReady: true, ListReady: true, DayOfReminder: true, NewSuggestions: true, RotationReview: true,
}

// Channel is a way of telling the household, as notification_outbox.channel
// names it.
type Channel string

// The channels.
const (
	ChannelPush Channel = "push"
	ChannelChat Channel = "chat"
)

// Event is something to tell the household.
type Event struct {
	// Type is what the event is about.
	Type EventType
	// Key identifies this event: it starts with Type and a colon, as in
	// "plan_ready:2026-10-12". An event emitted again with the same key is
	// ignored.
	Key string
	// Title is the notification's headline; it must not be blank.
	Title string
	// Body is the notification's text; it may be empty.
	Body string
	// URL is the path-absolute, same-origin path a tap on the notification
	// opens, such as "/plan".
	URL string
	// Week is the Monday of the week the event concerns, for the senders'
	// message builders; zero when the event concerns no week.
	Week store.LocalDate
}

// Payload is the JSON stored in notification_outbox.payload and handed to
// a Sender: the one shape every channel delivers from. Its title, body, tag
// and url are the notice the service worker shows (web/src/push/notification.ts),
// which ignores the other fields.
type Payload struct {
	Event EventType `json:"event"`
	Title string    `json:"title"`
	Body  string    `json:"body"`
	// URL is a path-absolute, same-origin path. A sender that needs an
	// absolute URL resolves it against the public URL.
	URL string `json:"url"`
	// Tag is the event key, so a notification shown again for the same
	// event replaces the first.
	Tag string `json:"tag"`
	// Week is the YYYY-MM-DD Monday of the event's week, or absent.
	Week string `json:"week,omitempty"`
}

// ErrInvalidEvent is returned by Emit for an event it refuses; nothing is
// stored.
var ErrInvalidEvent = errors.New("notify: invalid event")

// ErrPushOff is returned by EmitPush when the event type's preference
// switches push off; nothing is stored.
var ErrPushOff = errors.New("notify: push is switched off for this event")

// Service emits notifications and runs the worker that delivers them.
type Service struct {
	st     *store.Store
	logger *slog.Logger
	// now is the clock rows are created, claimed and retried on; tests
	// replace it.
	now func() time.Time
	// wake holds up to one token; Emit adds one so the worker looks for
	// the rows it stored.
	wake chan struct{}

	mu      sync.Mutex
	senders map[Channel]Sender
	started bool

	wg sync.WaitGroup
}

// NewService returns a service over st with no senders registered.
func NewService(st *store.Store, logger *slog.Logger) (*Service, error) {
	if st == nil {
		return nil, errors.New("notify: nil store")
	}
	if logger == nil {
		return nil, errors.New("notify: nil logger")
	}
	return &Service{
		st:      st,
		logger:  logger,
		now:     time.Now,
		wake:    make(chan struct{}, 1),
		senders: make(map[Channel]Sender),
	}, nil
}

// Emit stores one pending notification of ev for each channel its type's
// preference switches on, and wakes the worker. A channel already holding a
// row with ev.Key is left as it is. An invalid event is ErrInvalidEvent and
// stores nothing; a type with no preference row is an error.
func (s *Service) Emit(ctx context.Context, ev Event) error {
	err := s.st.WithTx(ctx, func(tx *sql.Tx) error {
		return s.EmitTx(ctx, tx, ev)
	})
	if err != nil {
		return err
	}
	s.Wake()
	return nil
}

// EmitPush stores one pending push notification of ev and wakes the
// worker, as Emit does but never for Google Chat, whatever the preference
// says of it. When the preference of ev's type switches push off it is
// ErrPushOff and stores nothing. It is for a test of push delivery, which
// must not post to the household's Chat space.
func (s *Service) EmitPush(ctx context.Context, ev Event) error {
	err := s.st.WithTx(ctx, func(tx *sql.Tx) error {
		return s.emitTx(ctx, tx, ev, func(pref db.NotificationPref) ([]Channel, error) {
			if pref.Push != 1 {
				return nil, fmt.Errorf("%w: %s", ErrPushOff, ev.Type)
			}
			return []Channel{ChannelPush}, nil
		})
	})
	if err != nil {
		return err
	}
	s.Wake()
	return nil
}

// EmitTx stores ev's notifications as Emit does, in tx, so they are
// committed with the change the event is about or not at all. The caller
// calls Wake once tx has committed.
func (s *Service) EmitTx(ctx context.Context, tx *sql.Tx, ev Event) error {
	return s.emitTx(ctx, tx, ev, func(pref db.NotificationPref) ([]Channel, error) {
		return enabledChannels(pref), nil
	})
}

// emitTx stores ev in tx for each channel that channels chooses from the
// preference of ev's type.
func (s *Service) emitTx(ctx context.Context, tx *sql.Tx, ev Event, channels func(db.NotificationPref) ([]Channel, error)) error {
	if err := validate(ev); err != nil {
		return err
	}
	payload := Payload{Event: ev.Type, Title: ev.Title, Body: ev.Body, URL: ev.URL, Tag: ev.Key}
	if !ev.Week.IsZero() {
		payload.Week = ev.Week.String()
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("notify: encode payload of %s: %w", ev.Key, err)
	}

	q := db.New(tx)
	pref, err := q.GetNotificationPref(ctx, string(ev.Type))
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("notify: no notification preference for %s", ev.Type)
	}
	if err != nil {
		return fmt.Errorf("notify: read notification preference for %s: %w", ev.Type, err)
	}
	chs, err := channels(pref)
	if err != nil {
		return err
	}
	now := timestamp(s.now())
	var queued []Channel
	for _, ch := range chs {
		n, err := q.InsertNotification(ctx, db.InsertNotificationParams{
			ID:            store.NewID(),
			EventKey:      ev.Key,
			Channel:       string(ch),
			Payload:       string(data),
			NextAttemptAt: sql.NullString{String: now, Valid: true},
			Now:           now,
		})
		if err != nil {
			return fmt.Errorf("notify: store %s notification %s: %w", ch, ev.Key, err)
		}
		if n == 1 {
			queued = append(queued, ch)
		}
	}
	s.logger.InfoContext(ctx, "notification stored in transaction", "event_key", ev.Key, "queued", queued)
	return nil
}

// ChannelOn reports whether the preference of event type ev switches
// channel ch on. A type with no preference row is an error.
func (s *Service) ChannelOn(ctx context.Context, ev EventType, ch Channel) (bool, error) {
	pref, err := db.New(s.st.Reader).GetNotificationPref(ctx, string(ev))
	if errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("notify: no notification preference for %s", ev)
	}
	if err != nil {
		return false, fmt.Errorf("notify: read notification preference for %s: %w", ev, err)
	}
	return slices.Contains(enabledChannels(pref), ch), nil
}

// enabledChannels returns the channels pref switches on.
func enabledChannels(pref db.NotificationPref) []Channel {
	var chs []Channel
	if pref.Push == 1 {
		chs = append(chs, ChannelPush)
	}
	if pref.Chat == 1 {
		chs = append(chs, ChannelChat)
	}
	return chs
}

// validate reports why ev cannot be emitted, wrapping ErrInvalidEvent.
func validate(ev Event) error {
	if !eventTypes[ev.Type] {
		return fmt.Errorf("%w: unknown type %q", ErrInvalidEvent, ev.Type)
	}
	prefix := string(ev.Type) + ":"
	if !strings.HasPrefix(ev.Key, prefix) || len(ev.Key) == len(prefix) {
		return fmt.Errorf("%w: key %q does not start with %q and an identifier", ErrInvalidEvent, ev.Key, prefix)
	}
	if strings.TrimSpace(ev.Title) == "" {
		return fmt.Errorf("%w: %s has a blank title", ErrInvalidEvent, ev.Key)
	}
	if !isAppPath(ev.URL) {
		return fmt.Errorf("%w: %s url %q is not a path-absolute same-origin path", ErrInvalidEvent, ev.Key, ev.URL)
	}
	return nil
}

// isAppPath reports whether raw is a path on the app's own origin, as the
// service worker requires: it starts with one slash and names no scheme or
// host, however it is parsed.
func isAppPath(raw string) bool {
	if !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") || strings.ContainsAny(raw, "\\\t\r\n") {
		return false
	}
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "" && u.Host == "" && !strings.HasPrefix(u.Path, "//")
}

// timestamp returns t in the stored UTC RFC 3339 form.
func timestamp(t time.Time) string {
	return store.Timestamp{Time: t.UTC().Truncate(time.Millisecond)}.String()
}

// parseTimestamp parses a stored timestamp.
func parseTimestamp(s string) (time.Time, error) {
	var ts store.Timestamp
	if err := ts.Scan(s); err != nil {
		return time.Time{}, err
	}
	return ts.Time, nil
}
