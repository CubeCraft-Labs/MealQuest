-- name: GetDinnerRemoval :one
-- An unknown removal returns sql.ErrNoRows.
SELECT * FROM dinner_removals
WHERE id = ?;

-- name: CreateDinnerRemoval :exec
INSERT INTO dinner_removals (id, dinner_id, outcome, created_by, created_at)
VALUES (?, ?, ?, ?, ?);
