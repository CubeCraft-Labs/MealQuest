-- name: UpsertPushSubscription :one
-- Stores a browser's push subscription, keyed by its endpoint. Subscribing
-- an endpoint already stored replaces its keys and user agent and clears
-- its failure count, keeping its id, created_at and last_success_at.
INSERT INTO push_subscriptions (
	id, endpoint, p256dh, auth, user_agent, failure_count, created_at, updated_at
) VALUES (
	?, ?, ?, ?, ?, 0, ?, ?
)
ON CONFLICT (endpoint) DO UPDATE SET
	p256dh = excluded.p256dh,
	auth = excluded.auth,
	user_agent = excluded.user_agent,
	failure_count = 0,
	updated_at = excluded.updated_at
RETURNING *;

-- name: DeletePushSubscription :execrows
DELETE FROM push_subscriptions
WHERE endpoint = ?;

-- name: ListPushSubscriptions :many
-- Every stored subscription with its keys, for the push sender, oldest
-- first.
SELECT id, endpoint, p256dh, auth
FROM push_subscriptions
ORDER BY created_at, id;

-- name: RecordPushSuccess :exec
-- Records that the push service accepted a notification for a subscription.
UPDATE push_subscriptions
SET last_success_at = ?
WHERE id = ?;

-- name: RecordPushFailure :exec
-- Counts a delivery to a subscription that the push service did not accept.
UPDATE push_subscriptions
SET failure_count = failure_count + 1
WHERE id = ?;

-- name: DeletePushSubscriptionByID :exec
-- Removes a subscription the push service reports gone (404 or 410).
DELETE FROM push_subscriptions
WHERE id = ?;
