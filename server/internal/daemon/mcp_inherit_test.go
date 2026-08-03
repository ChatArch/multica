package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// writeHostClaudeMcp seeds the host's user-level Claude MCP configuration —
// the servers an agent must NOT reach through an explicit mcp_config.
func writeHostClaudeMcp(t *testing.T, names ...string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)

	servers := map[string]any{}
	for _, name := range names {
		servers[name] = map[string]any{"command": name + "-mcp"}
	}
	raw, err := json.Marshal(map[string]any{"mcpServers": servers})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// serverNames lists the mcpServers keys of an effective config.
func serverNames(t *testing.T, raw json.RawMessage) []string {
	t.Helper()
	if len(raw) == 0 {
		return nil
	}
	var doc struct {
		McpServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal effective config %q: %v", string(raw), err)
	}
	out := make([]string, 0, len(doc.McpServers))
	for name := range doc.McpServers {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func equalNames(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// TestResolveEffectiveMcpConfigExplicitEmptyIsStrict is the direct regression
// test for GitHub #6283: an operator who saves `{"mcpServers":{}}` expects the
// agent to reach NO MCP server. Before the fix this resolved to the complete
// host set.
func TestResolveEffectiveMcpConfigExplicitEmptyIsStrict(t *testing.T) {
	writeHostClaudeMcp(t, "host-prod-db", "host-secrets")

	got := resolveEffectiveMcpConfig(
		"claude",
		json.RawMessage(`{"mcpServers":{}}`),
		false,
		nil,
		quietLogger(),
	)

	if names := serverNames(t, got); len(names) != 0 {
		t.Fatalf("explicit empty mcp_config must expose no MCP servers, got %v (config %q)", names, string(got))
	}
	// Must stay non-nil so the provider backend still passes
	// --mcp-config/--strict-mcp-config instead of falling back to native
	// inheritance.
	if !hasManagedJSONPayload(got) {
		t.Fatalf("explicit empty mcp_config must remain a managed config, got %q", string(got))
	}
}

// TestResolveEffectiveMcpConfigNonEmptyIsAllowlist — a saved allowlist must not
// be widened with same-named or unrelated host servers.
func TestResolveEffectiveMcpConfigNonEmptyIsAllowlist(t *testing.T) {
	writeHostClaudeMcp(t, "host-prod-db", "shared")

	got := resolveEffectiveMcpConfig(
		"claude",
		json.RawMessage(`{"mcpServers":{"shared":{"command":"agent-shared"},"agent-only":{"url":"https://agent.example/mcp"}}}`),
		false,
		nil,
		quietLogger(),
	)

	want := []string{"agent-only", "shared"}
	if names := serverNames(t, got); !equalNames(names, want) {
		t.Fatalf("managed allowlist = %v, want %v (config %q)", names, want, string(got))
	}
	// The agent's own entry must win for a colliding name, not the host's.
	var doc struct {
		McpServers map[string]map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal(got, &doc); err != nil {
		t.Fatal(err)
	}
	if cmd := doc.McpServers["shared"]["command"]; cmd != "agent-shared" {
		t.Fatalf("shared.command = %#v, want agent-shared", cmd)
	}
}

// TestResolveEffectiveMcpConfigNullInheritsNatively — null must keep the
// provider's own inheritance path, which the backends detect by a nil config.
func TestResolveEffectiveMcpConfigNullInheritsNatively(t *testing.T) {
	writeHostClaudeMcp(t, "host-prod-db")

	for _, raw := range []json.RawMessage{nil, json.RawMessage("null"), json.RawMessage(" null ")} {
		got := resolveEffectiveMcpConfig("claude", raw, false, nil, quietLogger())
		if string(got) != string(raw) {
			t.Fatalf("null mcp_config %q resolved to %q; want unchanged", string(raw), string(got))
		}
		if hasManagedJSONPayload(got) {
			t.Fatalf("null mcp_config must not become a managed config, got %q", string(got))
		}
	}
}

// TestResolveEffectiveMcpConfigOverlayOnlyKeepsRuntimeServers — enabling a
// per-task integration (Composio) on an agent that never configured MCP must
// not strip the host servers it was already inheriting.
func TestResolveEffectiveMcpConfigOverlayOnlyKeepsRuntimeServers(t *testing.T) {
	writeHostClaudeMcp(t, "host-prod-db")

	got := resolveEffectiveMcpConfig(
		"claude",
		json.RawMessage(`{"mcpServers":{"composio":{"url":"https://composio.example/mcp"}}}`),
		true,
		nil,
		quietLogger(),
	)

	want := []string{"composio", "host-prod-db"}
	if names := serverNames(t, got); !equalNames(names, want) {
		t.Fatalf("overlay-only config = %v, want %v (config %q)", names, want, string(got))
	}
}

// TestResolveEffectiveMcpConfigInheritOptInMerges — the explicit per-agent
// opt-in restores the additive behavior #5277 shipped.
func TestResolveEffectiveMcpConfigInheritOptInMerges(t *testing.T) {
	writeHostClaudeMcp(t, "host-prod-db")

	got := resolveEffectiveMcpConfig(
		"claude",
		json.RawMessage(`{"mcpServers":{"agent-only":{"command":"agent"}}}`),
		false,
		json.RawMessage(`{"mcp":{"inherit_runtime":true}}`),
		quietLogger(),
	)

	want := []string{"agent-only", "host-prod-db"}
	if names := serverNames(t, got); !equalNames(names, want) {
		t.Fatalf("inherit_runtime opt-in = %v, want %v (config %q)", names, want, string(got))
	}
}

// TestResolveEffectiveMcpConfigInheritOptInStillStrictWhenFalse — an explicit
// false, and an unrelated runtime_config, must both stay strict.
func TestResolveEffectiveMcpConfigInheritOptInStillStrictWhenFalse(t *testing.T) {
	writeHostClaudeMcp(t, "host-prod-db")

	for _, rc := range []json.RawMessage{
		nil,
		json.RawMessage("null"),
		json.RawMessage(`{}`),
		json.RawMessage(`{"mcp":{}}`),
		json.RawMessage(`{"mcp":{"inherit_runtime":false}}`),
		json.RawMessage(`{"mode":"gateway"}`),
	} {
		got := resolveEffectiveMcpConfig(
			"claude",
			json.RawMessage(`{"mcpServers":{}}`),
			false,
			rc,
			quietLogger(),
		)
		if names := serverNames(t, got); len(names) != 0 {
			t.Fatalf("runtime_config %q must stay strict, got %v", string(rc), names)
		}
	}
}

// TestDecodeMcpInheritRuntimeFailsClosedOnMalformed — a broken runtime_config
// must never widen the tool surface.
func TestDecodeMcpInheritRuntimeFailsClosedOnMalformed(t *testing.T) {
	for _, rc := range []json.RawMessage{
		json.RawMessage(`{"mcp":{"inherit_runtime":true}`), // truncated
		json.RawMessage(`{"mcp":"yes"}`),                   // wrong type
		json.RawMessage(`not json`),
	} {
		if decodeMcpInheritRuntime(rc, quietLogger()) {
			t.Fatalf("malformed runtime_config %q must not enable inheritance", string(rc))
		}
	}
}

// TestResolveEffectiveMcpConfigUnsupportedProviderStaysStrict — providers with
// no runtime MCP discovery must still honour the managed set verbatim.
func TestResolveEffectiveMcpConfigUnsupportedProviderStaysStrict(t *testing.T) {
	writeHostClaudeMcp(t, "host-prod-db")

	got := resolveEffectiveMcpConfig(
		"qwen",
		json.RawMessage(`{"mcpServers":{}}`),
		true, // even on the inherit path there is nothing to inherit
		json.RawMessage(`{"mcp":{"inherit_runtime":true}}`),
		quietLogger(),
	)
	if names := serverNames(t, got); len(names) != 0 {
		t.Fatalf("provider without runtime MCP discovery = %v, want none", names)
	}
}
