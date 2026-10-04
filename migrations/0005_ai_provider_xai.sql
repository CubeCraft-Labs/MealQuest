-- 0005_ai_provider_xai: ai_settings.provider and ai_runs.provider also
-- accept xai, the xAI Grok preset in internal/ai/llm/presets. Every 0004
-- value is kept, so existing rows copy unchanged. The rebuild is 0004's:
-- copy the rows aside, drop the table, create it again with the wider CHECK,
-- copy the rows back and drop the copy. Neither table has indexes or
-- triggers.
--
-- suggestion_batches.ai_run_id and week_plans.ai_run_id reference ai_runs,
-- so foreign keys are deferred until commit, after the ai_runs rows are
-- back. The pragma lasts only for this migration's transaction.

PRAGMA defer_foreign_keys = ON;

CREATE TABLE ai_settings_copy AS SELECT * FROM ai_settings;

DROP TABLE ai_settings;

CREATE TABLE ai_settings (
	id              INTEGER PRIMARY KEY CHECK (id = 1),
	provider        TEXT    NOT NULL CHECK (provider IN ('openai_compatible', 'anthropic', 'ollama', 'openai', 'openrouter', 'xai')),
	base_url        TEXT    NOT NULL,
	model           TEXT    NOT NULL,
	temperature     REAL    NOT NULL,
	system_prompt   TEXT,
	api_key_ct      BLOB,
	api_key_last4   TEXT,
	key_verified_at TEXT,
	created_at      TEXT    NOT NULL,
	updated_at      TEXT    NOT NULL
);

INSERT INTO ai_settings (id, provider, base_url, model, temperature, system_prompt, api_key_ct, api_key_last4, key_verified_at, created_at, updated_at)
SELECT id, provider, base_url, model, temperature, system_prompt, api_key_ct, api_key_last4, key_verified_at, created_at, updated_at
FROM ai_settings_copy;

DROP TABLE ai_settings_copy;

CREATE TABLE ai_runs_copy AS SELECT * FROM ai_runs;

DROP TABLE ai_runs;

CREATE TABLE ai_runs (
	id                 TEXT    NOT NULL PRIMARY KEY,
	skill              TEXT    NOT NULL,
	skill_version      TEXT    NOT NULL,
	provider           TEXT    NOT NULL CHECK (provider IN ('openai_compatible', 'anthropic', 'ollama', 'openai', 'openrouter', 'xai')),
	model              TEXT    NOT NULL,
	temperature        REAL    NOT NULL,
	status             TEXT    NOT NULL CHECK (status IN ('running', 'ok', 'error')),
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
