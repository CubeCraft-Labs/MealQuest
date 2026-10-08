-- 0016_shopping_list_untitled: a Supply run is generated from the plan
-- before it has a title, and stays untitled when the AI cannot write one,
-- so shopping_lists.title becomes nullable; an untitled list shows its
-- week label instead. A title is never empty. Each group also keeps the
-- plan day it shops for, so a list read back, or frozen into the Supply
-- archive, says which day each dinner was planned on.
-- SQLite cannot drop a NOT NULL constraint or add a NOT NULL column
-- without a default, so the three shopping tables are rebuilt as 0012
-- does: copy the rows aside, drop the tables children first, create them
-- again, copy the rows back parents first and drop the copies. A group
-- takes the date its dinner has on its list's plan; a group whose dinner
-- is not on that plan has no date and fails the migration. Nothing else
-- references the shopping tables.

CREATE TABLE shopping_items_copy AS SELECT * FROM shopping_items;
CREATE TABLE shopping_groups_copy AS SELECT * FROM shopping_groups;
CREATE TABLE shopping_lists_copy AS SELECT * FROM shopping_lists;

DROP TABLE shopping_items;
DROP TABLE shopping_groups;
DROP TABLE shopping_lists;

CREATE TABLE shopping_lists (
	id              TEXT NOT NULL PRIMARY KEY,
	week_start      TEXT NOT NULL UNIQUE,
	plan_id         TEXT NOT NULL REFERENCES week_plans (id),
	title           TEXT CHECK (title <> ''), -- NULL until the AI titles it
	status          TEXT NOT NULL CHECK (status IN ('current', 'archived')),
	sent_to_chat_at TEXT,
	frozen_at       TEXT,
	created_at      TEXT NOT NULL,
	updated_at      TEXT NOT NULL
);

CREATE TABLE shopping_groups (
	id         TEXT    NOT NULL PRIMARY KEY,
	list_id    TEXT    NOT NULL REFERENCES shopping_lists (id),
	dinner_id  TEXT    NOT NULL REFERENCES dinners (id),
	date       TEXT    NOT NULL, -- the plan day, household-local YYYY-MM-DD
	position   INTEGER NOT NULL,
	created_at TEXT    NOT NULL,
	updated_at TEXT    NOT NULL
);

CREATE TABLE shopping_items (
	id         TEXT    NOT NULL PRIMARY KEY,
	group_id   TEXT    NOT NULL REFERENCES shopping_groups (id),
	name       TEXT    NOT NULL,
	qty        TEXT,
	needed     INTEGER NOT NULL DEFAULT 1 CHECK (needed IN (0, 1)),
	position   INTEGER NOT NULL,
	created_at TEXT    NOT NULL,
	updated_at TEXT    NOT NULL
);

INSERT INTO shopping_lists (
	id, week_start, plan_id, title, status, sent_to_chat_at, frozen_at, created_at, updated_at
)
SELECT id, week_start, plan_id, title, status, sent_to_chat_at, frozen_at, created_at, updated_at
FROM shopping_lists_copy;

INSERT INTO shopping_groups (id, list_id, dinner_id, date, position, created_at, updated_at)
SELECT
	shopping_groups_copy.id, shopping_groups_copy.list_id, shopping_groups_copy.dinner_id,
	(
		SELECT MIN(plan_days.date)
		FROM plan_days
		JOIN shopping_lists_copy ON shopping_lists_copy.plan_id = plan_days.plan_id
		WHERE shopping_lists_copy.id = shopping_groups_copy.list_id
			AND plan_days.dinner_id = shopping_groups_copy.dinner_id
	),
	shopping_groups_copy.position, shopping_groups_copy.created_at, shopping_groups_copy.updated_at
FROM shopping_groups_copy;

INSERT INTO shopping_items (id, group_id, name, qty, needed, position, created_at, updated_at)
SELECT id, group_id, name, qty, needed, position, created_at, updated_at
FROM shopping_items_copy;

DROP TABLE shopping_items_copy;
DROP TABLE shopping_groups_copy;
DROP TABLE shopping_lists_copy;
