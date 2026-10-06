-- 0006_ai_runs_status: ai_runs.status becomes the harness run outcome:
--   ok             the first reply was usable
--   repaired       the first reply was not, the repair turn's was
--   bad_output     neither reply was usable, or post-processing failed
--   provider_error the provider failed on either turn
-- A run's row is written once, when it ends, so there is no running state.
-- Nothing wrote ai_runs before the harness did, so no row holds the old
-- running or error values; if one does, the copy back fails the CHECK and
-- the migration stops rather than guess its outcome. created_at gains an
-- index for the 30-day purge. The rebuild is 0005's: copy the rows aside,
-- drop the table, create it again, copy the rows back and drop the copy.
--
-- suggestion_batches.ai_run_id and week_plans.ai_run_id reference ai_runs,
-- so foreign keys are deferred until commit, after the ai_runs rows are
-- back. The pragma lasts only for this migration's transaction.

PRAGMA defer_foreign_keys = ON;

CREATE TABLE ai_runs_copy AS SELECT * FROM ai_runs;

DROP TABLE ai_runs;

CREATE TABLE ai_runs (
	id                 TEXT    NOT NULL PRIMARY KEY,
	skill              TEXT    NOT NULL,
	skill_version      TEXT    NOT NULL,
	provider           TEXT    NOT NULL CHECK (provider IN ('openai_compatible', 'anthropic', 'ollama', 'openai', 'openrouter', 'xai')),
	model              TEXT    NOT NULL,
	temperature        REAL    NOT NULL,
	status             TEXT    NOT NULL CHECK (status IN ('ok', 'repaired', 'bad_output', 'provider_error')),
	latency_ms         INTEGER,
	tokens_in          INTEGER,
	tokens_out         INTEGER,
	error              TEXT,
	request_redacted   TEXT    NOT NULL,
	response_redacted  TEXT,
	created_at         TEXT    NOT NULL,
	updated_at         TEXT    NOT NULL
);

INSERT INTO ai_runs (id, skill, skill_version, provider, model, temperature, status, latency_ms, tokens_in, tokens_out, error, request_redacted, response_redacted, created_at, updated_at)
SELECT id, skill, skill_version, provider, model, temperature, status, latency_ms, tokens_in, tokens_out, error, request_redacted, response_redacted, created_at, updated_at
FROM ai_runs_copy;

DROP TABLE ai_runs_copy;

CREATE INDEX ai_runs_created_at ON ai_runs (created_at);
