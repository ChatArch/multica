"use client";

import type { ReactNode } from "react";
import { ArrowLeft, Check } from "lucide-react";
import {
  ONBOARDING_STEP_ORDER,
  type OnboardingStep,
} from "@multica/core/onboarding";
import { cn } from "@multica/ui/lib/utils";
import {
  Stepper,
  StepperDescription,
  StepperIndicator,
  StepperItem,
  StepperNav,
  StepperSeparator,
  StepperTitle,
} from "@multica/ui/components/ui/stepper";
import { useT } from "../../i18n";

/**
 * The persistent rail: where the member is in onboarding, and how to go back.
 *
 * This replaces a horizontal row of dots that carried a "Step 2 of 3" counter
 * and nothing else. The counter told a member how much was left but never what
 * was coming, so each step arrived unannounced — the same reason the runtime
 * step read as a surprise. Naming all three steps up front makes onboarding
 * legible as a whole before the first field is filled in.
 *
 * Two deliberate choices:
 *
 *   - It is not a tablist. The ported stepper defaults to `role="tablist"`
 *     with each trigger owning `aria-controls` on a panel id — correct for a
 *     stepper that renders its own panels, wrong here, where the panel is a
 *     routed step and those ids would dangle. We use the presentational slots
 *     and mark position with `aria-current="step"` instead.
 *   - Only completed steps are clickable. Forward navigation has to run each
 *     step's validation and submit, so jumping ahead from the rail would skip
 *     it; going back is always safe.
 */
export function StepSidebar({
  currentStep,
  onBack,
  backDisabled,
  onStepChange,
  footer,
}: {
  currentStep: OnboardingStep;
  onBack?: () => void;
  /** Workspace step disables Back while its create request is in flight. */
  backDisabled?: boolean;
  /** Return to an already-completed step. Injected by the flow, which owns
   *  step order; absent means the rail is display-only. */
  onStepChange?: (step: OnboardingStep) => void;
  /** Injected by the flow — the Log out escape hatch. A slot rather than a
   *  direct render so this stays presentational: rendering the logout
   *  mutation inline forced a QueryClient into five step test files. */
  footer?: ReactNode;
}) {
  const { t } = useT("onboarding");
  const currentIndex = Math.max(0, ONBOARDING_STEP_ORDER.indexOf(currentStep));

  return (
    <aside className="flex w-[15rem] shrink-0 flex-col bg-foreground text-background lg:w-[19rem]">
      {/* Sits under the traffic lights, so the rail owns the window's top-left
          corner rather than leaving a light band above it. */}
      <div
        aria-hidden
        className="h-12 shrink-0"
        style={{ WebkitAppRegion: "drag" } as React.CSSProperties}
      />

      <div className="flex min-h-0 flex-1 flex-col px-6 pb-8 lg:px-8">
        {onBack ? (
          <button
            type="button"
            onClick={onBack}
            disabled={backDisabled}
            className="-ml-1 flex w-fit items-center gap-1.5 rounded-md px-1 py-1 text-body text-background/60 transition-colors hover:text-background focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-background/40 disabled:opacity-40"
          >
            <ArrowLeft className="h-3.5 w-3.5" />
            {t(($) => $.common.back)}
          </button>
        ) : null}

        <Stepper
          value={currentIndex + 1}
          orientation="vertical"
          role="group"
          aria-label={t(($) => $.step_nav.label)}
          className={cn("w-full", onBack ? "mt-10" : "mt-2")}
        >
          <StepperNav className="w-full gap-0">
            {ONBOARDING_STEP_ORDER.map((stepId, index) => {
              const isDone = index < currentIndex;
              const isCurrent = index === currentIndex;
              const isLast = index === ONBOARDING_STEP_ORDER.length - 1;
              const canReturn = isDone && !!onStepChange && !backDisabled;
              const key = stepId as Exclude<OnboardingStep, "welcome">;

              const body = (
                <>
                  <StepperIndicator
                    className={cn(
                      "size-6 border-0 transition-colors",
                      isDone || isCurrent
                        ? "bg-background text-foreground"
                        : "bg-background/10 text-background/50",
                    )}
                  >
                    {isDone ? (
                      <Check aria-hidden className="size-3.5" />
                    ) : (
                      index + 1
                    )}
                  </StepperIndicator>
                  <div className="min-w-0 text-left">
                    <StepperTitle
                      className={cn(
                        "transition-colors",
                        isCurrent ? "text-background" : "text-background/60",
                      )}
                    >
                      {t(($) => $.step_nav[key].label)}
                    </StepperTitle>
                    <StepperDescription className="mt-0.5 text-background/45">
                      {t(($) => $.step_nav[key].description)}
                    </StepperDescription>
                  </div>
                </>
              );

              return (
                <StepperItem
                  key={stepId}
                  step={index + 1}
                  completed={isDone}
                  className="w-full items-start not-last:flex-none"
                  {...(isCurrent ? { "aria-current": "step" as const } : {})}
                >
                  {canReturn ? (
                    <button
                      type="button"
                      onClick={() => onStepChange(stepId)}
                      className="flex w-full items-start gap-3 rounded-md py-1 text-left transition-opacity hover:opacity-80 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-background/40"
                    >
                      {body}
                    </button>
                  ) : (
                    <div className="flex w-full items-start gap-3 py-1">
                      {body}
                    </div>
                  )}

                  {/* Indented to line up under the indicator's centre. */}
                  {!isLast ? (
                    <StepperSeparator
                      className={cn(
                        "ml-[0.6875rem] group-data-[orientation=vertical]/stepper-nav:h-5",
                        isDone ? "bg-background/40" : "bg-background/15",
                      )}
                    />
                  ) : null}
                </StepperItem>
              );
            })}
          </StepperNav>
        </Stepper>

        {footer ? <div className="mt-auto pt-8">{footer}</div> : null}
      </div>
    </aside>
  );
}
