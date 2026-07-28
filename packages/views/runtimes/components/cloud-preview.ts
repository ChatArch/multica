/**
 * Cloud state for the redesigned entry points (MUL-5385).
 *
 * This module deliberately contains NO capability wiring. It holds the
 * client-side state the Cloud UI needs so the product shape ("Cloud is a
 * workspace capability, not a machine you provision") can be reviewed in a
 * running app before any server / Fleet work starts.
 *
 * Everything here is a mock:
 *   - `cloudOn` flips locally; nothing is provisioned, nothing is charged.
 *   - the credit / concurrency numbers are illustrative placeholders.
 * The UI labels itself as a preview so a reviewer never mistakes these for
 * live figures. This branch is for the test environment only — it is not
 * meant to merge to main as-is.
 */
import { createStore } from "zustand/vanilla";
import { useStore } from "zustand";

/** Speed tier is the ONLY hardware-adjacent choice the redesign keeps, and
 *  it is deliberately not an instance type — it lives in workspace settings,
 *  never in the agent-creation path. */
export type CloudSpeedTier = "standard" | "fast";

export interface CloudPreviewState {
  /** Whether the workspace has "turned on" Multica Cloud (mock). */
  cloudOn: boolean;
  tier: CloudSpeedTier;
  /** Illustrative wallet + usage figures (mock). */
  creditsRemaining: number;
  creditsUsedThisMonth: number;
  /** Illustrative elastic-capacity figures (mock). */
  activeRuns: number;
  concurrencyLimit: number;
  setCloudOn: (on: boolean) => void;
  setTier: (tier: CloudSpeedTier) => void;
  reset: () => void;
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
  ...MOCK_DEFAULTS,
  setCloudOn: (cloudOn) => set({ cloudOn }),
  setTier: (tier) => set({ tier }),
  reset: () => set({ ...MOCK_DEFAULTS }),
}));

export function useCloudPreview<T>(selector: (state: CloudPreviewState) => T): T {
  return useStore(cloudPreviewStore, selector);
}

/** Test helper — restores mock defaults between cases. */
export function resetCloudPreview(): void {
  cloudPreviewStore.getState().reset();
}
