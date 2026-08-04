"use client";

import { Sparkles } from "lucide-react";
import {
  Item,
  ItemContent,
  ItemDescription,
  ItemMedia,
  ItemTitle,
} from "@multica/ui/components/ui/item";
import { useT } from "../../i18n";
import { StepHeading } from "./step-shell";

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
    <div className="flex flex-col gap-5">
      <StepHeading title={t(($) => $.mika_intro.headline)} />
      <Item variant="outline">
        <ItemMedia>
          <span className="flex size-9 shrink-0 items-center justify-center rounded-lg bg-foreground text-background">
            <Sparkles aria-hidden className="size-4" />
          </span>
        </ItemMedia>
        <ItemContent>
          <ItemTitle>
            {t(($) => $.mika_intro.name)}
            <span className="text-muted-foreground">
              {" · "}
              {t(($) => $.mika_intro.role)}
            </span>
          </ItemTitle>
          <ItemDescription>{t(($) => $.mika_intro.blurb)}</ItemDescription>
        </ItemContent>
      </Item>
    </div>
  );
}
