-- 0010_dinner_grounding_revision: each ground_dinner job carries the
-- dinner's grounding_revision when it was enqueued, and records its result
-- only while the dinner still has that revision. Renaming a pending dinner
-- raises the revision before enqueuing the job for the new name, so an
-- earlier job — queued or running — can no longer write the dinner.

ALTER TABLE dinners ADD COLUMN grounding_revision INTEGER NOT NULL DEFAULT 0;

-- Every dinner starts at revision 0, so a ground_dinner job enqueued
-- before this migration, whose payload has only dinner_id, is for revision
-- 0. Recording it lets the boot cleanup, which matches a pending dinner to
-- its job by revision, see a job still queued for its dinner.
UPDATE jobs
SET payload = json_set(payload, '$.revision', 0)
WHERE kind = 'ground_dinner'
	AND json_type(payload, '$.revision') IS NULL;
