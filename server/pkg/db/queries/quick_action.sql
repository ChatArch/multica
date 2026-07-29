-- name: ListQuickActions :many
-- The settings page passes include_archived=true; the issue sidebar does not.
-- Ordering matches the index (workspace_id, status, position); name is the
-- stable tiebreaker so equal positions never render in random order.
SELECT * FROM quick_action
WHERE workspace_id = sqlc.arg('workspace_id')::uuid
  AND (sqlc.arg('include_archived')::bool OR status = 'active')
ORDER BY position ASC, LOWER(name) ASC;

-- name: GetQuickAction :one
SELECT * FROM quick_action
WHERE id = $1 AND workspace_id = $2;

-- name: CountActiveQuickActions :one
SELECT COUNT(*) FROM quick_action
WHERE workspace_id = $1 AND status = 'active';

-- name: CreateQuickAction :one
-- New actions append to the end: position = max + 1.
INSERT INTO quick_action (
    workspace_id, name, description, assignee_type, assignee_id, prompt,
    input_enabled, input_label, input_placeholder, input_required,
    position, created_by_type, created_by_id
)
SELECT sqlc.arg('workspace_id')::uuid,
       sqlc.arg('name')::text,
       sqlc.arg('description')::text,
       sqlc.arg('assignee_type')::text,
       sqlc.arg('assignee_id')::uuid,
       sqlc.arg('prompt')::text,
       sqlc.arg('input_enabled')::bool,
       sqlc.arg('input_label')::text,
       sqlc.arg('input_placeholder')::text,
       sqlc.arg('input_required')::bool,
       COALESCE((SELECT MAX(position) FROM quick_action WHERE workspace_id = sqlc.arg('workspace_id')::uuid), 0) + 1,
       sqlc.arg('created_by_type')::text,
       sqlc.arg('created_by_id')::uuid
RETURNING *;

-- name: UpdateQuickAction :one
-- COALESCE-on-narg partial update: an omitted field keeps its stored value.
-- assignee_type and assignee_id move together (the handler requires both), so
-- a type swap can never land with a mismatched id.
UPDATE quick_action SET
    name = COALESCE(sqlc.narg('name'), name),
    description = COALESCE(sqlc.narg('description'), description),
    assignee_type = COALESCE(sqlc.narg('assignee_type'), assignee_type),
    assignee_id = COALESCE(sqlc.narg('assignee_id'), assignee_id),
    prompt = COALESCE(sqlc.narg('prompt'), prompt),
    input_enabled = COALESCE(sqlc.narg('input_enabled'), input_enabled),
    input_label = COALESCE(sqlc.narg('input_label'), input_label),
    input_placeholder = COALESCE(sqlc.narg('input_placeholder'), input_placeholder),
    input_required = COALESCE(sqlc.narg('input_required'), input_required),
    status = COALESCE(sqlc.narg('status'), status),
    position = COALESCE(sqlc.narg('position'), position),
    updated_at = now()
WHERE id = $1 AND workspace_id = $2
RETURNING *;

-- name: DeleteQuickAction :exec
-- workspace_id is a SQL-layer tenant guard, matching DeleteComment.
DELETE FROM quick_action WHERE id = $1 AND workspace_id = $2;

-- name: TouchQuickActionUsage :exec
-- Best-effort usage telemetry, written after a successful run. Deliberately
-- not part of the run transaction: a failed counter bump must never lose the
-- run the user asked for.
UPDATE quick_action
SET use_count = use_count + 1, last_used_at = now()
WHERE id = $1 AND workspace_id = $2;
