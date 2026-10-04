-- name: CreatePhoto :one
INSERT INTO photos (
	id, source_url, local_path, width, height, sha256, fetched_at,
	created_at, updated_at
) VALUES (
	?, ?, ?, ?, ?, ?, ?,
	?, ?
)
RETURNING *;

-- name: GetPhotoBySHA256 :one
-- Finds stored image bytes by content hash. The schema does not make sha256
-- unique, so the earliest-created (lowest id) match is returned.
SELECT * FROM photos
WHERE sha256 = ?
ORDER BY id
LIMIT 1;
