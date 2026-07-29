"use client";

import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  Archive,
  ArchiveRestore,
  Info,
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
import type { QuickAction, QuickActionAssigneeType } from "@multica/core/types";
import {
  QUICK_ACTION_VARIABLES,
  findUnknownQuickActionVariables,
  promptUsesQuickActionInput,
} from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { Badge } from "@multica/ui/components/ui/badge";
import { Input } from "@multica/ui/components/ui/input";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { Label as FieldLabel } from "@multica/ui/components/ui/label";
import { Switch } from "@multica/ui/components/ui/switch";
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
  // Server-driven enum: every unknown value must land on a generic fallback
  // rather than rendering nothing (API compatibility rule).
  switch (action.visibility) {
    case "workspace":
      return (
        <Badge variant="secondary" className="gap-1 font-normal">
          {t(($) => $.quick_actions.visibility_workspace)}
        </Badge>
      );
    case "restricted":
      return (
        <Badge variant="outline" className="gap-1 font-normal">
          {t(($) => $.quick_actions.visibility_restricted)}
        </Badge>
      );
    case "owner_only":
      return (
        <Badge variant="outline" className="gap-1 border-amber-500/40 font-normal text-amber-600 dark:text-amber-400">
          <TriangleAlert className="size-3" />
          {t(($) => $.quick_actions.visibility_owner_only)}
        </Badge>
      );
    case "unavailable":
      return (
        <Badge variant="outline" className="gap-1 border-destructive/40 font-normal text-destructive">
          <TriangleAlert className="size-3" />
          {t(($) => $.quick_actions.visibility_unavailable)}
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

interface FormState {
  name: string;
  description: string;
  assigneeType: QuickActionAssigneeType;
  assigneeId: string;
  prompt: string;
  inputEnabled: boolean;
  inputLabel: string;
  inputPlaceholder: string;
  inputRequired: boolean;
}

const EMPTY_FORM: FormState = {
  name: "",
  description: "",
  assigneeType: "agent",
  assigneeId: "",
  prompt: "",
  inputEnabled: false,
  inputLabel: "",
  inputPlaceholder: "",
  inputRequired: false,
};

function toFormState(action: QuickAction): FormState {
  return {
    name: action.name,
    description: action.description,
    assigneeType: action.assignee_type === "squad" ? "squad" : "agent",
    assigneeId: action.assignee_id,
    prompt: action.prompt,
    inputEnabled: action.input_enabled,
    inputLabel: action.input_label,
    inputPlaceholder: action.input_placeholder,
    inputRequired: action.input_required,
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
                        {action.target_name || t(($) => $.quick_actions.target_hidden)}
                      </span>
                      <span aria-hidden>·</span>
                      <span className={cn("shrink-0", unused && "text-amber-600 dark:text-amber-400")}>
                        {action.use_count === 0
                          ? t(($) => $.quick_actions.never_used)
                          : t(($) => $.quick_actions.used_count, { count: action.use_count })}
                      </span>
                      {action.input_enabled ? (
                        <>
                          <span aria-hidden>·</span>
                          <span className="shrink-0">{t(($) => $.quick_actions.has_input)}</span>
                        </>
                      ) : null}
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
              input_enabled: form.inputEnabled,
              input_label: form.inputLabel,
              input_placeholder: form.inputPlaceholder,
              input_required: form.inputRequired,
            });
          } else {
            await createMutation.mutateAsync({
              name: form.name,
              description: form.description,
              assignee_type: form.assigneeType,
              assignee_id: form.assigneeId,
              prompt: form.prompt,
              input_enabled: form.inputEnabled,
              input_label: form.inputLabel,
              input_placeholder: form.inputPlaceholder,
              input_required: form.inputRequired,
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

  const unknownVars = useMemo(() => findUnknownQuickActionVariables(form.prompt), [form.prompt]);
  const usesInput = useMemo(() => promptUsesQuickActionInput(form.prompt), [form.prompt]);

  // Mirror the server's two-way {{input}} contract inline so the author sees
  // the problem while typing instead of on submit. The server stays the
  // authority; this is an affordance.
  const inputMismatch =
    (form.inputEnabled && !usesInput) || (!form.inputEnabled && usesInput);

  const canSave =
    form.name.trim().length > 0 &&
    form.prompt.trim().length > 0 &&
    form.assigneeId.length > 0 &&
    unknownVars.length === 0 &&
    !inputMismatch;

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

  const insertVariable = (variable: string) => {
    setForm((f) => ({ ...f, prompt: `${f.prompt}{{${variable}}}` }));
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

          <div className="space-y-1.5">
            <FieldLabel>{t(($) => $.quick_actions.field_target)}</FieldLabel>
            <AgentPicker
              assignee={form.assigneeId ? { type: form.assigneeType, id: form.assigneeId } : null}
              onChange={(next) =>
                setForm((f) => ({ ...f, assigneeType: next.type, assigneeId: next.id }))
              }
            />
            {/* The private-agent consequence, stated where it is caused. */}
            {action?.visibility === "owner_only" ? (
              <p className="flex items-start gap-1.5 rounded-md bg-amber-500/10 px-2.5 py-2 text-xs text-amber-700 dark:text-amber-400">
                <TriangleAlert className="mt-0.5 size-3.5 shrink-0" />
                <span>{t(($) => $.quick_actions.private_target_warning)}</span>
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
            <div className="flex flex-wrap items-center gap-1">
              <span className="inline-flex items-center gap-1 text-xs text-muted-foreground">
                <Info className="size-3" />
                {t(($) => $.quick_actions.variables_hint)}
              </span>
              {QUICK_ACTION_VARIABLES.filter((v) => v !== "input").map((variable) => (
                <button
                  key={variable}
                  type="button"
                  className="rounded border border-surface-border px-1.5 py-0.5 font-mono text-[11px] text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
                  onClick={() => insertVariable(variable)}
                >
                  {`{{${variable}}}`}
                </button>
              ))}
            </div>
            {unknownVars.length > 0 ? (
              <p className="text-xs text-destructive">
                {t(($) => $.quick_actions.unknown_variables, { vars: unknownVars.join(", ") })}
              </p>
            ) : null}
          </div>

          <div className="space-y-3 rounded-lg border border-surface-border p-3">
            <div className="flex items-center justify-between gap-4">
              <div className="min-w-0">
                <FieldLabel htmlFor="qa-input-enabled">
                  {t(($) => $.quick_actions.field_input_enabled)}
                </FieldLabel>
                <p className="mt-0.5 text-xs text-muted-foreground">
                  {t(($) => $.quick_actions.field_input_hint)}
                </p>
              </div>
              <Switch
                id="qa-input-enabled"
                checked={form.inputEnabled}
                onCheckedChange={(checked) =>
                  setForm((f) => ({
                    ...f,
                    inputEnabled: checked,
                    // Adding the token for the author is the difference between
                    // "toggle works" and "toggle produces an invalid pair they
                    // must debug". Removing it on disable is the same courtesy.
                    prompt: checked
                      ? promptUsesQuickActionInput(f.prompt)
                        ? f.prompt
                        : `${f.prompt}${f.prompt.endsWith("\n") || f.prompt === "" ? "" : "\n"}{{input}}`
                      : f.prompt.replace(/\{\{\s*input\s*\}\}/g, "").trimEnd(),
                    inputRequired: checked ? f.inputRequired : false,
                  }))
                }
              />
            </div>

            {form.inputEnabled ? (
              <div className="space-y-3 border-t border-surface-border pt-3">
                <div className="space-y-1.5">
                  <FieldLabel htmlFor="qa-input-label">
                    {t(($) => $.quick_actions.field_input_label)}
                  </FieldLabel>
                  <Input
                    id="qa-input-label"
                    value={form.inputLabel}
                    maxLength={60}
                    placeholder={t(($) => $.quick_actions.field_input_label_placeholder)}
                    onChange={(e) => setForm((f) => ({ ...f, inputLabel: e.target.value }))}
                  />
                </div>
                <div className="space-y-1.5">
                  <FieldLabel htmlFor="qa-input-placeholder">
                    {t(($) => $.quick_actions.field_input_placeholder)}
                  </FieldLabel>
                  <Input
                    id="qa-input-placeholder"
                    value={form.inputPlaceholder}
                    maxLength={60}
                    placeholder={t(($) => $.quick_actions.field_input_placeholder_placeholder)}
                    onChange={(e) => setForm((f) => ({ ...f, inputPlaceholder: e.target.value }))}
                  />
                </div>
                <div className="flex items-center justify-between gap-4">
                  <FieldLabel htmlFor="qa-input-required">
                    {t(($) => $.quick_actions.field_input_required)}
                  </FieldLabel>
                  <Switch
                    id="qa-input-required"
                    checked={form.inputRequired}
                    onCheckedChange={(checked) => setForm((f) => ({ ...f, inputRequired: checked }))}
                  />
                </div>
              </div>
            ) : null}

            {inputMismatch ? (
              <p className="text-xs text-destructive">
                {form.inputEnabled
                  ? t(($) => $.quick_actions.input_missing_token)
                  : t(($) => $.quick_actions.input_orphan_token)}
              </p>
            ) : null}
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
