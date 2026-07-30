// @vitest-environment jsdom

import { cleanup, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { WorkingAgentSummary } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";

const mockState = vi.hoisted(() => ({
  avatarAgentIds: undefined as readonly string[] | undefined,
  buttonVariant: undefined as string | undefined,
}));

vi.mock("@multica/core/workspace/hooks", () => ({
  useActorName: () => ({
    getActorName: (_type: string, id: string) => `Agent ${id}`,
    getActorInitials: () => "AG",
    getActorAvatarUrl: () => null,
  }),
}));

vi.mock("../../agents/components/agent-avatar-stack", () => ({
  AgentAvatarStack: ({ agentIds }: { agentIds: readonly string[] }) => {
    mockState.avatarAgentIds = agentIds;
    return <div data-testid="agent-avatar-stack">{agentIds.length}</div>;
  },
}));

vi.mock("@multica/ui/components/ui/button", async () => {
  const actual =
    await vi.importActual<typeof import("@multica/ui/components/ui/button")>(
      "@multica/ui/components/ui/button",
    );
  return {
    ...actual,
    Button: (props: React.ComponentProps<typeof actual.Button>) => {
      mockState.buttonVariant = props.variant ?? undefined;
      return <actual.Button {...props} />;
    },
  };
});

import {
  WorkspaceAgentWorkingChip,
  chipAppearance,
} from "./workspace-agent-working-chip";

function makeAgent(id: string, runningTaskCount = 1): WorkingAgentSummary {
  return { id, running_task_count: runningTaskCount };
}

beforeEach(() => {
  cleanup();
  vi.clearAllMocks();
  mockState.avatarAgentIds = undefined;
  mockState.buttonVariant = undefined;
});

describe("WorkspaceAgentWorkingChip", () => {
  it("counts exactly the agents the surface projection supplies", () => {
    renderWithI18n(
      <WorkspaceAgentWorkingChip
        value={false}
        onToggle={() => {}}
        agents={[makeAgent("agent-1"), makeAgent("agent-2", 3), makeAgent("agent-3")]}
      />,
    );

    expect(
      screen.getByRole("button", { name: "3 agents working" }),
    ).toBeTruthy();
    expect(mockState.avatarAgentIds).toEqual([
      "agent-1",
      "agent-2",
      "agent-3",
    ]);
    expect(mockState.buttonVariant).toBe("brandSubtle");
  });

  // The whole point of MUL-5525: the chip must not invent a count of its own.
  // A surface whose filters leave no working rows has to read zero even while
  // other agents are busy elsewhere in the workspace.
  it("shows a known zero for a surface with no working rows", () => {
    renderWithI18n(
      <WorkspaceAgentWorkingChip value={false} onToggle={() => {}} agents={[]} />,
    );

    expect(
      screen.getByRole("button", { name: "0 agents working" }),
    ).toBeTruthy();
    expect(screen.queryByTestId("agent-avatar-stack")).toBeNull();
    expect(mockState.buttonVariant).toBe("outline");
  });

  it("renders an indeterminate label while the projection is unresolved", () => {
    renderWithI18n(
      <WorkspaceAgentWorkingChip
        value={false}
        onToggle={() => {}}
        agents={undefined}
      />,
    );

    expect(
      screen.getByRole("button", { name: "Agents working: —" }),
    ).toBeTruthy();
    expect(screen.queryByTestId("agent-avatar-stack")).toBeNull();
  });

  it("keeps the active filter visually selected after the final agent stops", () => {
    renderWithI18n(
      <WorkspaceAgentWorkingChip value onToggle={() => {}} agents={[]} />,
    );

    expect(mockState.buttonVariant).toBe("brand");
  });
});

describe("chipAppearance", () => {
  it("wears the filled brand tier while the filter is on", () => {
    expect(chipAppearance(true, true).variant).toBe("brand");
  });

  it("wears the tint tier for activity without the filter", () => {
    expect(chipAppearance(false, true).variant).toBe("brandSubtle");
  });

  it("wears the plain tier with muted text when nothing is running", () => {
    const appearance = chipAppearance(false, false);
    expect(appearance.variant).toBe("outline");
    expect(appearance.className).toContain("text-muted-foreground");
  });

  it("does not mute the active zero state", () => {
    const appearance = chipAppearance(true, false);
    expect(appearance.variant).toBe("brand");
    expect(appearance.className).not.toContain("text-muted-foreground");
  });
});
