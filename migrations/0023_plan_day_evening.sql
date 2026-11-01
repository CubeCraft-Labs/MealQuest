-- 0023_plan_day_evening: a plan day is exactly one of a dinner, a
-- Delivery evening, a Dining-out evening, a one-off free-text label, or
-- cleared. A Delivery or Dining-out evening is the day's own state: it
-- has no dinner and no manual label, so nothing is cooked that night.
-- meal_source reuses the dinners vocabulary (cook, delivery, dining_out)
-- and defaults to cook, which is every existing day. SQLite cannot add a
-- table CHECK that mentions another column, so plan_days is rebuilt as
-- 0018 does: copy the rows aside, drop the table, create it again, copy
-- the rows back and drop the copy. Nothing references plan_days.

CREATE TABLE plan_days_copy AS SELECT * FROM plan_days;

DROP TABLE plan_days;

CREATE TABLE plan_days (
	id                TEXT    NOT NULL PRIMARY KEY,
	plan_id           TEXT    NOT NULL REFERENCES week_plans (id),
	date              TEXT    NOT NULL,
	dinner_id         TEXT    REFERENCES dinners (id),
	manual_label      TEXT,
	meal_source       TEXT    NOT NULL DEFAULT 'cook' CHECK (meal_source IN ('cook', 'delivery', 'dining_out')),
	is_new            INTEGER NOT NULL CHECK (is_new IN (0, 1)),
	calendar_event_id TEXT,
	created_at        TEXT    NOT NULL,
	updated_at        TEXT    NOT NULL,
	UNIQUE (plan_id, date),
	-- A night is a dinner, a one-off label, or cleared. Never both, and a
	-- label is never blank.
	CHECK (dinner_id IS NULL OR manual_label IS NULL),
	CHECK (manual_label IS NULL OR length(manual_label) > 0),
	-- A Delivery or Dining-out evening has no dinner and no manual label.
	-- cook is a dinner, a label, or cleared.
	CHECK (meal_source = 'cook' OR (dinner_id IS NULL AND manual_label IS NULL))
);

INSERT INTO plan_days (
	id, plan_id, date, dinner_id, manual_label, meal_source, is_new, calendar_event_id, created_at, updated_at
)
SELECT
	id, plan_id, date, dinner_id, manual_label, 'cook', is_new, calendar_event_id, created_at, updated_at
FROM plan_days_copy;

DROP TABLE plan_days_copy;
