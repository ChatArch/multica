// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { AgentRuntime } from "@multica/core/types";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales/en/common.json";
import enRuntimes from "../../locales/en/runtimes.json";

const TEST_RESOURCES = {
  en: { common: enCommon, runtimes: enRuntimes },
};

const mutate = vi.hoisted(() => vi.fn());

vi.mock("@multica/core/runtimes/mutations", () => ({
  useUpdateRuntimeModelConnection: () => ({
    mutate,
    isPending: false,
  }),
}));

vi.mock("sonner", () => ({
  toast: { error: vi.fn(), success: vi.fn() },
}));

import { PiModelConnectionCard } from "./pi-model-connection-card";

const runtime: AgentRuntime = {
  id: "runtime-1",
  workspace_id: "workspace-1",
  daemon_id: "daemon-1",
  name: "Pi",
  runtime_mode: "local",
  provider: "pi",
  launch_header: "",
  status: "online",
  device_info: "host",
  metadata: {},
  default_model_config: {},
  has_default_model_api_key: false,
  owner_id: "user-1",
  visibility: "private",
  last_seen_at: "2026-08-04T00:00:00Z",
  created_at: "2026-08-04T00:00:00Z",
  updated_at: "2026-08-04T00:00:00Z",
};

function renderCard(value: AgentRuntime = runtime) {
  render(
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      <PiModelConnectionCard
        runtime={value}
        wsId="workspace-1"
        canEdit
      />
    </I18nProvider>,
  );
}

describe("PiModelConnectionCard", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("turns an unconfigured runtime into a DeepSeek connection", async () => {
    const user = userEvent.setup();
    renderCard();

    expect(screen.getByText("Needs model setup")).toBeInTheDocument();
    await user.click(
      screen.getByRole("button", { name: "Configure model" }),
    );

    const key = screen.getByLabelText("API key");
    expect(screen.getByLabelText("Model provider")).toHaveValue("deepseek");
    expect(screen.getByLabelText("Model")).toHaveValue(
      "deepseek-v4-flash",
    );
    expect(
      screen.getByRole("button", { name: "Save connection" }),
    ).toBeDisabled();

    await user.type(key, "sk-deepseek");
    await user.click(
      screen.getByRole("button", { name: "Save connection" }),
    );

    expect(mutate).toHaveBeenCalledWith(
      {
        runtimeId: "runtime-1",
        connection: {
          provider: "deepseek",
          api: "openai-completions",
          base_url: "https://api.deepseek.com",
          model: "deepseek-v4-flash",
          api_key: "sk-deepseek",
        },
      },
      expect.any(Object),
    );
  });

  it("shows the configured model without exposing a key", () => {
    renderCard({
      ...runtime,
      default_model_config: {
        provider: "deepseek",
        api: "openai-completions",
        base_url: "https://api.deepseek.com",
        model: "deepseek-v4-pro",
      },
      has_default_model_api_key: true,
    });

    expect(screen.getByText("Ready")).toBeInTheDocument();
    expect(screen.getByText("deepseek-v4-pro")).toBeInTheDocument();
    expect(screen.queryByText(/sk-/)).not.toBeInTheDocument();
  });
});
