-- Rows written by the new path must lose the type before the constraint is
-- narrowed again, otherwise the re-add fails on existing data.
UPDATE comment SET type = 'comment' WHERE type = 'quick_action';

ALTER TABLE comment DROP CONSTRAINT IF EXISTS comment_type_check;
ALTER TABLE comment ADD CONSTRAINT comment_type_check
    CHECK (type IN ('comment', 'status_change', 'progress_update', 'system'));

ALTER TABLE comment DROP COLUMN IF EXISTS quick_action_id;
