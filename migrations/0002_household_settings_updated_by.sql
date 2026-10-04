-- 0002_household_settings_updated_by: record who last changed the household
-- settings, as dinners and suggestion_batches already do. NULL until the
-- first change made by a signed-in member.
ALTER TABLE household_settings ADD COLUMN updated_by TEXT;
