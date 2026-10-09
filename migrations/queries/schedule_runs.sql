-- name: ClaimScheduleRun :execrows
-- Claims one period of a schedule. The insert is the claim: 1 row means
-- this caller owns the period, 0 rows means it was already claimed.
INSERT INTO schedule_runs (id, schedule, period_key, created_at)
VALUES (?, ?, ?, ?)
ON CONFLICT (schedule, period_key) DO NOTHING;
