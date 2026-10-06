-- 0007_recipe_grounding: a grounded recipe keeps what recipes.Ground
-- returns, so a stored source_url is reused without fetching or normalizing
-- it again: its cached photo, and normalize_recipe's method, difficulty and
-- minutes_total (prep to table; total_minutes stays the page's own JSON-LD
-- total). Nothing wrote recipes before Ground did, so the columns are added
-- nullable rather than with a default no recipe has; Ground always sets
-- photo_id, method and difficulty and refuses to reuse a row without them.
--
-- recipe_search_settings gains api_key_last4, the last four characters of
-- the Brave API key, so the settings can show which key is stored without
-- ever returning it.

ALTER TABLE recipes ADD COLUMN photo_id TEXT REFERENCES photos (id);
ALTER TABLE recipes ADD COLUMN method TEXT CHECK (method IN ('stovetop', 'oven', 'crockpot', 'grill', 'other'));
ALTER TABLE recipes ADD COLUMN difficulty TEXT CHECK (difficulty IN ('easy', 'medium', 'hard'));
ALTER TABLE recipes ADD COLUMN minutes_total INTEGER;

ALTER TABLE recipe_search_settings ADD COLUMN api_key_last4 TEXT;
