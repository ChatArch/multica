-- name: AddIssueSubscriber :exec
-- Auto-subscribe path (creator / assignee / commenter / mentioned / autopilot /
-- delegated). DO NOTHING is load-bearing: a row tombstoned by an explicit
-- unsubscribe must NOT be resurrected by a later rule pass, or unsubscribe
-- would not stick on an issue tree an agent keeps adding to (MUL-5483).
INSERT INTO issue_subscriber (issue_id, user_type, user_id, reason)
VALUES ($1, $2, $3, $4)
ON CONFLICT (issue_id, user_type, user_id) DO NOTHING;

-- name: SubscribeToIssueExplicitly :exec
-- Explicit user action (the Subscribe button). Unlike the rule-driven path this
-- CLEARS an existing opt-out tombstone: the user is overriding their own
-- earlier unsubscribe, which is the one thing that should bring them back.
INSERT INTO issue_subscriber (issue_id, user_type, user_id, reason)
VALUES ($1, $2, $3, $4)
ON CONFLICT (issue_id, user_type, user_id)
DO UPDATE SET unsubscribed_at = NULL, reason = EXCLUDED.reason;

-- name: RemoveIssueSubscriber :exec
-- Tombstone rather than delete, so auto-subscribe rules can tell "never
-- subscribed" (no row) from "chose to leave" (row with unsubscribed_at).
UPDATE issue_subscriber
SET unsubscribed_at = now()
WHERE issue_id = $1 AND user_type = $2 AND user_id = $3 AND unsubscribed_at IS NULL;

-- name: UnsubscribeFromIssueSubtree :exec
-- Leave an issue AND every descendant in one action. An agent-built tree is
-- the unit a user actually wants to stop watching; leaving 30 sub-issues one
-- at a time is not a real escape hatch.
--
-- The root gets an UPSERTED tombstone rather than a conditional UPDATE, because
-- "I don't want this tree" has to persist even when the user holds no
-- subscription on the root itself — otherwise the opt-out records nothing and
-- the next child the agent files re-subscribes them. That root tombstone is
-- also what covers FUTURE descendants: HasAncestorOptOut walks up to it.
-- Descendants only need existing rows retired, so they take a plain UPDATE.
--
-- The data-modifying CTE runs to completion even though the primary query does
-- not read it, and excludes the root so the two halves never touch the same row.
WITH RECURSIVE subtree(node_id) AS (
    SELECT root.id FROM issue root WHERE root.id = $1
    UNION ALL
    SELECT i.id FROM issue i JOIN subtree s ON i.parent_issue_id = s.node_id
),
retire_descendants AS (
    UPDATE issue_subscriber
    SET unsubscribed_at = now()
    WHERE issue_id IN (SELECT node_id FROM subtree WHERE node_id <> $1)
      AND user_type = $2 AND user_id = $3
      AND unsubscribed_at IS NULL
    RETURNING 1
)
INSERT INTO issue_subscriber (issue_id, user_type, user_id, reason, unsubscribed_at)
VALUES ($1, $2, $3, 'manual', now())
ON CONFLICT (issue_id, user_type, user_id)
DO UPDATE SET unsubscribed_at = now();

-- name: HasAncestorOptOut :one
-- True when the user has unsubscribed from this issue or any ancestor of it.
-- Walking UP (rather than tombstoning descendants that do not exist yet) is
-- what makes a subtree opt-out durable: a child created an hour later still
-- sees the opt-out its parent carries. Issue trees are shallow and this runs
-- once per issue creation, so the recursive walk is cheap.
WITH RECURSIVE ancestors(node_id, parent_id) AS (
    SELECT root.id, root.parent_issue_id FROM issue root WHERE root.id = $1
    UNION ALL
    SELECT i.id, i.parent_issue_id FROM issue i JOIN ancestors a ON i.id = a.parent_id
)
SELECT EXISTS(
    SELECT 1 FROM issue_subscriber s
    WHERE s.issue_id IN (SELECT node_id FROM ancestors)
      AND s.user_type = $2 AND s.user_id = $3
      AND s.unsubscribed_at IS NOT NULL
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
