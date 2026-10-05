-- name: CreateAIRun :exec
INSERT INTO ai_runs (
	id, skill, skill_version, provider, model, temperature, status,
	latency_ms, tokens_in, tokens_out, error, request_redacted,
	response_redacted, created_at, updated_at
) VALUES (
	?, ?, ?, ?, ?, ?, ?,
	?, ?, ?, ?, ?,
	?, ?, ?
);

-- name: ClearSuggestionBatchAIRunsBefore :execrows
-- Drops references to the runs created before the cutoff, so the purge
-- can delete them.
UPDATE suggestion_batches
SET ai_run_id = NULL
WHERE ai_run_id IN (SELECT ai_runs.id FROM ai_runs WHERE ai_runs.created_at < sqlc.arg(cutoff));

-- name: ClearWeekPlanAIRunsBefore :execrows
-- Drops references to the runs created before the cutoff, so the purge
-- can delete them.
UPDATE week_plans
SET ai_run_id = NULL
WHERE ai_run_id IN (SELECT ai_runs.id FROM ai_runs WHERE ai_runs.created_at < sqlc.arg(cutoff));

-- name: DeleteAIRunsBefore :execrows
DELETE FROM ai_runs
WHERE created_at < sqlc.arg(cutoff);
