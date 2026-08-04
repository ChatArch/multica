-- Single statement: CREATE INDEX CONCURRENTLY cannot run inside a transaction
-- or share a multi-command migration file.
--
-- Backs the only hot read: one person's inbox, active rows first, newest
-- surfaced first. Column order follows the query — equality on
-- (workspace_id, recipient_id), then archived_at to separate the active view
-- from the archived view, then the sort key.
--
-- Both sort columns are DESC to match ListInboxGroups' ORDER BY exactly
-- (surfaced_at DESC, id DESC). The trailing id is what makes the
-- (surfaced_at, id) keyset cursor resolvable inside the index when two groups
-- share a surfaced_at; giving it the opposite direction to the query would
-- leave the tie-break ordering to a sort step instead.
CREATE INDEX CONCURRENTLY IF NOT EXISTS inbox_group_recipient_surfaced_idx
    ON inbox_group (workspace_id, recipient_id, archived_at, surfaced_at DESC, id DESC);
