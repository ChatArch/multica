"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { CheckCircle2, MessageSquare, MessagesSquare, Search } from "lucide-react";
import type { TimelineEntry } from "@multica/core/types";
import { useActorName } from "@multica/core/workspace/hooks";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@multica/ui/components/ui/popover";
import { Button } from "@multica/ui/components/ui/button";
import { cn } from "@multica/ui/lib/utils";
import { ActorAvatar } from "../../common/actor-avatar";
import { useT } from "../../i18n";
import { commentPreview } from "./thread-minimap";
import { useVisibleThreadIds } from "./use-visible-threads";

// ---------------------------------------------------------------------------
// ThreadNavPanel — header entry point for jumping between comment threads
// ---------------------------------------------------------------------------
//
// The right-edge rail (ThreadMinimap) is a good position indicator and a bad
// finder: a tick carries no text, so locating a specific thread costs one
// hover per candidate, and once threads outgrow the rail's height the ticks
// compress to their 5px floor and stop being countable at all. A list fixes
// the cost model — 8-10 titles readable in one glance instead of one at a
// time — and search/filter make 24 threads and 240 threads cost the same
// (MUL-5755).
//
// The two navigators are deliberately kept as one coordinate system rather
// than two competing lists: both derive "where am I" from the same
// `useVisibleThreadIds`, and hovering a row here lights that thread's tick on
// the rail.
//
// Open model — hover previews, press pins:
//
//   hover 200ms  -> open, unpinned. Pointer-out closes it. No focus is moved,
//                   so a half-typed comment never loses the caret.
//   press        -> pinned. Search takes focus, arrow keys drive the list, and
//                   only Escape / outside press / a second press closes it.
//   focus or scroll inside the panel -> pinned.
//
// Pure hover would have been the wrong trigger for a surface you must be able
// to scroll, type into, and arrow through: all three need the pointer free to
// leave the button. Pinning on *intent* (press, focus, scroll) rather than on
// mere pointer-entry keeps a pass-through glide from leaving the panel stuck
// open.

/** Below this the header button is noise — an issue with no discussion. */
const MIN_THREADS = 1;

/** Hover intent delay before the preview opens. */
const HOVER_OPEN_DELAY_MS = 200;
/** Grace period on leave — long enough to travel from the button into the panel. */
const HOVER_CLOSE_DELAY_MS = 200;

const DAY_MS = 24 * 60 * 60 * 1000;

export type ThreadNavFilter = "all" | "unresolved" | "resolved" | "mine";

export interface ThreadNavThread {
  /** Root comment id — also the `comment-${id}` DOM anchor of the rendered row. */
  id: string;
  /** The thread's root comment entry (preview text, author, timestamp). */
  entry: TimelineEntry;
  /** Derived by the caller with `deriveThreadResolution` — covers reply resolutions too. */
  resolved: boolean;
  /** Replies under the root, excluding the root itself. */
  replyCount: number;
  /** The current user authored, replied to, or was @mentioned in this thread. */
  involvesMe: boolean;
}

export type ThreadDayGroup = "today" | "yesterday" | "earlier";

/**
 * Bucket a thread by the calendar day of its root comment, in the reader's
 * local time zone. Only three buckets: past "yesterday" a per-day header
 * would produce more headers than rows on a long-running issue, which is
 * noise rather than structure — the timestamp on each row already carries
 * the exact date once the reader is in "earlier".
 */
export function threadDayGroup(createdAt: string, nowMs: number): ThreadDayGroup {
  const ts = Date.parse(createdAt);
  if (Number.isNaN(ts)) return "earlier";
  const startOfToday = new Date(nowMs).setHours(0, 0, 0, 0);
  if (ts >= startOfToday) return "today";
  if (ts >= startOfToday - DAY_MS) return "yesterday";
  return "earlier";
}

/** A thread plus everything the row and the filters need, computed once. */
interface PreparedThread {
  thread: ThreadNavThread;
  title: string;
  excerpt: string;
  authorName: string;
  group: ThreadDayGroup;
  /** Lowercased `title + excerpt + author`, the haystack the query runs against. */
  haystack: string;
}

export function matchesFilter(thread: ThreadNavThread, filter: ThreadNavFilter): boolean {
  switch (filter) {
    case "unresolved":
      return !thread.resolved;
    case "resolved":
      return thread.resolved;
    case "mine":
      return thread.involvesMe;
    case "all":
      return true;
    default:
      // Server-driven values never reach this union, but an exhaustive
      // default keeps a future filter from silently hiding every thread.
      return true;
  }
}

/**
 * Whether `content` @mentions `userId`. Mentions serialize as markdown links
 * to `mention://member/<uuid>`; the legacy `[@ id="..."]` shortcode form is
 * normalised into that shape by `preprocessMentionShortcodes` before storage,
 * so matching the link URL is sufficient.
 */
export function mentionsUser(content: string | undefined, userId: string): boolean {
  if (!content || !userId) return false;
  return content.includes(`mention://member/${userId}`);
}

function ThreadRow({
  prepared,
  isCurrent,
  isActive,
  onJump,
  onHover,
}: {
  prepared: PreparedThread;
  /** This thread is in the reader's viewport right now. */
  isCurrent: boolean;
  /** Keyboard cursor is on this row. */
  isActive: boolean;
  onJump: () => void;
  onHover: (threadId: string | null) => void;
}) {
  const { t } = useT("issues");
  const { thread, title, excerpt, authorName } = prepared;
  return (
    <button
      type="button"
      data-thread-id={thread.id}
      data-active={isActive || undefined}
      onClick={onJump}
      onPointerEnter={() => onHover(thread.id)}
      onPointerLeave={() => onHover(null)}
      className={cn(
        "relative flex w-full items-start gap-2 rounded-md px-2 py-1.5 text-left outline-none transition-colors",
        "hover:bg-surface-hover data-active:bg-surface-hover",
        // The current-position row must stay identifiable while hovered, so it
        // keeps a dimension hover does not touch: the brand rail on its left
        // edge (UI rule on active-vs-hover states).
        isCurrent && "bg-surface-selected hover:bg-surface-selected data-active:bg-surface-selected",
      )}
    >
      {isCurrent && (
        <span
          aria-hidden="true"
          className="absolute inset-y-1.5 left-0 w-0.5 rounded-full bg-brand"
        />
      )}
      <ActorAvatar
        actorType={thread.entry.actor_type}
        actorId={thread.entry.actor_id}
        size="sm"
        profileLink={false}
        className="mt-0.5 shrink-0"
      />
      <span className="flex min-w-0 flex-1 flex-col">
        <span className="flex items-baseline gap-1.5">
          <span
            className={cn(
              "min-w-0 flex-1 truncate text-body",
              thread.resolved ? "text-muted-foreground" : "font-medium text-foreground",
            )}
          >
            {title}
          </span>
          {isCurrent && (
            <span className="shrink-0 text-micro font-medium text-brand">
              {t(($) => $.detail.thread_nav.current)}
            </span>
          )}
          <span className="shrink-0 text-micro tabular-nums text-faint-foreground">
            {prepared.thread.entry.created_at ? formatClock(prepared.thread.entry.created_at) : ""}
          </span>
        </span>
        <span className="mt-0.5 flex min-w-0 items-center gap-1.5 text-caption text-muted-foreground">
          <span className="shrink-0">{authorName}</span>
          {thread.replyCount > 0 && (
            <span className="flex shrink-0 items-center gap-0.5 tabular-nums text-faint-foreground">
              <MessageSquare className="h-3 w-3" />
              {thread.replyCount}
            </span>
          )}
          {thread.resolved && (
            <span className="flex shrink-0 items-center gap-0.5 text-success">
              <CheckCircle2 className="h-3 w-3" />
              {t(($) => $.comment.resolve.thread_resolved_badge)}
            </span>
          )}
          {excerpt && (
            <span className="min-w-0 truncate text-faint-foreground">{excerpt}</span>
          )}
        </span>
      </span>
    </button>
  );
}

/**
 * Wall-clock for today/yesterday rows, calendar date beyond that — the group
 * header already says which day, so repeating it per row would be redundant
 * where it is known and missing where it is not.
 */
function formatClock(createdAt: string): string {
  const ts = Date.parse(createdAt);
  if (Number.isNaN(ts)) return "";
  const d = new Date(ts);
  const withinTwoDays = Date.now() - ts < 2 * DAY_MS;
  return withinTwoDays
    ? d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" })
    : d.toLocaleDateString(undefined, { month: "short", day: "numeric" });
}

interface ThreadNavPanelProps {
  threads: ThreadNavThread[];
  /** The issue detail scroll container; null until its callback ref populates. */
  scrollContainerEl: HTMLElement | null;
  onJump: (threadId: string) => void;
  /** Lights the matching tick on the right-edge rail. */
  onHoverThread: (threadId: string | null) => void;
  /** Controlled open state, so the global shortcut can pin the panel open. */
  open: boolean;
  onOpenChange: (open: boolean, pinned: boolean) => void;
  /** True when `open` was reached by a press / shortcut rather than by hover. */
  pinned: boolean;
}

export function ThreadNavPanel({
  threads,
  scrollContainerEl,
  onJump,
  onHoverThread,
  open,
  onOpenChange,
  pinned,
}: ThreadNavPanelProps) {
  const { t } = useT("issues");
  const { getActorName } = useActorName();

  const [query, setQuery] = useState("");
  const [filter, setFilter] = useState<ThreadNavFilter>("all");
  const [activeIndex, setActiveIndex] = useState(0);
  const searchRef = useRef<HTMLInputElement | null>(null);
  const listRef = useRef<HTMLDivElement | null>(null);

  const threadIds = useMemo(() => threads.map((th) => th.id), [threads]);
  // Only track the viewport while the panel is showing — the rail already
  // runs this pass continuously for its own ticks.
  const visibleIds = useVisibleThreadIds(threadIds, scrollContainerEl, open);

  // Previews are cached by content so an unrelated timeline update (a reaction,
  // a reply in another thread) doesn't re-flatten every root comment.
  const previewCacheRef = useRef(
    new Map<string, { content: string | undefined; preview: { title: string; body: string } }>(),
  );
  const prepared = useMemo<PreparedThread[]>(() => {
    const nowMs = Date.now();
    const nextCache = new Map<
      string,
      { content: string | undefined; preview: { title: string; body: string } }
    >();
    const rows = threads.map((thread) => {
      const cached = previewCacheRef.current.get(thread.id);
      const preview =
        cached && cached.content === thread.entry.content
          ? cached.preview
          : commentPreview(thread.entry.content ?? "");
      nextCache.set(thread.id, { content: thread.entry.content, preview });
      const authorName = getActorName(thread.entry.actor_type, thread.entry.actor_id);
      const title = preview.title || authorName;
      return {
        thread,
        title,
        excerpt: preview.body,
        authorName,
        group: threadDayGroup(thread.entry.created_at, nowMs),
        haystack: `${title}\n${preview.body}\n${authorName}`.toLowerCase(),
      };
    });
    previewCacheRef.current = nextCache;
    return rows;
  }, [threads, getActorName]);

  const rows = useMemo(() => {
    const needle = query.trim().toLowerCase();
    return prepared.filter(
      (row) =>
        matchesFilter(row.thread, filter) &&
        (needle === "" || row.haystack.includes(needle)),
    );
  }, [prepared, filter, query]);

  const counts = useMemo(
    () => ({
      all: threads.length,
      unresolved: threads.filter((th) => !th.resolved).length,
      resolved: threads.filter((th) => th.resolved).length,
      mine: threads.filter((th) => th.involvesMe).length,
    }),
    [threads],
  );

  // Opening lands the cursor on where the reader already is, so ↵ without any
  // other input is a no-op jump rather than a surprise trip to the top.
  const currentIndex = rows.findIndex((row) => visibleIds.has(row.thread.id));
  const openRef = useRef(open);
  useEffect(() => {
    if (open && !openRef.current) {
      setQuery("");
      setFilter("all");
      setActiveIndex(currentIndex >= 0 ? currentIndex : 0);
    }
    openRef.current = open;
  }, [open, currentIndex]);

  // Filtering shrinks the list under the cursor; clamp instead of leaving it
  // pointing past the end, where ↵ would do nothing.
  useEffect(() => {
    setActiveIndex((prev) => (prev >= rows.length ? Math.max(0, rows.length - 1) : prev));
  }, [rows.length]);

  // Keep the keyboard cursor in view — it can be driven far past the visible
  // slice by held arrow keys.
  useEffect(() => {
    if (!open) return;
    const row = rows[activeIndex];
    if (!row) return;
    listRef.current
      ?.querySelector<HTMLElement>(`[data-thread-id="${CSS.escape(row.thread.id)}"]`)
      ?.scrollIntoView({ block: "nearest" });
  }, [activeIndex, open, rows]);

  const pin = useCallback(() => onOpenChange(true, true), [onOpenChange]);

  // Focus follows `pinned`, not the press handler, so the global shortcut —
  // which sets the pinned state directly — lands the caret in search too.
  // Deferred a frame because Base UI runs its own focus pass on open and would
  // otherwise take the caret back.
  useEffect(() => {
    if (!open || !pinned) return;
    const raf = requestAnimationFrame(() => searchRef.current?.focus());
    return () => cancelAnimationFrame(raf);
  }, [open, pinned]);

  const close = useCallback(() => {
    onHoverThread(null);
    onOpenChange(false, false);
  }, [onHoverThread, onOpenChange]);

  const jump = useCallback(
    (threadId: string) => {
      onHoverThread(null);
      onJump(threadId);
      onOpenChange(false, false);
    },
    [onHoverThread, onJump, onOpenChange],
  );

  const handleOpenChange = useCallback(
    (next: boolean, details: { reason?: string }) => {
      const reason = details?.reason;
      if (next) {
        if (reason === "trigger-press") pin();
        else onOpenChange(true, false);
        return;
      }
      // Pressing the trigger of a hover-preview means "keep this, I want to
      // work in it" — not "close". Only a press on an already-pinned panel
      // closes it.
      if (reason === "trigger-press" && !pinned) {
        pin();
        return;
      }
      // A pinned panel outlives the pointer: the user has to be able to reach
      // the scrollbar and the rows without the panel evaporating behind them.
      if (pinned && reason === "trigger-hover") return;
      close();
    },
    [close, pin, pinned, onOpenChange],
  );

  const handleKeyDown = useCallback(
    (e: React.KeyboardEvent) => {
      if (e.key === "ArrowDown" || e.key === "ArrowUp") {
        e.preventDefault();
        if (rows.length === 0) return;
        const step = e.key === "ArrowDown" ? 1 : -1;
        setActiveIndex((prev) => (prev + step + rows.length) % rows.length);
        return;
      }
      if (e.key === "Enter") {
        const row = rows[activeIndex];
        if (!row) return;
        e.preventDefault();
        jump(row.thread.id);
      }
      // Escape is Base UI's job — it already routes through onOpenChange and
      // restores focus to the trigger.
    },
    [activeIndex, jump, rows],
  );

  if (threads.length < MIN_THREADS) return null;

  const filterPills: { id: ThreadNavFilter; label: string; count: number }[] = [
    { id: "all", label: t(($) => $.detail.thread_nav.filter_all), count: counts.all },
    { id: "unresolved", label: t(($) => $.detail.thread_nav.filter_unresolved), count: counts.unresolved },
    { id: "resolved", label: t(($) => $.detail.thread_nav.filter_resolved), count: counts.resolved },
    { id: "mine", label: t(($) => $.detail.thread_nav.filter_mine), count: counts.mine },
  ];

  return (
    <Popover open={open} onOpenChange={handleOpenChange}>
      {/* Base UI puts the hover config on the Trigger, not the Root (a popover
          may have several triggers). The trigger stays a real button so press
          and keyboard still work for touch and a11y. */}
      <PopoverTrigger
        openOnHover
        delay={HOVER_OPEN_DELAY_MS}
        closeDelay={HOVER_CLOSE_DELAY_MS}
        render={
          <Button
            variant={open ? "secondary" : "ghost"}
            size="sm"
            aria-label={t(($) => $.detail.thread_nav.button_label, { count: threads.length })}
            className={cn("h-7 gap-1.5 px-2", !open && "text-muted-foreground")}
          />
        }
      >
        {/* Explicit `size-4` so the icon matches the neighbouring header
            action buttons instead of the `sm` button's smaller default. */}
        <MessagesSquare className="size-4" />
        <span className="text-caption font-medium tabular-nums">{threads.length}</span>
      </PopoverTrigger>
      <PopoverContent
        align="end"
        // Step inside the right-edge rail's 32px strip so the two navigators
        // stay readable together: the rail keeps showing absolute position
        // while the panel does the finding. On the alignment axis a positive
        // offset moves the popup toward the edge opposite the aligned one, so
        // with `align="end"` this shifts left, away from the rail.
        alignOffset={24}
        // Hover previews must never steal the caret from a half-written
        // comment; `pin()` focuses the search field explicitly instead.
        initialFocus={false}
        onKeyDown={handleKeyDown}
        // Any deliberate interaction inside the panel upgrades a preview to
        // pinned. Pointer-entry alone deliberately does not: a glide across
        // the panel on the way somewhere else should not leave it stuck open.
        onFocusCapture={pinned ? undefined : pin}
        className="flex w-[380px] flex-col gap-0 p-0"
      >
        <div className="flex h-9 shrink-0 items-center gap-2 border-b border-border px-2.5">
          <Search className="h-3.5 w-3.5 shrink-0 text-faint-foreground" />
          <input
            ref={searchRef}
            value={query}
            onChange={(e) => {
              setQuery(e.target.value);
              setActiveIndex(0);
            }}
            placeholder={t(($) => $.detail.thread_nav.search_placeholder)}
            aria-label={t(($) => $.detail.thread_nav.search_placeholder)}
            className="min-w-0 flex-1 bg-transparent text-body text-foreground outline-none placeholder:text-faint-foreground"
          />
          {query.trim() !== "" && (
            <span className="shrink-0 text-caption tabular-nums text-faint-foreground">
              {t(($) => $.detail.thread_nav.match_count, { count: rows.length })}
            </span>
          )}
        </div>

        <div className="flex shrink-0 items-center gap-1 border-b border-border px-2 py-1.5">
          {filterPills.map((pill) => (
            <button
              key={pill.id}
              type="button"
              onClick={() => {
                setFilter(pill.id);
                setActiveIndex(0);
              }}
              data-active={filter === pill.id || undefined}
              className={cn(
                "flex h-6 items-center gap-1 rounded-full px-2.5 text-caption text-muted-foreground transition-colors",
                "hover:bg-surface-hover",
                "data-active:bg-surface-selected data-active:font-medium data-active:text-foreground data-active:hover:bg-surface-selected",
              )}
            >
              {pill.label}
              <span className="tabular-nums text-faint-foreground">{pill.count}</span>
            </button>
          ))}
        </div>

        <div
          ref={listRef}
          // Scrolling is an intent signal: the reader is working the list, so
          // the panel should survive the pointer leaving afterwards.
          onScroll={pinned ? undefined : pin}
          className="max-h-[26rem] min-h-0 flex-1 overflow-y-auto p-1"
        >
          {rows.length === 0 ? (
            <p className="px-2 py-6 text-center text-caption text-muted-foreground">
              {t(($) => $.detail.thread_nav.empty)}
            </p>
          ) : (
            rows.map((row, i) => {
              const prev = rows[i - 1];
              const showHeader = !prev || prev.group !== row.group;
              return (
                <div key={row.thread.id}>
                  {showHeader && (
                    <p className="px-2 pb-0.5 pt-2 text-micro font-medium text-faint-foreground">
                      {t(($) => $.detail.thread_nav.group[row.group])}
                    </p>
                  )}
                  <ThreadRow
                    prepared={row}
                    isCurrent={visibleIds.has(row.thread.id)}
                    isActive={i === activeIndex}
                    onJump={() => jump(row.thread.id)}
                    onHover={onHoverThread}
                  />
                </div>
              );
            })
          )}
        </div>

        <div className="flex h-7 shrink-0 items-center gap-2.5 border-t border-border px-2.5 text-micro text-faint-foreground">
          <span>{t(($) => $.detail.thread_nav.hint_select)}</span>
          <span>{t(($) => $.detail.thread_nav.hint_jump)}</span>
          <span>{t(($) => $.detail.thread_nav.hint_close)}</span>
        </div>
      </PopoverContent>
    </Popover>
  );
}
