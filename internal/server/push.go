package server

import (
	"errors"
	"log/slog"
	"net/http"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/notify"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/push"
)

// MaxPushBody bounds a POST or DELETE /push/subscriptions body: an endpoint
// of push.MaxEndpointBytes, the keys and a user agent of
// push.MaxUserAgentBytes, with room for JSON escaping.
const MaxPushBody = 8 << 10

type vapidPublicKeyResponse struct {
	PublicKey string `json:"public_key"`
}

// pushSubscriptionBody is the POST /push/subscriptions body: the
// endpoint and keys of PushSubscription.toJSON(), without its
// expirationTime, and an optional user_agent.
type pushSubscriptionBody struct {
	Endpoint  string               `json:"endpoint"`
	Keys      pushSubscriptionKeys `json:"keys"`
	UserAgent *string              `json:"user_agent"`
}

type pushSubscriptionKeys struct {
	P256dh string `json:"p256dh"`
	Auth   string `json:"auth"`
}

// pushUnsubscribeBody is the DELETE /push/subscriptions body.
type pushUnsubscribeBody struct {
	Endpoint string `json:"endpoint"`
}

// getVAPIDPublicKey answers GET /push/vapid-public-key with the key
// browsers pass as applicationServerKey. The private key is never served.
func getVAPIDPublicKey(logger *slog.Logger, svc *push.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, r, logger, http.StatusOK, vapidPublicKeyResponse{PublicKey: svc.PublicKey()})
	}
}

// postPushSubscription stores the browser's subscription by its endpoint
// and answers 200 with it, without its keys. Posting an endpoint already
// stored updates it.
func postPushSubscription(logger *slog.Logger, svc *push.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := decodePatch[pushSubscriptionBody](http.MaxBytesReader(w, r.Body, MaxPushBody))
		if err != nil {
			writeError(w, r, logger, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		sub, err := svc.Subscribe(r.Context(), push.SubscribeRequest{
			Endpoint:  body.Endpoint,
			P256dh:    body.Keys.P256dh,
			Auth:      body.Keys.Auth,
			UserAgent: body.UserAgent,
		})
		if err != nil {
			writePushError(w, r, logger, "store push subscription", err)
			return
		}
		writeJSON(w, r, logger, http.StatusOK, sub)
	}
}

// deletePushSubscription removes the subscription for the body's endpoint
// and answers 204, whether or not it was stored.
func deletePushSubscription(logger *slog.Logger, svc *push.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := decodePatch[pushUnsubscribeBody](http.MaxBytesReader(w, r.Body, MaxPushBody))
		if err != nil {
			writeError(w, r, logger, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		if _, err := svc.Unsubscribe(r.Context(), body.Endpoint); err != nil {
			writePushError(w, r, logger, "delete push subscription", err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// postTestPush answers POST /settings/notifications/test-push: it queues
// push.TestTitle for every device that turned on push, aimed at /plan, and
// answers 202 with the event key and how many subscriptions it goes to.
// Delivery follows through the notification outbox. With push switched
// off for plan_ready it is 409 push_off, whether or not any device has
// turned on push; otherwise with no subscription it is 409
// no_subscriptions. Neither sends anything. It never posts to Google Chat.
func postTestPush(logger *slog.Logger, svc *push.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sent, err := svc.SendTest(r.Context())
		switch {
		case errors.Is(err, push.ErrNoSubscriptions):
			writeError(w, r, logger, http.StatusConflict, "no_subscriptions",
				"no device has turned on notifications, so there is nothing to send the test to")
		case errors.Is(err, notify.ErrPushOff):
			writeError(w, r, logger, http.StatusConflict, "push_off",
				"push is switched off for New weekly plan ready in Notifications settings, so no test was sent")
		case err != nil:
			logger.ErrorContext(r.Context(), "send test push", "error", err)
			writeError(w, r, logger, http.StatusInternalServerError, "internal", "internal error")
		default:
			logger.InfoContext(r.Context(), "test push queued", "event_key", sent.EventKey, "subscriptions", sent.Subscriptions)
			writeJSON(w, r, logger, http.StatusAccepted, sent)
		}
	}
}

// writePushError maps a push.Service error to its API error: an invalid
// subscription is 400 invalid_subscription, anything else 500.
func writePushError(w http.ResponseWriter, r *http.Request, logger *slog.Logger, action string, err error) {
	var invalid *push.ValidationError
	if errors.As(err, &invalid) {
		writeError(w, r, logger, http.StatusBadRequest, "invalid_subscription", invalid.Error())
		return
	}
	logger.ErrorContext(r.Context(), action, "error", err)
	writeError(w, r, logger, http.StatusInternalServerError, "internal", "internal error")
}
