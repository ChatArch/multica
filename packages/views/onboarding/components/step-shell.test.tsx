import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales/en/common.json";
import enOnboarding from "../../locales/en/onboarding.json";
import {
  STEP_COLUMN,
  STEP_GUTTER,
  STEP_MEASURE,
  StepFooter,
  StepHeading,
  StepShell,
} from "./step-shell";

const TEST_RESOURCES = { en: { common: enCommon, onboarding: enOnboarding } };

function renderShell(props: Partial<Parameters<typeof StepShell>[0]> = {}) {
  return render(
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      <StepShell currentStep="workspace" {...props}>
        <div>step content</div>
      </StepShell>
    </I18nProvider>,
  );
}

describe("onboarding step shell", () => {
  // The regression this guards: the horizontal padding used to live on the
  // element that also carried the measure. Inside a max-w box the two fight —
  // `md` and `lg` resolved to different content widths for the same box, and
  // the reading column jumped from 508px to 620px at a breakpoint.
  it("puts the gutter on the scrolling pane and the measure on the content", () => {
    const { container } = renderShell();

    const main = container.querySelector("main")!;
    for (const cls of STEP_GUTTER.split(" ")) {
      expect(main.className).toContain(cls);
    }
    expect(main.className).not.toMatch(/\bmax-w-/);
  });

  // One measure, not three. Two centred measures of different widths do not
  // share a left edge, which is how the platform fork ended up ~150px right
  // of every other step.
  it("centres a single content measure", () => {
    expect(STEP_COLUMN).toContain("mx-auto");
    expect(STEP_COLUMN).toMatch(/max-w-\[[\d.]+rem\]/);
    expect(STEP_COLUMN).not.toMatch(/\bp[xlr]?-/);
  });

  // The column fills the pane so StepFooter's `mt-auto` has something to push
  // against; without it the actions ride up under short content.
  it("makes the column fill the pane so the footer can pin to the bottom", () => {
    const { container } = renderShell();

    const column = container.querySelector("main > div")!;
    expect(column.className).toContain("min-h-full");
    expect(column.className).toContain("flex-col");
  });

  it("keeps the in-column control cap left-aligned and padding-free", () => {
    expect(STEP_MEASURE).toMatch(/max-w-\[[\d.]+rem\]/);
    expect(STEP_MEASURE).not.toContain("mx-auto");
    expect(STEP_MEASURE).not.toMatch(/\bp[xlr]?-/);
  });

  // The panes persist across steps, so a screen reader user gets no
  // navigation event when the step changes — the heading has to announce.
  it("announces the step heading and renders it as the page h1", () => {
    render(
      <I18nProvider locale="en" resources={TEST_RESOURCES}>
        <StepHeading title="Name your workspace" description="Pick a URL." />
      </I18nProvider>,
    );

    const heading = screen.getByRole("heading", { name: "Name your workspace" });
    expect(heading.tagName).toBe("H1");
    expect(heading.closest("[aria-live]")).not.toBeNull();
    expect(screen.getByText("Pick a URL.")).toBeInTheDocument();
  });

  it("pins the step actions to the bottom of the column", () => {
    const { container } = render(
      <I18nProvider locale="en" resources={TEST_RESOURCES}>
        <StepFooter hint="Name it to continue.">
          <button type="button">Continue</button>
        </StepFooter>
      </I18nProvider>,
    );

    expect(container.firstElementChild!.className).toContain("mt-auto");
    expect(screen.getByText("Name it to continue.")).toBeInTheDocument();
  });

  it("renders Back only when the step can go back", () => {
    const { unmount } = renderShell();
    expect(screen.queryByRole("button", { name: /back/i })).toBeNull();
    unmount();

    renderShell({ onBack: () => {} });
    expect(screen.getByRole("button", { name: /back/i })).toBeInTheDocument();
  });

  it("disables Back while the step reports work in flight", () => {
    renderShell({ onBack: () => {}, backDisabled: true });
    expect(screen.getByRole("button", { name: /back/i })).toBeDisabled();
  });
});

describe("onboarding progress rail", () => {
  // The rail replaced a row of dots plus a "Step 2 of 3" counter, which said
  // how much was left but never what was coming — so the runtime step always
  // arrived unannounced. Naming every step is the whole point of the change.
  it("names all three steps up front", () => {
    renderShell({ currentStep: "about_you" });

    expect(screen.getByText("About you")).toBeInTheDocument();
    expect(screen.getByText("Workspace")).toBeInTheDocument();
    expect(screen.getByText("Meet Mika")).toBeInTheDocument();
  });

  it("marks the current step for assistive tech", () => {
    const { container } = renderShell({ currentStep: "workspace" });

    const current = container.querySelector('[aria-current="step"]')!;
    expect(current).toBeInTheDocument();
    expect(current.textContent).toContain("Workspace");
  });

  // Forward navigation has to run the current step's validation and submit, so
  // only the steps already behind the member are reachable from the rail.
  it("links completed steps and leaves the current and later ones inert", async () => {
    const onStepChange = vi.fn();
    renderShell({ currentStep: "workspace", onStepChange });

    const back = screen.getByRole("button", { name: /about you/i });
    await userEvent.click(back);
    expect(onStepChange).toHaveBeenCalledWith("about_you");

    expect(screen.queryByRole("button", { name: /meet mika/i })).toBeNull();
    expect(screen.queryByRole("button", { name: /^workspace/i })).toBeNull();
  });

  it("is display-only when the flow supplies no step handler", () => {
    renderShell({ currentStep: "runtime" });

    expect(screen.queryByRole("button", { name: /about you/i })).toBeNull();
    expect(screen.queryByRole("button", { name: /^workspace/i })).toBeNull();
  });

  // Back is disabled precisely while a step has a request in flight; letting
  // the rail jump away would abandon it mid-create.
  it("stops rail navigation while the step reports work in flight", () => {
    renderShell({
      currentStep: "workspace",
      onStepChange: vi.fn(),
      backDisabled: true,
    });

    expect(screen.queryByRole("button", { name: /about you/i })).toBeNull();
  });
});
