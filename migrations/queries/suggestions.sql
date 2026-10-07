-- name: CreateSuggestionBatch :one
INSERT INTO suggestion_batches (id, trigger, status, created_at, updated_at)
VALUES (?, ?, 'pending', ?, ?)
RETURNING *;

-- name: GetActiveSuggestionBatch :one
-- The batch still pending or running, oldest first. Returns sql.ErrNoRows
-- when there is none.
SELECT * FROM suggestion_batches
WHERE status IN ('pending', 'running')
ORDER BY id
LIMIT 1;

-- name: GetSuggestionBatchJobID :one
-- The id of the job that runs batch batch_id.
SELECT id FROM jobs
WHERE kind = sqlc.arg(kind)
	AND json_extract(payload, '$.batch_id') = sqlc.arg(batch_id)
ORDER BY id
LIMIT 1;

-- name: StartSuggestionBatch :execrows
-- Marks a pending batch running; a batch in any other status is left as
-- it is (0 rows).
UPDATE suggestion_batches
SET status = 'running', updated_at = ?
WHERE id = ? AND status = 'pending';

-- name: FinishSuggestionBatch :execrows
-- Records a pending or running batch as done or failed; a batch already
-- finished is left as it is (0 rows).
UPDATE suggestion_batches
SET status = ?, updated_at = ?
WHERE id = ? AND status IN ('pending', 'running');

-- name: FailOrphanedSuggestionBatches :execrows
-- Fails every batch still pending or running whose job is no longer queued
-- or running: the process stopped before enqueuing the job or before its
-- handler recorded a result. Run at boot, after the job runner fails the
-- jobs a previous process left running.
UPDATE suggestion_batches
SET status = 'failed', updated_at = sqlc.arg(updated_at)
WHERE status IN ('pending', 'running')
	AND NOT EXISTS (
		SELECT 1 FROM jobs
		WHERE jobs.kind = sqlc.arg(kind)
			AND jobs.status IN ('queued', 'running')
			AND json_extract(jobs.payload, '$.batch_id') = suggestion_batches.id
	);

-- name: CreateSuggestion :exec
INSERT INTO suggestions (
	id, dinner_id, batch_id, reason, cited_regular_ids, status, created_at, updated_at
) VALUES (
	?, ?, ?, ?, ?, 'pending', ?, ?
);

-- name: GetSuggestion :one
-- A suggestion with its dinner. Returns sql.ErrNoRows for an unknown id.
SELECT sqlc.embed(suggestions), sqlc.embed(dinners)
FROM suggestions
JOIN dinners ON dinners.id = suggestions.dinner_id
WHERE suggestions.id = ?;

-- name: ListDeck :many
-- The pending suggestions whose dinner is still suggested, oldest first,
-- with their dinners.
SELECT sqlc.embed(suggestions), sqlc.embed(dinners)
FROM suggestions
JOIN dinners ON dinners.id = suggestions.dinner_id
WHERE suggestions.status = 'pending' AND dinners.status = 'suggested'
ORDER BY suggestions.created_at, suggestions.id;

-- name: CountDeck :one
-- How many suggestions ListDeck returns.
SELECT COUNT(*)
FROM suggestions
JOIN dinners ON dinners.id = suggestions.dinner_id
WHERE suggestions.status = 'pending' AND dinners.status = 'suggested';

-- name: ListDeclinedSuggestionNames :many
-- The name of every declined suggestion's dinner, oldest decision first.
SELECT dinners.name
FROM suggestions
JOIN dinners ON dinners.id = suggestions.dinner_id
WHERE suggestions.status = 'declined'
ORDER BY suggestions.decided_at, suggestions.id;

-- name: DecideSuggestion :execrows
-- Records the household's decision on a pending suggestion; one already
-- decided is left as it is (0 rows).
UPDATE suggestions
SET status = sqlc.arg(status), decided_at = sqlc.arg(now), updated_at = sqlc.arg(now)
WHERE id = sqlc.arg(id) AND status = 'pending';

-- name: MoveSuggestedDinner :execrows
-- Moves a dinner still suggested to status (quest_log or archived),
-- recording updated_by; a dinner no longer suggested is left as it is
-- (0 rows).
UPDATE dinners
SET status = sqlc.arg(status), updated_by = sqlc.arg(updated_by), updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id) AND status = 'suggested';
