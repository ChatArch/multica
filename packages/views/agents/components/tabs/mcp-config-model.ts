export type McpConfigContainer = "mcpServers" | "mcp";

export type ManagedMcpServer = {
  name: string;
  config: Record<string, unknown>;
  container: McpConfigContainer;
  transport: string;
  enabled: boolean;
};

export function isRecord(value: unknown): value is Record<string, unknown> {
  return !!value && typeof value === "object" && !Array.isArray(value);
}

export function mcpTransport(config: Record<string, unknown>): string {
  const type = typeof config.type === "string" ? config.type.toLowerCase() : "";
  if (config.command || type === "local" || type === "stdio") return "stdio";
  if (type === "sse") return "sse";
  if (
    config.url ||
    type === "remote" ||
    type === "http" ||
    type === "streamable-http"
  ) {
    return "http";
  }
  return "unknown";
}

export function listManagedMcpServers(value: unknown): ManagedMcpServer[] {
  if (!isRecord(value)) return [];

  const out: ManagedMcpServer[] = [];
  const seen = new Set<string>();
  for (const container of ["mcpServers", "mcp"] as const) {
    const raw = value[container];
    if (!isRecord(raw)) continue;
    for (const [name, entry] of Object.entries(raw)) {
      if (seen.has(name) || !isRecord(entry)) continue;
      seen.add(name);
      out.push({
        name,
        config: entry,
        container,
        transport: mcpTransport(entry),
        enabled: entry.enabled !== false && entry.disabled !== true,
      });
    }
  }
  return out.sort((a, b) => a.name.localeCompare(b.name));
}

export function upsertManagedMcpServer(
  value: unknown,
  previous: ManagedMcpServer | null,
  name: string,
  config: Record<string, unknown>,
): Record<string, unknown> {
  const document = isRecord(value) ? { ...value } : {};
  const container: McpConfigContainer = previous?.container ?? "mcpServers";

  if (previous) {
    const previousMap = isRecord(document[previous.container])
      ? { ...(document[previous.container] as Record<string, unknown>) }
      : {};
    delete previousMap[previous.name];
    document[previous.container] = previousMap;
  }

  const target = isRecord(document[container])
    ? { ...(document[container] as Record<string, unknown>) }
    : {};
  target[name] = config;
  document[container] = target;
  return document;
}

export function removeManagedMcpServer(
  value: unknown,
  server: ManagedMcpServer,
): Record<string, unknown> | null {
  if (!isRecord(value)) return null;
  const document = { ...value };
  const container = isRecord(document[server.container])
    ? { ...(document[server.container] as Record<string, unknown>) }
    : {};
  delete container[server.name];

  if (Object.keys(container).length > 0) document[server.container] = container;
  else delete document[server.container];

  return Object.keys(document).length > 0 ? document : null;
}

/**
 * True when Multica manages an MCP config for this agent — including an
 * explicitly empty one, and including a config the viewer isn't allowed to
 * read. A managed config is an authoritative allowlist on the daemon side
 * (GitHub #6283), so this is what decides whether runtime servers reach the
 * agent at all.
 */
export function hasManagedMcpConfig(
  mcpConfig: unknown,
  redacted?: boolean,
): boolean {
  if (redacted === true) return true;
  return isRecord(mcpConfig);
}

/**
 * True when the local runtime's own MCP servers are still exposed to this
 * agent. That happens when Multica manages no config for it, or when the agent
 * explicitly opted back in via `runtime_config.mcp.inherit_runtime`.
 */
export function inheritsRuntimeMcp(
  mcpConfig: unknown,
  redacted: boolean | undefined,
  runtimeConfig: Record<string, unknown> | undefined,
): boolean {
  if (!hasManagedMcpConfig(mcpConfig, redacted)) return true;
  const mcp = runtimeConfig?.mcp;
  return isRecord(mcp) && mcp.inherit_runtime === true;
}
