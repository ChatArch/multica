"use client";

import { useRef } from "react";

/**
 * Absolute index Virtuoso assigns to the oldest message of a freshly opened
 * session. High enough that every later "load older" prepend can subtract from
 * it without ever reaching zero (react-virtuoso rejects a negative
 * `firstItemIndex`).
 */
export const CHAT_VIRTUOSO_INITIAL_FIRST_ITEM_INDEX = 1_000_000;

/**
 * Pins one loaded message to a fixed absolute index so the rest of the list can
 * be numbered relative to it across refetches.
 */
export interface ChatFirstItemIndexAnchor {
  sessionId: string;
  /** Message whose absolute index is held fixed. */
  anchorId: string;
  /** That message's absolute Virtuoso index. */
  anchorIndex: number;
  /** Last emitted value — the absolute index of `messages[0]`. */
  firstItemIndex: number;
}

/**
 * Compute the next `firstItemIndex` for a chat message list (MUL-5711).
 *
 * react-virtuoso reads this prop as "the absolute index of `data[0]`" and
 * diffs it between renders: a DECREASE means rows were prepended, an INCREASE
 * means rows were dropped off the head. Both branches shift its size/anchor
 * bookkeeping, so a value that moves for any other reason mis-attributes row
 * heights and jumps the scroll position.
 *
 * The previous derivation — `BASE - (messages in every page but the newest)` —
 * moved for another reason on every single turn. The newest page is a fixed
 * 50-message window: two new messages push two older ones out of it and into
 * the next page (TanStack re-derives each page cursor on refetch), so the
 * "older" count grew by 2 per turn and the index fell by 2 while nothing was
 * prepended. It could also jump by the whole BASE when the list mounted empty
 * (a pending task holds the list open) and the first messages arrived after.
 *
 * Anchoring fixes both: an already-loaded message keeps its absolute index for
 * the life of the session, so the value moves only when rows really do enter or
 * leave the head of the list.
 */
export function nextChatFirstItemIndex(
  anchor: ChatFirstItemIndexAnchor | null,
  sessionId: string | null,
  messages: readonly { id: string }[],
): { firstItemIndex: number; anchor: ChatFirstItemIndexAnchor | null } {
  if (!sessionId) {
    return { firstItemIndex: CHAT_VIRTUOSO_INITIAL_FIRST_ITEM_INDEX, anchor: null };
  }

  const current = anchor?.sessionId === sessionId ? anchor : null;

  const head = messages[0];
  if (!head) {
    // Empty list: hold the last emitted value. Re-basing to BASE here is what
    // made the index jump upward once the first messages landed.
    return {
      firstItemIndex: current?.firstItemIndex ?? CHAT_VIRTUOSO_INITIAL_FIRST_ITEM_INDEX,
      anchor: current,
    };
  }

  if (!current) {
    // First non-empty render for this session: the oldest loaded message
    // defines the origin.
    return {
      firstItemIndex: CHAT_VIRTUOSO_INITIAL_FIRST_ITEM_INDEX,
      anchor: {
        sessionId,
        anchorId: head.id,
        anchorIndex: CHAT_VIRTUOSO_INITIAL_FIRST_ITEM_INDEX,
        firstItemIndex: CHAT_VIRTUOSO_INITIAL_FIRST_ITEM_INDEX,
      },
    };
  }

  const position = messages.findIndex((m) => m.id === current.anchorId);
  if (position < 0) {
    // The anchor is gone (deleted turn, or it fell out of the loaded window).
    // Re-anchor on the current head WITHOUT moving the index: a spurious move
    // is exactly what this helper exists to prevent, and the alternative — a
    // guessed offset — would shift every row's recorded height.
    return {
      firstItemIndex: current.firstItemIndex,
      anchor: { ...current, anchorId: head.id, anchorIndex: current.firstItemIndex },
    };
  }

  // `position` rows now sit above the anchor, so data[0] is that many indexes
  // ahead of it. Appends leave this untouched; a real prepend lowers it by
  // exactly the number of rows prepended.
  const firstItemIndex = current.anchorIndex - position;
  return {
    firstItemIndex,
    anchor: current.firstItemIndex === firstItemIndex ? current : { ...current, firstItemIndex },
  };
}

/**
 * React binding for {@link nextChatFirstItemIndex}. Both chat surfaces (the tab
 * and the floating window) render the same Virtuoso list, so the numbering rule
 * lives here rather than being derived twice.
 *
 * The ref is written during render on purpose: the value has to be available to
 * the same render that consumes it, and the computation is idempotent — running
 * it twice with the same inputs (StrictMode) yields the same anchor and index.
 */
export function useChatFirstItemIndex(
  sessionId: string | null,
  messages: readonly { id: string }[],
): number {
  const anchorRef = useRef<ChatFirstItemIndexAnchor | null>(null);
  const { firstItemIndex, anchor } = nextChatFirstItemIndex(
    anchorRef.current,
    sessionId,
    messages,
  );
  anchorRef.current = anchor;
  return firstItemIndex;
}
