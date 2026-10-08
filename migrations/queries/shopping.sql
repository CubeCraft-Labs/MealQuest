-- name: GetShoppingList :one
-- The Supply run of the week starting week_start. Returns sql.ErrNoRows
-- when there is none.
SELECT * FROM shopping_lists
WHERE week_start = ?;

-- name: CreateShoppingList :exec
-- A new, untitled Supply run: the title is written later, by the AI.
INSERT INTO shopping_lists (id, week_start, plan_id, status, created_at, updated_at)
VALUES (?, ?, ?, 'current', ?, ?);

-- name: RegenerateShoppingList :exec
-- Records a generation of list id from plan plan_id.
UPDATE shopping_lists
SET plan_id = ?, updated_at = ?
WHERE id = ?;

-- name: ListShoppingGroups :many
-- The groups of list list_id, in list order.
SELECT * FROM shopping_groups
WHERE list_id = ?
ORDER BY position, id;

-- name: ListShoppingItems :many
-- The items of list list_id with their group's dinner, in list order: by
-- group, then by item.
SELECT sqlc.embed(shopping_items), shopping_groups.dinner_id
FROM shopping_items
JOIN shopping_groups ON shopping_groups.id = shopping_items.group_id
WHERE shopping_groups.list_id = ?
ORDER BY shopping_groups.position, shopping_items.position, shopping_items.id;

-- name: CreateShoppingGroup :exec
INSERT INTO shopping_groups (id, list_id, dinner_id, date, position, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: MoveShoppingGroup :exec
UPDATE shopping_groups
SET position = ?, updated_at = ?
WHERE id = ?;

-- name: DeleteShoppingGroup :exec
DELETE FROM shopping_groups
WHERE id = ?;

-- name: CreateShoppingItem :exec
-- A new item is needed until the household marks it "have it".
INSERT INTO shopping_items (id, group_id, name, qty, needed, position, created_at, updated_at)
VALUES (?, ?, ?, ?, 1, ?, ?, ?);

-- name: MoveShoppingItem :exec
-- Places item id in group group_id at position with quantity qty; its
-- name and needed flag are kept.
UPDATE shopping_items
SET group_id = ?, qty = ?, position = ?, updated_at = ?
WHERE id = ?;

-- name: DeleteShoppingItem :exec
DELETE FROM shopping_items
WHERE id = ?;

-- name: ListShoppingGroupDinners :many
-- The groups of list list_id with their dinner's name, in list order.
SELECT sqlc.embed(shopping_groups), dinners.name AS dinner_name
FROM shopping_groups
JOIN dinners ON dinners.id = shopping_groups.dinner_id
WHERE shopping_groups.list_id = ?
ORDER BY shopping_groups.position, shopping_groups.id;

-- name: GetShoppingItemList :one
-- The Supply run holding item id. Returns sql.ErrNoRows when there is no
-- such item.
SELECT sqlc.embed(shopping_lists)
FROM shopping_items
JOIN shopping_groups ON shopping_groups.id = shopping_items.group_id
JOIN shopping_lists ON shopping_lists.id = shopping_groups.list_id
WHERE shopping_items.id = ?;

-- name: SetShoppingItemNeeded :exec
-- Marks item id needed (1) or "have it" (0).
UPDATE shopping_items
SET needed = ?, updated_at = ?
WHERE id = ?;

-- name: SetShoppingListTitle :execrows
-- Titles list id, unless it has a title already or is frozen into the
-- Supply archive.
UPDATE shopping_lists
SET title = ?, updated_at = ?
WHERE id = ? AND title IS NULL AND status = 'current' AND frozen_at IS NULL;

-- name: FreezeShoppingList :execrows
-- Freezes list id into the Supply archive at frozen_at, unless it is
-- frozen already.
UPDATE shopping_lists
SET status = 'archived', frozen_at = ?, updated_at = ?
WHERE id = ? AND frozen_at IS NULL;

-- name: ListShoppingListsThrough :many
-- The Supply runs of the weeks starting on or before week_start, newest
-- week first.
SELECT * FROM shopping_lists
WHERE week_start <= ?
ORDER BY week_start DESC;
