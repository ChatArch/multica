-- Drop the tombstone first: rows that were opted out become plain absences
-- again, which is what the pre-235 code means by "not subscribed".
DELETE FROM issue_subscriber WHERE unsubscribed_at IS NOT NULL;
ALTER TABLE issue_subscriber DROP COLUMN unsubscribed_at;

-- 'delegated' has no pre-235 equivalent. 'manual' is the closest surviving
-- label (a subscription that is neither creator/assignee nor derived from a
-- comment or mention) and matches how 120_autopilot_subscriber.down.sql
-- retires its own added reason.
UPDATE issue_subscriber SET reason = 'manual' WHERE reason = 'delegated';

ALTER TABLE issue_subscriber DROP CONSTRAINT issue_subscriber_reason_check;
ALTER TABLE issue_subscriber ADD CONSTRAINT issue_subscriber_reason_check
    CHECK (reason IN ('creator', 'assignee', 'commenter', 'mentioned', 'manual', 'autopilot'));
