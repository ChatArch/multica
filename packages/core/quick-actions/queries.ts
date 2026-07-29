import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

/**
 * Quick action catalog queries (MUL-5465).
 *
 * Two distinct reads, deliberately cached under different keys:
 *   - `list`   — the settings catalog (everything, including archived)
 *   - `runnable` — the issue sidebar (only what THIS caller may invoke)
 *
 * They must not share a cache entry: the runnable projection is per-viewer, so
 * serving it to the settings page would hide rows an admin needs to audit, and
 * serving the full catalog to the sidebar would render dead buttons.
 */
export const quickActionKeys = {
  all: (wsId: string) => ["quick-actions", wsId] as const,
  list: (wsId: string, includeArchived = false) =>
    [...quickActionKeys.all(wsId), "list", includeArchived] as const,
  runnable: (wsId: string) => [...quickActionKeys.all(wsId), "runnable"] as const,
};

export function quickActionListOptions(wsId: string, includeArchived = false) {
  return queryOptions({
    queryKey: quickActionKeys.list(wsId, includeArchived),
    queryFn: () => api.listQuickActions({ includeArchived }),
    select: (data) => data.quick_actions,
  });
}

/**
 * The issue sidebar's list. `sidebar_limit` rides along so the "top N + More"
 * split is server-driven rather than hardcoded in three clients.
 */
export function runnableQuickActionsOptions(wsId: string) {
  return queryOptions({
    queryKey: quickActionKeys.runnable(wsId),
    queryFn: () => api.listQuickActions({ runnableOnly: true }),
  });
}
