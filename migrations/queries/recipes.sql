-- name: UpsertRecipe :one
-- A recipe is keyed by source_url: re-fetching the same URL updates the
-- existing row and keeps its id and created_at, which the returned row
-- carries. Replace its ingredients and steps in the same transaction with
-- DeleteRecipeIngredients/DeleteRecipeSteps then the Create queries.
INSERT INTO recipes (
	id, source_url, site_name, title, total_minutes, prep_minutes,
	cook_minutes, yield, raw_jsonld, photo_id, method, difficulty,
	minutes_total, fetched_at, created_at, updated_at
) VALUES (
	?, ?, ?, ?, ?, ?,
	?, ?, ?, ?, ?, ?,
	?, ?, ?, ?
)
ON CONFLICT (source_url) DO UPDATE SET
	site_name = excluded.site_name,
	title = excluded.title,
	total_minutes = excluded.total_minutes,
	prep_minutes = excluded.prep_minutes,
	cook_minutes = excluded.cook_minutes,
	yield = excluded.yield,
	raw_jsonld = excluded.raw_jsonld,
	photo_id = excluded.photo_id,
	method = excluded.method,
	difficulty = excluded.difficulty,
	minutes_total = excluded.minutes_total,
	fetched_at = excluded.fetched_at,
	updated_at = excluded.updated_at
RETURNING *;

-- name: GetRecipe :one
SELECT * FROM recipes
WHERE id = ?;

-- name: GetRecipeBySourceURL :one
SELECT * FROM recipes
WHERE source_url = ?;

-- name: CreateRecipeIngredient :exec
INSERT INTO recipe_ingredients (
	id, recipe_id, position, raw_text, name, qty, unit, note,
	created_at, updated_at
) VALUES (
	?, ?, ?, ?, ?, ?, ?, ?,
	?, ?
);

-- name: ListRecipeIngredients :many
SELECT * FROM recipe_ingredients
WHERE recipe_id = ?
ORDER BY position, id;

-- name: DeleteRecipeIngredients :exec
DELETE FROM recipe_ingredients
WHERE recipe_id = ?;

-- name: CreateRecipeStep :exec
INSERT INTO recipe_steps (
	id, recipe_id, position, text, created_at, updated_at
) VALUES (
	?, ?, ?, ?, ?, ?
);

-- name: ListRecipeSteps :many
SELECT * FROM recipe_steps
WHERE recipe_id = ?
ORDER BY position, id;

-- name: DeleteRecipeSteps :exec
DELETE FROM recipe_steps
WHERE recipe_id = ?;

-- name: CreateManualRecipe :one
-- A recipe the household typed in. Its source_url is a mealquest:manual:
-- URN rather than a page, so it never matches a fetched page.
INSERT INTO recipes (
	id, source_url, title, total_minutes, raw_jsonld, method,
	minutes_total, fetched_at, created_at, updated_at
) VALUES (
	?, ?, ?, ?, ?, ?,
	?, ?, ?, ?
)
RETURNING *;
