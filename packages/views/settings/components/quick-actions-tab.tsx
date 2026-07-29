"use client";

import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  Archive,
  ArchiveRestore,
  Globe,
  Info,
  Lock,
  Pencil,
  Plus,
  Trash2,
  TriangleAlert,
  Zap,
} from "lucide-react";
import { toast } from "sonner";
import { useWorkspaceId } from "@multica/core/hooks";
import {
  quickActionListOptions,
  useCreateQuickAction,
  useDeleteQuickAction,
  useUpdateQuickAction,
} from "@multica/core/quick-actions";
import type {
  QuickAction,
  QuickActionAssigneeType,
  QuickActionVisibility,
} from "@multica/core/types";
import { findQuickActionTemplateToken } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { Badge } from "@multica/ui/components/ui/badge";
import { Input } from "@multica/ui/components/ui/input";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { Label as FieldLabel } from "@multica/ui/components/ui/label";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@multica/ui/components/ui/alert-dialog";
import { Tooltip, TooltipContent, TooltipTrigger } from "@multica/ui/components/ui/tooltip";
import { cn } from "@multica/ui/lib/utils";
import { AgentPicker } from "../../autopilots/components/pickers/agent-picker";
import { useT } from "../../i18n";
import { SettingsTab, SettingsSection } from "./settings-layout";

// Quick Actions catalog (MUL-5465).
//
// Ordering is by use_count DESC, not created_at: the list's job is to answer
// "what does this workspace actually use", and recency of creation says
// nothing about that. Rows that have never been used sort last, which is also
// where the "unused" cleanup signal belongs.
//
// The visibility badge is the load-bearing UI here. Binding a workspace action
// to a private agent silently makes it single-user; showing that consequence
// at bind time is the entire fix for "you publish something nobody else can
// see and never find out" (decision D6).

const UNUSED_DAYS_THRESHOLD = 90;

function daysSince(iso: string | null): number | null {
  if (!iso) return null;
  const then = new Date(iso).getTime();
  if (Number.isNaN(then)) return null;
  return Math.floor((Date.now() - then) / 86_400_000);
}

type QuickActionsT = ReturnType<typeof useT<"settings">>["t"];

function VisibilityBadge({ action, t }: { action: QuickAction; t: QuickActionsT }) {
  // Server-driven enum: an unknown value must land on a generic fallback
  // rather than rendering nothing (API compatibility rule).
  switch (action.visibility) {
    case "public":
      return (
        <Badge variant="secondary" className="gap-1 font-normal">
          <Globe className="size-3" />
          {t(($) => $.quick_actions.visibility_public)}
        </Badge>
      );
    case "private":
      return (
        <Badge variant="outline" className="gap-1 font-normal">
          <Lock className="size-3" />
          {t(($) => $.quick_actions.visibility_private)}
        </Badge>
      );
    default:
      return (
        <Badge variant="outline" className="font-normal">
          {action.visibility}
        </Badge>
      );
  }
}

/**
 * The bound target plus its CURRENT reachability, as plain metadata.
 *
 * This replaces a derived "broken" flag: a `public` action whose agent has
 * since gone private simply reads wrong here — public badge, "private" target
 * — without a bespoke error state to maintain. The cost is that nobody is
 * actively notified; that trade was made deliberately (drift is rare and the
 * failure is loud at click time, not silent).
 */
function TargetLine({ action, t }: { action: QuickAction; t: QuickActionsT }) {
  if (action.target_missing === true) {
    return (
      <span className="inline-flex items-center gap-1 text-destructive">
        <TriangleAlert className="size-3" />
        {t(($) => $.quick_actions.target_missing)}
      </span>
    );
  }
  const mismatched = action.visibility === "public" && action.target_public !== true;
  return (
    <span className={cn("inline-flex items-center gap-1", mismatched && "text-amber-600 dark:text-amber-400")}>
      {mismatched ? <TriangleAlert className="size-3" /> : null}
      {action.target_name}
      {action.target_public !== true ? ` · ${t(($) => $.quick_actions.target_private)}` : ""}
    </span>
  );
}

interface FormState {
  name: string;
  description: string;
  assigneeType: QuickActionAssigneeType;
  assigneeId: string;
  prompt: string;
  visibility: QuickActionVisibility;
}

const EMPTY_FORM: FormState = {
  name: "",
  description: "",
  assigneeType: "agent",
  assigneeId: "",
  prompt: "",
  visibility: "public",
};

function toFormState(action: QuickAction): FormState {
  return {
    name: action.name,
    description: action.description,
    assigneeType: action.assignee_type === "squad" ? "squad" : "agent",
    assigneeId: action.assignee_id,
    prompt: action.prompt,
    visibility: action.visibility === "private" ? "private" : "public",
  };
}

export function QuickActionsTab() {
  const { t } = useT("settings");
  const wsId = useWorkspaceId();
  const { data: actions = [], isLoading } = useQuery(quickActionListOptions(wsId, true));

  const [editing, setEditing] = useState<QuickAction | null>(null);
  const [creating, setCreating] = useState(false);
  const [deleteTarget, setDeleteTarget] = useState<QuickAction | null>(null);

  const createMutation = useCreateQuickAction();
  const updateMutation = useUpdateQuickAction();
  const deleteMutation = useDeleteQuickAction();

  // Usage-first ordering; never-used rows fall to the bottom where the
  // "unused" signal is actionable.
  const sorted = useMemo(
    () =>
      [...actions].sort((a, b) => {
        if (a.status !== b.status) return a.status === "active" ? -1 : 1;
        if (b.use_count !== a.use_count) return b.use_count - a.use_count;
        return a.name.localeCompare(b.name);
      }),
    [actions],
  );

  const handleArchiveToggle = async (action: QuickAction) => {
    const next = action.status === "active" ? "archived" : "active";
    try {
      await updateMutation.mutateAsync({ id: action.id, status: next });
    } catch (error) {
      toast.error(error instanceof Error ? error.message : String(error));
    }
  };

  const handleDelete = async () => {
    if (!deleteTarget) return;
    try {
      await deleteMutation.mutateAsync(deleteTarget.id);
      setDeleteTarget(null);
    } catch (error) {
      toast.error(error instanceof Error ? error.message : String(error));
    }
  };

  return (
    <SettingsTab
      title={t(($) => $.quick_actions.title)}
      description={t(($) => $.quick_actions.description)}
    >
      <SettingsSection
        action={
          <Button size="sm" onClick={() => setCreating(true)}>
            <Plus className="size-4" />
            {t(($) => $.quick_actions.add)}
          </Button>
        }
      >
        {isLoading ? (
          <div className="space-y-2">
            {[0, 1, 2].map((i) => (
              <div key={i} className="h-14 animate-pulse rounded-md bg-muted/50" />
            ))}
          </div>
        ) : sorted.length === 0 ? (
          <div className="rounded-lg border border-dashed border-surface-border px-6 py-10 text-center">
            <Zap className="mx-auto size-5 text-muted-foreground" />
            <p className="mt-2 text-sm font-medium">{t(($) => $.quick_actions.empty_title)}</p>
            <p className="mt-1 text-xs text-muted-foreground">
              {t(($) => $.quick_actions.empty_hint)}
            </p>
          </div>
        ) : (
          <div className="divide-y divide-surface-border rounded-lg border border-surface-border">
            {sorted.map((action) => {
              const idleDays = daysSince(action.last_used_at);
              const unused = action.use_count === 0 || (idleDays !== null && idleDays >= UNUSED_DAYS_THRESHOLD);
              return (
                <div
                  key={action.id}
                  className={cn(
                    "flex items-center gap-3 px-3 py-2.5",
                    action.status !== "active" && "opacity-60",
                  )}
                >
                  <div className="min-w-0 flex-1">
                    <div className="flex min-w-0 items-center gap-2">
                      <span className="truncate text-sm font-medium">{action.name}</span>
                      <VisibilityBadge action={action} t={t} />
                      {action.status !== "active" ? (
                        <Badge variant="outline" className="font-normal">
                          {t(($) => $.quick_actions.archived)}
                        </Badge>
                      ) : null}
                    </div>
                    <div className="mt-0.5 flex min-w-0 items-center gap-2 text-xs text-muted-foreground">
                      <span className="truncate">
                        <TargetLine action={action} t={t} />
                      </span>
                      <span aria-hidden>·</span>
                      <span className={cn("shrink-0", unused && "text-amber-600 dark:text-amber-400")}>
                        {action.use_count === 0
                          ? t(($) => $.quick_actions.never_used)
                          : t(($) => $.quick_actions.used_count, { count: action.use_count })}
                      </span>
                    </div>
                  </div>
                  <div className="flex shrink-0 items-center gap-1">
                    <Tooltip>
                      <TooltipTrigger
                        render={
                          <Button variant="ghost" size="icon" onClick={() => setEditing(action)}>
                            <Pencil className="size-4" />
                          </Button>
                        }
                      />
                      <TooltipContent>{t(($) => $.quick_actions.edit)}</TooltipContent>
                    </Tooltip>
                    <Tooltip>
                      <TooltipTrigger
                        render={
                          <Button variant="ghost" size="icon" onClick={() => handleArchiveToggle(action)}>
                            {action.status === "active" ? (
                              <Archive className="size-4" />
                            ) : (
                              <ArchiveRestore className="size-4" />
                            )}
                          </Button>
                        }
                      />
                      <TooltipContent>
                        {action.status === "active"
                          ? t(($) => $.quick_actions.archive)
                          : t(($) => $.quick_actions.unarchive)}
                      </TooltipContent>
                    </Tooltip>
                    <Tooltip>
                      <TooltipTrigger
                        render={
                          <Button variant="ghost" size="icon" onClick={() => setDeleteTarget(action)}>
                            <Trash2 className="size-4" />
                          </Button>
                        }
                      />
                      <TooltipContent>{t(($) => $.quick_actions.delete)}</TooltipContent>
                    </Tooltip>
                  </div>
                </div>
              );
            })}
          </div>
        )}
      </SettingsSection>

      <QuickActionDialog
        open={creating || editing !== null}
        action={editing}
        onOpenChange={(open) => {
          if (!open) {
            setCreating(false);
            setEditing(null);
          }
        }}
        onSubmit={async (form) => {
          if (editing) {
            await updateMutation.mutateAsync({
              id: editing.id,
              name: form.name,
              description: form.description,
              assignee_type: form.assigneeType,
              assignee_id: form.assigneeId,
              prompt: form.prompt,
              visibility: form.visibility,
            });
          } else {
            await createMutation.mutateAsync({
              name: form.name,
              description: form.description,
              assignee_type: form.assigneeType,
              assignee_id: form.assigneeId,
              prompt: form.prompt,
              visibility: form.visibility,
            });
          }
        }}
      />

      <AlertDialog open={deleteTarget !== null} onOpenChange={(open) => !open && setDeleteTarget(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t(($) => $.quick_actions.delete_title)}</AlertDialogTitle>
            <AlertDialogDescription>
              {t(($) => $.quick_actions.delete_description)}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t(($) => $.quick_actions.cancel)}</AlertDialogCancel>
            <AlertDialogAction onClick={handleDelete}>
              {t(($) => $.quick_actions.delete)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </SettingsTab>
  );
}

function QuickActionDialog({
  open,
  action,
  onOpenChange,
  onSubmit,
}: {
  open: boolean;
  action: QuickAction | null;
  onOpenChange: (open: boolean) => void;
  onSubmit: (form: FormState) => Promise<void>;
}) {
  const { t } = useT("settings");
  const [form, setForm] = useState<FormState>(EMPTY_FORM);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    if (!open) return;
    setForm(action ? toFormState(action) : EMPTY_FORM);
  }, [open, action]);

  // Mirror the server's rejection inline so the author sees it while typing
  // instead of on submit. The server stays the authority; this is an
  // affordance.
  const templateToken = useMemo(() => findQuickActionTemplateToken(form.prompt), [form.prompt]);

  const canSave =
    form.name.trim().length > 0 &&
    form.prompt.trim().length > 0 &&
    form.assigneeId.length > 0 &&
    templateToken === null;

  const handleSubmit = async () => {
    if (!canSave) return;
    setSaving(true);
    try {
      await onSubmit(form);
      onOpenChange(false);
    } catch (error) {
      toast.error(error instanceof Error ? error.message : String(error));
    } finally {
      setSaving(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-xl">
        <DialogHeader>
          <DialogTitle>
            {action ? t(($) => $.quick_actions.edit_title) : t(($) => $.quick_actions.create_title)}
          </DialogTitle>
          <DialogDescription>{t(($) => $.quick_actions.dialog_description)}</DialogDescription>
        </DialogHeader>

        <div className="space-y-4">
          <div className="space-y-1.5">
            <FieldLabel htmlFor="qa-name">{t(($) => $.quick_actions.field_name)}</FieldLabel>
            <Input
              id="qa-name"
              value={form.name}
              maxLength={32}
              placeholder={t(($) => $.quick_actions.field_name_placeholder)}
              onChange={(e) => setForm((f) => ({ ...f, name: e.target.value }))}
            />
          </div>

          {/* Visibility first: it is the one choice that constrains the rest
              (a public action may only bind a publicly-invocable target), so
              asking it up front avoids a rejected Save. */}
          <div className="space-y-1.5">
            <FieldLabel>{t(($) => $.quick_actions.field_visibility)}</FieldLabel>
            <div className="grid grid-cols-2 gap-2">
              {(["public", "private"] as const).map((option) => (
                <button
                  key={option}
                  type="button"
                  onClick={() => setForm((f) => ({ ...f, visibility: option }))}
                  className={cn(
                    "flex items-start gap-2 rounded-lg border px-3 py-2 text-left transition-colors",
                    form.visibility === option
                      ? "border-primary bg-accent/50"
                      : "border-surface-border hover:bg-accent/30",
                  )}
                >
                  {option === "public" ? (
                    <Globe className="mt-0.5 size-4 shrink-0 text-muted-foreground" />
                  ) : (
                    <Lock className="mt-0.5 size-4 shrink-0 text-muted-foreground" />
                  )}
                  <span className="min-w-0">
                    <span className="block text-sm font-medium">
                      {option === "public"
                        ? t(($) => $.quick_actions.visibility_public)
                        : t(($) => $.quick_actions.visibility_private)}
                    </span>
                    <span className="mt-0.5 block text-xs text-muted-foreground">
                      {option === "public"
                        ? t(($) => $.quick_actions.visibility_public_hint)
                        : t(($) => $.quick_actions.visibility_private_hint)}
                    </span>
                  </span>
                </button>
              ))}
            </div>
          </div>

          <div className="space-y-1.5">
            <FieldLabel>{t(($) => $.quick_actions.field_target)}</FieldLabel>
            <AgentPicker
              assignee={form.assigneeId ? { type: form.assigneeType, id: form.assigneeId } : null}
              onChange={(next) =>
                setForm((f) => ({ ...f, assigneeType: next.type, assigneeId: next.id }))
              }
            />
            {/* Public promises "everyone can run this", so the server refuses a
                target the team cannot invoke. Saying so here turns a 400 into
                an expectation set before the user hits Save. */}
            {form.visibility === "public" ? (
              <p className="text-xs text-muted-foreground">
                {t(($) => $.quick_actions.public_target_hint)}
              </p>
            ) : null}
          </div>

          <div className="space-y-1.5">
            <FieldLabel htmlFor="qa-prompt">{t(($) => $.quick_actions.field_prompt)}</FieldLabel>
            <Textarea
              id="qa-prompt"
              value={form.prompt}
              rows={5}
              placeholder={t(($) => $.quick_actions.field_prompt_placeholder)}
              onChange={(e) => setForm((f) => ({ ...f, prompt: e.target.value }))}
            />
            {templateToken !== null ? (
              <p className="text-xs text-destructive">
                {t(($) => $.quick_actions.template_not_supported, { token: templateToken })}
              </p>
            ) : (
              <p className="inline-flex items-center gap-1 text-xs text-muted-foreground">
                <Info className="size-3" />
                {t(($) => $.quick_actions.prompt_hint)}
              </p>
            )}
          </div>
        </div>

        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            {t(($) => $.quick_actions.cancel)}
          </Button>
          <Button onClick={handleSubmit} disabled={!canSave || saving}>
            {t(($) => $.quick_actions.save)}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
