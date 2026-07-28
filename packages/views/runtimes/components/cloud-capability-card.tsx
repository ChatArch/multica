"use client";

import { useState } from "react";
import { Check, Cloud, Gauge, Settings2, Sparkles, Zap } from "lucide-react";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import { cn } from "@multica/ui/lib/utils";
import { CloudRuntimeDialog } from "./cloud-runtime-dialog";
import { useCloudPreview, type CloudSpeedTier } from "./cloud-preview";
import { useT } from "../../i18n";

/**
 * CloudCapabilityCard — the redesigned Cloud entry point (MUL-5385).
 *
 * Product intent: Cloud stops being an inventory of machines the user
 * provisions and becomes a workspace capability with one switch, a balance,
 * and an elastic concurrency figure. No instance type, no disk size, no
 * region, no start/stop — those are the scheduler's job, not the user's.
 *
 * The raw Fleet node list is NOT deleted; it moves behind "Advanced" so
 * power users and support can still reach it. This component is UI-only:
 * flipping the switch mutates local preview state (see cloud-preview.ts).
 */
export function CloudCapabilityCard({
  /** Whether the legacy Fleet node dialog can be opened from Advanced. */
  nodeInspectorAvailable = false,
}: {
  nodeInspectorAvailable?: boolean;
}) {
  const { t } = useT("runtimes");
  const cloudOn = useCloudPreview((state) => state.cloudOn);
  const setCloudOn = useCloudPreview((state) => state.setCloudOn);
  const [showNodeInspector, setShowNodeInspector] = useState(false);

  return (
    <section className="rounded-lg border bg-card">
      <header className="flex flex-wrap items-start justify-between gap-3 border-b px-4 py-3.5">
        <div className="flex min-w-0 items-start gap-3">
          <span className="flex h-10 w-10 shrink-0 items-center justify-center rounded-lg border bg-background">
            <Cloud aria-hidden="true" className="h-4 w-4 text-muted-foreground" />
          </span>
          <div className="min-w-0">
            <div className="flex items-center gap-2">
              <h2 className="truncate text-sm font-semibold">
                {t(($) => $.cloud_preview.title)}
              </h2>
              <Badge
                variant="secondary"
                className="h-5 shrink-0 rounded px-1.5 text-[10px] font-medium"
              >
                {t(($) => $.cloud_preview.badge)}
              </Badge>
            </div>
            <p className="mt-1 text-xs text-muted-foreground">
              {t(($) => $.cloud_preview.tagline)}
            </p>
          </div>
        </div>
        {cloudOn ? (
          <div className="flex shrink-0 items-center gap-1.5 text-xs text-success">
            <span className="h-2 w-2 rounded-full bg-success" aria-hidden="true" />
            {t(($) => $.cloud_preview.status_ready)}
          </div>
        ) : (
          <Button type="button" size="sm" onClick={() => setCloudOn(true)}>
            <Sparkles aria-hidden="true" className="h-3.5 w-3.5" />
            {t(($) => $.cloud_preview.enable)}
          </Button>
        )}
      </header>

      {cloudOn ? <CloudEnabledBody /> : <CloudDisabledBody />}

      <footer className="flex flex-wrap items-center justify-between gap-2 border-t px-4 py-2.5">
        <p className="text-[11px] text-muted-foreground">
          {t(($) => $.cloud_preview.mock_note)}
        </p>
        <div className="flex items-center gap-1">
          {cloudOn && (
            <Button
              type="button"
              variant="ghost"
              size="sm"
              className="h-7 px-2 text-xs text-muted-foreground"
              onClick={() => setCloudOn(false)}
            >
              {t(($) => $.cloud_preview.disable)}
            </Button>
          )}
          {nodeInspectorAvailable && (
            <Button
              type="button"
              variant="ghost"
              size="sm"
              className="h-7 px-2 text-xs text-muted-foreground"
              onClick={() => setShowNodeInspector(true)}
            >
              <Settings2 aria-hidden="true" className="h-3.5 w-3.5" />
              {t(($) => $.cloud_preview.advanced)}
            </Button>
          )}
        </div>
      </footer>

      {showNodeInspector && (
        <CloudRuntimeDialog onClose={() => setShowNodeInspector(false)} />
      )}
    </section>
  );
}

function CloudDisabledBody() {
  const { t } = useT("runtimes");
  const benefits = [
    t(($) => $.cloud_preview.benefits.no_install),
    t(($) => $.cloud_preview.benefits.no_api_key),
    t(($) => $.cloud_preview.benefits.elastic),
  ];
  return (
    <div className="px-4 py-4">
      <p className="text-sm text-muted-foreground">
        {t(($) => $.cloud_preview.pitch)}
      </p>
      <ul className="mt-3 grid gap-2 sm:grid-cols-3">
        {benefits.map((benefit) => (
          <li
            key={benefit}
            className="flex items-start gap-2 rounded-md border bg-muted/20 px-2.5 py-2 text-xs"
          >
            <Check
              aria-hidden="true"
              className="mt-0.5 h-3.5 w-3.5 shrink-0 text-success"
            />
            <span className="min-w-0">{benefit}</span>
          </li>
        ))}
      </ul>
    </div>
  );
}

function CloudEnabledBody() {
  const { t } = useT("runtimes");
  const creditsRemaining = useCloudPreview((state) => state.creditsRemaining);
  const creditsUsedThisMonth = useCloudPreview(
    (state) => state.creditsUsedThisMonth,
  );
  const activeRuns = useCloudPreview((state) => state.activeRuns);
  const concurrencyLimit = useCloudPreview((state) => state.concurrencyLimit);

  return (
    <div className="space-y-4 px-4 py-4">
      <dl className="grid grid-cols-2 gap-3 sm:grid-cols-3">
        <Stat
          label={t(($) => $.cloud_preview.stats.credits)}
          value={creditsRemaining.toLocaleString()}
        />
        <Stat
          label={t(($) => $.cloud_preview.stats.used_this_month)}
          value={creditsUsedThisMonth.toLocaleString()}
        />
        <Stat
          label={t(($) => $.cloud_preview.stats.concurrency)}
          value={t(($) => $.cloud_preview.stats.concurrency_value, {
            active: activeRuns,
            limit: concurrencyLimit,
          })}
        />
      </dl>
      <SpeedTierToggle />
    </div>
  );
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div className="min-w-0 rounded-md border bg-muted/20 px-3 py-2">
      <dt className="truncate text-[11px] text-muted-foreground">{label}</dt>
      <dd className="mt-0.5 truncate text-sm font-medium tabular-nums">
        {value}
      </dd>
    </div>
  );
}

/**
 * The one hardware-adjacent control the redesign keeps — and it is expressed
 * as an outcome ("standard" / "fast"), never as an instance type. It lives
 * here in workspace settings, not in the agent-creation path.
 */
function SpeedTierToggle() {
  const { t } = useT("runtimes");
  const tier = useCloudPreview((state) => state.tier);
  const setTier = useCloudPreview((state) => state.setTier);
  const options: {
    id: CloudSpeedTier;
    icon: typeof Gauge;
    title: string;
    hint: string;
  }[] = [
    {
      id: "standard",
      icon: Gauge,
      title: t(($) => $.cloud_preview.tier.standard),
      hint: t(($) => $.cloud_preview.tier.standard_hint),
    },
    {
      id: "fast",
      icon: Zap,
      title: t(($) => $.cloud_preview.tier.fast),
      hint: t(($) => $.cloud_preview.tier.fast_hint),
    },
  ];

  return (
    <fieldset>
      <legend className="text-xs text-muted-foreground">
        {t(($) => $.cloud_preview.tier.label)}
      </legend>
      <div className="mt-1.5 grid gap-2 sm:grid-cols-2">
        {options.map((option) => {
          const selected = option.id === tier;
          const Icon = option.icon;
          return (
            <label
              key={option.id}
              className={cn(
                "flex min-w-0 cursor-pointer items-start gap-2 rounded-lg border px-3 py-2.5 text-sm transition-colors hover:bg-muted",
                selected ? "border-primary bg-primary/5" : "border-border",
              )}
            >
              <input
                type="radio"
                name="cloud-preview-speed-tier"
                value={option.id}
                checked={selected}
                onChange={() => setTier(option.id)}
                className="mt-0.5 size-4 shrink-0 accent-foreground"
              />
              <Icon
                aria-hidden="true"
                className="mt-0.5 size-4 shrink-0 text-muted-foreground"
              />
              <span className="min-w-0">
                <span className="block font-medium">{option.title}</span>
                <span className="mt-0.5 block text-xs leading-4 text-muted-foreground">
                  {option.hint}
                </span>
              </span>
            </label>
          );
        })}
      </div>
    </fieldset>
  );
}

export default CloudCapabilityCard;
