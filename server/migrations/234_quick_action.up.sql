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
-- `visibility` is the AUTHOR'S INTENT, chosen at creation:
--   - 'public'  — meant for the team. The handler requires a target every
--                 workspace member can invoke, so a public action is runnable
--                 by everyone by construction.
--   - 'private' — meant for its creator only; any target is allowed.
--
-- It is intent, NOT an authorization decision: the run path always re-checks
-- canInvokeAgent, so a target whose permission_mode changes later can never
-- turn a stale row into an actual privilege. The worst case is a public action
-- whose agent went private, which then fails loudly at click time.
--
-- Deliberately absent columns:
--   - cron / webhook_token / execution_mode: those belong to autopilot. A
--     quick action is human-triggered and always acts on an existing issue.
--   - `position`: ordering is use_count DESC everywhere. A manual order plus a
--     usage order meant one list with two different sorts, which is its own
--     source of confusion.
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
    visibility TEXT NOT NULL DEFAULT 'public' CHECK (visibility IN ('private', 'public')),
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
