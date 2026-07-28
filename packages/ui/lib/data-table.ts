import type { Column, RowData } from "@tanstack/react-table";
import type * as React from "react";

// Extend TanStack Table's ColumnMeta with a `grow` flag. TanStack merges
// a default `size: 150` into every columnDef, so "no explicit size" can't
// be detected by inspecting columnDef.size (it's always a number). Setting
// `meta: { grow: true }` is the official extension point: DataTable skips
// the inline width for these columns until the user explicitly resizes them,
// then the resized width wins.
declare module "@tanstack/react-table" {
  interface ColumnMeta<TData extends RowData, TValue> {
    grow?: boolean;
  }
}

// Custom-property name carrying a column's width. Column ids are app-defined
// and may hold characters that are not valid in a CSS ident (`property:<uuid>`
// is the live example), so everything outside the ident set is folded to `_`.
// The ids in play differ well before that point, so folding cannot collide.
export function columnSizeVar(columnId: string) {
  return `--col-${columnId.replace(/[^a-zA-Z0-9_-]/g, "_")}-size`;
}

// Combined sizing + pinning style for a `<th>` / `<td>` cell. Width is
// emitted unless the column is flagged `meta.grow` (those rely on
// fixed-layout's leftover-space distribution). Pinned columns get
// sticky positioning — see notes below.
//
// The width is a reference to a custom property published once on the <table>
// rather than a number read per cell. During a resize that lets the browser
// apply every new width from a single declaration change, with no React work
// in the hundreds of cells that would otherwise each re-read column.getSize().
//
// Background is intentionally NOT set inline — the upstream Dice UI
// version writes `background: var(--background)` here, which can't
// react to `:hover`. Consumers set bg via Tailwind classes paired with
// `group-hover:`.
export function getCellStyle<TData>(
  column: Column<TData>,
  options?: { showPinnedEdge?: boolean; hasExplicitSize?: boolean },
): React.CSSProperties {
  const grow = column.columnDef.meta?.grow;
  const width =
    grow && !options?.hasExplicitSize
      ? undefined
      : `var(${columnSizeVar(column.id)})`;

  const isPinned = column.getIsPinned();
  if (!isPinned) {
    return width !== undefined ? { width } : {};
  }

  // The shadow marks where the frozen columns end and scrolled content passes
  // underneath. With nothing scrolled under them there is nothing to mark, and
  // it reads as a stray rule the other columns don't have — so callers turn it
  // on only once the surface is actually scrolled sideways.
  const showPinnedEdge = options?.showPinnedEdge ?? false;
  const isLastLeftPinned =
    isPinned === "left" && column.getIsLastColumn("left");
  const isFirstRightPinned =
    isPinned === "right" && column.getIsFirstColumn("right");

  return {
    width,
    position: "sticky",
    left: isPinned === "left" ? `${column.getStart("left")}px` : undefined,
    right: isPinned === "right" ? `${column.getAfter("right")}px` : undefined,
    zIndex: 1,
    boxShadow: showPinnedEdge
      ? isLastLeftPinned
        ? "-4px 0 4px -4px var(--border) inset"
        : isFirstRightPinned
          ? "4px 0 4px -4px var(--border) inset"
          : undefined
      : undefined,
  };
}
