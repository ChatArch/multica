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
