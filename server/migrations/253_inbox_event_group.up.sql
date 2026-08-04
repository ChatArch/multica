-- Inbox v2 storage: split the single `inbox_item` table into an immutable
-- fact table (inbox_event) and a per-recipient state table (inbox_group).
--
-- The old table stores one row per event but the product renders one row per
-- issue, so "how far have I read this issue" has nowhere to live: read is a
-- boolean on the newest row, archive rewrites the whole group, and each of the
-- three clients re-folds events into groups on its own. Splitting fact from
-- state gives that question exactly one home (inbox_group) and lets the server
-- return already-folded rows.
--
-- Nothing writes to these tables yet. This migration only creates them; the
-- delivery call sites keep writing `inbox_item` until the dual-write PR lands,
-- so applying and rolling this back is a no-op for running code.
--
-- Per the project migration rules these tables carry NO foreign keys or
-- cascades: relationships and dependent cleanup are resolved in application
-- code (issue/workspace/member teardown will sweep these rows explicitly).
-- The inline PRIMARY KEY / UNIQUE constraints stay because they back the
-- ON CONFLICT targets used by the append and upsert paths. The secondary
-- list index lives in migration 254, which cannot share a file with these
-- CREATE TABLEs because CREATE INDEX CONCURRENTLY rejects multi-command files.

-- Immutable fact. Business semantics are INSERT-only; rows are deleted only by
-- the lifecycle channels (source deleted, member removed, workspace deleted).
CREATE TABLE IF NOT EXISTS inbox_event (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    group_id        UUID NOT NULL,
    -- Denormalised so authorization checks and workspace teardown never join
    -- through inbox_group.
    workspace_id    UUID NOT NULL,
    -- Per-group monotonic sequence. This is the comparison domain for the read
    -- cursor, deliberately NOT a timestamp: same-millisecond events, backfilled
    -- rows and multi-node clock skew all break timestamp ordering, and a read
    -- cursor that mis-orders silently marks unread events as read. The value is
    -- allocated under the group row lock in the same transaction as the insert,
    -- so it has no gaps and no relationship to wall clock.
    event_seq       BIGINT NOT NULL,
    type            TEXT NOT NULL,
    actor_type      TEXT CHECK (actor_type IN ('member', 'agent', 'system')),
    actor_id        UUID,
    -- The jump target, promoted out of the old `details` JSON junk drawer into
    -- typed columns. The old shape had no contract, so when the newest row had
    -- no target the clients borrowed one from an older event — that is how a
    -- status-change row could open an unrelated comment. Columns plus the
    -- CHECKs below make borrowing unrepresentable rather than merely discouraged.
    target_kind     TEXT CHECK (target_kind IN ('comment', 'run', 'autopilot')),
    target_id       UUID,
    -- Self-contained render snapshot taken at delivery time. Rendering reads
    -- only this column, so the list path issues zero extra queries per row.
    payload         JSONB NOT NULL DEFAULT '{}'::jsonb,
    -- Lets an older client recognise a payload it was not built for and fall
    -- back, instead of mis-rendering fields it does not understand.
    payload_version SMALLINT NOT NULL DEFAULT 1,
    -- Stable idempotency key derived by the producer from the originating
    -- entity, never from the event id, so a retried delivery collides instead
    -- of creating a second event. Carries an explicit algorithm version prefix
    -- ("v1:") so changing the derivation later cannot silently break retry
    -- idempotency for keys already stored.
    --
    -- The UNIQUE is global rather than per-group on purpose. The key already
    -- contains workspace and recipient, so a global unique also catches the
    -- case where the grouping rule itself changes between the original
    -- delivery and the retry; a per-group unique would miss exactly that.
    -- Making it global is also why inbox_event does not duplicate
    -- recipient_id: the column would carry no query load and would only add a
    -- way for event.recipient_id and group.recipient_id to disagree.
    delivery_key    TEXT NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (group_id, event_seq),
    UNIQUE (delivery_key),

    -- Mirrors inboxv2.EventTypes in Go. A contract test walks the Go registry
    -- against a live database so the two definitions cannot drift apart.
    CONSTRAINT inbox_event_type_known CHECK (type IN (
        'issue_assigned', 'unassigned', 'assignee_changed',
        'status_changed', 'priority_changed',
        'start_date_changed', 'due_date_changed',
        'issue_subscribed',
        'new_comment', 'mentioned', 'reaction_added',
        'task_completed', 'task_failed',
        'agent_blocked', 'agent_completed',
        'autopilot_paused',
        'quick_create_done', 'quick_create_failed', 'quick_create_unconfirmed'
    )),

    -- A target is both columns or neither. A kind without an id is a dangling
    -- jump; an id without a kind cannot be resolved to a route.
    CONSTRAINT inbox_event_target_paired CHECK (
        (target_kind IS NULL AND target_id IS NULL)
        OR (target_kind IS NOT NULL AND target_id IS NOT NULL)
    ),

    -- Comment-bearing types must carry their comment: these are the types whose
    -- whole purpose is "open this specific comment", and they are the ones the
    -- old code borrowed targets for.
    --
    -- IS NOT DISTINCT FROM rather than `=`: a CHECK only rejects on FALSE, and
    -- `NULL = 'comment'` is NULL, so a plain equality would wave through the
    -- exact row this constraint exists to stop — a comment notification with no
    -- comment.
    CONSTRAINT inbox_event_comment_target CHECK (
        type NOT IN ('new_comment', 'mentioned', 'reaction_added')
        OR target_kind IS NOT DISTINCT FROM 'comment'
    ),

    -- Field and status changes must NOT carry a target. They are about the
    -- issue itself, so the correct behaviour is to open the source overview
    -- without scrolling or highlighting anything.
    CONSTRAINT inbox_event_field_change_no_target CHECK (
        type NOT IN (
            'issue_assigned', 'unassigned', 'assignee_changed',
            'status_changed', 'priority_changed',
            'start_date_changed', 'due_date_changed',
            'issue_subscribed'
        )
        OR target_kind IS NULL
    )
);

-- Per-recipient state. Exactly one row per (recipient, source); the only home
-- for anything that changes about a person's relationship to a source.
CREATE TABLE IF NOT EXISTS inbox_group (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id     UUID NOT NULL,
    -- A user id, matching the semantics of inbox_item.recipient_id. Only
    -- members get groups: agents receive work through the run queue, and the
    -- old table's agent rows were dead rows nothing ever read.
    recipient_id     UUID NOT NULL,
    -- The group's identity, generalised from "issue_id". The state machine
    -- below never inspects these two columns, which is what makes adding a
    -- source later a delivery-and-render change rather than a state change.
    source_kind      TEXT NOT NULL CHECK (source_kind IN ('issue', 'standalone')),
    -- NOT NULL for every kind, including standalone. A nullable column here
    -- would defeat the UNIQUE below, because Postgres treats NULLs as distinct
    -- and one person could accumulate several groups for the same source.
    -- Standalone notifications derive a deterministic id from their delivery
    -- key so a retry lands on the same group.
    source_id        UUID NOT NULL,

    -- Content version: advances only when a new event arrives.
    latest_seq       BIGINT NOT NULL DEFAULT 0,
    latest_event_id  UUID,
    -- Display only. Never used to decide read state — that is what the seq
    -- comparison is for.
    latest_event_at  TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- Read cursor. Unread is a comparison, not a stored flag, so a newly
    -- arrived event is unread without anything having to write "unread"
    -- anywhere, and an event that an older row already covered cannot come
    -- back as a ghost.
    read_through_seq BIGINT NOT NULL DEFAULT 0,
    -- Explicit user intent, which outranks every automatic rule. Durable
    -- precisely because the old implementation kept it in a React ref, so it
    -- died on refresh and the user's own action was silently undone.
    manual_unread    BOOLEAN NOT NULL DEFAULT false,
    -- State version: advances on EVERY state change (event arrival, read,
    -- unread, archive, unarchive, snooze). Separate from latest_seq because
    -- read/unread/archive do not change content, yet their patches still need
    -- an ordering, and because it is the only way to tell "a re-open that saw
    -- the manual unread" from "an automatic read issued before it" — both
    -- carry the same observed_seq.
    state_version    BIGINT NOT NULL DEFAULT 0,

    -- Timestamp rather than boolean: the archived view sorts by it, and NULL
    -- already means active.
    archived_at      TIMESTAMPTZ,
    -- Reserved for snooze. A timestamp expires on its own, so the query side
    -- needs no sweeper to bring a group back.
    snoozed_until    TIMESTAMPTZ,
    -- Sort key, separate from latest_event_at because a snooze expiring brings
    -- a group back with no event to sort by; without this the group would
    -- silently return below everything newer.
    surfaced_at      TIMESTAMPTZ NOT NULL DEFAULT now(),

    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (workspace_id, recipient_id, source_kind, source_id)
);
