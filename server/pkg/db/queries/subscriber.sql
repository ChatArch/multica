-- name: AddIssueSubscriber :execrows
-- Auto-subscribe path (creator / assignee / commenter / mentioned / autopilot /
-- delegated).
--
-- Two behaviors are load-bearing here:
--
--  1. A tombstoned row is NEVER resurrected. The WHERE on the DO UPDATE fails
--     for an opted-out row, which degrades to DO NOTHING — so unsubscribe still
--     sticks on a tree an agent keeps adding to (MUL-5483).
--
--  2. An ACTIVE 'delegated' row is upgraded when the user becomes directly
--     involved (assigned / mentioned / commented). Without this the reason
--     stays 'delegated' forever and someone who is now a real participant keeps
--     getting the reduced delivery tier.
--
-- Returns rows affected so the caller only broadcasts subscriber:added when an
-- active subscription actually changed. Publishing unconditionally made the
-- frontend insert a subscriber the DB had refused to write.
INSERT INTO issue_subscriber (issue_id, user_type, user_id, reason)
VALUES ($1, $2, $3, $4)
ON CONFLICT (issue_id, user_type, user_id) DO UPDATE
SET reason = EXCLUDED.reason
WHERE issue_subscriber.unsubscribed_at IS NULL
  AND issue_subscriber.reason = 'delegated'
  AND EXCLUDED.reason <> 'delegated';

-- name: SubscribeToIssueExplicitly :exec
-- Explicit user action (the Subscribe button). Unlike the rule-driven path this
-- CLEARS an existing opt-out tombstone and its scope: the user is overriding
-- their own earlier unsubscribe, which is the one thing that should bring them
-- back.
INSERT INTO issue_subscriber (issue_id, user_type, user_id, reason)
VALUES ($1, $2, $3, $4)
ON CONFLICT (issue_id, user_type, user_id)
DO UPDATE SET unsubscribed_at = NULL, opt_out_scope = NULL, reason = EXCLUDED.reason;

-- name: RemoveIssueSubscriber :exec
-- Leave THIS issue only. Tombstone rather than delete, so auto-subscribe rules
-- can tell "never subscribed" (no row) from "chose to leave" (row with
-- unsubscribed_at). scope='issue' keeps the opt-out from reaching descendants:
-- future children of this issue are still allowed to subscribe the user.
UPDATE issue_subscriber
SET unsubscribed_at = now(), opt_out_scope = 'issue'
WHERE issue_id = $1 AND user_type = $2 AND user_id = $3 AND unsubscribed_at IS NULL;

-- name: UnsubscribeFromIssueSubtree :many
-- Leave an issue AND every descendant in one action. An agent-built tree is
-- the unit a user actually wants to stop watching; leaving 30 sub-issues one
-- at a time is not a real escape hatch.
--
-- The root gets an UPSERTED tombstone rather than a conditional UPDATE, because
-- "I don't want this tree" has to persist even when the user holds no
-- subscription on the root itself — otherwise the opt-out records nothing and
-- the next child the agent files re-subscribes them. That root tombstone is
-- also what covers FUTURE descendants: HasAncestorOptOut walks up to it and
-- honors it because its scope is 'subtree'.
--
-- Returns every issue id it actually tombstoned so the caller can broadcast one
-- subscriber:removed per issue; publishing only the root left other open tabs
-- showing a stale subscription on the children.
WITH RECURSIVE subtree(node_id) AS (
    SELECT root.id FROM issue root WHERE root.id = $1
    UNION ALL
    SELECT i.id FROM issue i JOIN subtree s ON i.parent_issue_id = s.node_id
),
retire_descendants AS (
    UPDATE issue_subscriber sub
    SET unsubscribed_at = now(), opt_out_scope = 'subtree'
    WHERE sub.issue_id IN (SELECT node_id FROM subtree WHERE node_id <> $1)
      AND sub.user_type = $2 AND sub.user_id = $3
      AND sub.unsubscribed_at IS NULL
    RETURNING sub.issue_id
),
retire_root AS (
    INSERT INTO issue_subscriber (issue_id, user_type, user_id, reason, unsubscribed_at, opt_out_scope)
    VALUES ($1, $2, $3, 'manual', now(), 'subtree')
    ON CONFLICT (issue_id, user_type, user_id)
    DO UPDATE SET unsubscribed_at = now(), opt_out_scope = 'subtree'
    RETURNING issue_id
)
SELECT issue_id FROM retire_descendants
UNION ALL
SELECT issue_id FROM retire_root;

-- name: HasAncestorOptOut :one
-- True when the user has opted out in a way that should keep them off THIS
-- issue.
--
-- Two distinct cases, and the distinction is the whole point of opt_out_scope:
--
--   * a tombstone on this issue itself, at any scope — they left this issue;
--   * a 'subtree'-scoped tombstone on a STRICT ancestor — they left a tree this
--     issue belongs to, so a child created an hour later is still covered.
--
-- An 'issue'-scoped tombstone on an ancestor deliberately does NOT match: the
-- user declined that one issue, not everything the agent files beneath it.
WITH RECURSIVE ancestors(node_id, parent_id, depth) AS (
    SELECT root.id, root.parent_issue_id, 0 FROM issue root WHERE root.id = $1
    UNION ALL
    SELECT i.id, i.parent_issue_id, a.depth + 1 FROM issue i JOIN ancestors a ON i.id = a.parent_id
)
SELECT EXISTS(
    SELECT 1
    FROM issue_subscriber s
    JOIN ancestors a ON a.node_id = s.issue_id
    WHERE s.user_type = $2 AND s.user_id = $3
      AND s.unsubscribed_at IS NOT NULL
      AND (a.depth = 0 OR s.opt_out_scope = 'subtree')
) AS opted_out;

-- name: ListIssueSubscribers :many
SELECT * FROM issue_subscriber
WHERE issue_id = $1 AND unsubscribed_at IS NULL
ORDER BY created_at;

-- name: IsIssueSubscriber :one
SELECT EXISTS(
    SELECT 1 FROM issue_subscriber
    WHERE issue_id = $1 AND user_type = $2 AND user_id = $3
      AND unsubscribed_at IS NULL
) AS subscribed;

-- name: ListDelegatedMemberSubscribers :many
-- Members holding an ACTIVE delegated subscription on any of the given issues.
-- One batched read replaces the per-issue N+1 the subtree roll-up used to do
-- while collecting recipients. Ordered so a failure is reproducible.
SELECT DISTINCT user_id
FROM issue_subscriber
WHERE issue_id = ANY (sqlc.arg('issue_ids')::uuid[])
  AND user_type = 'member'
  AND reason = 'delegated'
  AND unsubscribed_at IS NULL
ORDER BY user_id;
