package handler

import (
	"encoding/json"
	"testing"
)

// The claim response must tell the daemon whether mcp_config is the agent's own
// managed allowlist or purely the per-task integration overlay. The daemon uses
// that to keep MCP access control fail-closed without stripping runtime servers
// from agents that never configured MCP (GitHub #6283).

func TestResolveClaimMcpConfigNoOverlayIsNeverOverlayOnly(t *testing.T) {
	t.Parallel()

	for _, agentCfg := range []json.RawMessage{
		nil,
		json.RawMessage("null"),
		json.RawMessage(`{"mcpServers":{}}`),
		json.RawMessage(`{"mcpServers":{"a":{"command":"a"}}}`),
	} {
		got, overlayOnly, err := resolveClaimMcpConfig(agentCfg, nil)
		if err != nil {
			t.Fatalf("agent %q: %v", string(agentCfg), err)
		}
		if overlayOnly {
			t.Fatalf("agent %q with no overlay must not be flagged overlay-only", string(agentCfg))
		}
		if want := string(passthroughAgentMcpConfig(agentCfg)); string(got) != want {
			t.Fatalf("agent %q: config = %q, want %q", string(agentCfg), string(got), want)
		}
	}
}

func TestResolveClaimMcpConfigOverlayOnlyWhenAgentHasNoConfig(t *testing.T) {
	t.Parallel()

	overlay := json.RawMessage(`{"mcpServers":{"composio":{"url":"https://composio.example/mcp"}}}`)
	for _, agentCfg := range []json.RawMessage{nil, json.RawMessage("null")} {
		got, overlayOnly, err := resolveClaimMcpConfig(agentCfg, overlay)
		if err != nil {
			t.Fatalf("agent %q: %v", string(agentCfg), err)
		}
		if !overlayOnly {
			t.Fatalf("agent %q + overlay must be flagged overlay-only, got config %q", string(agentCfg), string(got))
		}
		if !hasManagedJSON(got) {
			t.Fatalf("agent %q + overlay must yield a managed config, got %q", string(agentCfg), string(got))
		}
	}
}

// An explicitly empty agent config is a deliberate "no MCP servers" decision,
// so it counts as agent-authored: the daemon must treat the result as strict
// and must not fold the host's servers back in.
func TestResolveClaimMcpConfigExplicitEmptyAgentConfigIsAuthored(t *testing.T) {
	t.Parallel()

	overlay := json.RawMessage(`{"mcpServers":{"composio":{"url":"https://composio.example/mcp"}}}`)
	got, overlayOnly, err := resolveClaimMcpConfig(json.RawMessage(`{"mcpServers":{}}`), overlay)
	if err != nil {
		t.Fatal(err)
	}
	if overlayOnly {
		t.Fatalf("explicit empty agent mcp_config must not be treated as overlay-only (config %q)", string(got))
	}
	var doc struct {
		McpServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(got, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.McpServers) != 1 || doc.McpServers["composio"] == nil {
		t.Fatalf("expected only the overlay server, got %q", string(got))
	}
}

func TestResolveClaimMcpConfigNonEmptyAgentConfigIsAuthored(t *testing.T) {
	t.Parallel()

	overlay := json.RawMessage(`{"mcpServers":{"composio":{"url":"https://composio.example/mcp"}}}`)
	got, overlayOnly, err := resolveClaimMcpConfig(json.RawMessage(`{"mcpServers":{"a":{"command":"a"}}}`), overlay)
	if err != nil {
		t.Fatal(err)
	}
	if overlayOnly {
		t.Fatalf("agent-authored mcp_config must not be flagged overlay-only (config %q)", string(got))
	}
}

// A malformed overlay must fall back to the agent's saved config, and must not
// claim overlay-only provenance for it.
func TestResolveClaimMcpConfigBadOverlayFallsBackNotOverlayOnly(t *testing.T) {
	t.Parallel()

	agentCfg := json.RawMessage(`{"mcpServers":{"a":{"command":"a"}}}`)
	got, overlayOnly, err := resolveClaimMcpConfig(agentCfg, json.RawMessage(`{"mcpServers":`))
	if err == nil {
		t.Fatal("expected a parse error for the malformed overlay")
	}
	if overlayOnly {
		t.Fatal("malformed overlay must not be flagged overlay-only")
	}
	if string(got) != string(agentCfg) {
		t.Fatalf("config = %q, want the agent config %q unchanged", string(got), string(agentCfg))
	}
}

// Same failure with no agent config: nothing to fall back to, and the daemon
// must end up on its native-inheritance path rather than a strict empty set.
func TestResolveClaimMcpConfigBadOverlayWithNoAgentConfigYieldsNil(t *testing.T) {
	t.Parallel()

	got, overlayOnly, err := resolveClaimMcpConfig(nil, json.RawMessage(`{"mcpServers":`))
	if err == nil {
		t.Fatal("expected a parse error for the malformed overlay")
	}
	if overlayOnly {
		t.Fatal("malformed overlay must not be flagged overlay-only")
	}
	if hasManagedJSON(got) {
		t.Fatalf("config = %q, want absent so the daemon keeps native inheritance", string(got))
	}
}

// The strict mcp_config semantics are enforced by the DAEMON. A daemon that
// predates them still merges the host's MCP servers into a managed config, so
// the claim path must refuse rather than run the agent with tools the operator
// scoped out (GitHub #6283).

// The provider set is a FROZEN record of what pre-capability daemons actually
// merged — not a mirror of the daemon's current provider switch. Pinning the
// exact membership makes the difference explicit: growing this set because a new
// provider gained runtime MCP discovery would start failing tasks on old daemons
// that never merged for it, re-creating the qwen false-positive. Discovery for a
// new provider can only ship in a daemon that already advertises the capability,
// which is never gated.
func TestProvidersOldDaemonsMergedRuntimeMcpIsFrozen(t *testing.T) {
	t.Parallel()

	want := map[string]bool{
		"claude":    true,
		"codebuddy": true,
		"codex":     true,
		"cursor":    true,
		"opencode":  true,
		"openclaw":  true,
	}
	if len(providersOldDaemonsMergedRuntimeMcp) != len(want) {
		t.Fatalf("frozen provider set has %d entries, want %d: %v",
			len(providersOldDaemonsMergedRuntimeMcp), len(want), providersOldDaemonsMergedRuntimeMcp)
	}
	for provider := range want {
		if !providersOldDaemonsMergedRuntimeMcp[provider] {
			t.Errorf("provider %q merged host MCP before the capability and must stay in the set", provider)
		}
	}
	// A provider that only gains runtime MCP discovery AFTER the capability
	// shipped must not be added: no pre-capability daemon ever merged for it.
	for _, provider := range []string{"qwen", "hermes"} {
		if providersOldDaemonsMergedRuntimeMcp[provider] {
			t.Errorf("provider %q was never merged by a pre-capability daemon; gating it would fail safe tasks", provider)
		}
	}
}

// The gate must only fire for providers whose pre-#6283 daemon actually merged
// host MCP. qwen was never in that switch and already had strict semantics, so
// gating it would cancel tasks that carry no risk at all.
func TestMcpConfigNeedsAuthoritativeDaemonOnlyForMergingProviders(t *testing.T) {
	t.Parallel()

	managed := json.RawMessage(`{"mcpServers":{"a":{"command":"a"}}}`)

	for _, provider := range []string{"claude", "codebuddy", "codex", "cursor", "opencode", "openclaw", "CLAUDE", " claude "} {
		if !mcpConfigNeedsAuthoritativeDaemon(provider, managed, nil) {
			t.Fatalf("provider %q merged host MCP before the fix and must gate", provider)
		}
	}
	// qwen is deliberately absent from the merge switch; unknown providers fail
	// to the same non-gating side because we cannot point at old behaviour.
	for _, provider := range []string{"qwen", "", "hermes", "something-new"} {
		if mcpConfigNeedsAuthoritativeDaemon(provider, managed, nil) {
			t.Fatalf("provider %q never merged host MCP and must not gate", provider)
		}
	}
}

func TestMcpConfigNeedsAuthoritativeDaemonForManagedConfigs(t *testing.T) {
	t.Parallel()

	for _, agentCfg := range []json.RawMessage{
		json.RawMessage(`{"mcpServers":{}}`),
		json.RawMessage(`{"mcpServers":{"a":{"command":"a"}}}`),
		json.RawMessage(`{}`),
	} {
		if !mcpConfigNeedsAuthoritativeDaemon("claude", agentCfg, nil) {
			t.Fatalf("managed config %q must require an authoritative daemon", string(agentCfg))
		}
	}
}

func TestMcpConfigNeedsAuthoritativeDaemonSkipsUnmanaged(t *testing.T) {
	t.Parallel()

	// Nothing to widen: the host's servers were always in scope for an agent
	// with no config of its own.
	for _, agentCfg := range []json.RawMessage{nil, json.RawMessage("null"), json.RawMessage(" null ")} {
		if mcpConfigNeedsAuthoritativeDaemon("claude", agentCfg, nil) {
			t.Fatalf("unmanaged config %q must not gate the claim", string(agentCfg))
		}
	}
}

// A non-object cannot carry `mcpServers`, so it expresses no boundary — and an
// old daemon does not widen it either, because mergeRuntimeAndAgentMcpConfig
// fails to unmarshal it and falls back to the agent config alone. Gating these
// blocked real tasks with no security benefit.
func TestMcpConfigNeedsAuthoritativeDaemonSkipsNonObjectConfigs(t *testing.T) {
	t.Parallel()

	for _, agentCfg := range []json.RawMessage{
		json.RawMessage(`[]`),
		json.RawMessage(`[{"mcpServers":{}}]`),
		json.RawMessage(`"mcpServers"`),
		json.RawMessage(`7`),
		json.RawMessage(`true`),
	} {
		if mcpConfigNeedsAuthoritativeDaemon("claude", agentCfg, nil) {
			t.Fatalf("non-object config %q must not gate the claim", string(agentCfg))
		}
	}
}

// The inherit opt-in doubles as the documented escape hatch: the operator has
// declared they accept the host's servers, so an old daemon already matches
// intent and the task must keep running.
func TestMcpConfigNeedsAuthoritativeDaemonSkipsExplicitInherit(t *testing.T) {
	t.Parallel()

	if mcpConfigNeedsAuthoritativeDaemon(
		"claude",
		json.RawMessage(`{"mcpServers":{"a":{"command":"a"}}}`),
		json.RawMessage(`{"mcp":{"inherit_runtime":true}}`),
	) {
		t.Fatal("an explicit inherit opt-in must not gate the claim")
	}
}

func TestMcpConfigNeedsAuthoritativeDaemonFailsClosedOnBadRuntimeConfig(t *testing.T) {
	t.Parallel()

	managed := json.RawMessage(`{"mcpServers":{}}`)
	for _, rc := range []json.RawMessage{
		nil,
		json.RawMessage(`{}`),
		json.RawMessage(`{"mcp":{"inherit_runtime":false}}`),
		json.RawMessage(`{"mcp":{"inherit_runtime":"true"}}`), // wrong type
		json.RawMessage(`{"mcp":{"inherit_runtime":true}`),    // truncated
		json.RawMessage(`{"mode":"gateway"}`),
	} {
		if !mcpConfigNeedsAuthoritativeDaemon("claude", managed, rc) {
			t.Fatalf("runtime_config %q must not count as an inherit opt-in", string(rc))
		}
	}
}

// Mirrors the daemon's decodeMcpInheritRuntime so the two sides cannot drift.
func TestRuntimeConfigInheritsRuntimeMcp(t *testing.T) {
	t.Parallel()

	if !runtimeConfigInheritsRuntimeMcp(json.RawMessage(`{"mcp":{"inherit_runtime":true}}`)) {
		t.Fatal("explicit true must be honoured")
	}
	for _, rc := range []json.RawMessage{
		nil,
		json.RawMessage("null"),
		json.RawMessage(`{"mcp":{}}`),
		json.RawMessage(`not json`),
	} {
		if runtimeConfigInheritsRuntimeMcp(rc) {
			t.Fatalf("runtime_config %q must not opt in", string(rc))
		}
	}
}
