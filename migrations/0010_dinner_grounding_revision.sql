-- 0010_dinner_grounding_revision: each ground_dinner job carries the
-- dinner's grounding_revision when it was enqueued, and records its result
-- only while the dinner still has that revision. Renaming a pending dinner
-- raises the revision before enqueuing the job for the new name, so an
-- earlier job — queued or running — can no longer write the dinner.

ALTER TABLE dinners ADD COLUMN grounding_revision INTEGER NOT NULL DEFAULT 0;
