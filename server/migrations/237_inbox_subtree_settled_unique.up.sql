-- MUL-5483 review round 2, finding 3: the subtree roll-up had no durable
-- idempotency. notifyDelegatedSubtreeSettled reads "every sibling settled" and
-- then inserts, with nothing between the read and the write — so when the last
-- two siblings settle concurrently, both transactions can observe a closed
-- barrier and each insert its own roll-up.
--
-- An application-level check cannot close that window; only a uniqueness
-- constraint can. At most ONE un-archived roll-up per (recipient, parent issue):
-- the second concurrent insert loses the race and is swallowed by
-- ON CONFLICT DO NOTHING.
--
-- Scoped to archived = false on purpose. A tree that reopens (a child leaves a
-- settled status) archives its prior roll-up, which frees the slot so a genuine
-- re-settle notifies again instead of being silently deduped forever.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS uq_inbox_subtree_settled_active
    ON inbox_item (recipient_type, recipient_id, issue_id)
    WHERE type = 'subtree_settled' AND archived = false;
