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
-- Every dinner a new or renamed dinner must not repeat, with the
-- source_url of the recipe it is grounded in (NULL while it has none).
SELECT sqlc.embed(dinners), recipes.source_url AS recipe_source_url
FROM dinners
LEFT JOIN recipes ON recipes.id = dinners.recipe_id
WHERE dinners.status <> 'archived'
ORDER BY dinners.id;

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
	recipe_url = ?,
	photo_id = ?,
	grounding_status = ?,
	grounding_revision = ?,
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
-- Records a dinner's grounding by hand (grounded or manual) and clears any
-- grounding_error and pasted recipe_url: the recipe chosen replaces the
-- link. It writes only an unarchived dinner still as it was observed
-- before grounding (same normalized_name, grounding_status,
-- grounding_revision and recipe_id), so a dinner renamed, archived or
-- grounded some other way meanwhile is left as it is. That dinner, or an
-- unknown id, returns sql.ErrNoRows.
UPDATE dinners
SET recipe_id = sqlc.arg(recipe_id),
	recipe_url = NULL,
	photo_id = sqlc.arg(photo_id),
	method = sqlc.arg(method),
	minutes_total = sqlc.arg(minutes_total),
	difficulty = sqlc.arg(difficulty),
	grounding_status = sqlc.arg(grounding_status),
	grounding_error = NULL,
	updated_by = sqlc.arg(updated_by),
	updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id)
	AND status <> 'archived'
	AND normalized_name = sqlc.arg(observed_normalized_name)
	AND grounding_status = sqlc.arg(observed_grounding_status)
	AND grounding_revision = sqlc.arg(observed_grounding_revision)
	AND recipe_id IS sqlc.narg(observed_recipe_id)
RETURNING *;

-- name: GroundPendingDinner :execrows
-- Records the background grounding of an unarchived dinner still pending at
-- the grounding_revision it was grounded for; a dinner grounded some other
-- way, renamed or archived meanwhile is left as it is (0 rows). A method,
-- minutes_total or difficulty already set is kept: a pending dinner has
-- them only when a member edited them while it was grounding.
UPDATE dinners
SET recipe_id = sqlc.arg(recipe_id),
	photo_id = sqlc.arg(photo_id),
	method = COALESCE(method, sqlc.arg(method)),
	minutes_total = COALESCE(minutes_total, sqlc.arg(minutes_total)),
	difficulty = COALESCE(difficulty, sqlc.arg(difficulty)),
	grounding_status = 'grounded',
	grounding_error = NULL,
	updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id)
	AND status <> 'archived'
	AND grounding_status = 'pending'
	AND grounding_revision = sqlc.arg(grounding_revision);

-- name: FailPendingDinnerGrounding :execrows
-- Records why the background grounding of an unarchived dinner still
-- pending at the grounding_revision it was grounded for failed; a dinner
-- grounded some other way, renamed or archived meanwhile is left as it is
-- (0 rows).
UPDATE dinners
SET grounding_status = 'failed',
	grounding_error = ?,
	updated_at = ?
WHERE id = ?
	AND status <> 'archived'
	AND grounding_status = 'pending'
	AND grounding_revision = ?;

-- name: FailOrphanedPendingDinnerGroundings :execrows
-- Fails every unarchived dinner still pending whose grounding job for its
-- current grounding_revision is no longer queued or running: the process
-- stopped before the job, or before its handler recorded a result. An
-- archived dinner is left as it is; archiving stopped its grounding. Run at
-- boot, after the job runner fails the jobs a previous process left running.
UPDATE dinners
SET grounding_status = 'failed',
	grounding_error = sqlc.arg(grounding_error),
	updated_at = sqlc.arg(updated_at)
WHERE grounding_status = 'pending'
	AND status <> 'archived'
	AND NOT EXISTS (
		SELECT 1 FROM jobs
		WHERE jobs.kind = sqlc.arg(kind)
			AND jobs.status IN ('queued', 'running')
			AND json_extract(jobs.payload, '$.dinner_id') = dinners.id
			AND json_extract(jobs.payload, '$.revision') = dinners.grounding_revision
	);
