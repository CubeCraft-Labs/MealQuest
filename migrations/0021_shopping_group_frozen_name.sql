-- 0021_shopping_group_frozen_name: a Supply run frozen into the Supply
-- archive keeps the names its dinners had when it was frozen. A dinner can
-- be renamed at any time, so a frozen list reads each group's dinner name
-- from frozen_dinner_name, never from the dinner, as 0012 does for a Pass.
-- Only a group of a frozen list has one; a current list reads its dinners'
-- names live. A list frozen before this takes its dinners' names now.

ALTER TABLE shopping_groups ADD COLUMN frozen_dinner_name TEXT CHECK (frozen_dinner_name <> '');

UPDATE shopping_groups
SET frozen_dinner_name = (SELECT dinners.name FROM dinners WHERE dinners.id = shopping_groups.dinner_id)
WHERE list_id IN (
	SELECT id FROM shopping_lists WHERE status = 'archived' OR frozen_at IS NOT NULL
);
