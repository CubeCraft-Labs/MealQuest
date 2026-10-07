-- 0011_ground_dinner_job_revision: every dinner starts at grounding
-- revision 0 (0010), so a ground_dinner job enqueued before 0010, whose
-- payload has only dinner_id, is for revision 0. Recording it lets the boot
-- cleanup, which matches a pending dinner to its job by revision, see a job
-- still queued for its dinner. This is a migration of its own, not part of
-- 0010, so it also runs on a database that has already applied 0010.

UPDATE jobs
SET payload = json_set(payload, '$.revision', 0)
WHERE kind = 'ground_dinner'
	AND json_type(payload, '$.revision') IS NULL;
