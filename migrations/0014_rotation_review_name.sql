-- 0014_rotation_review_name: a Rotation check decided drop ("Not for us")
-- keeps the name its dinner had when the household dropped it. The dinner
-- can be renamed, even once archived, so the passed names every later
-- batch refuses are read from here, never from the dinner, as 0012 does
-- for a Pass. Only a drop has a name. A check dropped before this takes
-- its dinner's name now.
-- SQLite cannot add a column with a CHECK that existing drops would fail
-- before they are filled in, so the table is rebuilt as 0012 does: copy
-- the rows aside, drop the table, create it again, copy the rows back and
-- drop the copy. Nothing references rotation_reviews.

CREATE TABLE rotation_reviews_copy AS SELECT * FROM rotation_reviews;

DROP TABLE rotation_reviews;

CREATE TABLE rotation_reviews (
	id         TEXT NOT NULL PRIMARY KEY,
	dinner_id  TEXT NOT NULL REFERENCES dinners (id),
	plan_date  TEXT NOT NULL,
	status     TEXT NOT NULL CHECK (status IN ('pending', 'decided')),
	decision   TEXT CHECK (decision IN ('keep', 'drop', 'not_made')),
	name       TEXT CHECK (name <> ''), -- the dinner's name when dropped
	decided_at TEXT,
	updated_by TEXT,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	UNIQUE (dinner_id, plan_date),
	CHECK ((decision IS 'drop') = (name IS NOT NULL))
);

INSERT INTO rotation_reviews (
	id, dinner_id, plan_date, status, decision, name, decided_at, updated_by, created_at, updated_at
)
SELECT
	rotation_reviews_copy.id, rotation_reviews_copy.dinner_id, rotation_reviews_copy.plan_date,
	rotation_reviews_copy.status, rotation_reviews_copy.decision,
	CASE WHEN rotation_reviews_copy.decision = 'drop' THEN dinners.name END,
	rotation_reviews_copy.decided_at, rotation_reviews_copy.updated_by,
	rotation_reviews_copy.created_at, rotation_reviews_copy.updated_at
FROM rotation_reviews_copy
JOIN dinners ON dinners.id = rotation_reviews_copy.dinner_id;

DROP TABLE rotation_reviews_copy;
