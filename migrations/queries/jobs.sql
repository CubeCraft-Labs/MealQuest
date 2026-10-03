-- name: CreateJob :exec
INSERT INTO jobs (id, kind, status, payload, created_at, updated_at)
VALUES (?, ?, 'queued', ?, ?, ?);

-- name: ClaimNextJob :one
-- Marks the oldest queued job running and returns it. Returns sql.ErrNoRows
-- when no job is queued.
UPDATE jobs
SET status = 'running', started_at = sqlc.arg(now), updated_at = sqlc.arg(now)
WHERE id = (SELECT id FROM jobs WHERE status = 'queued' ORDER BY id LIMIT 1)
RETURNING *;

-- name: PublishJobResult :execrows
-- Stores a partial result of a running job.
UPDATE jobs
SET result = ?, updated_at = ?
WHERE id = ? AND status = 'running';

-- name: SucceedJob :execrows
UPDATE jobs
SET status = 'succeeded', result = sqlc.arg(result), finished_at = sqlc.arg(now), updated_at = sqlc.arg(now)
WHERE id = sqlc.arg(id) AND status = 'running';

-- name: FailJob :execrows
UPDATE jobs
SET status = 'failed', error = sqlc.arg(error), finished_at = sqlc.arg(now), updated_at = sqlc.arg(now)
WHERE id = sqlc.arg(id) AND status = 'running';

-- name: FailRunningJobs :execrows
-- Fails every job left running, at boot before any worker starts.
UPDATE jobs
SET status = 'failed', error = sqlc.arg(error), finished_at = sqlc.arg(now), updated_at = sqlc.arg(now)
WHERE status = 'running';

-- name: GetJob :one
SELECT * FROM jobs
WHERE id = ?;
