-- 0018_dinner_meal_source: a dinner is cooked at home (cook), ordered in
-- (delivery) or eaten out (dining_out). Delivery and dining out are not
-- planned as cooked dinners: the draft pool leaves them out, a Supply run
-- takes no ingredients from them, and they open no Rotation check.
-- Existing dinners are cooked at home.

ALTER TABLE dinners ADD COLUMN meal_source TEXT NOT NULL DEFAULT 'cook'
	CHECK (meal_source IN ('cook', 'delivery', 'dining_out'));
