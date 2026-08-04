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
SELECT * FROM inbox_event
WHERE group_id = @group_id
ORDER BY event_seq DESC
LIMIT @page_size;

-- name: ListUnreadInboxEventsForGroup :many
-- The events a group is unread *for*, oldest first. The newest of these is the
-- one a click jumps to.
SELECT * FROM inbox_event
WHERE group_id = @group_id AND event_seq > @read_through_seq
ORDER BY event_seq ASC;

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
