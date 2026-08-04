"use client";

import { useRef, type ReactNode } from "react";
import { cn } from "@multica/ui/lib/utils";
import { useScrollFade } from "@multica/ui/hooks/use-scroll-fade";
import type { OnboardingStep } from "@multica/core/onboarding";
import { StepSidebar } from "./step-sidebar";

/**
 * Geometry for the onboarding steps, taken from the ReUI onboarding-3 block so
 * the two panes read as one design.
 *
 * The old model had three competing measures — a 920px frame, a 620px column
 * and an in-frame cap — which is how the platform fork ended up ~150px off the
 * left edge of every other step. There is now one measure. A step that needs
 * to break out of it says so locally rather than picking a different global.
 *
 * STEP_MEASURE stays exported because the workspace form uses it to keep an
 * input from stretching the full column width.
 */
export const STEP_COLUMN = "mx-auto flex min-h-full w-full max-w-[28rem] flex-col";
export const STEP_GUTTER = "px-6 py-8 sm:px-10 lg:px-14 lg:py-10";

/** Cap for a control that should not run the full column width. */
export const STEP_MEASURE = "max-w-[24rem]";

/**
 * Title + supporting line for a step.
 *
 * Replaces a serif display headline over a grey uppercase eyebrow. The block
 * leads with a plain semibold sans title and one muted line under it, and the
 * eyebrow has no equivalent there — with the rail naming the step, a third
 * label for the same screen was redundant.
 *
 * `aria-live` because the panes persist across steps: a screen reader user
 * moving between steps gets no navigation event, so the heading has to
 * announce itself.
 */
export function StepHeading({
  title,
  description,
}: {
  title: ReactNode;
  description?: ReactNode;
}) {
  return (
    <div className="flex flex-col gap-1.5" aria-live="polite">
      <h1 className="text-balance text-title-lg font-semibold text-foreground">
        {title}
      </h1>
      {description ? (
        <p className="text-pretty text-body text-muted-foreground">
          {description}
        </p>
      ) : null}
    </div>
  );
}

/**
 * The step's actions, pinned to the bottom of the column.
 *
 * Full-width stacked buttons rather than a right-aligned inline row: the
 * column is 28rem, so an inline bar leaves the primary action floating in the
 * middle of the screen instead of landing where the eye finishes the form.
 * `mt-auto` against the column's `min-h-full` is what pins it.
 */
export function StepFooter({
  children,
  hint,
}: {
  children: ReactNode;
  /** Optional status line above the buttons (validation, progress). */
  hint?: ReactNode;
}) {
  return (
    <div className="mt-auto flex flex-col gap-2 pb-2 pt-10">
      {hint ? (
        <p aria-live="polite" className="text-caption text-muted-foreground">
          {hint}
        </p>
      ) : null}
      {children}
    </div>
  );
}

/**
 * The frame every onboarding step renders into: progress rail on the left,
 * the step's own content scrolling on the right.
 *
 * It owns the window, not just a header strip. That is what lets the four
 * steps stop repeating an identical wrapper / DragStrip / header / `<main>`
 * preamble, including the scroll-fade wiring that was duplicated verbatim in
 * all four and is now set up once.
 *
 * The column is `min-h-full` inside a scrolling pane rather than centred by
 * the pane: `items-center` on a scroll container clips the top of anything
 * taller than the viewport, and these steps do overflow on short windows.
 */
export function StepShell({
  currentStep,
  onBack,
  backDisabled,
  onStepChange,
  sidebarFooter,
  children,
}: {
  currentStep: OnboardingStep;
  onBack?: () => void;
  /** Workspace step disables Back while its create request is in flight. */
  backDisabled?: boolean;
  /** Return to an already-completed step from the rail. */
  onStepChange?: (step: OnboardingStep) => void;
  /** Injected by the flow — the Log out escape hatch. */
  sidebarFooter?: ReactNode;
  children: ReactNode;
}) {
  const mainRef = useRef<HTMLElement>(null);
  const fadeStyle = useScrollFade(mainRef);

  return (
    <div className="animate-onboarding-enter flex h-full min-h-0 bg-background">
      <StepSidebar
        currentStep={currentStep}
        onBack={onBack}
        backDisabled={backDisabled}
        onStepChange={onStepChange}
        footer={sidebarFooter}
      />

      <div className="flex min-w-0 flex-1 flex-col">
        <div
          aria-hidden
          className="h-12 shrink-0"
          style={{ WebkitAppRegion: "drag" } as React.CSSProperties}
        />
        <main
          ref={mainRef}
          style={fadeStyle}
          className={cn("min-h-0 flex-1 overflow-y-auto", STEP_GUTTER)}
        >
          <div className={STEP_COLUMN}>{children}</div>
        </main>
      </div>
    </div>
  );
}
