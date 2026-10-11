-- 0018_recipe_item_position_unique: position is the only record of recipe
-- order, so a recipe never has two ingredients or two steps at one
-- position. Writers replace a recipe's items whole, numbered from 0, and
-- the detail read orders by position alone. A database already holding a
-- duplicate fails this migration rather than keep an order nobody can
-- trust.

CREATE UNIQUE INDEX recipe_ingredients_recipe_position ON recipe_ingredients (recipe_id, position);
CREATE UNIQUE INDEX recipe_steps_recipe_position ON recipe_steps (recipe_id, position);
