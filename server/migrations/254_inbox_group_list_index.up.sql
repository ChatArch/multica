-- Single statement: CREATE INDEX CONCURRENTLY cannot run inside a transaction
-- or share a multi-command migration file.
--
-- Backs the only hot read: one person's inbox, active rows first, newest
-- surfaced first. Column order follows the query — equality on
-- (workspace_id, recipient_id), then archived_at to separate the active view
-- from the archived view, then the sort key. The trailing id makes the
-- (surfaced_at, id) pagination cursor a pure index scan, so a page never
-- repeats or skips a row when two groups share a surfaced_at.
CREATE INDEX CONCURRENTLY IF NOT EXISTS inbox_group_recipient_surfaced_idx
    ON inbox_group (workspace_id, recipient_id, archived_at, surfaced_at DESC, id);
