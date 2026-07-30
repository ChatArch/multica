"use client";

import { ActorAvatar } from "@multica/ui/components/common/actor-avatar";
import { Button } from "@multica/ui/components/ui/button";
import {
  HoverCard,
  HoverCardContent,
  HoverCardTrigger,
} from "@multica/ui/components/ui/hover-card";
import { useActorName } from "@multica/core/workspace/hooks";
import type { WorkingAgentSummary } from "@multica/core/types";
import { AgentAvatarStack } from "../../agents/components/agent-avatar-stack";
import { useT } from "../../i18n";

interface WorkspaceAgentWorkingChipProps {
  value: boolean;
  onToggle: () => void;
  /** Agents working inside the surface this header belongs to, already narrowed
   *  by its scope and every active filter. `undefined` = not resolved yet. */
  agents: readonly WorkingAgentSummary[] | undefined;
}

/**
 * Which colour tier the chip wears, and the only classes allowed alongside
 * it. Activity uses a tint, the active filter uses the filled brand tier,
 * and an idle workspace stays neutral.
 */
export function chipAppearance(
  value: boolean,
  hasAgents: boolean,
): { variant: "brand" | "brandSubtle" | "outline"; className: string } {
  const layout = "h-8 px-2 md:h-7 md:px-2.5";
  if (value) return { variant: "brand", className: layout };
  if (hasAgents) return { variant: "brandSubtle", className: layout };
  return { variant: "outline", className: `${layout} text-muted-foreground` };
}

/**
 * Hover body for every surface that reads a working-agents projection — the
 * surface filter chip here and the sub-issues header chip on issue detail.
 * Shared so a narrowed read and an unnarrowed one describe activity the same
 * way; the only difference between the two is the projection's scope.
 *
 * Identity comes from the workspace agent directory rather than the payload:
 * the surface projection is a facet of ids and counts, and resolving names
 * here keeps one definition of an agent's name/avatar across both callers.
 */
export function WorkingAgentsHoverContent({
  agents,
}: {
  agents: readonly WorkingAgentSummary[];
}) {
  const { t } = useT("issues");
  const { getActorName, getActorInitials, getActorAvatarUrl } = useActorName();

  if (agents.length === 0) {
    return (
      <p className="text-caption text-muted-foreground">
        {t(($) => $.agent_activity.empty_hover)}
      </p>
    );
  }

  return (
    <div className="flex flex-col gap-2">
      <div className="text-caption font-medium text-muted-foreground">
        {t(($) => $.agent_activity.hover_header, { count: agents.length })}
      </div>
      <div className="flex flex-col gap-1.5">
        {agents.map((agent) => (
          <div key={agent.id} className="flex items-center gap-2 text-caption">
            <ActorAvatar
              name={getActorName("agent", agent.id)}
              initials={getActorInitials("agent", agent.id)}
              avatarUrl={getActorAvatarUrl("agent", agent.id) ?? undefined}
              isAgent
              size="sm"
            />
            <span className="min-w-0 flex-1 truncate font-medium">
              {getActorName("agent", agent.id)}
            </span>
            <span className="shrink-0 tabular-nums text-muted-foreground">
              {t(($) => $.agent_activity.tasks_count, {
                count: agent.running_task_count,
              })}
            </span>
          </div>
        ))}
      </div>
    </div>
  );
}

/**
 * Agents-working filter chip for an issue surface header.
 *
 * The number IS the post-click row count's authority: it counts the agents
 * working on rows this surface's scope AND active filters would show, resolved
 * by the surface controller from the server-side `working_agents` facet — the
 * same compiled query the rows come from. Before MUL-5525 it ran its own
 * workspace-wide `/api/working-agents` read, so on a project page it could
 * advertise agents working nowhere near that project and open an empty list.
 *
 * `agents === undefined` means the projection has not resolved. The chip then
 * renders an explicit indeterminate label instead of a number, because "0" and
 * "not known yet" are different claims.
 *
 * Clicking only toggles view state; the controller turns the running-issue set
 * into the query's `working_issue_ids` filter.
 */
export function WorkspaceAgentWorkingChip({
  value,
  onToggle,
  agents,
}: WorkspaceAgentWorkingChipProps) {
  const { t } = useT("issues");
  const resolved = agents !== undefined;
  const agentIds = agents?.map((agent) => agent.id) ?? [];
  const agentCount = agentIds.length;
  const hasAgents = agentCount > 0;
  const label = resolved
    ? t(($) => $.agent_activity.chip_agents_working, { count: agentCount })
    : t(($) => $.agent_activity.chip_agents_working_unknown);
  const appearance = chipAppearance(value, hasAgents);

  const trigger = (
    <Button
      variant={appearance.variant}
      size="sm"
      className={appearance.className}
      onClick={onToggle}
      aria-pressed={value}
      aria-label={label}
    >
      {hasAgents && <AgentAvatarStack agentIds={agentIds} size="sm" max={3} />}
      <span className="tabular-nums md:hidden">
        {resolved ? agentCount : "—"}
      </span>
      <span className="hidden tabular-nums md:inline">{label}</span>
    </Button>
  );

  return (
    <HoverCard>
      <HoverCardTrigger render={trigger} />
      <HoverCardContent align="end" className="w-72">
        <WorkingAgentsHoverContent agents={agents ?? []} />
      </HoverCardContent>
    </HoverCard>
  );
}
