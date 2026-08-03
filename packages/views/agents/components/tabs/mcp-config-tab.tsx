"use client";

import { useEffect, useMemo, useState } from "react";
import {
  Loader2,
  Lock,
  Pencil,
  Plus,
  RefreshCw,
  Server,
  Trash2,
} from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import type { Agent, AgentRuntime } from "@multica/core/types";
import { ApiError } from "@multica/core/api";
import {
  runtimeCapabilitiesOptions,
  runtimeDisplayLabel,
} from "@multica/core/runtimes";
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
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import { toast } from "sonner";
import { useT } from "../../../i18n";
import {
  clearManagedMcpConfig,
  inheritsRuntimeMcp,
  listManagedMcpServers,
  removeManagedMcpServer,
  upsertManagedMcpServer,
  type ManagedMcpServer,
} from "./mcp-config-model";
import { McpServerDialog } from "./mcp-server-dialog";

export function McpConfigTab({
  agent,
  runtime,
  onSave,
  onDirtyChange,
}: {
  agent: Agent;
  runtime: AgentRuntime | null;
  onSave: (updates: { mcp_config: unknown | null }) => Promise<void>;
  onDirtyChange?: (dirty: boolean) => void;
}) {
  const { t } = useT("agents");
  const runtimeId =
    runtime?.runtime_mode === "local" && runtime.status === "online"
      ? runtime.id
      : null;
  const runtimeQuery = useQuery(runtimeCapabilitiesOptions(runtimeId));
  const redacted = agent.mcp_config_redacted === true;
  // A managed mcp_config is an authoritative allowlist on the daemon side, so
  // the runtime's own servers are only exposed when Multica manages nothing for
  // this agent or it explicitly opted back in (GitHub #6283). The copy below
  // has to say which of the two is in effect — an operator who believes the
  // wrong one mis-scopes the agent's tool access.
  const inheritsRuntime = useMemo(
    () =>
      inheritsRuntimeMcp(
        agent.mcp_config,
        agent.mcp_config_redacted,
        agent.runtime_config,
      ),
    [agent.mcp_config, agent.mcp_config_redacted, agent.runtime_config],
  );
  const managedServers = useMemo(
    () => listManagedMcpServers(agent.mcp_config),
    [agent.mcp_config],
  );
  const managedNames = useMemo(
    () => new Set(managedServers.map((server) => server.name)),
    [managedServers],
  );
  const [editorOpen, setEditorOpen] = useState(false);
  const [editingServer, setEditingServer] = useState<ManagedMcpServer | null>(
    null,
  );
  const [deletingServer, setDeletingServer] =
    useState<ManagedMcpServer | null>(null);
  const [deleting, setDeleting] = useState(false);
  const [clearOpen, setClearOpen] = useState(false);
  const [clearing, setClearing] = useState(false);

  // The strict semantics are enforced by the DAEMON, so a config saved against
  // an older daemon is not yet in effect: that daemon still merges the host's
  // MCP servers. Claiming "Not exposed" in that window would be the same false
  // security guarantee this issue is about, so only trust a daemon that
  // reports the capability (GitHub #6283).
  const runtimeAuthoritative = runtimeQuery.data?.authoritativeMcp === true;
  const daemonEnforcesManagedConfig = !inheritsRuntime && runtimeAuthoritative;
  const daemonNeedsUpgradeForMcp =
    !inheritsRuntime && runtimeQuery.data !== undefined && !runtimeAuthoritative;

  useEffect(() => onDirtyChange?.(false), [onDirtyChange]);

  const openAddDialog = () => {
    setEditingServer(null);
    setEditorOpen(true);
  };

  const openEditDialog = (server: ManagedMcpServer) => {
    setEditingServer(server);
    setEditorOpen(true);
  };

  const handleSaveServer = async (
    name: string,
    config: Record<string, unknown>,
  ) => {
    const next = upsertManagedMcpServer(
      agent.mcp_config,
      editingServer,
      name,
      config,
    );
    try {
      await onSave({ mcp_config: next });
      toast.success(
        editingServer
          ? t(($) => $.tab_body.mcp_config.updated_toast)
          : t(($) => $.tab_body.mcp_config.added_toast),
      );
    } catch (error) {
      toast.error(
        error instanceof Error && error.message
          ? error.message
          : t(($) => $.tab_body.mcp_config.save_failed_toast),
      );
      throw error;
    }
  };

  const handleDelete = async () => {
    if (!deletingServer) return;
    setDeleting(true);
    try {
      await onSave({
        mcp_config: removeManagedMcpServer(agent.mcp_config, deletingServer),
      });
      toast.success(t(($) => $.tab_body.mcp_config.deleted_toast));
      setDeletingServer(null);
    } catch (error) {
      toast.error(
        error instanceof Error && error.message
          ? error.message
          : t(($) => $.tab_body.mcp_config.delete_failed_toast),
      );
    } finally {
      setDeleting(false);
    }
  };

  const handleClearManaged = async () => {
    setClearing(true);
    try {
      await onSave({ mcp_config: clearManagedMcpConfig() });
      toast.success(t(($) => $.tab_body.mcp_config.cleared_toast));
      setClearOpen(false);
    } catch (error) {
      toast.error(
        error instanceof Error && error.message
          ? error.message
          : t(($) => $.tab_body.mcp_config.save_failed_toast),
      );
    } finally {
      setClearing(false);
    }
  };

  return (
    <div className="space-y-8">
      <p className="max-w-2xl break-words text-pretty text-body leading-6 text-muted-foreground">
        {t(($) => $.tab_body.mcp_config.intro)}
      </p>

      <section className="space-y-3">
        <div className="flex items-start justify-between gap-4">
          <div>
            <h3 className="text-body font-medium">
              {t(($) => $.tab_body.mcp_config.managed_title)}
            </h3>
            <p className="mt-1 max-w-2xl text-caption leading-5 text-muted-foreground">
              {inheritsRuntime
                ? t(($) => $.tab_body.mcp_config.managed_hint)
                : t(($) => $.tab_body.mcp_config.managed_hint_authoritative)}
            </p>
          </div>
          {!redacted && (
            <Button size="sm" variant="outline" onClick={openAddDialog}>
              <Plus aria-hidden="true" />
              {t(($) => $.tab_body.mcp_config.add_action)}
            </Button>
          )}
        </div>

        {/* The saved config is only a real boundary once the daemon enforces
            it. Warn instead of letting the operator assume it already does. */}
        {daemonNeedsUpgradeForMcp && (
          <McpNotice
            text={t(($) => $.tab_body.mcp_config.daemon_upgrade_required)}
          />
        )}

        {redacted ? (
          <div className="flex items-start gap-2 rounded-lg border px-4 py-3">
            <Lock
              className="mt-0.5 h-4 w-4 text-muted-foreground"
              aria-hidden="true"
            />
            <div>
              <p className="text-body font-medium">
                {t(($) => $.tab_body.mcp_config.redacted_title)}
              </p>
              <p className="mt-1 text-caption text-muted-foreground">
                {t(($) => $.tab_body.mcp_config.redacted_hint)}
              </p>
            </div>
          </div>
        ) : managedServers.length > 0 ? (
          <McpServerList
            servers={managedServers}
            disabledLabel={t(($) => $.tab_body.mcp_config.agent_disabled_badge)}
            onEdit={openEditDialog}
            onDelete={setDeletingServer}
            editLabel={t(($) => $.tab_body.mcp_config.edit_aria)}
            deleteLabel={t(($) => $.tab_body.mcp_config.delete_aria)}
          />
        ) : (
          <McpNotice
            text={
              inheritsRuntime
                ? t(($) => $.tab_body.mcp_config.managed_empty)
                : t(($) => $.tab_body.mcp_config.managed_empty_strict)
            }
          />
        )}

        {/* Restoring inheritance WIDENS the agent's tool surface, so it is its
            own deliberate action rather than a side effect of deleting the last
            server (GitHub #6283). Only offered when a managed config exists. */}
        {!redacted && !inheritsRuntime && (
          <Button
            size="sm"
            variant="ghost"
            onClick={() => setClearOpen(true)}
            disabled={clearing}
          >
            {t(($) => $.tab_body.mcp_config.clear_action)}
          </Button>
        )}
      </section>

      <section className="space-y-3">
        <div className="flex items-start justify-between gap-4">
          <div>
            <h3 className="text-body font-medium">
              {t(($) => $.tab_body.mcp_config.runtime_title)}
            </h3>
            <p className="mt-1 max-w-2xl text-caption leading-5 text-muted-foreground">
              {inheritsRuntime
                ? t(($) => $.tab_body.mcp_config.runtime_hint, {
                    runtime: runtime ? runtimeDisplayLabel(runtime) : "Runtime",
                  })
                : daemonNeedsUpgradeForMcp
                  ? t(($) => $.tab_body.mcp_config.runtime_hint_needs_upgrade, {
                      runtime: runtime
                        ? runtimeDisplayLabel(runtime)
                        : "Runtime",
                    })
                  : t(($) => $.tab_body.mcp_config.runtime_hint_excluded, {
                      runtime: runtime
                        ? runtimeDisplayLabel(runtime)
                        : "Runtime",
                    })}
            </p>
          </div>
          {runtimeId && (
            <Button
              variant="ghost"
              size="sm"
              onClick={() => runtimeQuery.refetch()}
              disabled={runtimeQuery.isFetching}
            >
              <RefreshCw
                className={
                  runtimeQuery.isFetching
                    ? "h-3.5 w-3.5 animate-spin motion-reduce:animate-none"
                    : "h-3.5 w-3.5"
                }
                aria-hidden="true"
              />
              {t(($) => $.tab_body.mcp_config.refresh_action)}
            </Button>
          )}
        </div>
        {!runtime ? (
          <McpNotice text={t(($) => $.tab_body.mcp_config.runtime_missing)} />
        ) : runtime.status !== "online" ? (
          <McpNotice text={t(($) => $.tab_body.mcp_config.runtime_offline)} />
        ) : runtimeQuery.isLoading ? (
          <McpNotice
            loading
            text={t(($) => $.tab_body.mcp_config.runtime_discovering)}
          />
        ) : runtimeQuery.isError ? (
          <McpNotice
            text={
              runtimeQuery.error instanceof ApiError &&
              runtimeQuery.error.status === 403
                ? t(($) => $.tab_body.mcp_config.runtime_forbidden)
                : t(($) => $.tab_body.mcp_config.runtime_failed)
            }
          />
        ) : runtimeQuery.data?.mcpSupported !== true ? (
          <McpNotice
            text={t(($) => $.tab_body.mcp_config.runtime_unsupported)}
          />
        ) : runtimeQuery.data.mcpServers.length === 0 ? (
          <McpNotice text={t(($) => $.tab_body.mcp_config.runtime_empty)} />
        ) : (
          <McpServerList
            servers={runtimeQuery.data.mcpServers.map((server) => ({
              name: server.name,
              transport: server.transport || "unknown",
              enabled: server.enabled,
              source: server.source,
              // Only claim exclusion once the daemon actually enforces it.
              // Against an older daemon these servers ARE still reachable, so
              // fall back to the name-collision badge and let the section hint
              // carry the "needs upgrade" warning.
              overridden: daemonEnforcesManagedConfig
                ? true
                : managedNames.has(server.name),
            }))}
            disabledLabel={t(($) => $.tab_body.mcp_config.runtime_disabled_badge)}
            overriddenLabel={
              daemonEnforcesManagedConfig
                ? t(($) => $.tab_body.mcp_config.runtime_excluded_badge)
                : t(($) => $.tab_body.mcp_config.runtime_overridden_badge)
            }
          />
        )}
      </section>

      {!redacted && (
        <McpServerDialog
          open={editorOpen}
          server={editingServer}
          existingNames={managedNames}
          onOpenChange={setEditorOpen}
          onSave={handleSaveServer}
        />
      )}

      <AlertDialog
        open={deletingServer !== null}
        onOpenChange={(open) => !open && !deleting && setDeletingServer(null)}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t(($) => $.tab_body.mcp_config.delete_dialog_title)}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t(($) => $.tab_body.mcp_config.delete_dialog_description, {
                name: deletingServer?.name ?? "",
              })}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={deleting}>
              {t(($) => $.tab_body.mcp_config.dialog_cancel)}
            </AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              onClick={handleDelete}
              disabled={deleting}
            >
              {deleting && (
                <Loader2
                  className="h-3.5 w-3.5 animate-spin motion-reduce:animate-none"
                  aria-hidden="true"
                />
              )}
              {t(($) => $.tab_body.mcp_config.delete_action)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <AlertDialog
        open={clearOpen}
        onOpenChange={(open) => !open && !clearing && setClearOpen(false)}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t(($) => $.tab_body.mcp_config.clear_dialog_title)}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t(($) => $.tab_body.mcp_config.clear_dialog_description, {
                runtime: runtime ? runtimeDisplayLabel(runtime) : "Runtime",
              })}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={clearing}>
              {t(($) => $.tab_body.mcp_config.dialog_cancel)}
            </AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              onClick={handleClearManaged}
              disabled={clearing}
            >
              {clearing && (
                <Loader2
                  className="h-3.5 w-3.5 animate-spin motion-reduce:animate-none"
                  aria-hidden="true"
                />
              )}
              {t(($) => $.tab_body.mcp_config.clear_confirm_action)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

type McpServerView = {
  name: string;
  transport: string;
  enabled: boolean;
  source?: string;
  overridden?: boolean;
};

function McpServerList({
  servers,
  disabledLabel,
  overriddenLabel,
  onEdit,
  onDelete,
  editLabel,
  deleteLabel,
}: {
  servers: McpServerView[];
  disabledLabel: string;
  overriddenLabel?: string;
  onEdit?: (server: ManagedMcpServer) => void;
  onDelete?: (server: ManagedMcpServer) => void;
  editLabel?: string;
  deleteLabel?: string;
}) {
  return (
    <ul className="divide-y rounded-lg border bg-surface-raised/40">
      {servers.map((server) => (
        <li key={server.name} className="flex items-center gap-3 p-3">
          <span className="flex size-9 shrink-0 items-center justify-center rounded-md bg-muted text-muted-foreground">
            <Server className="h-4 w-4" aria-hidden="true" />
          </span>
          <div className="min-w-0 flex-1">
            <p className="truncate text-body font-medium">{server.name}</p>
            <p className="text-caption text-muted-foreground">
              <span className="uppercase">{server.transport}</span>
              {server.source ? ` · ${server.source}` : null}
            </p>
          </div>
          {server.overridden && overriddenLabel ? (
            <Badge variant="outline">{overriddenLabel}</Badge>
          ) : !server.enabled ? (
            <Badge variant="outline">{disabledLabel}</Badge>
          ) : null}
          {onEdit && onDelete && (
            <div className="flex items-center gap-1">
              <Button
                type="button"
                variant="ghost"
                size="icon-sm"
                aria-label={`${editLabel} ${server.name}`}
                onClick={() => onEdit(server as ManagedMcpServer)}
              >
                <Pencil aria-hidden="true" />
              </Button>
              <Button
                type="button"
                variant="ghost"
                size="icon-sm"
                aria-label={`${deleteLabel} ${server.name}`}
                onClick={() => onDelete(server as ManagedMcpServer)}
              >
                <Trash2 aria-hidden="true" />
              </Button>
            </div>
          )}
        </li>
      ))}
    </ul>
  );
}

function McpNotice({ text, loading = false }: { text: string; loading?: boolean }) {
  return (
    <div className="flex items-center gap-2 rounded-lg border border-dashed px-4 py-6 text-caption text-muted-foreground">
      {loading ? (
        <Loader2
          className="h-4 w-4 animate-spin motion-reduce:animate-none"
          aria-hidden="true"
        />
      ) : (
        <Server className="h-4 w-4" aria-hidden="true" />
      )}
      {text}
    </div>
  );
}
