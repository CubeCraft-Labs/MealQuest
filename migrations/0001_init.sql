-- 0001_init: the initial v1 schema (CTXD CONTEXT.MD data model).
--
-- Conventions:
--   * Row ids are ULIDs stored as TEXT. Single-row settings tables use
--     id INTEGER PRIMARY KEY CHECK (id = 1) so a second row is refused.
--   * Timestamps are UTC RFC 3339 TEXT. Household-local dates (week_start,
--     plan day) are YYYY-MM-DD TEXT; household-local times are HH:MM TEXT.
--   * Columns ending in _ct only ever hold secretbox ciphertext.
--   * Every enum column has a CHECK. Booleans are INTEGER CHECK (x IN (0, 1)).
--     Days of the week are INTEGER 0-6, Sunday = 0 (Go time.Weekday).
--   * Foreign keys use the default NO ACTION, so deleting a parent that still
--     has children is refused. The one exception is ai_run_id: ai_runs rows
--     are purged after 30 days, so references to them are ON DELETE SET NULL.
--   * Defaults exist only where CTXD names one (household_settings times and
--     shopping_items.needed).
--
-- Enum value sets not spelled out in CONTEXT.MD, chosen here:
--   ai_settings.provider, ai_runs.provider: openai_compatible, anthropic, ollama
--   ai_runs.status: running, ok, error
--   chat_settings.last_test_status: ok, failed
--   jobs.status, suggestion_batches.status: pending, running, done, failed
--   notification_outbox.status: pending, sent, failed
--   schedule_runs.schedule: suggestions_weekly, plan_draft,
--     shopping_chat_autosend, day_of_reminder, rotation_check, week_rollover,
--     ai_runs_purge
-- jobs.kind and ai_runs.skill are open identifiers that grow with features,
-- not enums, so they carry no CHECK.

CREATE TABLE household_settings (
	id                      INTEGER PRIMARY KEY CHECK (id = 1),
	timezone                TEXT    NOT NULL,
	dinner_time             TEXT    NOT NULL DEFAULT '17:30',
	reminder_time           TEXT    NOT NULL DEFAULT '16:30',
	review_time             TEXT    NOT NULL DEFAULT '09:00',
	plan_draft_dow          INTEGER NOT NULL DEFAULT 5 CHECK (plan_draft_dow BETWEEN 0 AND 6),
	plan_draft_time         TEXT    NOT NULL DEFAULT '09:00',
	suggestions_dow         INTEGER NOT NULL DEFAULT 0 CHECK (suggestions_dow BETWEEN 0 AND 6),
	suggestions_time        TEXT    NOT NULL DEFAULT '08:00',
	chat_autosend_dow       INTEGER NOT NULL DEFAULT 6 CHECK (chat_autosend_dow BETWEEN 0 AND 6),
	chat_autosend_time      TEXT    NOT NULL DEFAULT '08:00',
	onboarding_completed_at TEXT,
	updates_seen_at         TEXT,
	created_at              TEXT    NOT NULL,
	updated_at              TEXT    NOT NULL
);

CREATE TABLE ai_runs (
	id                 TEXT    PRIMARY KEY,
	skill              TEXT    NOT NULL,
	skill_version      TEXT    NOT NULL,
	provider           TEXT    NOT NULL CHECK (provider IN ('openai_compatible', 'anthropic', 'ollama')),
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

CREATE TABLE recipes (
	id            TEXT    PRIMARY KEY,
	source_url    TEXT    NOT NULL UNIQUE,
	site_name     TEXT,
	title         TEXT    NOT NULL,
	total_minutes INTEGER,
	prep_minutes  INTEGER,
	cook_minutes  INTEGER,
	yield         TEXT,
	raw_jsonld    TEXT    NOT NULL,
	fetched_at    TEXT    NOT NULL,
	created_at    TEXT    NOT NULL,
	updated_at    TEXT    NOT NULL
);

CREATE TABLE recipe_ingredients (
	id         TEXT    PRIMARY KEY,
	recipe_id  TEXT    NOT NULL REFERENCES recipes (id),
	position   INTEGER NOT NULL,
	raw_text   TEXT    NOT NULL,
	name       TEXT,
	qty        TEXT,
	unit       TEXT,
	note       TEXT,
	created_at TEXT    NOT NULL,
	updated_at TEXT    NOT NULL
);

CREATE TABLE recipe_steps (
	id         TEXT    PRIMARY KEY,
	recipe_id  TEXT    NOT NULL REFERENCES recipes (id),
	position   INTEGER NOT NULL,
	text       TEXT    NOT NULL,
	created_at TEXT    NOT NULL,
	updated_at TEXT    NOT NULL
);

CREATE TABLE photos (
	id         TEXT    PRIMARY KEY,
	source_url TEXT    NOT NULL,
	local_path TEXT    NOT NULL,
	width      INTEGER NOT NULL,
	height     INTEGER NOT NULL,
	sha256     TEXT    NOT NULL,
	fetched_at TEXT    NOT NULL,
	created_at TEXT    NOT NULL,
	updated_at TEXT    NOT NULL
);

CREATE TABLE dinners (
	id               TEXT    PRIMARY KEY,
	name             TEXT    NOT NULL,
	normalized_name  TEXT    NOT NULL,
	status           TEXT    NOT NULL CHECK (status IN ('suggested', 'quest_log', 'tried', 'regular', 'archived')),
	origin           TEXT    NOT NULL CHECK (origin IN ('regular', 'side_quest', 'ingredients', 'quick', 'manual')),
	method           TEXT    CHECK (method IN ('stovetop', 'oven', 'crockpot', 'grill', 'other')),
	minutes_total    INTEGER,
	difficulty       TEXT    CHECK (difficulty IN ('easy', 'medium', 'hard')),
	cuisine          TEXT,
	is_favorite      INTEGER NOT NULL CHECK (is_favorite IN (0, 1)),
	recipe_id        TEXT    REFERENCES recipes (id),
	photo_id         TEXT    REFERENCES photos (id),
	grounding_status TEXT    NOT NULL CHECK (grounding_status IN ('pending', 'grounded', 'failed', 'manual')),
	updated_by       TEXT,
	created_at       TEXT    NOT NULL,
	updated_at       TEXT    NOT NULL
);

CREATE TABLE suggestion_batches (
	id         TEXT PRIMARY KEY,
	trigger    TEXT NOT NULL CHECK (trigger IN ('scheduled', 'manual', 'replenish')),
	status     TEXT NOT NULL CHECK (status IN ('pending', 'running', 'done', 'failed')),
	ai_run_id  TEXT REFERENCES ai_runs (id) ON DELETE SET NULL,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);

CREATE TABLE suggestions (
	id                TEXT PRIMARY KEY,
	dinner_id         TEXT NOT NULL REFERENCES dinners (id),
	batch_id          TEXT NOT NULL REFERENCES suggestion_batches (id),
	reason            TEXT NOT NULL,
	cited_regular_ids TEXT NOT NULL, -- JSON array of dinner ids
	status            TEXT NOT NULL CHECK (status IN ('pending', 'accepted', 'declined')),
	decided_at        TEXT,
	created_at        TEXT NOT NULL,
	updated_at        TEXT NOT NULL
);

CREATE TABLE week_plans (
	id                 TEXT PRIMARY KEY,
	week_start         TEXT NOT NULL UNIQUE,
	status             TEXT NOT NULL CHECK (status IN ('draft', 'synced', 'changed')),
	ai_note            TEXT,
	ai_run_id          TEXT REFERENCES ai_runs (id) ON DELETE SET NULL,
	calendar_synced_at TEXT,
	created_at         TEXT NOT NULL,
	updated_at         TEXT NOT NULL
);

CREATE TABLE plan_days (
	id                TEXT    PRIMARY KEY,
	plan_id           TEXT    NOT NULL REFERENCES week_plans (id),
	date              TEXT    NOT NULL,
	dinner_id         TEXT    REFERENCES dinners (id),
	is_new            INTEGER NOT NULL CHECK (is_new IN (0, 1)),
	calendar_event_id TEXT,
	created_at        TEXT    NOT NULL,
	updated_at        TEXT    NOT NULL,
	UNIQUE (plan_id, date)
);

CREATE TABLE rotation_reviews (
	id         TEXT PRIMARY KEY,
	dinner_id  TEXT NOT NULL REFERENCES dinners (id),
	plan_date  TEXT NOT NULL,
	status     TEXT NOT NULL CHECK (status IN ('pending', 'decided')),
	decision   TEXT CHECK (decision IN ('keep', 'drop', 'not_made')),
	decided_at TEXT,
	updated_by TEXT,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	UNIQUE (dinner_id, plan_date)
);

CREATE TABLE shopping_lists (
	id              TEXT PRIMARY KEY,
	week_start      TEXT NOT NULL UNIQUE,
	plan_id         TEXT NOT NULL REFERENCES week_plans (id),
	title           TEXT NOT NULL,
	status          TEXT NOT NULL CHECK (status IN ('current', 'archived')),
	sent_to_chat_at TEXT,
	frozen_at       TEXT,
	created_at      TEXT NOT NULL,
	updated_at      TEXT NOT NULL
);

CREATE TABLE shopping_groups (
	id         TEXT    PRIMARY KEY,
	list_id    TEXT    NOT NULL REFERENCES shopping_lists (id),
	dinner_id  TEXT    NOT NULL REFERENCES dinners (id),
	position   INTEGER NOT NULL,
	created_at TEXT    NOT NULL,
	updated_at TEXT    NOT NULL
);

CREATE TABLE shopping_items (
	id         TEXT    PRIMARY KEY,
	group_id   TEXT    NOT NULL REFERENCES shopping_groups (id),
	name       TEXT    NOT NULL,
	qty        TEXT,
	needed     INTEGER NOT NULL DEFAULT 1 CHECK (needed IN (0, 1)),
	position   INTEGER NOT NULL,
	created_at TEXT    NOT NULL,
	updated_at TEXT    NOT NULL
);

CREATE TABLE ai_settings (
	id              INTEGER PRIMARY KEY CHECK (id = 1),
	provider        TEXT    NOT NULL CHECK (provider IN ('openai_compatible', 'anthropic', 'ollama')),
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

CREATE TABLE recipe_search_settings (
	id         INTEGER PRIMARY KEY CHECK (id = 1),
	provider   TEXT    NOT NULL CHECK (provider IN ('searxng', 'brave')),
	base_url   TEXT,
	api_key_ct BLOB,
	created_at TEXT    NOT NULL,
	updated_at TEXT    NOT NULL
);

CREATE TABLE integrations_google (
	id               INTEGER PRIMARY KEY CHECK (id = 1),
	refresh_token_ct BLOB    NOT NULL,
	calendar_id      TEXT,
	calendar_name    TEXT,
	connected_at     TEXT    NOT NULL,
	last_sync_error  TEXT,
	created_at       TEXT    NOT NULL,
	updated_at       TEXT    NOT NULL
);

CREATE TABLE chat_settings (
	id               INTEGER PRIMARY KEY CHECK (id = 1),
	webhook_url_ct   BLOB    NOT NULL,
	space_label      TEXT,
	last_test_at     TEXT,
	last_test_status TEXT    CHECK (last_test_status IN ('ok', 'failed')),
	created_at       TEXT    NOT NULL,
	updated_at       TEXT    NOT NULL
);

CREATE TABLE notification_prefs (
	event      TEXT    PRIMARY KEY CHECK (event IN ('plan_ready', 'list_ready', 'day_of_reminder', 'new_suggestions', 'rotation_review')),
	push       INTEGER NOT NULL CHECK (push IN (0, 1)),
	chat       INTEGER NOT NULL CHECK (chat IN (0, 1)),
	created_at TEXT    NOT NULL,
	updated_at TEXT    NOT NULL
);

CREATE TABLE push_subscriptions (
	id              TEXT    PRIMARY KEY,
	endpoint        TEXT    NOT NULL UNIQUE,
	p256dh          TEXT    NOT NULL,
	auth            TEXT    NOT NULL,
	user_agent      TEXT,
	failure_count   INTEGER NOT NULL,
	last_success_at TEXT,
	created_at      TEXT    NOT NULL,
	updated_at      TEXT    NOT NULL
);

CREATE TABLE notification_outbox (
	id              TEXT    PRIMARY KEY,
	event_key       TEXT    NOT NULL,
	channel         TEXT    NOT NULL CHECK (channel IN ('push', 'chat')),
	payload         TEXT    NOT NULL, -- JSON
	status          TEXT    NOT NULL CHECK (status IN ('pending', 'sent', 'failed')),
	attempts        INTEGER NOT NULL,
	next_attempt_at TEXT,
	last_error      TEXT,
	created_at      TEXT    NOT NULL,
	updated_at      TEXT    NOT NULL,
	UNIQUE (event_key, channel)
);

CREATE TABLE jobs (
	id          TEXT PRIMARY KEY,
	kind        TEXT NOT NULL,
	status      TEXT NOT NULL CHECK (status IN ('pending', 'running', 'done', 'failed')),
	payload     TEXT NOT NULL, -- JSON
	result      TEXT,          -- JSON, written progressively
	error       TEXT,
	started_at  TEXT,
	finished_at TEXT,
	created_at  TEXT NOT NULL,
	updated_at  TEXT NOT NULL
);

CREATE TABLE schedule_runs (
	id         TEXT PRIMARY KEY,
	schedule   TEXT NOT NULL CHECK (schedule IN ('suggestions_weekly', 'plan_draft', 'shopping_chat_autosend', 'day_of_reminder', 'rotation_check', 'week_rollover', 'ai_runs_purge')),
	period_key TEXT NOT NULL,
	created_at TEXT NOT NULL,
	UNIQUE (schedule, period_key)
);

CREATE TABLE sessions (
	id_hash      TEXT PRIMARY KEY,
	subject      TEXT NOT NULL,
	email        TEXT NOT NULL,
	name         TEXT NOT NULL,
	groups       TEXT NOT NULL, -- JSON array
	csrf_token   TEXT NOT NULL,
	created_at   TEXT NOT NULL,
	last_seen_at TEXT NOT NULL,
	expires_at   TEXT NOT NULL
);
