-- Quick-action-triggered comments (MUL-5465).
--
-- A quick action posts a real comment so the trigger stays auditable and rides
-- the existing mention path. It gets its own `type` so the timeline can render
-- it as a collapsed one-line card instead of dumping the rendered prompt into
-- the discussion -- eight Code Review clicks would otherwise bury the thread.
--
-- quick_action_id points at the action that produced the comment. No FK per
-- repo policy: actions archive rather than delete, and the render path treats
-- an unresolvable id as a plain comment.
ALTER TABLE comment DROP CONSTRAINT IF EXISTS comment_type_check;
ALTER TABLE comment ADD CONSTRAINT comment_type_check
    CHECK (type IN ('comment', 'status_change', 'progress_update', 'system', 'quick_action'));

ALTER TABLE comment ADD COLUMN IF NOT EXISTS quick_action_id UUID;
