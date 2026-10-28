-- 0018_plan_day_manual_label: a plan day may name one night with a
-- free-text label instead of a dinner. The label is that night only: it
-- is not a dinners row, it is not a Regular or a Quest-log dinner, and a
-- day is never both a dinner and a label. A day may also be cleared
-- (neither). SQLite cannot add a table CHECK that mentions another
-- column, so plan_days is rebuilt as 0014 does: copy the rows aside, drop
-- the table, create it again, copy the rows back and drop the copy.
-- Nothing references plan_days.

CREATE TABLE plan_days_copy AS SELECT * FROM plan_days;

DROP TABLE plan_days;

CREATE TABLE plan_days (
	id                TEXT    NOT NULL PRIMARY KEY,
	plan_id           TEXT    NOT NULL REFERENCES week_plans (id),
	date              TEXT    NOT NULL,
	dinner_id         TEXT    REFERENCES dinners (id),
	manual_label      TEXT,
	is_new            INTEGER NOT NULL CHECK (is_new IN (0, 1)),
	calendar_event_id TEXT,
	created_at        TEXT    NOT NULL,
	updated_at        TEXT    NOT NULL,
	UNIQUE (plan_id, date),
	-- A night is a dinner, a one-off label, or cleared. Never both, and a
	-- label is never blank.
	CHECK (dinner_id IS NULL OR manual_label IS NULL),
	CHECK (manual_label IS NULL OR length(manual_label) > 0)
);

INSERT INTO plan_days (
	id, plan_id, date, dinner_id, manual_label, is_new, calendar_event_id, created_at, updated_at
)
SELECT
	id, plan_id, date, dinner_id, NULL, is_new, calendar_event_id, created_at, updated_at
FROM plan_days_copy;

DROP TABLE plan_days_copy;
