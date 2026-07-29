// ---------------------------------------------------------------------------
// Where a click lands: Navigation vs Reference
// ---------------------------------------------------------------------------
//
// Read this before changing the click behaviour of any surface that opens an
// entity. Every "should this open in a new tab?" question in the app is
// answered by one distinction — and answering it per component is exactly how
// the behaviour ended up spread across five layers that disagreed with each
// other, with the same issue opening differently depending on which view you
// clicked it from (MUL-5453).
//
// NAVIGATION — open in place: plain `<AppLink>` or `push()`.
//   The user is in a container whose purpose is to be consumed; they are
//   picking which thing to look at. The container has done its job the moment
//   they pick, so replacing it costs them nothing.
//   → List / Board / Table / Gantt / Swimlane rows, search results, Inbox,
//     and the header breadcrumb's ancestor crumbs — a crumb is a "go up"
//     affordance, so the user is asking to leave, not to glance sideways.
//
// REFERENCE — open in a foreground new tab: `<AppLink target="_blank">`.
//   The user is reading A, and A points at B. Leaving A costs them scroll
//   position, expanded sections, a half-written comment draft — state the app
//   cannot restore for them. Most readers come back.
//   → Mentions and bare links inside content, sub-issue rows, the parent-issue
//     breadcrumb, an avatar leading to a profile, PRs and attachments.
//
// The question is never "is the destination an issue?" or "which component is
// this?" — it is "is the surface the user is standing on worth preserving?"
// A sub-issue row and an issue list row both open an issue and are
// deliberately different: the sub-issue list is part of the parent's reading
// context, the issue list is a picker.
//
// Modifiers invert the surface's default and always land in the background: on
// a Reference, cmd/ctrl-click and middle-click open a background tab instead
// of a foreground one; on a Navigation surface they open a tab instead of
// navigating in place. An explicit "Open in new tab" menu item is always
// foreground — the user asked to move.
//
// On web `openInNewTab` is deliberately left undefined. `AppLink` renders a
// real anchor, so leaving the event alone hands the click to the browser,
// which already implements this entire table — and does it better than
// `window.open` can (it distinguishes background, foreground and new window,
// and no popup blocker gets a say). Only non-anchor surfaces need a
// `window.open` fallback; see `use-row-link.ts`.

export interface NavigationAdapter {
  push(path: string): void;
  replace(path: string): void;
  back(): void;
  pathname: string;
  searchParams: URLSearchParams;
  /**
   * Desktop only: open a path in a new tab. Optional `title` overrides the
   * default tab label. `opts.activate` controls focus:
   *   - `false` / omitted → background tab (browser cmd+click semantics; what
   *     modifier-click on links and mentions should use).
   *   - `true` → foreground tab (explicit "Open in new tab" toolbar buttons,
   *     where the user is asking to move into the new context).
   * Cross-workspace paths always switch workspace, regardless of `activate`.
   */
  openInNewTab?: (
    path: string,
    title?: string,
    opts?: { activate?: boolean },
  ) => void;
  /** Return a shareable URL for a path. Web: origin + path. Desktop: public web URL of the connected environment. */
  getShareableUrl: (path: string) => string;
  /**
   * Optional: warm up route assets / RSC payload for a path. Web wires this
   * to `router.prefetch`; desktop leaves it undefined because react-router
   * already loads the whole SPA. Callers must invoke via `prefetch?.(href)`.
   */
  prefetch?: (path: string) => void;
  /**
   * Optional: is there an in-app page behind the current one, so that `back()`
   * lands somewhere inside Multica rather than stepping off the app? Only the
   * platform can answer — web reads the browser's session history, desktop the
   * active tab's virtual history. Adapters that cannot answer leave this
   * undefined and callers must treat that as `false`.
   *
   * Read it through `useBackOrReplace()` rather than calling it directly.
   */
  canGoBack?: () => boolean;
}
