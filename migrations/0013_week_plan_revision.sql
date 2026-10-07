-- 0013_week_plan_revision: every edit of a week's plan — a swap or a
-- stored draft — raises its revision. A draft_week job reads the revision
-- before draft_week runs and stores its week only while the plan still
-- has it, so a draft never overwrites an edit made while the AI ran.

ALTER TABLE week_plans ADD COLUMN revision INTEGER NOT NULL DEFAULT 0;
