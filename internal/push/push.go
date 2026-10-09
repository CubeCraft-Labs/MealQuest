// Package push keeps the household's Web Push subscriptions, one per
// browser, keyed by the push service endpoint the browser was given, and
// the VAPID public key browsers subscribe with.
package push

import (
	"context"
	"crypto/ecdh"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/notify"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/vapid"
)

// Limits on a subscription's fields.
const (
	MaxEndpointBytes  = 2048
	MaxUserAgentBytes = 512
	p256dhSize        = 65 // uncompressed P-256 point
	authSize          = 16
)

// keyEncoding is the unpadded base64url PushSubscription.toJSON() gives
// the keys in.
var keyEncoding = base64.RawURLEncoding

// Subscription is a stored push subscription. Its keys are never returned:
// only the sender needs them.
type Subscription struct {
	ID        string  `json:"id"`
	Endpoint  string  `json:"endpoint"`
	UserAgent *string `json:"user_agent"`
	CreatedAt string  `json:"created_at"`
	UpdatedAt string  `json:"updated_at"`
}

// SubscribeRequest is a browser's push subscription: the endpoint and keys
// of its PushSubscription, and optionally a user agent to tell the
// household's devices apart.
type SubscribeRequest struct {
	Endpoint  string
	P256dh    string
	Auth      string
	UserAgent *string
}

// Problem is one invalid field of a request, named by its JSON key.
type Problem struct {
	Field  string
	Reason string
}

// ValidationError lists every invalid field of a request.
type ValidationError struct {
	Problems []Problem
}

func (e *ValidationError) Error() string {
	parts := make([]string, len(e.Problems))
	for i, p := range e.Problems {
		parts[i] = p.Field + ": " + p.Reason
	}
	return "invalid push subscription: " + strings.Join(parts, "; ")
}

// Service stores push subscriptions and sends a test notification to them.
type Service struct {
	st        *store.Store
	publicKey string
	notifier  *notify.Service
}

// NewService returns a Service over st handing out publicKey, the VAPID
// public key in vapid's encoding, that sends its test notification through
// notifier.
func NewService(st *store.Store, publicKey string, notifier *notify.Service) (*Service, error) {
	if st == nil {
		return nil, errors.New("push: nil store")
	}
	if _, err := vapid.ParsePublicKey(publicKey); err != nil {
		return nil, fmt.Errorf("push: VAPID public key: %w", err)
	}
	if notifier == nil {
		return nil, errors.New("push: nil notifier")
	}
	return &Service{st: st, publicKey: publicKey, notifier: notifier}, nil
}

// The test notification SendTest sends.
const (
	TestTitle = "MealQuest test notification"
	TestBody  = "Push works on this device. Tap to open the plan."
	TestURL   = "/plan"
)

// ErrNoSubscriptions is returned by SendTest when no device has turned on
// push: there is nothing to send the test to.
var ErrNoSubscriptions = errors.New("push: no device has turned on notifications")

// TestSent is a test notification queued for delivery: its event key and
// how many subscriptions were stored when it was queued.
type TestSent struct {
	EventKey      string `json:"event_key"`
	Subscriptions int    `json:"subscriptions"`
}

// SendTest queues TestTitle for every stored subscription, through the
// notification outbox as a plan_ready event whose tap opens TestURL. Its
// key, "plan_ready:test-" and a new id, is never used twice, so it is never
// taken for an event already sent. It goes to push alone, never to Google
// Chat. With push switched off for plan_ready it is notify.ErrPushOff,
// whether or not any device has turned on push; otherwise with no
// subscription it is ErrNoSubscriptions. Neither queues anything. Delivery
// happens after SendTest returns, with the outbox's retries.
func (s *Service) SendTest(ctx context.Context) (TestSent, error) {
	q := db.New(s.st.Reader)
	pref, err := q.GetNotificationPref(ctx, string(notify.PlanReady))
	if errors.Is(err, sql.ErrNoRows) {
		return TestSent{}, fmt.Errorf("push: no notification preference for %s", notify.PlanReady)
	}
	if err != nil {
		return TestSent{}, fmt.Errorf("push: read notification preference for %s: %w", notify.PlanReady, err)
	}
	if pref.Push != 1 {
		return TestSent{}, fmt.Errorf("%w: %s", notify.ErrPushOff, notify.PlanReady)
	}
	subs, err := q.ListPushSubscriptions(ctx)
	if err != nil {
		return TestSent{}, fmt.Errorf("push: list subscriptions: %w", err)
	}
	if len(subs) == 0 {
		return TestSent{}, ErrNoSubscriptions
	}
	key := string(notify.PlanReady) + ":test-" + store.NewID()
	err = s.notifier.EmitPush(ctx, notify.Event{
		Type:  notify.PlanReady,
		Key:   key,
		Title: TestTitle,
		Body:  TestBody,
		URL:   TestURL,
	})
	if err != nil {
		return TestSent{}, fmt.Errorf("push: send test notification: %w", err)
	}
	return TestSent{EventKey: key, Subscriptions: len(subs)}, nil
}

// PublicKey returns the VAPID public key, for applicationServerKey.
func (s *Service) PublicKey() string { return s.publicKey }

// Subscribe stores req by its endpoint. An endpoint already stored keeps
// its id and created_at and has its keys and user agent replaced, so
// subscribing twice leaves one subscription. An invalid request is a
// *ValidationError.
func (s *Service) Subscribe(ctx context.Context, req SubscribeRequest) (Subscription, error) {
	var problems []Problem
	if reason := checkEndpoint(req.Endpoint); reason != "" {
		problems = append(problems, Problem{"endpoint", reason})
	}
	if reason := checkP256dh(req.P256dh); reason != "" {
		problems = append(problems, Problem{"keys.p256dh", reason})
	}
	if reason := checkAuth(req.Auth); reason != "" {
		problems = append(problems, Problem{"keys.auth", reason})
	}
	var ua sql.NullString
	if req.UserAgent != nil {
		if reason := checkUserAgent(*req.UserAgent); reason != "" {
			problems = append(problems, Problem{"user_agent", reason})
		}
		ua = sql.NullString{String: *req.UserAgent, Valid: true}
	}
	if len(problems) > 0 {
		return Subscription{}, &ValidationError{Problems: problems}
	}

	now := store.Now().String()
	row, err := db.New(s.st.Writer).UpsertPushSubscription(ctx, db.UpsertPushSubscriptionParams{
		ID:        store.NewID(),
		Endpoint:  req.Endpoint,
		P256dh:    req.P256dh,
		Auth:      req.Auth,
		UserAgent: ua,
		CreatedAt: now,
		UpdatedAt: now,
	})
	if err != nil {
		return Subscription{}, fmt.Errorf("push: store subscription: %w", err)
	}
	sub := Subscription{ID: row.ID, Endpoint: row.Endpoint, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
	if row.UserAgent.Valid {
		sub.UserAgent = &row.UserAgent.String
	}
	return sub, nil
}

// Unsubscribe removes the subscription for endpoint and reports whether
// one was stored. An endpoint that is not stored is not an error: the
// browser may unsubscribe after a failed delivery already removed it. An
// invalid endpoint is a *ValidationError.
func (s *Service) Unsubscribe(ctx context.Context, endpoint string) (bool, error) {
	if reason := checkEndpoint(endpoint); reason != "" {
		return false, &ValidationError{Problems: []Problem{{"endpoint", reason}}}
	}
	n, err := db.New(s.st.Writer).DeletePushSubscription(ctx, endpoint)
	if err != nil {
		return false, fmt.Errorf("push: delete subscription: %w", err)
	}
	return n > 0, nil
}

// checkEndpoint returns why endpoint is not a push service URL, or "".
// Push services are always reached over https.
func checkEndpoint(endpoint string) string {
	if endpoint == "" {
		return "required"
	}
	if len(endpoint) > MaxEndpointBytes {
		return fmt.Sprintf("longer than %d bytes", MaxEndpointBytes)
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
		return "want an absolute https:// URL without credentials or fragment"
	}
	return ""
}

func checkP256dh(s string) string {
	if s == "" {
		return "required"
	}
	b, err := keyEncoding.DecodeString(s)
	if err != nil || len(b) != p256dhSize {
		return fmt.Sprintf("want the %d-byte uncompressed P-256 point in unpadded base64url", p256dhSize)
	}
	if _, err := ecdh.P256().NewPublicKey(b); err != nil {
		return "not a point on P-256"
	}
	return ""
}

func checkAuth(s string) string {
	if s == "" {
		return "required"
	}
	b, err := keyEncoding.DecodeString(s)
	if err != nil || len(b) != authSize {
		return fmt.Sprintf("want %d bytes in unpadded base64url", authSize)
	}
	return ""
}

func checkUserAgent(s string) string {
	switch {
	case strings.TrimSpace(s) == "":
		return "must not be blank; omit it instead"
	case len(s) > MaxUserAgentBytes:
		return fmt.Sprintf("longer than %d bytes", MaxUserAgentBytes)
	}
	return ""
}
