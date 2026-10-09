-- name: GetNotificationPref :one
SELECT * FROM notification_prefs
WHERE event = ?;

-- name: InsertNotification :execrows
-- Stores a pending notification for one channel, due at once. A row already
-- stored for the event key and channel, in any status, is left unchanged and
-- no row is inserted.
INSERT INTO notification_outbox (
	id, event_key, channel, payload, status, attempts, next_attempt_at, created_at, updated_at
) VALUES (
	sqlc.arg(id), sqlc.arg(event_key), sqlc.arg(channel), sqlc.arg(payload), 'pending', 0, sqlc.arg(next_attempt_at), sqlc.arg(now), sqlc.arg(now)
)
ON CONFLICT (event_key, channel) DO NOTHING;

-- name: ClaimDueNotification :one
-- Leases the pending notification due longest ago by moving its
-- next_attempt_at to lease_until, and returns it. A claimed row is not due
-- again until the lease ends, so no other claim takes it while it is being
-- delivered. Returns sql.ErrNoRows when none is due.
UPDATE notification_outbox
SET next_attempt_at = sqlc.arg(lease_until), updated_at = sqlc.arg(now)
WHERE id = (
	SELECT id FROM notification_outbox
	WHERE status = 'pending' AND next_attempt_at <= sqlc.arg(now)
	ORDER BY next_attempt_at, id
	LIMIT 1
)
RETURNING *;

-- name: MarkNotificationSent :execrows
-- Records a delivered notification; updated_at is when it was sent. The
-- lease must still be the claim's.
UPDATE notification_outbox
SET status = 'sent', attempts = attempts + 1, next_attempt_at = NULL, updated_at = sqlc.arg(now)
WHERE id = sqlc.arg(id) AND status = 'pending' AND next_attempt_at = sqlc.arg(lease_until);

-- name: RetryNotification :execrows
-- Records a failed delivery and when to try again. The lease must still be
-- the claim's.
UPDATE notification_outbox
SET attempts = attempts + 1, next_attempt_at = sqlc.arg(next_attempt_at), last_error = sqlc.arg(last_error), updated_at = sqlc.arg(now)
WHERE id = sqlc.arg(id) AND status = 'pending' AND next_attempt_at = sqlc.arg(lease_until);

-- name: FailNotification :execrows
-- Gives up on a notification. attempts counts the delivery attempt that
-- failed, if one was made. The lease must still be the claim's.
UPDATE notification_outbox
SET status = 'failed', attempts = attempts + sqlc.arg(attempted), next_attempt_at = NULL, last_error = sqlc.arg(last_error), updated_at = sqlc.arg(now)
WHERE id = sqlc.arg(id) AND status = 'pending' AND next_attempt_at = sqlc.arg(lease_until);

-- name: NextNotificationDue :one
-- Returns when the next pending notification is due. Returns sql.ErrNoRows
-- when none is pending.
SELECT next_attempt_at FROM notification_outbox
WHERE status = 'pending'
ORDER BY next_attempt_at
LIMIT 1;
