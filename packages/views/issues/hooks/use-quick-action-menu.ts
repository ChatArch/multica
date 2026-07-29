"use client";

import { useCallback, useMemo, useRef } from "react";
import { useQuery } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import { useCurrentWorkspace } from "@multica/core/paths";
import { runnableQuickActionsOptions } from "@multica/core/quick-actions";

/**
 * Supplies the comment composer's `/` menu with this workspace's quick actions
 * (MUL-5465).
 *
 * Picking one INSERTS the rendered body rather than running it — the composer
 * path exists precisely for the "this time is different" case, so the user
 * gets to edit and then send with the normal shortcut. Running without review
 * is what the sidebar button is for.
 *
 * The list is the runnable projection, so the menu can never offer an action
 * whose target the user is not allowed to invoke.
 *
 * Reads workspace identity through `useCurrentWorkspace` (nullable) rather
 * than `useWorkspaceId` (throws): the comment composer also mounts outside a
 * workspace route, and an enhancement like this must degrade to "no quick
 * actions in the menu", never take the composer down with it.
 */
export function useQuickActionMenu(issueId: string) {
  const workspace = useCurrentWorkspace();
  const wsId = workspace?.id ?? "";
  const { data } = useQuery({
    ...runnableQuickActionsOptions(wsId),
    enabled: wsId !== "",
  });

  // The extension reads through a getter on every keystroke; a ref keeps that
  // read cheap and current without re-creating the editor.
  const actionsRef = useRef<{ id: string; name: string; description?: string }[]>([]);
  actionsRef.current = useMemo(
    () =>
      (data?.quick_actions ?? [])
        .filter((a) => a.status === "active")
        .map((a) => ({ id: a.id, name: a.name, description: a.description || undefined })),
    [data],
  );

  const getQuickActions = useCallback(() => actionsRef.current, []);
  const renderQuickAction = useCallback(
    (quickActionId: string) => api.renderQuickAction(issueId, quickActionId),
    [issueId],
  );

  return useMemo(
    () => ({ getQuickActions, renderQuickAction }),
    [getQuickActions, renderQuickAction],
  );
}
