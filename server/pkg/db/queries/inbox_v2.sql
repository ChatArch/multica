-- Inbox v2 queries (inbox_event / inbox_group).
--
-- Nothing calls these yet; the delivery call sites still write inbox_item.
-- They are the storage half of the refactor, landed ahead of the dual-write
-- switch so the concurrency contracts can be tested against a real database
-- before any producer depends on them.

-- name: FindInboxEventByDeliveryKey :one
-- Dedup probe for the append path. This is only an optimisation: two
-- concurrent transactions can both miss, and the UNIQUE (delivery_key) is what
-- actually decides the winner. The loser rolls back and re-runs this query.
SELECT * FROM inbox_event WHERE delivery_key = @delivery_key;

-- name: AcquireInboxGroup :one
-- Get-or-create the group and leave its row locked for the rest of the
-- transaction. ON CONFLICT DO UPDATE (rather than DO NOTHING) is deliberate:
-- DO NOTHING returns no row and takes no lock, so the caller would have to
-- issue a second SELECT ... FOR UPDATE and race between the two. The no-op
-- SET keeps the row unchanged while still taking the lock.
INSERT INTO inbox_group (
    workspace_id, recipient_id, source_kind, source_id,
    latest_event_at, surfaced_at
) VALUES (
    @workspace_id, @recipient_id, @source_kind, @source_id,
    @now, @now
)
ON CONFLICT (workspace_id, recipient_id, source_kind, source_id)
DO UPDATE SET updated_at = inbox_group.updated_at
RETURNING *;

-- name: InsertInboxEvent :one
-- event_seq is supplied by the caller, which read it from the group row it
-- already holds a lock on. Allocating it here from a subquery would reintroduce
-- the race the lock exists to prevent.
INSERT INTO inbox_event (
    group_id, workspace_id, event_seq, type,
    actor_type, actor_id, target_kind, target_id,
    payload, payload_version, delivery_key, created_at
) VALUES (
    @group_id, @workspace_id, @event_seq, @type,
    @actor_type, @actor_id, @target_kind, @target_id,
    @payload, @payload_version, @delivery_key, @now
)
RETURNING *;

-- name: AdvanceInboxGroupForEvent :one
-- Second half of the append, run in the same transaction as InsertInboxEvent.
-- Clearing archived_at is the "archive is not unsubscribe" rule: a new event
-- pulls a previously archived group back into the inbox.
UPDATE inbox_group
SET latest_seq       = @event_seq,
    latest_event_id  = @event_id,
    latest_event_at  = @now,
    surfaced_at      = @now,
    archived_at      = NULL,
    state_version    = state_version + 1,
    updated_at       = @now
WHERE id = @id
RETURNING *;

-- name: MarkInboxGroupRead :one
-- The read cursor contract.
--
-- GREATEST keeps the cursor monotonic so a late request cannot rewind it.
-- LEAST clamps to latest_seq so a client that sends an inflated observed_seq
-- cannot mark events that have not happened yet as read forever.
--
-- manual_unread clears ONLY when the caller's observed state_version still
-- matches. That is what separates "the user re-opened the group, having seen
-- the manual unread" from "an automatic read that was issued before the user
-- marked it unread and arrived afterwards". Both carry the same observed_seq,
-- so the seq alone cannot tell them apart. On a version mismatch the cursor
-- still advances, which is harmless: manual_unread keeps the group unread.
UPDATE inbox_group
SET read_through_seq = GREATEST(read_through_seq, LEAST(@observed_seq, latest_seq)),
    manual_unread    = CASE WHEN state_version = @observed_state_version
                            THEN false ELSE manual_unread END,
    state_version    = state_version + 1,
    updated_at       = @now
WHERE id = @id AND workspace_id = @workspace_id AND recipient_id = @recipient_id
RETURNING *;

-- name: MarkInboxGroupUnread :one
-- Explicit user intent. The cursor is left alone on purpose: it records how far
-- the user actually read, and manual_unread is what overrides the derived
-- state. Rewinding the cursor instead would lose that distinction and make the
-- group go unread again on the next automatic read.
UPDATE inbox_group
SET manual_unread = true,
    state_version = state_version + 1,
    updated_at    = @now
WHERE id = @id AND workspace_id = @workspace_id AND recipient_id = @recipient_id
RETURNING *;

-- name: ArchiveInboxGroup :one
-- Archive means handled: the cursor is pushed flat and the manual unread is
-- dropped, so the "archived but unread" limbo state cannot be written.
UPDATE inbox_group
SET archived_at      = @now,
    read_through_seq = latest_seq,
    manual_unread    = false,
    state_version    = state_version + 1,
    updated_at       = @now
WHERE id = @id AND workspace_id = @workspace_id AND recipient_id = @recipient_id
RETURNING *;

-- name: UnarchiveInboxGroup :one
-- Read state is not restored: the group was read when it was archived, and
-- pulling it back should not resurrect a badge the user already cleared.
UPDATE inbox_group
SET archived_at   = NULL,
    surfaced_at   = @now,
    state_version = state_version + 1,
    updated_at    = @now
WHERE id = @id AND workspace_id = @workspace_id AND recipient_id = @recipient_id
RETURNING *;

-- name: GetInboxGroupForRecipient :one
-- Every read of a single group goes through the recipient scope rather than the
-- bare id, so a caller cannot reach another person's group by guessing a UUID.
SELECT * FROM inbox_group
WHERE id = @id AND workspace_id = @workspace_id AND recipient_id = @recipient_id;

-- name: ListInboxGroups :many
-- Active view, newest surfaced first, keyset paginated.
--
-- The cursor is (surfaced_at, id) rather than an offset: groups re-sort while
-- the user is paging (any new event moves one to the top), and an offset would
-- silently repeat or skip rows when that happens. Passing a NULL cursor asks
-- for the first page.
SELECT * FROM inbox_group
WHERE workspace_id = @workspace_id
  AND recipient_id = @recipient_id
  AND archived_at IS NULL
  AND (snoozed_until IS NULL OR snoozed_until < @now)
  AND (
        @cursor_surfaced_at::timestamptz IS NULL
        OR (surfaced_at, id) < (@cursor_surfaced_at::timestamptz, @cursor_id::uuid)
      )
ORDER BY surfaced_at DESC, id DESC
LIMIT @page_size;

-- name: ListArchivedInboxGroups :many
-- Archived view. Sorted by archived_at because "when did I deal with this" is
-- the question this list answers; surfaced_at would order it by when the group
-- last appeared in the inbox, which is not the same thing.
SELECT * FROM inbox_group
WHERE workspace_id = @workspace_id
  AND recipient_id = @recipient_id
  AND archived_at IS NOT NULL
  AND (
        @cursor_archived_at::timestamptz IS NULL
        OR (archived_at, id) < (@cursor_archived_at::timestamptz, @cursor_id::uuid)
      )
ORDER BY archived_at DESC, id DESC
LIMIT @page_size;

-- name: CountUnreadInboxGroups :one
-- The single unread definition. Every badge on every client reads this number,
-- replacing the two disagreeing counts the old schema supported.
SELECT COUNT(*) FROM inbox_group
WHERE workspace_id = @workspace_id
  AND recipient_id = @recipient_id
  AND archived_at IS NULL
  AND (snoozed_until IS NULL OR snoozed_until < @now)
  AND (manual_unread OR read_through_seq < latest_seq);

-- name: ListInboxEventsForGroup :many
-- Event history for one group, newest first. Backs "N new updates" when a group
-- is opened with several unread events.
--
-- Joined to inbox_group and scoped by (workspace_id, recipient_id) rather than
-- trusting the group id alone. A bare group UUID arriving from a request is not
-- proof of ownership, and an events endpoint that took one would hand any
-- authenticated user another person's notification history. Scoping here means
-- the guarantee does not depend on every future caller remembering to load the
-- group through GetInboxGroupForRecipient first.
SELECT e.* FROM inbox_event e
JOIN inbox_group g ON g.id = e.group_id
WHERE e.group_id = @group_id
  AND g.workspace_id = @workspace_id
  AND g.recipient_id = @recipient_id
ORDER BY e.event_seq DESC
LIMIT @page_size;

-- name: ListUnreadInboxEventsForGroup :many
-- The events a group is unread *for*, oldest first. The newest of these is the
-- one a click jumps to. Same ownership scoping as ListInboxEventsForGroup.
SELECT e.* FROM inbox_event e
JOIN inbox_group g ON g.id = e.group_id
WHERE e.group_id = @group_id
  AND g.workspace_id = @workspace_id
  AND g.recipient_id = @recipient_id
  AND e.event_seq > @read_through_seq
ORDER BY e.event_seq ASC;

-- name: DeleteInboxGroupsForSource :exec
-- Lifecycle channel: the source entity was deleted. No database cascade exists
-- (repo rule), so this runs from application code in the same transaction as
-- the source delete.
DELETE FROM inbox_group
WHERE workspace_id = @workspace_id AND source_kind = @source_kind AND source_id = @source_id;

-- name: DeleteInboxEventsForDeletedGroups :exec
-- Companion to DeleteInboxGroupsForSource. Events are addressed by group, so
-- they are swept by the same group identity rather than by their own columns.
DELETE FROM inbox_event
WHERE workspace_id = @workspace_id
  AND group_id = ANY(@group_ids::uuid[]);

-- name: DeleteInboxGroupsForRecipient :exec
-- Lifecycle channel: the member left or was removed from the workspace.
-- Notifications are point-in-time, so rejoining does not restore them.
DELETE FROM inbox_group
WHERE workspace_id = @workspace_id AND recipient_id = @recipient_id;

-- name: ListInboxGroupIDsForRecipient :many
-- Read the ids first so the matching events can be deleted in the same
-- transaction as the groups.
SELECT id FROM inbox_group
WHERE workspace_id = @workspace_id AND recipient_id = @recipient_id;

-- name: ListInboxGroupIDsForSource :many
SELECT id FROM inbox_group
WHERE workspace_id = @workspace_id AND source_kind = @source_kind AND source_id = @source_id;

-- name: ListInboxGroupsWithLatestEvent :many
-- The projection source for the legacy GET /api/inbox.
--
-- One row per group, joined to the group's latest event, which is exactly the
-- shape the old clients fold to themselves — so their fold becomes an identity
-- operation and their unread counts and archived view line up without any
-- client change. The join is on latest_event_id rather than a correlated MAX
-- so the list stays one index scan per group.
SELECT sqlc.embed(g), sqlc.embed(e)
FROM inbox_group g
JOIN inbox_event e ON e.id = g.latest_event_id
WHERE g.workspace_id = @workspace_id
  AND g.recipient_id = @recipient_id
  AND g.archived_at IS NULL
  AND (g.snoozed_until IS NULL OR g.snoozed_until < @now)
ORDER BY g.surfaced_at DESC, g.id DESC;

-- name: ListArchivedInboxGroupsWithLatestEvent :many
-- Archived counterpart. The two lists are mutually exclusive by construction
-- here: a group is either archived or it is not, which is what removes the old
-- table's "same issue shows in both views" problem rather than papering over it
-- with a NOT EXISTS subquery.
SELECT sqlc.embed(g), sqlc.embed(e)
FROM inbox_group g
JOIN inbox_event e ON e.id = g.latest_event_id
WHERE g.workspace_id = @workspace_id
  AND g.recipient_id = @recipient_id
  AND g.archived_at IS NOT NULL
ORDER BY g.archived_at DESC, g.id DESC
LIMIT @page_size;

-- name: FindInboxGroupByEventID :one
-- Legacy id translation: old single-item write endpoints address a row by its
-- event id, so the projection has to resolve that back to the group the write
-- actually applies to. Scoped by recipient so a stray id cannot reach another
-- person's group.
SELECT sqlc.embed(g), sqlc.embed(e)
FROM inbox_event e
JOIN inbox_group g ON g.id = e.group_id
WHERE e.id = @event_id
  AND g.workspace_id = @workspace_id
  AND g.recipient_id = @recipient_id;

-- name: MarkAllInboxGroupsRead :execrows
-- Batch counterpart of MarkInboxGroupRead.
--
-- Each group is pushed to its OWN latest_seq rather than to a snapshot taken
-- when the request started: the cursor is per group, so "read everything" means
-- "each group is read through its own newest event". An event arriving while
-- the statement runs lands above the cursor it just set and stays unread, which
-- is the behaviour the old boolean update could not express.
UPDATE inbox_group
SET read_through_seq = latest_seq,
    manual_unread    = false,
    state_version    = state_version + 1,
    updated_at       = @now
WHERE workspace_id = @workspace_id
  AND recipient_id = @recipient_id
  AND archived_at IS NULL
  AND (manual_unread OR read_through_seq < latest_seq);

-- name: ArchiveAllInboxGroups :execrows
UPDATE inbox_group
SET archived_at      = @now,
    read_through_seq = latest_seq,
    manual_unread    = false,
    state_version    = state_version + 1,
    updated_at       = @now
WHERE workspace_id = @workspace_id
  AND recipient_id = @recipient_id
  AND archived_at IS NULL;

-- name: ArchiveReadInboxGroups :execrows
-- "Archive everything I have already read". The read predicate is the same
-- expression the unread count uses, negated — one definition, so the button and
-- the badge can never disagree about which rows it will take.
UPDATE inbox_group
SET archived_at   = @now,
    state_version = state_version + 1,
    updated_at    = @now
WHERE workspace_id = @workspace_id
  AND recipient_id = @recipient_id
  AND archived_at IS NULL
  AND NOT (manual_unread OR read_through_seq < latest_seq);

-- name: ArchiveCompletedInboxGroups :execrows
-- Mirrors the legacy ArchiveCompletedInbox: notifications about issues that are
-- finished. Only issue-sourced groups can qualify, which the source_kind filter
-- makes explicit rather than relying on the join to drop the rest.
UPDATE inbox_group g
SET archived_at      = @now,
    read_through_seq = g.latest_seq,
    manual_unread    = false,
    state_version    = g.state_version + 1,
    updated_at       = @now
WHERE g.workspace_id = @workspace_id
  AND g.recipient_id = @recipient_id
  AND g.archived_at IS NULL
  AND g.source_kind = 'issue'
  AND g.source_id IN (SELECT id FROM issue WHERE status IN ('done', 'cancelled'));

-- name: CountUnreadInboxGroupsByWorkspace :many
-- Cross-workspace unread summary for the workspace switcher. Account-level by
-- nature: keyed only on the user, ignoring whichever workspace is active.
SELECT workspace_id, COUNT(*) AS count
FROM inbox_group
WHERE recipient_id = @recipient_id
  AND archived_at IS NULL
  AND (snoozed_until IS NULL OR snoozed_until < @now)
  AND (manual_unread OR read_through_seq < latest_seq)
GROUP BY workspace_id;
