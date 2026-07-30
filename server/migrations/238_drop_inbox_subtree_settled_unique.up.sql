-- MUL-5483 review round 3, finding 1: 237's uniqueness key was per (recipient,
-- parent), which is coarser than the barrier it is supposed to dedupe. Once
-- stage 1's roll-up occupied the slot, stage 2 closing was swallowed by
-- ON CONFLICT DO NOTHING — and normal stage promotion (backlog -> todo) never
-- archives, so the slot was never freed. Two closed stages produced one
-- notification.
--
-- Replaced in 239 by a key that includes the barrier identity. Dropped in its
-- own file because DROP INDEX CONCURRENTLY cannot share a multi-command string.
DROP INDEX CONCURRENTLY IF EXISTS uq_inbox_subtree_settled_active;
