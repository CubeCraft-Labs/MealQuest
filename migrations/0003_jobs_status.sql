-- 0003_jobs_status: jobs.status becomes queued, running, succeeded, failed,
-- the states the job runner (internal/jobs) reports. SQLite cannot change a
-- CHECK in place, so the table is rebuilt: create the new table, copy every
-- row, drop the old one and rename. Nothing references jobs and it has no
-- indexes or triggers, so the rebuild touches no other schema object. Rows
-- written under the 0001 values are mapped (pending -> queued,
-- done -> succeeded) so the copy cannot fail on an existing database.

CREATE TABLE jobs_new (
	id          TEXT NOT NULL PRIMARY KEY,
	kind        TEXT NOT NULL,
	status      TEXT NOT NULL CHECK (status IN ('queued', 'running', 'succeeded', 'failed')),
	payload     TEXT NOT NULL, -- JSON
	result      TEXT,          -- JSON, written progressively
	error       TEXT,
	started_at  TEXT,
	finished_at TEXT,
	created_at  TEXT NOT NULL,
	updated_at  TEXT NOT NULL
);

INSERT INTO jobs_new (id, kind, status, payload, result, error, started_at, finished_at, created_at, updated_at)
SELECT
	id,
	kind,
	CASE status WHEN 'pending' THEN 'queued' WHEN 'done' THEN 'succeeded' ELSE status END,
	payload, result, error, started_at, finished_at, created_at, updated_at
FROM jobs;

DROP TABLE jobs;

ALTER TABLE jobs_new RENAME TO jobs;
