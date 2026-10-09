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
