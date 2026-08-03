package daemon

import (
	"bytes"
	"encoding/json"
	"log/slog"
)

// hasManagedJSONPayload reports whether a raw JSON field carries an actual
// payload rather than being absent or the literal `null`. Mirrors the
// "absent" convention mergeRuntimeAndAgentMcpConfig and
// agent.hasManagedMcpConfig already use.
func hasManagedJSONPayload(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
}

// mcpRuntimeConfig is the agent-level knob that re-enables inheriting the
// host's own MCP servers on top of a managed `mcp_config`.
//
// It lives under `runtime_config.mcp` rather than inside `mcp_config` itself so
// the MCP document the daemon hands to a provider stays a pure MCP config —
// an extra sibling key there would leak into the generated Claude/Codex config
// file. `runtime_config` is already the established home for per-agent knobs
// the daemon decodes itself (see decodeOpenclawRuntimeConfig).
type mcpRuntimeConfig struct {
	Mcp struct {
		// InheritRuntime opts back in to the additive behavior #5277
		// shipped: the host's user-level MCP servers are merged underneath
		// the agent's managed servers. Default false — a managed config is
		// an authoritative allowlist (see resolveEffectiveMcpConfig).
		InheritRuntime bool `json:"inherit_runtime"`
	} `json:"mcp"`
}

// decodeMcpInheritRuntime reports whether the agent explicitly opted into
// inheriting the runtime's own MCP servers.
//
// Fails CLOSED: absent, empty, or malformed `runtime_config` yields false, so a
// broken JSON blob can never silently widen the agent's tool surface.
func decodeMcpInheritRuntime(raw json.RawMessage, logger *slog.Logger) bool {
	if !hasManagedJSONPayload(raw) {
		return false
	}
	var cfg mcpRuntimeConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		if logger != nil {
			logger.Warn("mcp_config: runtime_config parse failed; not inheriting runtime MCP servers",
				"error", err,
			)
		}
		return false
	}
	return cfg.Mcp.InheritRuntime
}

// resolveEffectiveMcpConfig decides which MCP servers a task may actually see.
//
// This is the access-control decision for MCP tools, so the three states of
// `mcp_config` must stay distinguishable (GitHub #6283):
//
//   - null / unset          → inherit the provider's native MCP configuration.
//     Returned as-is so the backend omits --mcp-config
//     and Claude reads the user's own servers.
//   - `{"mcpServers":{}}`   → strict empty. No host servers, no tools.
//   - non-empty object      → strict allowlist. Exactly those servers.
//
// Before this, ANY non-null config was merged with the host's user-level MCP
// servers, so an explicitly empty config produced the *complete host set* —
// the opposite of what the operator configured.
//
// Two escape hatches keep the previous behavior reachable without weakening
// the default:
//
//   - overlayOnly: the config the server sent is purely a per-task integration
//     overlay (Composio) and the agent itself has no saved mcp_config. That
//     agent was inheriting host MCP before the overlay existed, so enabling an
//     integration must not silently strip its tools.
//   - runtime_config.mcp.inherit_runtime: an explicit, per-agent opt-in to the
//     additive behavior.
func resolveEffectiveMcpConfig(
	provider string,
	agentMcpConfig json.RawMessage,
	overlayOnly bool,
	runtimeConfig json.RawMessage,
	logger *slog.Logger,
) json.RawMessage {
	// A managed config is authoritative unless the agent (or the overlay-only
	// carve-out) explicitly asks to inherit the host's servers.
	if !overlayOnly && !decodeMcpInheritRuntime(runtimeConfig, logger) {
		return agentMcpConfig
	}
	merged, err := mergeRuntimeAndAgentMcpConfig(provider, agentMcpConfig)
	if err != nil {
		if logger != nil {
			logger.Warn("mcp_config: runtime merge failed; using agent configuration only",
				"provider", provider,
				"error", err,
			)
		}
		// Fail closed: the agent's own config only.
		return agentMcpConfig
	}
	return merged
}
