-- Issue Quick Actions (MUL-5465).
--
-- A quick action is a workspace-level preset for "who to call and what to say"
-- on an existing issue. Clicking one renders `prompt` server-side and posts a
-- `quick_action` comment carrying the target's mention markup, which then runs
-- through the existing comment -> mention -> task trigger path. There is no
-- separate dispatch engine: permission (canInvokeAgent), attribution, squad
-- leader routing, the execution log, and pending-task coalescing are all
-- inherited from that path by construction.
--
-- Deliberately absent columns:
--   - `visibility`: derived, never stored. Who may see and run an action is a
--     function of the bound agent's permission_mode (and the squad leader's
--     for squad bindings), evaluated per request. Storing it would let the
--     two drift, and the permission model is the authority.
--   - cron / webhook_token / execution_mode: those belong to autopilot. A
--     quick action is human-triggered and always acts on an existing issue.
--
-- Actions archive (status='archived') instead of deleting so historical
-- `quick_action` comments stay resolvable to a name.
CREATE TABLE quick_action (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    -- Mirrors autopilot's polymorphic assignee: 'agent' -> agent(id),
    -- 'squad' -> squad(id), resolved to squad.leader_id at run time.
    assignee_type TEXT NOT NULL CHECK (assignee_type IN ('agent', 'squad')),
    assignee_id UUID NOT NULL,
    -- Flat {{...}} substitution only. Whitelist-validated at write time; no
    -- conditionals, loops, or filters, now or later (MUL-5465 D5) -- the agent
    -- already reads the whole issue, so natural language is the control flow.
    prompt TEXT NOT NULL,
    -- The single optional runtime input. When enabled the prompt MUST contain
    -- {{input}} and vice versa; the handler enforces both directions so an
    -- action can never silently drop what the user typed.
    input_enabled BOOLEAN NOT NULL DEFAULT false,
    input_label TEXT NOT NULL DEFAULT '',
    input_placeholder TEXT NOT NULL DEFAULT '',
    input_required BOOLEAN NOT NULL DEFAULT false,
    position FLOAT NOT NULL DEFAULT 0,
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'archived')),
    last_used_at TIMESTAMPTZ,
    use_count BIGINT NOT NULL DEFAULT 0,
    created_by_type TEXT NOT NULL CHECK (created_by_type IN ('member', 'agent')),
    created_by_id UUID NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Indexes live in follow-up single-statement migrations: CREATE INDEX
-- CONCURRENTLY cannot share a migration with other statements.
