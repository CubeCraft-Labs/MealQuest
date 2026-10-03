-- name: GetHouseholdSettings :one
-- Returns sql.ErrNoRows until the row exists.
SELECT * FROM household_settings
WHERE id = 1;

-- name: UpsertHouseholdSettings :one
-- Creates the single row (id 1) or replaces every column except id and
-- created_at.
INSERT INTO household_settings (
	id, timezone, dinner_time, reminder_time, review_time, plan_draft_dow,
	plan_draft_time, suggestions_dow, suggestions_time, chat_autosend_dow,
	chat_autosend_time, onboarding_completed_at, updates_seen_at,
	created_at, updated_at
) VALUES (
	1, ?, ?, ?, ?, ?,
	?, ?, ?, ?,
	?, ?, ?,
	?, ?
)
ON CONFLICT (id) DO UPDATE SET
	timezone = excluded.timezone,
	dinner_time = excluded.dinner_time,
	reminder_time = excluded.reminder_time,
	review_time = excluded.review_time,
	plan_draft_dow = excluded.plan_draft_dow,
	plan_draft_time = excluded.plan_draft_time,
	suggestions_dow = excluded.suggestions_dow,
	suggestions_time = excluded.suggestions_time,
	chat_autosend_dow = excluded.chat_autosend_dow,
	chat_autosend_time = excluded.chat_autosend_time,
	onboarding_completed_at = excluded.onboarding_completed_at,
	updates_seen_at = excluded.updates_seen_at,
	updated_at = excluded.updated_at
RETURNING *;

-- name: GetAISettings :one
-- Returns sql.ErrNoRows until the row exists.
SELECT * FROM ai_settings
WHERE id = 1;

-- name: UpsertAISettings :one
-- Creates the single row (id 1) or replaces every column except id and
-- created_at.
INSERT INTO ai_settings (
	id, provider, base_url, model, temperature, system_prompt, api_key_ct,
	api_key_last4, key_verified_at, created_at, updated_at
) VALUES (
	1, ?, ?, ?, ?, ?, ?,
	?, ?, ?, ?
)
ON CONFLICT (id) DO UPDATE SET
	provider = excluded.provider,
	base_url = excluded.base_url,
	model = excluded.model,
	temperature = excluded.temperature,
	system_prompt = excluded.system_prompt,
	api_key_ct = excluded.api_key_ct,
	api_key_last4 = excluded.api_key_last4,
	key_verified_at = excluded.key_verified_at,
	updated_at = excluded.updated_at
RETURNING *;

-- name: GetRecipeSearchSettings :one
-- Returns sql.ErrNoRows until the row exists.
SELECT * FROM recipe_search_settings
WHERE id = 1;

-- name: UpsertRecipeSearchSettings :one
-- Creates the single row (id 1) or replaces every column except id and
-- created_at.
INSERT INTO recipe_search_settings (
	id, provider, base_url, api_key_ct, created_at, updated_at
) VALUES (
	1, ?, ?, ?, ?, ?
)
ON CONFLICT (id) DO UPDATE SET
	provider = excluded.provider,
	base_url = excluded.base_url,
	api_key_ct = excluded.api_key_ct,
	updated_at = excluded.updated_at
RETURNING *;

-- name: GetIntegrationsGoogle :one
-- Returns sql.ErrNoRows until the row exists.
SELECT * FROM integrations_google
WHERE id = 1;

-- name: UpsertIntegrationsGoogle :one
-- Creates the single row (id 1) or replaces every column except id and
-- created_at.
INSERT INTO integrations_google (
	id, refresh_token_ct, calendar_id, calendar_name, connected_at,
	last_sync_error, created_at, updated_at
) VALUES (
	1, ?, ?, ?, ?,
	?, ?, ?
)
ON CONFLICT (id) DO UPDATE SET
	refresh_token_ct = excluded.refresh_token_ct,
	calendar_id = excluded.calendar_id,
	calendar_name = excluded.calendar_name,
	connected_at = excluded.connected_at,
	last_sync_error = excluded.last_sync_error,
	updated_at = excluded.updated_at
RETURNING *;

-- name: GetChatSettings :one
-- Returns sql.ErrNoRows until the row exists.
SELECT * FROM chat_settings
WHERE id = 1;

-- name: UpsertChatSettings :one
-- Creates the single row (id 1) or replaces every column except id and
-- created_at.
INSERT INTO chat_settings (
	id, webhook_url_ct, space_label, last_test_at, last_test_status,
	created_at, updated_at
) VALUES (
	1, ?, ?, ?, ?,
	?, ?
)
ON CONFLICT (id) DO UPDATE SET
	webhook_url_ct = excluded.webhook_url_ct,
	space_label = excluded.space_label,
	last_test_at = excluded.last_test_at,
	last_test_status = excluded.last_test_status,
	updated_at = excluded.updated_at
RETURNING *;
