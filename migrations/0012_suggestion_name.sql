-- 0012_suggestion_name: a suggestion keeps the name its dish was suggested
-- under. Its dinner can be renamed, even once archived by a Pass, so the
-- declined names every later batch refuses are read from here, never from
-- the dinner. A suggestion stored before this takes its dinner's name now.
-- SQLite cannot add a NOT NULL column without a default, so the table is
-- rebuilt as 0005 does: copy the rows aside, drop the table, create it
-- again, copy the rows back and drop the copy. Nothing references
-- suggestions.

CREATE TABLE suggestions_copy AS SELECT * FROM suggestions;

DROP TABLE suggestions;

CREATE TABLE suggestions (
	id                TEXT NOT NULL PRIMARY KEY,
	dinner_id         TEXT NOT NULL REFERENCES dinners (id),
	batch_id          TEXT NOT NULL REFERENCES suggestion_batches (id),
	name              TEXT NOT NULL CHECK (name <> ''),
	reason            TEXT NOT NULL,
	cited_regular_ids TEXT NOT NULL, -- JSON array of dinner ids
	status            TEXT NOT NULL CHECK (status IN ('pending', 'accepted', 'declined')),
	decided_at        TEXT,
	created_at        TEXT NOT NULL,
	updated_at        TEXT NOT NULL
);

INSERT INTO suggestions (
	id, dinner_id, batch_id, name, reason, cited_regular_ids, status, decided_at, created_at, updated_at
)
SELECT
	suggestions_copy.id, suggestions_copy.dinner_id, suggestions_copy.batch_id, dinners.name,
	suggestions_copy.reason, suggestions_copy.cited_regular_ids, suggestions_copy.status,
	suggestions_copy.decided_at, suggestions_copy.created_at, suggestions_copy.updated_at
FROM suggestions_copy
JOIN dinners ON dinners.id = suggestions_copy.dinner_id;

DROP TABLE suggestions_copy;
