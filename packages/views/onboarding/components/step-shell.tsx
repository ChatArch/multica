"use client";

import { useRef, type ReactNode } from "react";
import { cn } from "@multica/ui/lib/utils";
import { useScrollFade } from "@multica/ui/hooks/use-scroll-fade";
import type { OnboardingStep } from "@multica/core/onboarding";
import { StepSidebar } from "./step-sidebar";

/**
 * Shared geometry for the onboarding steps, so four screens the member sees
 * back-to-back cannot drift apart again. They already had: the headers were
 * flush to the window edge while the content columns were centred, which was
 * invisible while a 480px rail absorbed the difference and became a 267px
 * misalignment the moment the rail came out.
 *
 * Three pieces, and the split between them is the point:
 *
 *   - STEP_GUTTER is the only place horizontal padding lives. Keeping it off
 *     the measured element is what stops the reading width from jumping at a
 *     breakpoint — with padding inside a max-w box, `md` and `lg` resolve to
 *     different content widths for the same box.
 *   - STEP_FRAME is the wide content measure, for steps whose content needs
 *     the width (the questionnaire's option grid, the runtime list).
 *   - STEP_COLUMN is the reading measure for prose and forms, centred. Both
 *     centre within the main pane, so content stays on one centreline
 *     whichever a step picks.
 */
export const STEP_GUTTER = "px-6 sm:px-10 md:px-14 lg:px-16";
export const STEP_FRAME = "mx-auto w-full max-w-[920px]";
export const STEP_COLUMN = "mx-auto w-full max-w-[620px]";

/**
 * Reading measure applied INSIDE the frame, left-aligned rather than centred.
 * Use it for prose and form fields on a step that sits on STEP_FRAME: the
 * page margins then match every other step, while a workspace name still gets
 * an input someone would want to type into instead of an 800px one.
 */
export const STEP_MEASURE = "max-w-[620px]";

/** Vertical rhythm shared by every step's scrolling region. */
export const STEP_BLOCK_PADDING = "py-10";

/**
 * The frame every onboarding step renders into: progress rail on the left,
 * the step's own content scrolling on the right.
 *
 * It owns the window, not just a header strip. That is what lets the four
 * steps stop repeating an identical `<div>` / DragStrip / header / `<main>`
 * preamble — including the scroll-fade wiring, which was duplicated verbatim
 * in all four and is now set up once here.
 *
 * Each pane gets its own drag strip rather than one spanning the top: the
 * rail's has to be inside the dark surface to keep the window's top-left
 * corner from showing a light band, and the main pane's has to sit outside
 * the scroll container so it doesn't scroll away from the traffic lights.
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
          {children}
        </main>
      </div>
    </div>
  );
}
