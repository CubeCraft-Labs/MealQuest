package notify

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/dinners"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/jobs"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

// Delivery timing.
const (
	// retryFirst is the wait after a row's first failed delivery; each
	// further failure doubles it, up to retryMax.
	retryFirst = time.Minute
	retryMax   = time.Hour
	// giveUpAfter is how long after its creation a row may be delivered.
	// The last retry is due at that moment; a row claimed then or later is
	// marked failed without being sent, and a delivery failing at or
	// after it marks the row failed.
	giveUpAfter = 24 * time.Hour
	// sendTimeout bounds one Send.
	sendTimeout = 30 * time.Second
	// leaseFor is how long a claimed row is not due again. It outlasts
	// sendTimeout and the final write, so a row is only claimed again when
	// the process that claimed it stopped before recording the delivery.
	leaseFor = 2 * time.Minute
	// pollRetryMin and pollRetryMax bound the backoff after a database
	// error while claiming rows.
	pollRetryMin = time.Second
	pollRetryMax = time.Minute
)

// Message is one notification for a Sender to deliver.
type Message struct {
	// ID is the notification_outbox row id.
	ID string
	// EventKey is the event's idempotency key.
	EventKey string
	Channel  Channel
	Payload  Payload
	// Attempt is this delivery's number, from 1.
	Attempt int64
}

// ErrUndeliverable is wrapped by a Sender's error when retrying cannot
// help, such as a channel refusing the message outright: the worker marks
// the row failed at once instead of retrying it.
var ErrUndeliverable = errors.New("notify: undeliverable")

// Sender delivers notifications on one channel. Send returns nil once the
// channel has accepted m, and an error otherwise; the worker retries it,
// unless the error wraps ErrUndeliverable.
// The error's text is stored in notification_outbox.last_error and logged,
// so it must never carry a secret: no webhook URL, key or token (a
// *url.Error quotes its URL).
type Sender interface {
	Send(ctx context.Context, m Message) error
}

// Register makes sender the Sender of ch. It must be called before Start,
// once per channel.
func (s *Service) Register(ch Channel, sender Sender) error {
	if ch != ChannelPush && ch != ChannelChat {
		return fmt.Errorf("notify: register: unknown channel %q", ch)
	}
	if sender == nil {
		return fmt.Errorf("notify: register %s: nil sender", ch)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return fmt.Errorf("notify: register %s: worker already started", ch)
	}
	if _, ok := s.senders[ch]; ok {
		return fmt.Errorf("notify: register %s: channel already has a sender", ch)
	}
	s.senders[ch] = sender
	return nil
}

// Start starts the worker. It stops when ctx is cancelled; Wait waits for
// it. A row whose delivery is cut short by ctx keeps its lease and is
// delivered again once the lease ends.
func (s *Service) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return errors.New("notify: worker already started")
	}
	s.started = true
	s.wg.Add(1)
	go s.work(ctx)
	return nil
}

// Wait blocks until the worker has stopped.
func (s *Service) Wait() {
	s.wg.Wait()
}

// Wake tells the worker to look for due rows, as after EmitTx's
// transaction commits.
func (s *Service) Wake() {
	select {
	case s.wake <- struct{}{}:
	default: // the worker already has a token and will look again
	}
}

// work delivers due rows until ctx is cancelled, then sleeps until the next
// pending row is due or Emit stores one. A database error is retried with
// backoff.
func (s *Service) work(ctx context.Context) {
	defer s.wg.Done()
	backoff := pollRetryMin
	for ctx.Err() == nil {
		wait, err := s.deliverDue(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			s.logger.ErrorContext(ctx, "deliver notifications failed; retrying", "error", err, "retry_in", backoff)
			wait, backoff = backoff, min(2*backoff, pollRetryMax)
		} else {
			backoff = pollRetryMin
		}
		var timeout <-chan time.Time
		var timer *time.Timer
		if wait >= 0 {
			timer = time.NewTimer(wait)
			timeout = timer.C
		}
		select {
		case <-s.wake:
		case <-timeout:
		case <-ctx.Done():
		}
		if timer != nil {
			timer.Stop()
		}
	}
}

// deliverDue delivers every row due now and returns how long until the next
// pending row is due, or -1 when none is pending.
func (s *Service) deliverDue(ctx context.Context) (time.Duration, error) {
	q := db.New(s.st.Writer)
	for ctx.Err() == nil {
		now := s.now()
		lease := sql.NullString{String: timestamp(now.Add(leaseFor)), Valid: true}
		row, err := q.ClaimDueNotification(ctx, db.ClaimDueNotificationParams{LeaseUntil: lease, Now: timestamp(now)})
		if errors.Is(err, sql.ErrNoRows) {
			break
		}
		if err != nil {
			return 0, fmt.Errorf("notify: claim notification: %w", err)
		}
		s.deliver(ctx, row)
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	next, err := db.New(s.st.Reader).NextNotificationDue(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return -1, nil
	}
	if err != nil {
		return 0, fmt.Errorf("notify: read next due notification: %w", err)
	}
	due, err := parseTimestamp(next.String)
	if err != nil {
		return 0, fmt.Errorf("notify: next due notification: %w", err)
	}
	return max(due.Sub(s.now()), 0), nil
}

// deliver sends a claimed row through its channel's sender, offering a
// recipe only as recipeNow allows, and records the outcome. A row that cannot be sent at all, because its payload does not
// decode or its channel has no sender, is failed at once: retrying cannot
// help. A row 24 hours or more past its creation is failed without being
// sent: it is too stale to deliver.
func (s *Service) deliver(ctx context.Context, row db.NotificationOutbox) {
	logger := s.logger.With("notification_id", row.ID, "event_key", row.EventKey, "channel", row.Channel)
	var payload Payload
	if err := json.Unmarshal([]byte(row.Payload), &payload); err != nil {
		logger.ErrorContext(ctx, "notification failed", "reason", "payload does not decode")
		s.giveUp(ctx, logger, row, 0, "payload does not decode")
		return
	}
	s.mu.Lock()
	sender, ok := s.senders[Channel(row.Channel)]
	s.mu.Unlock()
	if !ok {
		reason := fmt.Sprintf("no sender registered for channel %s", row.Channel)
		logger.ErrorContext(ctx, "notification failed", "reason", reason)
		s.giveUp(ctx, logger, row, 0, reason)
		return
	}

	created, err := parseTimestamp(row.CreatedAt)
	if err != nil {
		reason := fmt.Sprintf("created_at does not parse: %v", err)
		logger.ErrorContext(ctx, "notification failed", "reason", reason)
		s.giveUp(ctx, logger, row, 0, reason)
		return
	}
	deadline := created.Add(giveUpAfter)
	if !s.now().Before(deadline) {
		reason := "not delivered within 24 hours of creation"
		if row.LastError.Valid {
			reason += "; last error: " + row.LastError.String
		}
		logger.ErrorContext(ctx, "notification failed; giving up", "reason", reason)
		s.giveUp(ctx, logger, row, 0, reason)
		return
	}

	attempt := row.Attempts + 1
	sendCtx, cancel := context.WithTimeout(ctx, sendTimeout)
	payload, err = s.recipeNow(sendCtx, payload)
	if err == nil {
		err = sender.Send(sendCtx, Message{
			ID: row.ID, EventKey: row.EventKey, Channel: Channel(row.Channel), Payload: payload, Attempt: attempt,
		})
	}
	cancel()
	now := s.now()
	if err == nil {
		s.record(ctx, logger, "record notification sent", func(write context.Context) (int64, error) {
			return db.New(s.st.Writer).MarkNotificationSent(write, db.MarkNotificationSentParams{
				Now: timestamp(now), ID: row.ID, LeaseUntil: row.NextAttemptAt,
			})
		})
		logger.InfoContext(ctx, "notification sent", "attempt", attempt)
		return
	}
	if ctx.Err() != nil {
		logger.WarnContext(context.WithoutCancel(ctx), "notification delivery interrupted by shutdown; it is retried when its lease ends")
		return
	}

	if errors.Is(err, ErrUndeliverable) || !now.Before(deadline) {
		logger.ErrorContext(ctx, "notification failed; giving up", "attempt", attempt, "error", err)
		s.giveUp(ctx, logger, row, 1, err.Error())
		return
	}
	next := now.Add(retryDelay(attempt))
	if next.After(deadline) {
		next = deadline
	}
	logger.WarnContext(ctx, "notification delivery failed; retrying", "attempt", attempt, "error", err, "retry_at", timestamp(next))
	s.record(ctx, logger, "record notification retry", func(write context.Context) (int64, error) {
		return db.New(s.st.Writer).RetryNotification(write, db.RetryNotificationParams{
			NextAttemptAt: sql.NullString{String: timestamp(next), Valid: true},
			LastError:     sql.NullString{String: err.Error(), Valid: true},
			Now:           timestamp(now),
			ID:            row.ID,
			LeaseUntil:    row.NextAttemptAt,
		})
	})
}

// giveUp marks a claimed row failed with reason. attempted is 1 when a
// delivery was attempted, else 0.
func (s *Service) giveUp(ctx context.Context, logger *slog.Logger, row db.NotificationOutbox, attempted int64, reason string) {
	now := s.now()
	s.record(ctx, logger, "record notification failed", func(write context.Context) (int64, error) {
		return db.New(s.st.Writer).FailNotification(write, db.FailNotificationParams{
			Attempted:  attempted,
			LastError:  sql.NullString{String: reason, Valid: true},
			Now:        timestamp(now),
			ID:         row.ID,
			LeaseUntil: row.NextAttemptAt,
		})
	})
}

// record makes a row's final write for one delivery, even when ctx has been
// cancelled, bounded by jobs.FinalWriteTimeout. A write that fails, or that
// finds the row no longer leased to this claim, is logged; the row keeps
// its lease and is delivered again once the lease ends.
func (s *Service) record(ctx context.Context, logger *slog.Logger, what string, write func(context.Context) (int64, error)) {
	attempt, cancel := jobs.FinalWriteContext(ctx)
	defer cancel()
	n, err := write(attempt)
	if err != nil {
		logger.ErrorContext(context.WithoutCancel(ctx), what+" failed; it is delivered again when its lease ends", "error", err)
		return
	}
	if n != 1 {
		logger.ErrorContext(context.WithoutCancel(ctx), what, "error", "notification is no longer leased to this delivery")
	}
}

// retryDelay is the wait after the attempt-th failed delivery: retryFirst,
// doubling with each failure, at most retryMax.
func retryDelay(attempt int64) time.Duration {
	d := retryFirst
	for i := int64(1); i < attempt && d < retryMax; i++ {
		d *= 2
	}
	return min(d, retryMax)
}

// recipeNow returns p as it may be delivered now. A payload offering a
// dinner's recipe was stored when the dinner had one; the dinner may have
// been archived since. A dinner that still has a recipe is offered, named,
// with its recipe path, so a payload stored before payloads named their
// dinner names it too. One archived, or gone, offers none: the payload names
// no dinner, opens its week's plan, its title loses RecipeCallToAction and,
// with no body, it says ArchivedBody, as such a reminder emitted after the
// archival says (chat.DayOfReminder, chat.StartReminder). A recipe payload
// whose dinner or week cannot be read wraps ErrUndeliverable; a database
// error is returned as it is, so the row is retried.
func (s *Service) recipeNow(ctx context.Context, p Payload) (Payload, error) {
	if !OpensRecipe(p.URL) {
		return p, nil
	}
	var id string
	if p.Dinner != nil {
		id = p.Dinner.ID
	} else {
		u, err := url.Parse(p.URL)
		if err != nil {
			return Payload{}, fmt.Errorf("%w: recipe url %q does not parse: %v", ErrUndeliverable, p.URL, err)
		}
		id = strings.TrimPrefix(u.Path, "/dinner/")
	}
	if id == "" || strings.Contains(id, "/") {
		return Payload{}, fmt.Errorf("%w: recipe url %q names no dinner", ErrUndeliverable, p.URL)
	}
	d, err := db.New(s.st.Reader).GetDinner(ctx, id)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Payload{}, fmt.Errorf("notify: read dinner %s of recipe url %q: %w", id, p.URL, err)
	}
	if err == nil && d.Status != dinners.StatusArchived {
		if p.Dinner == nil {
			p.Dinner = &Dinner{ID: d.ID, Name: d.Name}
		}
		p.URL = RecipePath(p.Dinner.ID)
		return p, nil
	}
	week, err := store.ParseLocalDate(p.Week)
	if err != nil {
		return Payload{}, fmt.Errorf("%w: recipe url %q of an archived dinner has no week: %v", ErrUndeliverable, p.URL, err)
	}
	p.Dinner = nil
	p.URL = PlanPath(week)
	p.Title = strings.TrimSuffix(p.Title, RecipeCallToAction)
	if p.Body == "" {
		p.Body = ArchivedBody
	}
	return p, nil
}
