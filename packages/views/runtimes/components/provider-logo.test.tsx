import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { ProviderLogo } from "./provider-logo";

describe("ProviderLogo", () => {
  it("renders the official Reasonix logo", () => {
    const { container } = render(
      <ProviderLogo provider="reasonix" className="runtime-logo" />,
    );

    const logo = container.querySelector('img[alt="Reasonix"]');
    const logoSrc = logo?.getAttribute("src") ?? "";
    const logoSvg = atob(logoSrc.split(",")[1] ?? "");

    expect(logoSrc).toMatch(/^data:image\/svg\+xml;base64,/);
    expect(logoSvg).toContain('viewBox="0 0 64 64"');
    expect(logoSvg).toContain('stop-color="#4f9dff"');
    expect(logoSvg).toContain('stop-color="#c46bff"');
    expect(logo?.classList.contains("runtime-logo")).toBe(true);
  });

  it("renders the dedicated Qwen Code mark", () => {
    const { container } = render(<ProviderLogo provider="qwen" className="runtime-logo" />);

    const logo = container.querySelector('img[aria-hidden="true"]');
    const logoSrc = decodeURIComponent(logo?.getAttribute("src") ?? "");

    expect(logo?.getAttribute("alt")).toBe("");
    expect(logoSrc).toContain("viewBox='0 0 141.38 140'");
    expect(logoSrc).toContain("fill='#6D44E8'");
    expect(logo?.classList.contains("runtime-logo")).toBe(true);
  });
});
