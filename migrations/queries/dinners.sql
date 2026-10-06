-- name: CreateDinner :one
INSERT INTO dinners (
	id, name, normalized_name, status, origin, method, minutes_total,
	difficulty, cuisine, is_favorite, recipe_id, recipe_url, photo_id,
	grounding_status, updated_by, created_at, updated_at
) VALUES (
	?, ?, ?, ?, ?, ?, ?,
	?, ?, ?, ?, ?, ?,
	?, ?, ?, ?
)
RETURNING *;

-- name: GetDinner :one
SELECT * FROM dinners
WHERE id = ?;

-- name: ListDinnersByStatus :many
SELECT * FROM dinners
WHERE status = ?
ORDER BY normalized_name, id;

-- name: CountRegularDinners :one
-- How many Regulars the household has; archived dinners are not counted.
SELECT COUNT(*) FROM dinners
WHERE status = 'regular';

-- name: ListUnarchivedDinners :many
-- Every dinner a new or renamed dinner must not repeat.
SELECT * FROM dinners
WHERE status <> 'archived'
ORDER BY id;

-- name: UpdateDinner :one
-- An unknown id returns sql.ErrNoRows.
UPDATE dinners
SET name = ?,
	normalized_name = ?,
	status = ?,
	origin = ?,
	method = ?,
	minutes_total = ?,
	difficulty = ?,
	cuisine = ?,
	recipe_id = ?,
	photo_id = ?,
	grounding_status = ?,
	updated_by = ?,
	updated_at = ?
WHERE id = ?
RETURNING *;

-- name: ArchiveDinner :one
-- An unknown id returns sql.ErrNoRows.
UPDATE dinners
SET status = 'archived',
	updated_by = ?,
	updated_at = ?
WHERE id = ?
RETURNING *;

-- name: SetDinnerFavorite :one
-- An unknown id returns sql.ErrNoRows.
UPDATE dinners
SET is_favorite = ?,
	updated_by = ?,
	updated_at = ?
WHERE id = ?
RETURNING *;

-- name: ToggleDinnerFavorite :one
-- An unknown id returns sql.ErrNoRows.
UPDATE dinners
SET is_favorite = 1 - is_favorite,
	updated_by = ?,
	updated_at = ?
WHERE id = ?
RETURNING *;

-- name: GroundDinner :one
-- Records a dinner's grounding (grounded or manual) and clears any
-- grounding_error. An unknown id returns sql.ErrNoRows.
UPDATE dinners
SET recipe_id = ?,
	photo_id = ?,
	method = ?,
	minutes_total = ?,
	difficulty = ?,
	grounding_status = ?,
	grounding_error = NULL,
	updated_by = ?,
	updated_at = ?
WHERE id = ?
RETURNING *;

-- name: GroundPendingDinner :execrows
-- Records the background grounding of a dinner still pending under the
-- normalized name it was grounded for; a dinner grounded some other way or
-- renamed meanwhile is left as it is (0 rows).
UPDATE dinners
SET recipe_id = ?,
	photo_id = ?,
	method = ?,
	minutes_total = ?,
	difficulty = ?,
	grounding_status = 'grounded',
	grounding_error = NULL,
	updated_at = ?
WHERE id = ? AND grounding_status = 'pending' AND normalized_name = ?;

-- name: FailPendingDinnerGrounding :execrows
-- Records why the background grounding of a dinner still pending under the
-- normalized name it was grounded for failed; a dinner grounded some other
-- way or renamed meanwhile is left as it is (0 rows).
UPDATE dinners
SET grounding_status = 'failed',
	grounding_error = ?,
	updated_at = ?
WHERE id = ? AND grounding_status = 'pending' AND normalized_name = ?;

-- name: FailOrphanedPendingDinnerGroundings :execrows
-- Fails every dinner still pending whose grounding job is no longer queued
-- or running: the process stopped before the job, or before its handler
-- recorded a result. Run at boot, after the job runner fails the jobs a
-- previous process left running.
UPDATE dinners
SET grounding_status = 'failed',
	grounding_error = sqlc.arg(grounding_error),
	updated_at = sqlc.arg(updated_at)
WHERE grounding_status = 'pending'
	AND NOT EXISTS (
		SELECT 1 FROM jobs
		WHERE jobs.kind = sqlc.arg(kind)
			AND jobs.status IN ('queued', 'running')
			AND json_extract(jobs.payload, '$.dinner_id') = dinners.id
	);
