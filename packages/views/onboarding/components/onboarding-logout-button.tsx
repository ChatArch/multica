"use client";

import { LogOut } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import { cn } from "@multica/ui/lib/utils";
import { useLogout } from "../../auth";
import { useT } from "../../i18n";

/**
 * Account-switch escape hatch shared by every onboarding step.
 *
 * `fixed` only for the welcome screen, which has no step header to sit in.
 * Every other step passes `inline` so it rides the header row on STEP_FRAME:
 * pinning it to the window corner put it outside the measure and above
 * Back / Step N of N, which read as a second header row.
 */
export function OnboardingLogoutButton({
  inline = false,
}: {
  /** Render in normal flow (inside a step header) instead of pinned. */
  inline?: boolean;
} = {}) {
  const { t } = useT("onboarding");
  const logout = useLogout();

  return (
    <Button
      variant="ghost"
      size="sm"
      className={cn(
        "text-muted-foreground hover:text-destructive",
        inline ? "-mr-2 shrink-0" : "fixed right-8 top-8 z-50",
      )}
      style={{ WebkitAppRegion: "no-drag" } as React.CSSProperties}
      onClick={logout}
    >
      <LogOut />
      {t(($) => $.common.log_out)}
    </Button>
  );
}
