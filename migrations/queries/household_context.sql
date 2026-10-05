-- Reads for the AI household context block (internal/ai/harness).

-- name: ListRecentDeclinedNames :many
-- The names of the most recently declined suggestions, newest first.
SELECT dinners.name
FROM suggestions
JOIN dinners ON dinners.id = suggestions.dinner_id
WHERE suggestions.status = 'declined'
ORDER BY suggestions.decided_at DESC, suggestions.id DESC
LIMIT ?;

-- name: ListPlannedDinners :many
-- The planned dinners dated from from_date up to but not including
-- to_date, oldest first. Days with no dinner are left out.
SELECT plan_days.date, dinners.name
FROM plan_days
JOIN dinners ON dinners.id = plan_days.dinner_id
WHERE plan_days.date >= sqlc.arg(from_date) AND plan_days.date < sqlc.arg(to_date)
ORDER BY plan_days.date, plan_days.id;
