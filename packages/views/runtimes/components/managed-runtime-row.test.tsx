// @vitest-environment jsdom

import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { I18nProvider } from "@multica/core/i18n/react";
import enAgents from "../../locales/en/agents.json";
import enRuntimes from "../../locales/en/runtimes.json";
import { pendingManagedRuntimeFromSetup } from "./managed-runtime-setup";

vi.mock("@tanstack/react-query", async (importOriginal) => {
  const actual = await importOriginal<
    typeof import("@tanstack/react-query")
  >();
  return {
    ...actual,
    useQuery: () => ({ data: [] }),
  };
});
vi.mock("@multica/core/auth", () => ({
  useAuthStore: (selector: (state: { user: { id: string } }) => unknown) =>
    selector({ user: { id: "user-1" } }),
}));
vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));
vi.mock("@multica/core/paths", () => ({
  useWorkspacePaths: () => ({ runtimeDetail: () => "/runtime" }),
}));
vi.mock("../../navigation", () => ({
  useRowLink: () => () => ({}),
}));
vi.mock("./provider-logo", () => ({ ProviderLogo: () => null }));
vi.mock("../../common/actor-avatar", () => ({ ActorAvatar: () => null }));

import { RuntimeList } from "./runtime-list";

const resources = {
  en: { agents: enAgents, runtimes: enRuntimes },
};

describe("managed runtime setup row", () => {
  it.each([
    ["installing", "Installing..."],
    ["ready", "Ready · waiting for daemon"],
    ["failed", "Installation failed"],
  ] as const)(
    "renders Pi with the %s status without making the row interactive",
    (phase, label) => {
      const runtime = pendingManagedRuntimeFromSetup({
        setup: {
          provider: "pi",
          phase,
          startedAt: "2026-08-04T00:00:00Z",
        },
        workspaceId: "ws-1",
        localMachineName: "MacBook",
      });

      render(
        <I18nProvider locale="en" resources={resources}>
          <RuntimeList runtimes={[runtime]} now={Date.now()} />
        </I18nProvider>,
      );

      expect(screen.getByText("Pi")).toBeInTheDocument();
      expect(screen.getByText(label)).toBeInTheDocument();
      expect(screen.queryByRole("link")).not.toBeInTheDocument();
    },
  );
});
