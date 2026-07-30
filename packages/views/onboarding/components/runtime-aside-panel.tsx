"use client";

import { useT } from "../../i18n";

/**
 * Shared right-rail aside for Step 3 (runtime), on both the desktop
 * (runtime-connect FancyView) and web (platform-fork) paths.
 *
 * Deliberately short. This rail once carried a "Good to know" section
 * promising the runtime was swappable and that more could be added later —
 * which is exactly what the step's own lede already says, in a fifth of the
 * words, on the same screen. It also defined "agent runtime" at length while
 * the member was looking at a list of named, online runtimes under a headline
 * reading "This computer is connected"; by then the definition answers a
 * question they have stopped asking. What remains is the one thing the screen
 * does not show: what that background process actually is.
 *
 * If this rail grows again, put the new sentence in the main column instead —
 * it is the column members read.
 */
export function RuntimeAsidePanel() {
  const { t, i18n } = useT("onboarding");
  const installDocHref = i18n.language?.startsWith("zh")
    ? "https://multica.ai/docs/zh/install-agent-runtime"
    : "https://multica.ai/docs/install-agent-runtime";
  return (
    <div className="flex flex-col gap-4">
      <div className="text-caption font-medium uppercase tracking-[0.08em] text-muted-foreground">
        {t(($) => $.runtime_aside.what_eyebrow)}
      </div>
      <p className="text-body leading-[1.6] text-foreground">
        {t(($) => $.runtime_aside.what_body)}
      </p>
      <a
        href={installDocHref}
        target="_blank"
        rel="noopener noreferrer"
        className="self-start text-label text-muted-foreground underline underline-offset-4 transition-colors hover:text-foreground"
      >
        {t(($) => $.runtime_aside.learn_more)}
      </a>
    </div>
  );
}
