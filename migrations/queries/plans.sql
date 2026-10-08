-- name: GetWeekPlan :one
-- The plan of the week starting week_start. Returns sql.ErrNoRows when
-- there is none.
SELECT * FROM week_plans
WHERE week_start = ?;

-- name: ListPlanDays :many
-- The days of plan plan_id with their dinners, Monday first. A day with
-- no dinner is left out.
SELECT plan_days.date, plan_days.is_new, plan_days.calendar_event_id, sqlc.embed(dinners)
FROM plan_days
JOIN dinners ON dinners.id = plan_days.dinner_id
WHERE plan_days.plan_id = ?
ORDER BY plan_days.date;

-- name: CreateWeekPlan :exec
INSERT INTO week_plans (id, week_start, status, ai_note, created_at, updated_at)
VALUES (?, ?, 'draft', ?, ?, ?);

-- name: RedraftWeekPlan :exec
-- Records a new draft over plan id: its status and note, raising its
-- revision.
UPDATE week_plans
SET status = ?, ai_note = ?, revision = revision + 1, updated_at = ?
WHERE id = ?;

-- name: PutPlanDay :exec
-- Plans dinner_id on date of plan_id. An existing day keeps its calendar
-- event id while its dinner stays the same; a new dinner clears it, since
-- Calendar no longer shows the day as planned.
INSERT INTO plan_days (id, plan_id, date, dinner_id, is_new, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (plan_id, date) DO UPDATE
SET dinner_id = excluded.dinner_id, is_new = excluded.is_new,
	calendar_event_id = CASE WHEN plan_days.dinner_id IS excluded.dinner_id THEN plan_days.calendar_event_id END,
	updated_at = excluded.updated_at;

-- name: SwapPlanDay :execrows
-- Plans dinner_id on an existing day of plan_id, clearing its calendar
-- event id since Calendar no longer shows the day as planned; a date the
-- plan does not have is left as it is (0 rows).
UPDATE plan_days
SET dinner_id = ?, is_new = ?, calendar_event_id = NULL, updated_at = ?
WHERE plan_id = ? AND date = ?;

-- name: SetPlanDayCalendarEvent :execrows
-- Records that the event calendar_event_id on Calendar shows date of
-- plan_id as planned.
UPDATE plan_days
SET calendar_event_id = ?, updated_at = ?
WHERE plan_id = ? AND date = ?;

-- name: MarkWeekPlanSynced :execrows
-- Records a Push to Calendar of plan id as it was at revision: the plan
-- becomes synced. A plan edited since (another revision) is left as it is
-- (0 rows).
UPDATE week_plans
SET status = 'synced', calendar_synced_at = ?, updated_at = ?
WHERE id = ? AND revision = ?;

-- name: MarkWeekPlanEdited :exec
-- Records an edit of plan id, raising its revision: a synced plan becomes
-- changed, any other keeps its status.
UPDATE week_plans
SET status = CASE WHEN status = 'synced' THEN 'changed' ELSE status END,
	revision = revision + 1,
	updated_at = ?
WHERE id = ?;

-- name: ListDraftPool :many
-- The dinners a draft of the week starting week_start may plan: every
-- Regular, and every Quest log dinner no other plan for the week starting
-- from_week_start or later has on any day.
SELECT * FROM dinners
WHERE status = 'regular'
	OR (status = 'quest_log' AND NOT EXISTS (
		SELECT 1 FROM plan_days
		JOIN week_plans ON week_plans.id = plan_days.plan_id
		WHERE plan_days.dinner_id = dinners.id
			AND week_plans.week_start >= sqlc.arg(from_week_start)
			AND week_plans.week_start <> sqlc.arg(week_start)
	))
ORDER BY normalized_name, id;

-- name: ListWeekPlanDinnerIDs :many
-- The ids of the dinners planned in the week starting week_start, Monday
-- first.
SELECT plan_days.dinner_id
FROM plan_days
JOIN week_plans ON week_plans.id = plan_days.plan_id
WHERE week_plans.week_start = ? AND plan_days.dinner_id IS NOT NULL
ORDER BY plan_days.date;

-- name: GetActiveDraftJob :one
-- The job of kind queued or running for the week starting week_start,
-- with whether its payload has confirm. StartDraft enqueues a draft only
-- when there is none, so there is at most one. Returns sql.ErrNoRows when
-- there is none.
SELECT id, CAST(json_extract(payload, '$.confirm') = 1 AS INTEGER) AS confirmed
FROM jobs
WHERE kind = sqlc.arg(kind)
	AND status IN ('queued', 'running')
	AND json_extract(payload, '$.week_start') = sqlc.arg(week_start)
ORDER BY id
LIMIT 1;
