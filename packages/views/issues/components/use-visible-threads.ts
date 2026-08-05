import { useEffect, useState } from "react";

// ---------------------------------------------------------------------------
// useVisibleThreadIds — "which comment threads are on screen right now"
// ---------------------------------------------------------------------------
//
// Shared by the two thread navigators so they can never disagree about where
// the reader is: the right-edge rail (ThreadMinimap) darkens the ticks it
// returns, and the header panel (ThreadNavPanel) marks the same threads as the
// current position and scrolls to them on open.
//
// Computed from DOM rects on scroll/resize instead of an IntersectionObserver
// because Virtuoso mounts/unmounts rows while scrolling — an observer would
// lose its targets. Unmounted rows are by definition outside the (overscanned)
// viewport, so "no element" correctly counts as not visible.

function sameIdSet(a: Set<string>, b: Set<string>): boolean {
  if (a.size !== b.size) return false;
  for (const v of a) if (!b.has(v)) return false;
  return true;
}

export function useVisibleThreadIds(
  threadIds: readonly string[],
  scrollContainerEl: HTMLElement | null,
  /** Skip the listeners entirely while the consumer is closed/hidden. */
  enabled = true,
): Set<string> {
  const [visibleIds, setVisibleIds] = useState<Set<string>>(() => new Set());

  useEffect(() => {
    const container = scrollContainerEl;
    if (!container || !enabled) return;

    let raf = 0;
    const compute = () => {
      raf = 0;
      const rect = container.getBoundingClientRect();
      const next = new Set<string>();
      for (const id of threadIds) {
        const el = document.getElementById(`comment-${id}`);
        if (!el) continue;
        const r = el.getBoundingClientRect();
        if (r.bottom > rect.top && r.top < rect.bottom) next.add(id);
      }
      setVisibleIds((prev) => (sameIdSet(prev, next) ? prev : next));
    };
    const schedule = () => {
      if (!raf) raf = requestAnimationFrame(compute);
    };

    compute();
    container.addEventListener("scroll", schedule, { passive: true });
    // Content height changes without scroll events: Virtuoso mounting rows
    // after first paint, streamed agent replies growing, window resizes.
    const ro = new ResizeObserver(schedule);
    ro.observe(container);
    if (container.firstElementChild) ro.observe(container.firstElementChild);
    return () => {
      container.removeEventListener("scroll", schedule);
      ro.disconnect();
      if (raf) cancelAnimationFrame(raf);
    };
  }, [threadIds, scrollContainerEl, enabled]);

  return visibleIds;
}
