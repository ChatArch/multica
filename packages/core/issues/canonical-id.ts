import { useEffect } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import type { Issue } from "../types";
import { issueDetailOptions, issueKeys } from "./queries";

const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/** True for a canonical issue UUID; false for an identifier like `MUL-123`. */
export function isIssueUuid(value: string): boolean {
  return UUID_RE.test(value);
}

export interface CanonicalIssueId {
  /**
   * The issue's UUID, or `null` while an identifier is still resolving or if
   * it resolved to nothing.
   */
  canonicalId: string | null;
  isResolving: boolean;
}

/**
 * Resolve a raw `/{ws}/issues/{segment}` route parameter — a UUID or a
 * human-readable identifier (`MUL-123`) — to the issue's UUID.
 *
 * Why every caller must key on the UUID rather than the raw segment: the
 * realtime updaters patch `issueKeys.detail(wsId, issue.id)` with the UUID
 * carried by the websocket payload (`ws-updaters.ts`). A view keyed on the
 * identifier would sit on a cache entry no realtime event can ever reach, so
 * the detail page would silently stop updating. The identifier therefore stays
 * a presentation concern: it lives in the URL, never in a cache key.
 *
 * Resolution reuses `GET /api/issues/{id}`, which accepts either form, so it
 * is the same request the detail view would have made anyway.
 */
export function useCanonicalIssueId(wsId: string, routeId: string): CanonicalIssueId {
  const qc = useQueryClient();
  const isUuid = isIssueUuid(routeId);
  const enabled = !isUuid && !!wsId && !!routeId;

  const { data, isPending } = useQuery({
    ...issueDetailOptions(wsId, routeId),
    enabled,
  });

  useEffect(() => {
    if (isUuid || !data) return;
    // Hand the resolved row to the UUID-keyed entry the detail view reads, so
    // opening an identifier URL costs one request instead of two. Never
    // clobber an existing entry: it may already carry realtime patches this
    // identifier-keyed response predates.
    qc.setQueryData<Issue>(issueKeys.detail(wsId, data.id), (old) => old ?? data);
  }, [qc, wsId, data, isUuid]);

  return {
    canonicalId: isUuid ? routeId : (data?.id ?? null),
    isResolving: enabled && isPending,
  };
}
