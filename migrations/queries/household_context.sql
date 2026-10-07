-- Reads for the AI household context block (internal/ai/harness).

-- name: ListRecentPassedNames :many
-- The most recent names the household turned down, newest first, as
-- ListPassedNames has them.
SELECT name FROM (
	SELECT name, decided_at, id
	FROM suggestions
	WHERE status = 'declined'
	UNION ALL
	SELECT name, decided_at, id
	FROM rotation_reviews
	WHERE decision = 'drop'
)
ORDER BY decided_at DESC, id DESC
LIMIT ?;

-- name: ListPlannedDinners :many
-- The planned dinners dated from from_date up to but not including
-- to_date, oldest first. Days with no dinner are left out.
SELECT plan_days.date, dinners.name
FROM plan_days
JOIN dinners ON dinners.id = plan_days.dinner_id
WHERE plan_days.date >= sqlc.arg(from_date) AND plan_days.date < sqlc.arg(to_date)
ORDER BY plan_days.date, plan_days.id;
