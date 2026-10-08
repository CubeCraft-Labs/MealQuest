-- 0015_google_reconnect_required: when Google refuses the stored grant
-- (invalid_grant on refresh, or HTTP 401 from the Calendar API) the
-- connection is kept as reconnect required: its refresh token is cleared
-- and last_sync_error says why. SQLite cannot drop a NOT NULL constraint,
-- so the table is rebuilt with refresh_token_ct nullable. A row without a
-- refresh token always carries the reason. No table references
-- integrations_google.

CREATE TABLE integrations_google_new (
	id               INTEGER PRIMARY KEY CHECK (id = 1),
	refresh_token_ct BLOB,
	calendar_id      TEXT,
	calendar_name    TEXT,
	connected_at     TEXT    NOT NULL,
	last_sync_error  TEXT,
	created_at       TEXT    NOT NULL,
	updated_at       TEXT    NOT NULL,
	CHECK (refresh_token_ct IS NOT NULL OR last_sync_error IS NOT NULL)
);

INSERT INTO integrations_google_new (
	id, refresh_token_ct, calendar_id, calendar_name, connected_at,
	last_sync_error, created_at, updated_at
)
SELECT id, refresh_token_ct, calendar_id, calendar_name, connected_at,
	last_sync_error, created_at, updated_at
FROM integrations_google;

DROP TABLE integrations_google;

ALTER TABLE integrations_google_new RENAME TO integrations_google;
