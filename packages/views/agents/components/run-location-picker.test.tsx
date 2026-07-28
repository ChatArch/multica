// @vitest-environment jsdom

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, fireEvent, cleanup, screen } from "@testing-library/react";
import type { MemberWithUser, RuntimeDevice } from "@multica/core/types";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales/en/common.json";
import enAgents from "../../locales/en/agents.json";
import enIssues from "../../locales/en/issues.json";

vi.mock("../../common/actor-avatar", () => ({
  ActorAvatar: () => null,
}));

vi.mock("../../runtimes/components/provider-logo", () => ({
  ProviderLogo: () => null,
}));

import { RuntimePicker } from "./runtime-picker";
import { resetCloudPreview } from "../../runtimes/components/cloud-preview";

const TEST_RESOURCES = {
  en: { common: enCommon, agents: enAgents, issues: enIssues },
};

const ME = "user-me";
const MEMBERS = [
  { user_id: ME, name: "Me", role: "member" },
] as unknown as MemberWithUser[];

function makeRuntime(overrides: Partial<RuntimeDevice>): RuntimeDevice {
  return {
    id: "rt",
    workspace_id: "ws-1",
    daemon_id: "daemon-1",
    name: "Claude (host.local)",
    runtime_mode: "local",
    provider: "claude",
    launch_header: "",
    status: "online",
    device_info: "host.local · macOS (arm64)",
    metadata: {},
    owner_id: ME,
    visibility: "private",
    last_seen_at: "2026-07-11T00:00:00Z",
    created_at: "2026-07-01T00:00:00Z",
    updated_at: "2026-07-01T00:00:00Z",
    ...overrides,
  } as RuntimeDevice;
}

const LOCAL_RUNTIMES = [makeRuntime({ id: "rt-a", name: "Claude (a.local)" })];
const CLOUD_RUNTIME = makeRuntime({
  id: "rt-cloud",
  name: "Multica Cloud",
  runtime_mode: "cloud",
  daemon_id: null as unknown as string,
});

function renderPicker(
  props: Partial<React.ComponentProps<typeof RuntimePicker>> = {},
) {
  const onSelect = vi.fn();
  const utils = render(
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      <RuntimePicker
        runtimes={LOCAL_RUNTIMES}
        members={MEMBERS}
        currentUserId={ME}
        selectedRuntimeId="rt-a"
        onSelect={onSelect}
        {...props}
      />
    </I18nProvider>,
  );
  return { ...utils, onSelect };
}

describe("RuntimePicker run-location entry (MUL-5385)", () => {
  beforeEach(() => {
    cleanup();
    resetCloudPreview();
  });
  afterEach(() => {
    cleanup();
    resetCloudPreview();
  });

  it("asks where the agent should run", () => {
    renderPicker();
    expect(
      screen.getByRole("radio", { name: /Multica Cloud/ }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("radio", { name: /My computer/ }),
    ).toBeInTheDocument();
  });

  // No capability wired and no managed runtime in the workspace: Cloud must
  // clear the selection (so the caller's Create stays disabled) and say why,
  // rather than pretend it can run.
  it("clears the selection and explains itself when Cloud has no runtime", () => {
    const { onSelect } = renderPicker();
    fireEvent.click(screen.getByRole("radio", { name: /Multica Cloud/ }));
    expect(onSelect).toHaveBeenCalledWith("");
    expect(screen.getByRole("note")).toHaveTextContent(/Preview only/i);
  });

  // When the workspace does have a managed cloud runtime, Cloud is the default
  // and binds to it without the user ever seeing a machine.
  it("defaults to Cloud and binds to the managed runtime when one exists", () => {
    const { onSelect } = renderPicker({
      runtimes: [...LOCAL_RUNTIMES, CLOUD_RUNTIME],
      selectedRuntimeId: "",
    });
    expect(
      (screen.getByRole("radio", { name: /Multica Cloud/ }) as HTMLInputElement)
        .checked,
    ).toBe(true);
    expect(onSelect).toHaveBeenCalledWith("rt-cloud");
    expect(screen.queryByRole("note")).toBeNull();
  });

  // Escape hatch: picking "My computer" must reveal the untouched machine ×
  // CLI picker and reset the selection so its own seeding effect runs.
  it("reveals the machine picker under My computer", () => {
    const { container, onSelect } = renderPicker({
      runtimes: [...LOCAL_RUNTIMES, CLOUD_RUNTIME],
      selectedRuntimeId: "rt-cloud",
    });
    fireEvent.click(screen.getByRole("radio", { name: /My computer/ }));
    expect(onSelect).toHaveBeenCalledWith("");
    expect(
      container.querySelector('[data-slot="popover-trigger"]'),
    ).not.toBeNull();
  });
});
