package server

import (
	"errors"
	"log/slog"
	"net/http"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/push"
)

// maxPushBody bounds a POST or DELETE /push/subscriptions body: an endpoint
// of push.MaxEndpointBytes, the keys and a user agent of
// push.MaxUserAgentBytes, with room for JSON escaping.
const maxPushBody = 8 << 10

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
		body, err := decodePatch[pushSubscriptionBody](http.MaxBytesReader(w, r.Body, maxPushBody))
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
		body, err := decodePatch[pushUnsubscribeBody](http.MaxBytesReader(w, r.Body, maxPushBody))
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
