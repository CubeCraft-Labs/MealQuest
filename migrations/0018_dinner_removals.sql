-- 0018_dinner_removals: each removal of a dinner (DELETE /dinners/{id}) the
-- PWA sends carries an id the PWA chose, recorded here with its outcome:
-- archived when the server archived the dinner for it, withdrawn when the
-- PWA asked, with no answer that settled it, for it never to be made. A
-- removal is recorded once, so it is either made or withdrawn for good: a
-- withdrawn removal arriving late archives nothing. Existing dinners have no
-- removals recorded.

CREATE TABLE dinner_removals (
	id         TEXT NOT NULL PRIMARY KEY,
	dinner_id  TEXT NOT NULL REFERENCES dinners (id),
	outcome    TEXT NOT NULL CHECK (outcome IN ('archived', 'withdrawn')),
	created_by TEXT NOT NULL,
	created_at TEXT NOT NULL
);

CREATE INDEX dinner_removals_dinner_id ON dinner_removals (dinner_id);
