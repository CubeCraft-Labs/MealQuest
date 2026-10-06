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
-- Records the background grounding of a dinner still pending; a dinner
-- grounded some other way meanwhile is left as it is (0 rows).
UPDATE dinners
SET recipe_id = ?,
	photo_id = ?,
	method = ?,
	minutes_total = ?,
	difficulty = ?,
	grounding_status = 'grounded',
	grounding_error = NULL,
	updated_at = ?
WHERE id = ? AND grounding_status = 'pending';

-- name: FailPendingDinnerGrounding :execrows
-- Records why the background grounding of a dinner still pending failed; a
-- dinner grounded some other way meanwhile is left as it is (0 rows).
UPDATE dinners
SET grounding_status = 'failed',
	grounding_error = ?,
	updated_at = ?
WHERE id = ? AND grounding_status = 'pending';
