import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales/en/common.json";
import enOnboarding from "../../locales/en/onboarding.json";
import {
  STEP_BLOCK_PADDING,
  STEP_COLUMN,
  STEP_FRAME,
  STEP_GUTTER,
  STEP_MEASURE,
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

  it("centres both measures so content stays on one centreline", () => {
    for (const measure of [STEP_FRAME, STEP_COLUMN]) {
      expect(measure).toContain("mx-auto");
      expect(measure).toMatch(/max-w-\[\d+px\]/);
      expect(measure).not.toMatch(/\bp[xlr]?-/);
    }
    expect(STEP_BLOCK_PADDING).toMatch(/^py-/);
  });

  // STEP_MEASURE caps prose and form fields inside a step that sits on the
  // frame. It must NOT centre: centring it would pull the content off the
  // frame's left edge, which is the alignment the frame exists to provide.
  it("keeps the in-frame reading measure left-aligned and padding-free", () => {
    expect(STEP_MEASURE).toMatch(/max-w-\[\d+px\]/);
    expect(STEP_MEASURE).not.toContain("mx-auto");
    expect(STEP_MEASURE).not.toMatch(/\bp[xlr]?-/);
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
