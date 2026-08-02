"use client";

import { Sparkles } from "lucide-react";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../i18n";
import { STEP_MEASURE } from "./step-shell";

/**
 * The subject of step 3, stated as an object rather than a footnote.
 *
 * The step used to be titled after its dependency — "Pick an agent runtime" —
 * with Mika named only in the grey lede. That put the thing being created in
 * the small print and the technical prerequisite in the headline, so a member
 * reached a button saying "Start with Mika" without ever being told who that
 * is. Headline plus a card with a mark on it is what makes the introduction
 * survive not reading carefully.
 *
 * Mika does not exist yet at this point in the flow — she is created when the
 * member commits — so this cannot render her stored avatar. It reuses the mark
 * the Runtimes page already uses for "Start with Mika" instead, which keeps
 * the two entry points recognisably the same thing.
 */
export function MikaIntro() {
  const { t } = useT("onboarding");
  return (
    <>
      <h1 className="text-balance font-serif text-display font-medium leading-[1.1] tracking-tight text-foreground">
        {t(($) => $.mika_intro.headline)}
      </h1>
      <div
        className={cn(
          "mt-6 flex items-start gap-4 rounded-xl border bg-card px-5 py-4",
          STEP_MEASURE,
        )}
      >
        <span className="flex size-10 shrink-0 items-center justify-center rounded-lg bg-foreground text-background">
          <Sparkles aria-hidden className="size-4" />
        </span>
        <div className="min-w-0">
          <p className="text-body font-medium text-foreground">
            {t(($) => $.mika_intro.name)}
            <span className="text-muted-foreground">
              {" · "}
              {t(($) => $.mika_intro.role)}
            </span>
          </p>
          <p className="mt-1 text-body leading-[1.55] text-muted-foreground">
            {t(($) => $.mika_intro.blurb)}
          </p>
        </div>
      </div>
    </>
  );
}
