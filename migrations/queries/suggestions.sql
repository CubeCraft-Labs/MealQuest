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

-- name: GetLatestSuggestionBatch :one
-- The newest batch, whatever its status. Returns sql.ErrNoRows when there
-- is none.
SELECT * FROM suggestion_batches
ORDER BY id DESC
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
-- or running: the process stopped, or recording the batch's end failed,
-- before its handler recorded a result. Run before reading the active batch.
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
-- name is the name the dish is suggested under, kept whatever its dinner is
-- later renamed.
INSERT INTO suggestions (
	id, dinner_id, batch_id, name, reason, cited_regular_ids, status, created_at, updated_at
) VALUES (
	?, ?, ?, ?, ?, ?, 'pending', ?, ?
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

-- name: ListPassedNames :many
-- Every name the household turned down, oldest decision first: the name
-- each declined suggestion was suggested under, which renaming its dinner
-- does not change, and the name each dinner a Rotation check dropped
-- ("Not for us") had when dropped, which renaming it does not change.
SELECT name FROM (
	SELECT name, decided_at, id
	FROM suggestions
	WHERE status = 'declined'
	UNION ALL
	SELECT name, decided_at, id
	FROM rotation_reviews
	WHERE decision = 'drop'
)
ORDER BY decided_at, id;

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
