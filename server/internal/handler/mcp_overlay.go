package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// mcpConfigDaemonOutdatedMessage is stored on the failed task AND returned on
// the per-runtime claim path, so the operator reads the same actionable text
// wherever they see the failure. Names both remedies: upgrading is the fix,
// inherit_runtime is the honest opt-out (it declares that the host's MCP servers
// are acceptable for this agent).
const mcpConfigDaemonOutdatedMessage = "this agent has a managed mcp_config, which requires a daemon that enforces it as an authoritative allowlist; upgrade the local daemon, or set runtime_config.mcp.inherit_runtime=true to accept the runtime host's MCP servers as well"

// providersOldDaemonsMergedRuntimeMcp is a FROZEN historical set: the providers
// that daemons WITHOUT DaemonCapabilityAuthoritativeMcpV1 actually merged host
// MCP servers for. It describes shipped behaviour of old binaries, so it is
// finished — do NOT keep it in sync with the daemon's current provider switch.
//
// Why the distinction matters. Runtime MCP discovery for a NEW provider can only
// ship in a daemon that already advertises the capability, so such a daemon is
// never gated in the first place. Adding that provider here would instead start
// failing tasks on old daemons that never merged for it — re-creating exactly
// the false-positive this list was introduced to fix (qwen was gated for a risk
// that did not exist). A provider belongs here only if some released,
// pre-capability daemon merged host MCP for it.
//
// Every provider in this set was present in loadRuntimeMcpServerConfigs at the
// time the capability shipped; qwen was deliberately absent and already had
// strict semantics.
var providersOldDaemonsMergedRuntimeMcp = map[string]bool{
	"claude":    true,
	"codebuddy": true,
	"codex":     true,
	"cursor":    true,
	"opencode":  true,
	"openclaw":  true,
}

// mcpConfigNeedsAuthoritativeDaemon reports whether handing this agent's
// mcp_config to a daemon that predates DaemonCapabilityAuthoritativeMcpV1 would
// widen the agent's tool surface beyond what the operator configured.
//
// True only when all of the following hold:
//
//   - The agent itself saved a managed config OBJECT. Only an object can carry
//     `mcpServers`, so a non-object (array or primitive) expresses no boundary.
//     An old daemon does not widen one either: mergeRuntimeAndAgentMcpConfig
//     fails to unmarshal it and falls back to the agent config alone.
//   - The agent has NOT opted into inheriting the host's servers via
//     runtime_config.mcp.inherit_runtime. That opt-in means the merge is exactly
//     what the operator asked for, so an old daemon already matches intent — and
//     it doubles as the documented escape hatch for an operator who cannot
//     upgrade the daemon yet.
//   - The runtime's provider is one an old daemon actually merged for. A stale
//     qwen daemon, for example, never merged host MCP, so failing its claims
//     would be a pure false positive.
//
// An unknown/empty provider is treated as NOT merging: the gate only fires where
// we can point at the concrete old behaviour it protects against.
func mcpConfigNeedsAuthoritativeDaemon(provider string, agentMcpConfig, runtimeConfig json.RawMessage) bool {
	if !providersOldDaemonsMergedRuntimeMcp[strings.TrimSpace(strings.ToLower(provider))] {
		return false
	}
	if !isManagedMcpConfigObject(agentMcpConfig) {
		return false
	}
	return !runtimeConfigInheritsRuntimeMcp(runtimeConfig)
}

// isManagedMcpConfigObject reports whether the raw value is a JSON object, the
// only shape that can express a managed MCP server set.
func isManagedMcpConfigObject(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && trimmed[0] == '{'
}

// runtimeConfigInheritsRuntimeMcp mirrors the daemon's
// decodeMcpInheritRuntime. Fails closed on malformed JSON: an unreadable
// runtime_config never counts as an opt-in to the wider tool surface.
func runtimeConfigInheritsRuntimeMcp(raw json.RawMessage) bool {
	if !hasManagedJSON(raw) {
		return false
	}
	var cfg struct {
		Mcp struct {
			InheritRuntime bool `json:"inherit_runtime"`
		} `json:"mcp"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return false
	}
	return cfg.Mcp.InheritRuntime
}

// resolveClaimMcpConfig builds the `mcp_config` a claim response carries and
// reports whether that payload came ONLY from the per-task overlay.
//
// The daemon treats a managed mcp_config as an authoritative allowlist and will
// not merge the host's own MCP servers into it (GitHub #6283). That decision
// needs one fact only the server has: whether the agent itself saved an
// mcp_config, or whether the field is populated purely because the initiator
// has an integration enabled. Without it, enabling a Composio integration on an
// agent that never configured MCP would silently strip the runtime servers it
// was already inheriting.
//
// On a malformed overlay the agent's own config is returned unchanged and
// overlayOnly is false, matching the pre-existing "never surprise-disable the
// agent's saved servers" failure mode. The error is returned for logging.
func resolveClaimMcpConfig(agentMcpConfig, overlay json.RawMessage) (json.RawMessage, bool, error) {
	agentAuthored := hasManagedJSON(agentMcpConfig)
	if !hasManagedJSON(overlay) {
		return passthroughAgentMcpConfig(agentMcpConfig), false, nil
	}
	merged, err := mergeMCPOverlay(agentMcpConfig, overlay)
	if err != nil {
		return passthroughAgentMcpConfig(agentMcpConfig), false, err
	}
	return merged, !agentAuthored && hasManagedJSON(merged), nil
}

// mergeMCPOverlay layers a per-task overlay on top of an agent's saved
// mcp_config and returns the merged JSON for the daemon claim wire shape.
//
// The merge contract (kept deliberately shallow):
//
//   - Both inputs are expected to be the Claude-style
//     `{"mcpServers": {<name>: <object>}}` shape every supported runtime
//     consumes via execenv's cursor / openclaw / opencode / codex / hermes
//     sidecar generators. Anything not under `mcpServers` is preserved from
//     the AGENT side only — overlays today only carry server entries and
//     should not silently introduce other top-level keys.
//
//   - Merge is by SERVER NAME (the inner-map key under `mcpServers`).
//     On a name collision the OVERLAY wins. This is on purpose: the overlay
//     carries the live, user-scoped session URL (e.g. the user's own
//     Composio MCP bearer), whereas the agent's saved entry under the same
//     name — if any — would be a stale or admin-shared placeholder.
//
//   - Either side may be empty / nil / the literal `null`. Empty-everywhere
//     returns `nil` so the daemon's `hasManagedCursorMcpConfig` short-circuit
//     keeps treating the task as "no managed MCP at all".
//
// Failure mode: on malformed input the agent config is returned unchanged
// (with the error). Callers must never silently drop the agent's saved
// servers because the overlay JSON was bad — that would surprise-disable
// existing MCP tools.
func mergeMCPOverlay(agentMcpConfig, overlay json.RawMessage) (json.RawMessage, error) {
	if !hasManagedJSON(overlay) {
		return passthroughAgentMcpConfig(agentMcpConfig), nil
	}
	if !hasManagedJSON(agentMcpConfig) {
		// Re-marshal the overlay alone so the daemon receives the exact
		// canonical shape (the input may have been stored with arbitrary
		// whitespace by Postgres' JSONB representation).
		var oCfg map[string]json.RawMessage
		if err := json.Unmarshal(overlay, &oCfg); err != nil {
			return nil, fmt.Errorf("merge mcp overlay: parse overlay: %w", err)
		}
		out, err := json.Marshal(oCfg)
		if err != nil {
			return nil, fmt.Errorf("merge mcp overlay: marshal overlay: %w", err)
		}
		return out, nil
	}

	var aCfg map[string]json.RawMessage
	if err := json.Unmarshal(agentMcpConfig, &aCfg); err != nil {
		// Agent config malformed: surface the error but return the original
		// bytes unchanged so the agent's setup behavior matches the no-overlay
		// path (which would also have shipped the bad bytes downstream).
		return passthroughAgentMcpConfig(agentMcpConfig), fmt.Errorf("merge mcp overlay: parse agent mcp_config: %w", err)
	}
	var oCfg map[string]json.RawMessage
	if err := json.Unmarshal(overlay, &oCfg); err != nil {
		return passthroughAgentMcpConfig(agentMcpConfig), fmt.Errorf("merge mcp overlay: parse overlay: %w", err)
	}

	// Pull each side's `mcpServers` sub-map, default to empty so a
	// well-formed top level with no servers is treated like absent.
	aServers, err := unmarshalServerMap(aCfg["mcpServers"])
	if err != nil {
		return passthroughAgentMcpConfig(agentMcpConfig), fmt.Errorf("merge mcp overlay: agent mcpServers: %w", err)
	}
	oServers, err := unmarshalServerMap(oCfg["mcpServers"])
	if err != nil {
		return passthroughAgentMcpConfig(agentMcpConfig), fmt.Errorf("merge mcp overlay: overlay mcpServers: %w", err)
	}

	merged := make(map[string]json.RawMessage, len(aServers)+len(oServers))
	for k, v := range aServers {
		merged[k] = v
	}
	// Overlay wins on collisions.
	for k, v := range oServers {
		merged[k] = v
	}

	// Rebuild: keep any non-mcpServers top-level keys from the agent config,
	// then write the merged mcpServers map back. This is the only place we
	// touch top-level keys; everything else is left alone.
	out := make(map[string]json.RawMessage, len(aCfg)+1)
	for k, v := range aCfg {
		if k == "mcpServers" {
			continue
		}
		out[k] = v
	}
	if len(merged) > 0 {
		serversBytes, err := json.Marshal(merged)
		if err != nil {
			return nil, fmt.Errorf("merge mcp overlay: marshal merged servers: %w", err)
		}
		out["mcpServers"] = serversBytes
	}
	if len(out) == 0 {
		return nil, nil
	}
	final, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("merge mcp overlay: marshal merged: %w", err)
	}
	return final, nil
}

// hasManagedJSON reports whether a raw JSON column carries an actual managed
// payload (non-empty and not the literal `null`). Matches the convention
// hasManagedCursorMcpConfig uses on the daemon side so the merge respects
// the same "absent" semantics every consumer already agrees on.
func hasManagedJSON(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
}

// passthroughAgentMcpConfig returns the agent config unchanged, or nil when
// it is absent. Used on the early-return paths so a caller assigning the
// return value into a wire field gets the same nil/non-nil split as today.
func passthroughAgentMcpConfig(agentMcpConfig json.RawMessage) json.RawMessage {
	if !hasManagedJSON(agentMcpConfig) {
		return nil
	}
	return agentMcpConfig
}

// unmarshalServerMap decodes the `mcpServers` sub-object into a map keyed by
// server name. A nil/absent value returns an empty map (not an error) so the
// caller can compose without an extra nil-check.
func unmarshalServerMap(raw json.RawMessage) (map[string]json.RawMessage, error) {
	if !hasManagedJSON(raw) {
		return map[string]json.RawMessage{}, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	if m == nil {
		return map[string]json.RawMessage{}, nil
	}
	// Reject non-object server entries early — every runtime expects the
	// inner value to be an object and would 500 in the sidecar generator
	// otherwise. Mirrors parseCursorManagedMcpServers' guard.
	for name, server := range m {
		if name == "" {
			return nil, errors.New("mcp server name must not be empty")
		}
		trimmed := bytes.TrimSpace(server)
		if len(trimmed) == 0 || trimmed[0] != '{' {
			return nil, fmt.Errorf("mcpServers.%s must be a JSON object", name)
		}
	}
	return m, nil
}
