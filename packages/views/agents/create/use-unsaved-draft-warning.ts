"use client";

import { useEffect } from "react";

/**
 * Browser-level guard against losing a half-filled draft to a hard unload
 * (tab close, reload). In-app navigation is deliberately not covered here —
 * that path is the routes' own business.
 */
export function useUnsavedDraftWarning(active: boolean): void {
  useEffect(() => {
    if (!active) return;
    const warnBeforeUnload = (event: BeforeUnloadEvent) => {
      event.preventDefault();
      event.returnValue = "";
    };
    window.addEventListener("beforeunload", warnBeforeUnload);
    return () => window.removeEventListener("beforeunload", warnBeforeUnload);
  }, [active]);
}
