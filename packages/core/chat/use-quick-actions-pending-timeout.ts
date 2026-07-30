import { useEffect } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { chatKeys } from "./queries";
import type { ChatQuickActionsPendingState } from "../types/chat";

/**
 * How long a quick-actions pending marker may stay unresolved before we give up
 * and clear it. Normally a chat:quick_actions supplement resolves the marker
 * well within this window; the timeout only covers the paths where that event
 * never arrives (daemon died, or its supplement HTTP ultimately failed).
 */
export const QUICK_ACTIONS_PENDING_TIMEOUT_MS = 30_000;

/**
 * Clears a session's `quickActionsPending` marker from the query cache when no
 * chat:quick_actions supplement resolves it in time (MUL-5149). Unlike a
 * component-local timer, this drops the REAL cache state, so the pill spinner /
 * skeleton stops AND a later refresh — even after closing and reopening chat —
 * starts clean instead of re-reading a stuck marker.
 *
 * Mount once per surface that reads the marker (chat page / floating window).
 * The effect re-arms whenever the pending message changes, and the final clear
 * is message-scoped so it never wipes a newer turn's marker.
 */
export function useQuickActionsPendingTimeout(
  sessionId: string | null,
  pending: ChatQuickActionsPendingState | null,
): void {
  const qc = useQueryClient();
  const messageId = pending?.message_id ?? null;
  useEffect(() => {
    if (!sessionId || !messageId) return;
    const timer = setTimeout(() => {
      qc.setQueryData<ChatQuickActionsPendingState | null>(
        chatKeys.quickActionsPending(sessionId),
        (current) =>
          current && current.message_id === messageId ? null : current,
      );
    }, QUICK_ACTIONS_PENDING_TIMEOUT_MS);
    return () => clearTimeout(timer);
  }, [qc, sessionId, messageId]);
}
