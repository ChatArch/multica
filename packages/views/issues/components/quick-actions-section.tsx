"use client";

import { useMemo, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { ChevronRight, CornerDownLeft, Loader2, Zap } from "lucide-react";
import { toast } from "sonner";
import { useCurrentWorkspace } from "@multica/core/paths";
import { runnableQuickActionsOptions, useRunQuickAction } from "@multica/core/quick-actions";
import type { Comment, CommentTriggerOutcome, QuickAction } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@multica/ui/components/ui/popover";
import { Tooltip, TooltipContent, TooltipTrigger } from "@multica/ui/components/ui/tooltip";
import { cn } from "@multica/ui/lib/utils";
import { ActorAvatar } from "../../common/actor-avatar";
import { useT } from "../../i18n";

// Quick Actions sidebar section (MUL-5465).
//
// Three interaction tiers, in ascending cost:
//   1. no runtime input  -> click runs it (one click)
//   2. runtime input     -> click opens ONE field, Enter runs it
//   3. any action, ⌥-click -> hand off to the comment composer to edit freely
//
// Tier 3 is the universal escape hatch. It costs almost nothing to build (the
// composer and its trigger preview already exist) and it removes the pressure
// to make every prompt cover its own 5% exceptions.
//
// The result toast is deliberately outcome-specific rather than a generic
// "done". A second click against an agent that already has a pending task on
// this issue does NOT start a second run — the DB permits one pending task per
// (issue, agent) and the comment is merged into the existing one. Reporting
// that as success would be a lie the user later has to debug.

interface QuickActionsSectionProps {
  issueId: string;
  /**
   * Hands the rendered prompt to the comment composer instead of running it.
   * Wired by the issue detail view; when absent, ⌥-click falls back to a
   * normal run so the modifier is never a dead end.
   */
  onEditInComposer?: (action: QuickAction) => void;
}

export function QuickActionsSection({ issueId, onEditInComposer }: QuickActionsSectionProps) {
  const { t } = useT("issues");
  // Nullable read, not useWorkspaceId(): this section is an enhancement on the
  // sidebar and must degrade to "not rendered" outside a workspace route
  // rather than throwing and taking the whole issue detail down with it.
  const wsId = useCurrentWorkspace()?.id ?? "";
  const [open, setOpen] = useState(true);
  const [showAll, setShowAll] = useState(false);

  const { data, isLoading } = useQuery({
    ...runnableQuickActionsOptions(wsId),
    enabled: wsId !== "",
  });
  const actions = useMemo(
    () => (data?.quick_actions ?? []).filter((a) => a.status === "active"),
    [data],
  );
  const sidebarLimit = data?.sidebar_limit ?? 5;

  // Nothing configured (or nothing this member may run) renders nothing at
  // all: an empty section header in the sidebar is pure noise.
  if (isLoading || actions.length === 0) return null;

  const visible = showAll ? actions : actions.slice(0, sidebarLimit);
  const hiddenCount = actions.length - visible.length;

  return (
    <div>
      <button
        type="button"
        className={cn(
          "mb-2 flex w-full items-center gap-1 rounded-md px-2 py-1 text-xs font-medium transition-colors hover:bg-accent/70",
          !open && "text-muted-foreground hover:text-foreground",
        )}
        onClick={() => setOpen(!open)}
      >
        {t(($) => $.detail.section_quick_actions)}
        <ChevronRight
          className={cn(
            "!size-3 shrink-0 stroke-[2.5] text-muted-foreground transition-transform",
            open && "rotate-90",
          )}
        />
      </button>
      {open ? (
        <div className="space-y-0.5 pl-2">
          {visible.map((action) => (
            <QuickActionRow
              key={action.id}
              action={action}
              issueId={issueId}
              onEditInComposer={onEditInComposer}
            />
          ))}
          {hiddenCount > 0 ? (
            <button
              type="button"
              className="w-full rounded-md px-2 py-1 text-left text-xs text-muted-foreground transition-colors hover:bg-accent/70 hover:text-foreground"
              onClick={() => setShowAll(true)}
            >
              {t(($) => $.detail.quick_actions_more, { count: hiddenCount })}
            </button>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}

/**
 * Translate the per-target outcome into an honest sentence.
 *
 * `coalesced` / `already_active` is the one that matters: the click was
 * accepted but no NEW run started. "Added to Lambda's queue" is the truth;
 * "Lambda started working" is not.
 */
function outcomeMessage(
  outcome: CommentTriggerOutcome | undefined,
  targetName: string,
  t: ReturnType<typeof useT<"issues">>["t"],
): { message: string; kind: "success" | "info" | "error" } {
  if (!outcome) {
    // No outcome at all means the comment saved but nothing was reported —
    // surface it as neutral rather than claiming a run.
    return { message: t(($) => $.detail.quick_action_posted), kind: "info" };
  }
  switch (outcome.status) {
    case "queued":
      return { message: t(($) => $.detail.quick_action_queued, { name: targetName }), kind: "success" };
    case "coalesced":
      return { message: t(($) => $.detail.quick_action_coalesced, { name: targetName }), kind: "info" };
    case "deferred":
      return { message: t(($) => $.detail.quick_action_deferred, { name: targetName }), kind: "info" };
    case "blocked":
      return { message: t(($) => $.detail.quick_action_blocked, { name: targetName }), kind: "error" };
    default:
      // Server-driven enum: an unknown status must not be silently rendered
      // as success.
      return { message: t(($) => $.detail.quick_action_posted), kind: "info" };
  }
}

function QuickActionRow({
  action,
  issueId,
  onEditInComposer,
}: {
  action: QuickAction;
  issueId: string;
  onEditInComposer?: (action: QuickAction) => void;
}) {
  const { t } = useT("issues");
  const [inputOpen, setInputOpen] = useState(false);
  const [inputValue, setInputValue] = useState("");
  const runMutation = useRunQuickAction(issueId);
  const running = runMutation.isPending;
  const rowRef = useRef<HTMLDivElement>(null);

  const targetName = action.target_name || t(($) => $.detail.quick_action_target_fallback);

  const handleRun = async (input?: string) => {
    try {
      const comment: Comment = await runMutation.mutateAsync({
        quickActionId: action.id,
        input,
      });
      const outcome = comment.trigger_outcomes?.[0];
      const { message, kind } = outcomeMessage(outcome, targetName, t);
      if (kind === "success") toast.success(message);
      else if (kind === "error") toast.error(message);
      else toast.info(message);
      setInputOpen(false);
      setInputValue("");
    } catch (error) {
      toast.error(error instanceof Error ? error.message : String(error));
    }
  };

  const handleClick = (e: React.MouseEvent) => {
    // ⌥-click = "let me edit this one" for ANY action, with or without a
    // configured input.
    if (e.altKey && onEditInComposer) {
      onEditInComposer(action);
      return;
    }
    if (action.input_enabled) {
      setInputOpen(true);
      return;
    }
    void handleRun();
  };

  const row = (
    <div ref={rowRef}>
      <button
        type="button"
        disabled={running}
        onClick={handleClick}
        className={cn(
          "group flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-sm transition-colors",
          "hover:bg-accent/70 disabled:cursor-default disabled:opacity-60",
        )}
      >
        {running ? (
          <Loader2 className="size-3.5 shrink-0 animate-spin text-muted-foreground" />
        ) : (
          <Zap className="size-3.5 shrink-0 text-muted-foreground" />
        )}
        <span className="min-w-0 flex-1 truncate">{action.name}</span>
        {action.target_name ? (
          <ActorAvatar
            actorType={action.assignee_type === "squad" ? "squad" : "agent"}
            actorId={action.assignee_id}
            size="xs"
            className="shrink-0"
            showStatusDot={action.assignee_type === "agent"}
          />
        ) : null}
      </button>
    </div>
  );

  const tooltipBody = (
    <div className="space-y-0.5">
      <div className="font-medium">{action.name}</div>
      {action.description ? <div>{action.description}</div> : null}
      <div className="text-muted-foreground">
        {t(($) => $.detail.quick_action_runs_as, { name: targetName })}
      </div>
      {onEditInComposer ? (
        <div className="text-muted-foreground">{t(($) => $.detail.quick_action_alt_click)}</div>
      ) : null}
    </div>
  );

  if (!action.input_enabled) {
    return (
      <Tooltip>
        <TooltipTrigger render={row} />
        <TooltipContent side="left">{tooltipBody}</TooltipContent>
      </Tooltip>
    );
  }

  return (
    <Popover open={inputOpen} onOpenChange={setInputOpen}>
      <PopoverTrigger render={row} />
      <PopoverContent align="start" side="left" className="w-72 p-3">
        <div className="space-y-2">
          <label htmlFor={`qa-input-${action.id}`} className="text-xs font-medium">
            {action.input_label || t(($) => $.detail.quick_action_input_fallback_label)}
          </label>
          <Input
            id={`qa-input-${action.id}`}
            autoFocus
            value={inputValue}
            placeholder={action.input_placeholder}
            onChange={(e) => setInputValue(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter" && !e.nativeEvent.isComposing) {
                e.preventDefault();
                // A required input blocks submit; an optional one runs blank.
                if (action.input_required && inputValue.trim() === "") return;
                void handleRun(inputValue);
              }
            }}
          />
          <div className="flex items-center justify-between">
            {/* The placeholder already sits in the field; repeating it here
                would say the same thing twice. This line carries the one thing
                the field cannot: whether leaving it blank is allowed. */}
            <span className="text-[11px] text-muted-foreground">
              {action.input_required
                ? t(($) => $.detail.quick_action_input_required_hint)
                : t(($) => $.detail.quick_action_input_optional_hint)}
            </span>
            <Button
              size="sm"
              disabled={running || (action.input_required && inputValue.trim() === "")}
              onClick={() => void handleRun(inputValue)}
            >
              <CornerDownLeft className="size-3" />
              {t(($) => $.detail.quick_action_run)}
            </Button>
          </div>
        </div>
      </PopoverContent>
    </Popover>
  );
}
