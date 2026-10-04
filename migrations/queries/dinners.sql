-- name: CreateDinner :one
INSERT INTO dinners (
	id, name, normalized_name, status, origin, method, minutes_total,
	difficulty, cuisine, is_favorite, recipe_id, photo_id, grounding_status,
	updated_by, created_at, updated_at
) VALUES (
	?, ?, ?, ?, ?, ?, ?,
	?, ?, ?, ?, ?, ?,
	?, ?, ?
)
RETURNING *;

-- name: GetDinner :one
SELECT * FROM dinners
WHERE id = ?;

-- name: ListDinnersByStatus :many
SELECT * FROM dinners
WHERE status = ?
ORDER BY normalized_name, id;

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
