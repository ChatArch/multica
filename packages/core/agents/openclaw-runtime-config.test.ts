import { describe, expect, it } from "vitest";
import {
  OPENCLAW_GATEWAY_TOKEN_MASK,
  openclawRuntimeConfigEquals,
  parseOpenclawRuntimeConfig,
  serializeOpenclawRuntimeConfig,
} from "./openclaw-runtime-config";

describe("serializeOpenclawRuntimeConfig", () => {
  it("keeps the masked gateway token sentinel so the API can preserve the persisted token", () => {
    expect(
      serializeOpenclawRuntimeConfig({
        mode: "gateway",
        gateway: {
          host: "gw.internal",
          port: 18789,
          token: OPENCLAW_GATEWAY_TOKEN_MASK,
          tls: true,
        },
      }),
    ).toEqual({
      mode: "gateway",
      gateway: {
        host: "gw.internal",
        port: 18789,
        token: OPENCLAW_GATEWAY_TOKEN_MASK,
        tls: true,
      },
    });
  });

  it("omits an empty gateway token so users can clear a persisted token", () => {
    expect(
      serializeOpenclawRuntimeConfig({
        mode: "gateway",
        gateway: {
          host: "gw.internal",
          port: 18789,
        },
      }),
    ).toEqual({
      mode: "gateway",
      gateway: {
        host: "gw.internal",
        port: 18789,
      },
    });
  });

  it("passes through a real gateway token value", () => {
    expect(
      serializeOpenclawRuntimeConfig({
        mode: "gateway",
        gateway: {
          token: "rotated-secret",
        },
      }),
    ).toEqual({
      mode: "gateway",
      gateway: {
        token: "rotated-secret",
      },
    });
  });
});

// `runtime_config` is one shared JSONB column and the OpenClaw tab persists its
// serialized output as the WHOLE object. Dropping unknown keys therefore let an
// unrelated routing save silently delete `mcp.inherit_runtime`, which controls
// whether the agent may reach the host's MCP servers (GitHub #6283).
describe("openclaw runtime_config passthrough", () => {
  it("preserves keys owned by other tabs across a parse/serialize round-trip", () => {
    const stored = {
      mode: "gateway",
      gateway: { host: "box.local", port: 4599 },
      mcp: { inherit_runtime: true },
    };

    const parsed = parseOpenclawRuntimeConfig(stored);
    expect(parsed.passthrough).toEqual({ mcp: { inherit_runtime: true } });

    expect(serializeOpenclawRuntimeConfig(parsed)).toEqual(stored);
  });

  it("keeps the foreign keys when the OpenClaw settings themselves change", () => {
    const parsed = parseOpenclawRuntimeConfig({
      mode: "gateway",
      gateway: { host: "old.local" },
      mcp: { inherit_runtime: true },
    });

    // Simulate the form switching back to local mode and saving.
    const saved = serializeOpenclawRuntimeConfig({
      mode: "local",
      passthrough: parsed.passthrough,
    });

    expect(saved).toEqual({ mode: "local", mcp: { inherit_runtime: true } });
  });

  it("never lets a foreign key overwrite the fields this form owns", () => {
    const saved = serializeOpenclawRuntimeConfig({
      mode: "local",
      passthrough: { mode: "gateway", mcp: { inherit_runtime: false } },
    });

    expect(saved.mode).toBe("local");
    expect(saved.mcp).toEqual({ inherit_runtime: false });
  });

  it("omits passthrough entirely when there are no foreign keys", () => {
    const parsed = parseOpenclawRuntimeConfig({ mode: "local" });
    expect(parsed.passthrough).toBeUndefined();
    expect(serializeOpenclawRuntimeConfig(parsed)).toEqual({ mode: "local" });
  });

  it("does not report a config as dirty because of passthrough alone", () => {
    expect(
      openclawRuntimeConfigEquals(
        { mode: "local", passthrough: { mcp: { inherit_runtime: true } } },
        { mode: "local" },
      ),
    ).toBe(true);
  });
});
