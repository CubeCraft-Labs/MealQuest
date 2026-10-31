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
-- Plans dinner_id on date of plan_id, clearing any manual label. An
-- existing day keeps its calendar event id while its dinner stays the
-- same and it was not a manual label; a new dinner clears it, since
-- Calendar no longer shows the day as planned.
INSERT INTO plan_days (id, plan_id, date, dinner_id, manual_label, is_new, created_at, updated_at)
VALUES (?, ?, ?, ?, NULL, ?, ?, ?)
ON CONFLICT (plan_id, date) DO UPDATE
SET dinner_id = excluded.dinner_id, manual_label = NULL, is_new = excluded.is_new,
	calendar_event_id = CASE
		WHEN plan_days.dinner_id IS excluded.dinner_id AND plan_days.manual_label IS NULL THEN plan_days.calendar_event_id
	END,
	updated_at = excluded.updated_at;

-- name: SwapPlanDay :execrows
-- Plans dinner_id on an existing day of plan_id, clearing its manual
-- label and its calendar event id since Calendar no longer shows the day
-- as planned; a date the plan does not have is left as it is (0 rows).
UPDATE plan_days
SET dinner_id = ?, manual_label = NULL, is_new = ?, calendar_event_id = NULL, updated_at = ?
WHERE plan_id = ? AND date = ?;

-- name: SetManualPlanDay :execrows
-- Names an existing day of plan_id with a one-off label and no dinner,
-- clearing its calendar event id. A date the plan does not have is left
-- as it is (0 rows).
UPDATE plan_days
SET dinner_id = NULL, manual_label = ?, is_new = 0, calendar_event_id = NULL, updated_at = ?
WHERE plan_id = ? AND date = ?;

-- name: ClearPlanDay :execrows
-- Clears an existing day of plan_id: no dinner and no manual label, and
-- its calendar event id, since Calendar no longer shows the day as
-- planned. A date the plan does not have is left as it is (0 rows).
UPDATE plan_days
SET dinner_id = NULL, manual_label = NULL, is_new = 0, calendar_event_id = NULL, updated_at = ?
WHERE plan_id = ? AND date = ?;

-- name: ListPlanSlots :many
-- Every day of plan_id, Monday first. A day is a dinner (dinner_id set),
-- a one-off manual label, or cleared (both null).
SELECT date, is_new, calendar_event_id, dinner_id, manual_label
FROM plan_days
WHERE plan_id = ?
ORDER BY date;

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

-- name: MarkCalendarPlansEdited :exec
-- Records that Calendar no longer shows the days with a calendar event id
-- as planned, since another calendar or none is chosen: every plan with
-- such a day is edited as MarkWeekPlanEdited does, so a synced plan becomes
-- changed and a push that read it before is refused.
UPDATE week_plans
SET status = CASE WHEN status = 'synced' THEN 'changed' ELSE status END,
	revision = revision + 1,
	updated_at = ?
WHERE EXISTS (
	SELECT 1 FROM plan_days
	WHERE plan_days.plan_id = week_plans.id AND plan_days.calendar_event_id IS NOT NULL
);

-- name: ListDinnerPlanDaysFrom :many
-- Plan days of dinner dinner_id on from_date or later. from_date is a
-- household-local YYYY-MM-DD. A day before it is past and is left out.
SELECT plan_days.plan_id, plan_days.date, plan_days.calendar_event_id
FROM plan_days
WHERE plan_days.dinner_id = sqlc.arg(dinner_id)
	AND plan_days.date >= sqlc.arg(from_date)
ORDER BY plan_days.date;

-- name: DinnerPlannedOnDate :one
-- Reports whether dinner_id is the dinner planned on date, a
-- household-local YYYY-MM-DD. A cleared night, or another dinner, is false.
SELECT EXISTS (
	SELECT 1 FROM plan_days
	WHERE plan_days.dinner_id = sqlc.arg(dinner_id) AND plan_days.date = sqlc.arg(date)
) AS planned;

-- name: ManualLabelOnDate :one
-- The one-off label of the plan day on date, a household-local YYYY-MM-DD.
-- Returns sql.ErrNoRows when that date has no plan day. A dinner or a
-- cleared night has a null label.
SELECT manual_label FROM plan_days
WHERE date = ?;

-- name: ClearPlanDayCalendarEvent :execrows
-- Clears the calendar event id of date on plan_id. Calendar no longer
-- shows that day as it was planned, so the next Push to Calendar writes
-- it again. A day that has no event id is left as it is (0 rows).
UPDATE plan_days
SET calendar_event_id = NULL, updated_at = ?
WHERE plan_id = ? AND date = ? AND calendar_event_id IS NOT NULL;

-- name: ClearCalendarEvents :exec
-- Clears every day's calendar event id: none is on the chosen calendar.
UPDATE plan_days
SET calendar_event_id = NULL, updated_at = ?
WHERE calendar_event_id IS NOT NULL;

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
-- cooked Regular, and every cooked Quest log dinner no other plan for the
-- week starting from_week_start or later has on any day. Delivery and
-- dining out are not cooked, so a draft never selects them.
SELECT * FROM dinners
WHERE meal_source = 'cook'
	AND (status = 'regular'
	OR (status = 'quest_log' AND NOT EXISTS (
		SELECT 1 FROM plan_days
		JOIN week_plans ON week_plans.id = plan_days.plan_id
		WHERE plan_days.dinner_id = dinners.id
			AND week_plans.week_start >= sqlc.arg(from_week_start)
			AND week_plans.week_start <> sqlc.arg(week_start)
	)))
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
