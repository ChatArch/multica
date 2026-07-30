CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS uq_inbox_subtree_settled_active
    ON inbox_item (recipient_type, recipient_id, issue_id)
    WHERE type = 'subtree_settled' AND archived = false;
