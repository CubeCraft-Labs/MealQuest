-- 0009_dinner_grounding_error: a dinner whose grounding failed keeps why,
-- as one of the reason codes package dinners defines (no_recipe_found,
-- recipe_unusable, search_not_configured, search_failed, ai_failed,
-- interrupted, grounding_error). It is NULL unless grounding_status is
-- failed.

ALTER TABLE dinners ADD COLUMN grounding_error TEXT;
