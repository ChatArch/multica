// @vitest-environment jsdom

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, fireEvent, cleanup, screen } from "@testing-library/react";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales/en/common.json";
import enRuntimes from "../../locales/en/runtimes.json";

// The legacy Fleet node dialog is a separate surface with its own queries; the
// card only needs to be able to open it.
vi.mock("./cloud-runtime-dialog", () => ({
  CloudRuntimeDialog: () => <div data-testid="node-inspector" />,
}));

import { CloudCapabilityCard } from "./cloud-capability-card";
import { resetCloudPreview } from "./cloud-preview";

const TEST_RESOURCES = { en: { common: enCommon, runtimes: enRuntimes } };

function renderCard(
  props: Partial<React.ComponentProps<typeof CloudCapabilityCard>> = {},
) {
  return render(
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      <CloudCapabilityCard {...props} />
    </I18nProvider>,
  );
}

describe("CloudCapabilityCard (MUL-5385 preview)", () => {
  beforeEach(() => {
    cleanup();
    resetCloudPreview();
  });
  afterEach(() => {
    cleanup();
    resetCloudPreview();
  });

  it("offers a single switch instead of a machine form", () => {
    renderCard();
    expect(
      screen.getByRole("button", { name: /Turn on Multica Cloud/ }),
    ).toBeInTheDocument();
    // No capacity numbers before it is on.
    expect(screen.queryByText(/Credits left/)).toBeNull();
  });

  it("shows balance, concurrency and a speed tier once turned on", () => {
    renderCard();
    fireEvent.click(
      screen.getByRole("button", { name: /Turn on Multica Cloud/ }),
    );
    expect(screen.getByText(/Credits left/)).toBeInTheDocument();
    expect(screen.getByText(/Running now/)).toBeInTheDocument();
    expect(
      (screen.getByRole("radio", { name: /Standard/ }) as HTMLInputElement)
        .checked,
    ).toBe(true);
    fireEvent.click(screen.getByRole("radio", { name: /Fast/ }));
    expect(
      (screen.getByRole("radio", { name: /Fast/ }) as HTMLInputElement).checked,
    ).toBe(true);
  });

  /**
   * The product invariant of this redesign: the Cloud entry point never speaks
   * IaaS. If someone reintroduces an instance type, disk size or region here,
   * this test is the tripwire.
   */
  it("never exposes instance type, disk size or region vocabulary", () => {
    const { container } = renderCard();
    fireEvent.click(
      screen.getByRole("button", { name: /Turn on Multica Cloud/ }),
    );
    const text = container.textContent ?? "";
    for (const banned of [
      "instance",
      "t4g",
      "disk",
      "GB",
      "region",
      "EC2",
      "node",
    ]) {
      expect(text.toLowerCase()).not.toContain(banned.toLowerCase());
    }
  });

  it("keeps the raw node list reachable behind Advanced", () => {
    renderCard({ nodeInspectorAvailable: true });
    fireEvent.click(screen.getByRole("button", { name: /Advanced/ }));
    expect(screen.getByTestId("node-inspector")).toBeInTheDocument();
  });

  it("hides Advanced when the node API is not available", () => {
    renderCard();
    expect(screen.queryByRole("button", { name: /Advanced/ })).toBeNull();
  });
});
