-- name: ListTriedCandidates :many
-- The ids of the dinners with status quest_log planned on date, on a plan
-- of any status, that have no Rotation check for date yet.
SELECT DISTINCT dinners.id
FROM plan_days
JOIN dinners ON dinners.id = plan_days.dinner_id
WHERE plan_days.date = sqlc.arg(date)
	AND dinners.status = 'quest_log'
	AND NOT EXISTS (
		SELECT 1 FROM rotation_reviews
		WHERE rotation_reviews.dinner_id = dinners.id
			AND rotation_reviews.plan_date = plan_days.date
	)
ORDER BY dinners.id;

-- name: MarkDinnerTried :execrows
-- Moves a dinner still in the Quest log to tried; a dinner with any other
-- status is left as it is (0 rows).
UPDATE dinners
SET status = 'tried', updated_at = ?
WHERE id = ? AND status = 'quest_log';

-- name: CreateRotationReview :exec
INSERT INTO rotation_reviews (id, dinner_id, plan_date, status, created_at, updated_at)
VALUES (?, ?, ?, 'pending', ?, ?);

-- name: ListPendingRotationReviews :many
-- The pending Rotation checks whose dinner is still tried, oldest plan
-- date first, with their dinners.
SELECT rotation_reviews.plan_date, sqlc.embed(dinners)
FROM rotation_reviews
JOIN dinners ON dinners.id = rotation_reviews.dinner_id
WHERE rotation_reviews.status = 'pending' AND dinners.status = 'tried'
ORDER BY rotation_reviews.plan_date, rotation_reviews.id;

-- name: GetPendingRotationReview :one
-- The pending Rotation check of dinner_id. MarkTried makes one only for a
-- dinner in the Quest log, which it moves to tried, so a dinner has at
-- most one. Returns sql.ErrNoRows when there is none.
SELECT * FROM rotation_reviews
WHERE dinner_id = ? AND status = 'pending';

-- name: CountRotationReviews :one
-- How many Rotation checks dinner_id has had, pending or decided.
SELECT COUNT(*) FROM rotation_reviews WHERE dinner_id = ?;

-- name: DecideRotationReview :execrows
-- Records the household's decision on a pending Rotation check, with the
-- dinner's name for a drop and NULL otherwise; one already decided is left
-- as it is (0 rows).
UPDATE rotation_reviews
SET status = 'decided',
	decision = sqlc.arg(decision),
	name = sqlc.narg(name),
	decided_at = sqlc.arg(now),
	updated_by = sqlc.arg(updated_by),
	updated_at = sqlc.arg(now)
WHERE id = sqlc.arg(id) AND status = 'pending';

-- name: MoveTriedDinner :execrows
-- Moves a dinner still tried to status (regular, archived or quest_log)
-- by a Rotation check decision, recording updated_by; its origin is kept.
-- A dinner no longer tried is left as it is (0 rows).
UPDATE dinners
SET status = sqlc.arg(status), updated_by = sqlc.arg(updated_by), updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id) AND status = 'tried';
