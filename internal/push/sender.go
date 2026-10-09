package push

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/notify"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/vapid"
)

// pushTTL is how long a push service keeps a notification for a device
// that is offline.
const pushTTL = 12 * time.Hour

// maxDrainBytes bounds how much of a push service response body is read.
const maxDrainBytes = 4096

// notice is what the service worker shows (web/src/push/notification.ts).
type notice struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	URL   string `json:"url"`
	Tag   string `json:"tag"`
}

// Sender is the notify.Sender of the push channel: it sends a notification
// to every stored subscription, signed with the VAPID key pair.
//
// Per subscription, a notification the push service accepts records
// last_success_at; one it refuses or cannot be sent increments
// failure_count, and a 404 or 410 deletes the subscription, which the push
// service no longer has. Send succeeds when at least one subscription
// accepted the notification, and when there are no subscriptions at all.
//
// Errors and logs name a subscription by its id, never by its endpoint,
// which is a capability URL.
type Sender struct {
	st     *store.Store
	logger *slog.Logger
	// subscriber is the VAPID subject as webpush-go takes it: an https://
	// URL, or the bare email address of a mailto: subject.
	subscriber string
	publicKey  string
	privateKey string
	// client sends the push requests; tests replace it.
	client webpush.HTTPClient
	// now is the clock last_success_at is recorded on; tests replace it.
	now func() time.Time
}

// NewSender returns the push channel's sender over st, signing with the
// VAPID key pair publicKey and privateKey (vapid's encoding) and naming
// subject, a mailto: URI or https:// URL, as the contact.
func NewSender(st *store.Store, logger *slog.Logger, publicKey, privateKey, subject string) (*Sender, error) {
	if st == nil {
		return nil, errors.New("push: nil store")
	}
	if logger == nil {
		return nil, errors.New("push: nil logger")
	}
	pub, err := vapid.ParsePublicKey(publicKey)
	if err != nil {
		return nil, fmt.Errorf("push: VAPID public key: %w", err)
	}
	priv, err := vapid.ParsePrivateKey(privateKey)
	if err != nil {
		return nil, fmt.Errorf("push: VAPID private key: %w", err)
	}
	if err := vapid.CheckPair(pub, priv); err != nil {
		return nil, fmt.Errorf("push: VAPID keys: %w", err)
	}
	// webpush-go prefixes mailto: to any subject not starting with https:,
	// so a mailto: subject is handed over as its bare address.
	var subscriber string
	if addr, ok := strings.CutPrefix(subject, "mailto:"); ok && addr != "" {
		subscriber = addr
	} else if strings.HasPrefix(subject, "https://") {
		subscriber = subject
	} else {
		return nil, fmt.Errorf("push: VAPID subject %q is not a mailto: URI or https:// URL", subject)
	}
	return &Sender{
		st:         st,
		logger:     logger,
		subscriber: subscriber,
		publicKey:  publicKey,
		privateKey: privateKey,
		client:     &http.Client{},
		now:        time.Now,
	}, nil
}

// Send sends m to every stored subscription. It returns nil when at least
// one accepted it or none is stored, and otherwise an error listing why
// each failed, so the outbox retries. A database error is returned at once.
func (s *Sender) Send(ctx context.Context, m notify.Message) error {
	logger := s.logger.With("notification_id", m.ID, "event_key", m.EventKey)
	subs, err := db.New(s.st.Reader).ListPushSubscriptions(ctx)
	if err != nil {
		return fmt.Errorf("push: list subscriptions: %w", err)
	}
	if len(subs) == 0 {
		logger.InfoContext(ctx, "push notification has no subscriptions to send to; nothing sent")
		return nil
	}
	message, err := json.Marshal(notice{Title: m.Payload.Title, Body: m.Payload.Body, URL: m.Payload.URL, Tag: m.Payload.Tag})
	if err != nil {
		return fmt.Errorf("push: encode notification: %w", err)
	}

	q := db.New(s.st.Writer)
	delivered := 0
	var failures []string
	for _, sub := range subs {
		status, err := s.sendOne(ctx, message, sub)
		switch {
		case err == nil && status >= 200 && status < 300:
			delivered++
			at := sql.NullString{String: timestamp(s.now()), Valid: true}
			if err := q.RecordPushSuccess(ctx, db.RecordPushSuccessParams{LastSuccessAt: at, ID: sub.ID}); err != nil {
				return fmt.Errorf("push: record success of subscription %s: %w", sub.ID, err)
			}
			continue
		case err == nil && (status == http.StatusNotFound || status == http.StatusGone):
			failures = append(failures, fmt.Sprintf("subscription %s gone (status %d), removed", sub.ID, status))
			logger.InfoContext(ctx, "push subscription gone; removed", "subscription_id", sub.ID, "status", status)
			if err := q.DeletePushSubscriptionByID(ctx, sub.ID); err != nil {
				return fmt.Errorf("push: remove gone subscription %s: %w", sub.ID, err)
			}
			continue
		case err == nil:
			failures = append(failures, fmt.Sprintf("subscription %s: status %d", sub.ID, status))
		default:
			failures = append(failures, fmt.Sprintf("subscription %s: %v", sub.ID, err))
		}
		if err := q.RecordPushFailure(ctx, sub.ID); err != nil {
			return fmt.Errorf("push: record failure of subscription %s: %w", sub.ID, err)
		}
	}
	if delivered == 0 {
		return fmt.Errorf("push: no subscription accepted the notification: %s", strings.Join(failures, "; "))
	}
	if len(failures) > 0 {
		logger.WarnContext(ctx, "push notification not accepted by every subscription",
			"delivered", delivered, "failures", strings.Join(failures, "; "))
	}
	return nil
}

// sendOne sends message to sub and returns the push service's status. Its
// error never quotes the endpoint.
func (s *Sender) sendOne(ctx context.Context, message []byte, sub db.ListPushSubscriptionsRow) (int, error) {
	resp, err := webpush.SendNotificationWithContext(ctx, message, &webpush.Subscription{
		Endpoint: sub.Endpoint,
		Keys:     webpush.Keys{Auth: sub.Auth, P256dh: sub.P256dh},
	}, &webpush.Options{
		HTTPClient:      s.client,
		Subscriber:      s.subscriber,
		TTL:             int(pushTTL / time.Second),
		VAPIDPublicKey:  s.publicKey,
		VAPIDPrivateKey: s.privateKey,
	})
	if err != nil {
		// A *url.Error quotes the request URL, the endpoint; keep only
		// its cause.
		var ue *url.Error
		if errors.As(err, &ue) {
			return 0, fmt.Errorf("request failed: %w", ue.Err)
		}
		return 0, err
	}
	defer resp.Body.Close()
	// The status is the outcome; the body is read only so the connection
	// can be reused.
	io.CopyN(io.Discard, resp.Body, maxDrainBytes)
	return resp.StatusCode, nil
}

// timestamp returns t in the stored UTC RFC 3339 form.
func timestamp(t time.Time) string {
	return store.Timestamp{Time: t.UTC().Truncate(time.Millisecond)}.String()
}
