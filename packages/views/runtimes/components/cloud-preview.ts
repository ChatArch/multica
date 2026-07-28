/**
 * Cloud UI preview — product-review scaffolding for MUL-5385.
 *
 * This module deliberately contains NO capability wiring. It holds the
 * client-side state the redesigned Cloud entry points need so the product
 * shape ("Cloud is a workspace capability, not a machine you provision")
 * can be reviewed in a running app before any server / Fleet work starts.
 *
 * Everything here is a mock:
 *   - `cloudOn` flips locally; nothing is provisioned, nothing is charged.
 *   - the credit / concurrency numbers are illustrative placeholders.
 * The UI labels them as a preview so a reviewer never mistakes them for
 * live data.
 *
 * Rollout: OFF unless `NEXT_PUBLIC_CLOUD_UI_PREVIEW=true`, so production and
 * every existing surface keep their current behaviour byte-for-byte.
 */
import { createStore } from "zustand/vanilla";
import { useStore } from "zustand";

/** Speed tier is the ONLY hardware-adjacent choice the redesign keeps, and
 *  it is deliberately not an instance type — it lives in workspace settings,
 *  never in the agent-creation path. */
export type CloudSpeedTier = "standard" | "fast";

export interface CloudPreviewState {
  /** Renders the redesigned Cloud entry points instead of the legacy
   *  "create a node" affordances. Env-seeded; overridable in tests. */
  previewEnabled: boolean;
  /** Whether the workspace has "turned on" Multica Cloud (mock). */
  cloudOn: boolean;
  tier: CloudSpeedTier;
  /** Illustrative wallet + usage figures (mock). */
  creditsRemaining: number;
  creditsUsedThisMonth: number;
  /** Illustrative elastic-capacity figures (mock). */
  activeRuns: number;
  concurrencyLimit: number;
  setPreviewEnabled: (enabled: boolean) => void;
  setCloudOn: (on: boolean) => void;
  setTier: (tier: CloudSpeedTier) => void;
  reset: () => void;
}

/**
 * Reading NEXT_PUBLIC_* inside packages/views is safe: apps/web lists
 * `@multica/views` in `transpilePackages`, so Next inlines the value at build
 * time. Guarded anyway for non-Next consumers (mobile / bare vitest) where
 * `process` may be absent.
 */
function previewEnabledFromEnv(): boolean {
  try {
    const raw =
      typeof process !== "undefined"
        ? process.env?.NEXT_PUBLIC_CLOUD_UI_PREVIEW
        : undefined;
    return raw === "true" || raw === "1";
  } catch {
    return false;
  }
}

const MOCK_DEFAULTS = {
  cloudOn: false,
  tier: "standard" as CloudSpeedTier,
  creditsRemaining: 4820,
  creditsUsedThisMonth: 1180,
  activeRuns: 2,
  concurrencyLimit: 5,
};

export const cloudPreviewStore = createStore<CloudPreviewState>((set) => ({
  previewEnabled: previewEnabledFromEnv(),
  ...MOCK_DEFAULTS,
  setPreviewEnabled: (previewEnabled) => set({ previewEnabled }),
  setCloudOn: (cloudOn) => set({ cloudOn }),
  setTier: (tier) => set({ tier }),
  reset: () =>
    set({ previewEnabled: previewEnabledFromEnv(), ...MOCK_DEFAULTS }),
}));

export function useCloudPreview<T>(selector: (state: CloudPreviewState) => T): T {
  return useStore(cloudPreviewStore, selector);
}

/** True when the redesigned Cloud entry points should render. */
export function useCloudUiPreview(): boolean {
  return useCloudPreview((state) => state.previewEnabled);
}

/** Test helper — flips the flag without touching env. */
export function setCloudUiPreview(enabled: boolean): void {
  cloudPreviewStore.getState().setPreviewEnabled(enabled);
}

/** Test helper — restores mock defaults between cases. */
export function resetCloudPreview(): void {
  cloudPreviewStore.getState().reset();
}
