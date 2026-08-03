import { describe, expect, it } from "vitest";
import {
  clearManagedMcpConfig,
  hasManagedMcpConfig,
  inheritsRuntimeMcp,
  listManagedMcpServers,
  removeManagedMcpServer,
  upsertManagedMcpServer,
} from "./mcp-config-model";

describe("mcp config compatibility model", () => {
  it("reads canonical and historical OpenCode-native containers without duplication", () => {
    const servers = listManagedMcpServers({
      mcpServers: { shared: { command: "canonical" } },
      mcp: {
        shared: { type: "local", command: ["native"] },
        legacy: { type: "remote", url: "https://example.test/mcp" },
      },
    });

    expect(servers.map(({ name, container }) => ({ name, container }))).toEqual([
      { name: "legacy", container: "mcp" },
      { name: "shared", container: "mcpServers" },
    ]);
  });

  it("updates a single legacy entry while preserving unknown document fields", () => {
    const value = {
      provider: "opencode",
      mcp: {
        legacy: { type: "remote", url: "https://old.test/mcp" },
        sibling: { type: "local", command: ["node", "server.js"] },
      },
    };
    const legacy = listManagedMcpServers(value).find(
      (server) => server.name === "legacy",
    );
    expect(legacy).toBeDefined();

    expect(
      upsertManagedMcpServer(
        value,
        legacy!,
        "legacy",
        { type: "remote", url: "https://new.test/mcp" },
      ),
    ).toEqual({
      provider: "opencode",
      mcp: {
        legacy: { type: "remote", url: "https://new.test/mcp" },
        sibling: { type: "local", command: ["node", "server.js"] },
      },
    });
  });

  // Deleting the last managed server must NOT clear the config to null: null
  // means "inherit the runtime host's MCP servers" on the daemon side, so
  // collapsing to it would turn a delete into a privilege widening — from one
  // allowed server to every server on the host (GitHub #6283).
  it("keeps an explicitly empty config when the last server is deleted", () => {
    const value = { mcpServers: { fetch: { command: "uvx" } } };
    const [fetch] = listManagedMcpServers(value);
    expect(removeManagedMcpServer(value, fetch!)).toEqual({ mcpServers: {} });

    const withMetadata = { version: 1, ...value };
    const [withMetadataFetch] = listManagedMcpServers(withMetadata);
    expect(removeManagedMcpServer(withMetadata, withMetadataFetch!)).toEqual({
      version: 1,
      mcpServers: {},
    });
  });

  it("removes only the named server when siblings remain", () => {
    const value = {
      mcpServers: { fetch: { command: "uvx" }, docs: { url: "https://x/mcp" } },
    };
    const fetch = listManagedMcpServers(value).find((s) => s.name === "fetch");
    expect(removeManagedMcpServer(value, fetch!)).toEqual({
      mcpServers: { docs: { url: "https://x/mcp" } },
    });
  });

  // Restoring inheritance is a separate, deliberate action.
  it("clearManagedMcpConfig is the only path back to inheritance", () => {
    expect(clearManagedMcpConfig()).toBeNull();
  });
});

// A managed mcp_config is an authoritative allowlist on the daemon side
// (GitHub #6283), so the tab has to know which mode an agent is in before it
// can describe the runtime servers honestly.
describe("hasManagedMcpConfig", () => {
  it("treats an absent config as unmanaged", () => {
    expect(hasManagedMcpConfig(null)).toBe(false);
    expect(hasManagedMcpConfig(undefined)).toBe(false);
  });

  it("treats an explicitly empty object as managed", () => {
    expect(hasManagedMcpConfig({})).toBe(true);
    expect(hasManagedMcpConfig({ mcpServers: {} })).toBe(true);
  });

  it("treats a redacted config as managed even though it is unreadable", () => {
    expect(hasManagedMcpConfig(null, true)).toBe(true);
  });
});

describe("inheritsRuntimeMcp", () => {
  it("inherits when Multica manages nothing", () => {
    expect(inheritsRuntimeMcp(null, false, undefined)).toBe(true);
    expect(inheritsRuntimeMcp(null, false, {})).toBe(true);
  });

  it("does not inherit for an explicitly empty managed config", () => {
    expect(inheritsRuntimeMcp({ mcpServers: {} }, false, undefined)).toBe(
      false,
    );
  });

  it("does not inherit for a managed allowlist", () => {
    expect(
      inheritsRuntimeMcp(
        { mcpServers: { a: { command: "a" } } },
        false,
        undefined,
      ),
    ).toBe(false);
  });

  it("does not inherit for a redacted config", () => {
    expect(inheritsRuntimeMcp(null, true, undefined)).toBe(false);
  });

  it("inherits again when the agent explicitly opts back in", () => {
    expect(
      inheritsRuntimeMcp({ mcpServers: {} }, false, {
        mcp: { inherit_runtime: true },
      }),
    ).toBe(true);
  });

  it("stays strict for a falsy or malformed opt-in", () => {
    expect(
      inheritsRuntimeMcp({ mcpServers: {} }, false, {
        mcp: { inherit_runtime: false },
      }),
    ).toBe(false);
    expect(
      inheritsRuntimeMcp({ mcpServers: {} }, false, {
        mcp: { inherit_runtime: "true" },
      }),
    ).toBe(false);
    expect(inheritsRuntimeMcp({ mcpServers: {} }, false, { mcp: true })).toBe(
      false,
    );
  });
});
