-- MUL-5483 review round 3, finding 1. Uniqueness now includes the BARRIER the
-- roll-up describes, not just the parent, so each closed stage gets its own
-- notification while a concurrent double-close of the SAME barrier is still
-- deduped.
--
-- details->>'barrier_key' is 'all' for an unstaged sibling set (one implicit
-- barrier) and the stage number for a staged one. It is always written, so the
-- expression is never NULL — a NULL would not collide with itself and would
-- silently disable the guard.
--
-- Still scoped to archived = false: reopening a barrier archives that barrier's
-- roll-up, freeing the slot so a genuine re-settle notifies again.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS uq_inbox_subtree_settled_barrier
    ON inbox_item (recipient_type, recipient_id, issue_id, (details ->> 'barrier_key'))
    WHERE type = 'subtree_settled' AND archived = false;
