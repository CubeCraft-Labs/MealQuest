-- 0008_dinner_recipe_url: a Regular added from a pasted recipe link keeps
-- that link until grounding resolves it to a recipe (recipe_id). Nothing
-- else records the link: recipes rows exist only for fetched pages.

ALTER TABLE dinners ADD COLUMN recipe_url TEXT;
