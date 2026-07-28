"use client";

import { Cloud, Info, Monitor } from "lucide-react";
import type { ReactNode } from "react";
import type { RuntimeDevice } from "@multica/core/types";
import { runtimeDisplayName } from "@multica/core/runtimes";
import { Label } from "@multica/ui/components/ui/label";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../i18n";

export type RunLocation = "cloud" | "local";

/**
 * RunLocationPicker — the redesigned agent-creation entry (MUL-5385).
 *
 * Today the first question the create flow asks is "which runtime?", i.e.
 * "which machine × which CLI" — a question only a user who already owns
 * machines can answer. The redesign asks the one question that is actually
 * irreducible: **where should this agent work?** Cloud is the recommended
 * default because it needs no install and no BYO API key; "my computer"
 * keeps the full existing picker underneath, unchanged.
 *
 * UI-only: when Cloud is picked we bind to a real managed cloud runtime if
 * the workspace already has one, otherwise we surface a preview notice and
 * leave the selection empty (which keeps the parent's Create button disabled
 * through its existing `!selectedRuntime` guard — no fake submits).
 */
export function RunLocationPicker({
  location,
  onLocationChange,
  cloudRuntime,
  localPicker,
}: {
  location: RunLocation;
  onLocationChange: (next: RunLocation) => void;
  /** A real `runtime_mode === "cloud"` runtime, when the workspace has one. */
  cloudRuntime: RuntimeDevice | null;
  /** The existing machine × CLI picker, rendered under "my computer". */
  localPicker: ReactNode;
}) {
  const { t } = useT("agents");

  return (
    <div className="flex min-w-0 flex-col">
      <Label className="text-xs text-muted-foreground">
        {t(($) => $.create_dialog.run_location.label)}
      </Label>
      <fieldset className="mt-1.5 grid gap-2 sm:grid-cols-2">
        <legend className="sr-only">
          {t(($) => $.create_dialog.run_location.label)}
        </legend>
        <LocationChoice
          value="cloud"
          icon={Cloud}
          title={t(($) => $.create_dialog.run_location.cloud_title)}
          description={t(($) => $.create_dialog.run_location.cloud_desc)}
          badge={t(($) => $.create_dialog.run_location.recommended)}
          selected={location === "cloud"}
          onSelect={() => onLocationChange("cloud")}
        />
        <LocationChoice
          value="local"
          icon={Monitor}
          title={t(($) => $.create_dialog.run_location.local_title)}
          description={t(($) => $.create_dialog.run_location.local_desc)}
          selected={location === "local"}
          onSelect={() => onLocationChange("local")}
        />
      </fieldset>

      {location === "cloud" ? (
        <div className="mt-2">
          {cloudRuntime ? (
            <p className="text-xs text-muted-foreground">
              {t(($) => $.create_dialog.run_location.cloud_bound_hint, {
                name: runtimeDisplayName(cloudRuntime),
              })}
            </p>
          ) : (
            <p
              role="note"
              className="flex items-start gap-1.5 rounded-md border border-dashed bg-muted/30 px-2.5 py-2 text-xs text-muted-foreground"
            >
              <Info aria-hidden="true" className="mt-0.5 h-3.5 w-3.5 shrink-0" />
              <span className="min-w-0">
                {t(($) => $.create_dialog.run_location.cloud_preview_notice)}
              </span>
            </p>
          )}
        </div>
      ) : (
        <div className="mt-2">{localPicker}</div>
      )}
    </div>
  );
}

function LocationChoice({
  value,
  icon: Icon,
  title,
  description,
  badge,
  selected,
  onSelect,
}: {
  value: RunLocation;
  icon: typeof Cloud;
  title: string;
  description: string;
  badge?: string;
  selected: boolean;
  onSelect: () => void;
}) {
  return (
    <label
      className={cn(
        "flex min-w-0 cursor-pointer items-start gap-2 rounded-lg border px-3 py-2.5 text-sm transition-colors hover:bg-muted",
        selected ? "border-primary bg-primary/5" : "border-border",
      )}
    >
      <input
        type="radio"
        name="agent-run-location"
        value={value}
        checked={selected}
        onChange={onSelect}
        className="mt-0.5 size-4 shrink-0 accent-foreground"
      />
      <Icon
        aria-hidden="true"
        className="mt-0.5 size-4 shrink-0 text-muted-foreground"
      />
      <span className="min-w-0">
        <span className="flex items-center gap-1.5">
          <span className="truncate font-medium">{title}</span>
          {badge && (
            <span className="shrink-0 rounded bg-info/10 px-1.5 py-0.5 text-[10px] font-medium text-info">
              {badge}
            </span>
          )}
        </span>
        <span className="mt-0.5 block text-xs leading-4 text-muted-foreground">
          {description}
        </span>
      </span>
    </label>
  );
}
