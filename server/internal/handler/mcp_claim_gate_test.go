package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

// The authoritative mcp_config semantics are enforced by the DAEMON, so handing
// a strictly-scoped task to a daemon that predates them would run the agent with
// the runtime host's MCP servers merged in — the GitHub #6283 vulnerability.
// The claim path fails closed instead.

func claimForOutdatedDaemon(t *testing.T, runtimeID string, advertiseCapability bool) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := newDaemonTokenRequest(http.MethodPost, "/api/daemon/runtimes/"+runtimeID+"/claim", nil, testWorkspaceID, "mcp-gate-daemon")
	if !advertiseCapability {
		// Simulate a pre-#6283 daemon: the shared helper defaults the
		// capability on, so drop it explicitly.
		req.Header.Del("X-Client-Capabilities")
	}
	req = withURLParam(req, "runtimeId", runtimeID)
	testHandler.ClaimTaskByRuntime(w, req)
	return w
}

// queueMcpGateTask seeds an agent with the given mcp_config / runtime_config and
// one queued task, returning the agent's runtime and the task id.
func queueMcpGateTask(t *testing.T, name string, mcpConfig, runtimeConfig string) (runtimeID, taskID string) {
	t.Helper()
	ctx := context.Background()

	agentID := createHandlerTestAgent(t, name, []byte(mcpConfig))
	if runtimeConfig != "" {
		if _, err := testPool.Exec(ctx,
			`UPDATE agent SET runtime_config = $2::jsonb WHERE id = $1`, agentID, runtimeConfig); err != nil {
			t.Fatalf("set runtime_config: %v", err)
		}
	}
	if err := testPool.QueryRow(ctx,
		`SELECT runtime_id FROM agent WHERE id = $1`, agentID).Scan(&runtimeID); err != nil {
		t.Fatalf("get agent runtime: %v", err)
	}
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, status, priority)
		VALUES ($1, $2, 'queued', 9000) RETURNING id
	`, agentID, runtimeID).Scan(&taskID); err != nil {
		t.Fatalf("queue task: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, taskID)
	})
	return runtimeID, taskID
}

func mcpGateTaskStatus(t *testing.T, taskID string) string {
	t.Helper()
	var status string
	if err := testPool.QueryRow(context.Background(),
		`SELECT status FROM agent_task_queue WHERE id = $1`, taskID).Scan(&status); err != nil {
		t.Fatalf("read task status: %v", err)
	}
	return status
}

func TestClaimTaskByRuntime_OutdatedDaemonRefusesManagedMcpConfig(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	runtimeID, taskID := queueMcpGateTask(t, "McpGateStrictAgent", `{"mcpServers":{}}`, "")

	w := claimForOutdatedDaemon(t, runtimeID, false)
	if w.Code != http.StatusPreconditionFailed {
		t.Fatalf("expected 412 for a daemon without %s, got %d: %s",
			protocol.DaemonCapabilityAuthoritativeMcpV1, w.Code, w.Body.String())
	}
	// The error must tell the operator what to do, not just fail.
	var body struct{ Error string }
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	for _, want := range []string{"upgrade the local daemon", "inherit_runtime"} {
		if !strings.Contains(body.Error, want) {
			t.Fatalf("error message must mention %q; got %q", want, body.Error)
		}
	}
	// Fail closed rather than leaving the task to be retried forever by the
	// same outdated daemon.
	if got := mcpGateTaskStatus(t, taskID); got != "cancelled" {
		t.Fatalf("task status = %q, want cancelled", got)
	}
}

func TestClaimTaskByRuntime_CurrentDaemonAcceptsManagedMcpConfig(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	runtimeID, _ := queueMcpGateTask(t, "McpGateCurrentAgent", `{"mcpServers":{}}`, "")

	w := claimForOutdatedDaemon(t, runtimeID, true)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for a capability-advertising daemon, got %d: %s", w.Code, w.Body.String())
	}
}

// The inherit opt-in is the documented escape hatch for an operator who cannot
// upgrade the daemon yet: it declares that the host's servers are acceptable, so
// an old daemon already matches intent and the task must run.
func TestClaimTaskByRuntime_InheritOptInAllowsOutdatedDaemon(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	runtimeID, _ := queueMcpGateTask(t, "McpGateInheritAgent",
		`{"mcpServers":{"a":{"command":"a"}}}`, `{"mcp":{"inherit_runtime":true}}`)

	w := claimForOutdatedDaemon(t, runtimeID, false)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 with inherit_runtime opt-in, got %d: %s", w.Code, w.Body.String())
	}
}

// An agent that never configured MCP has nothing to widen — the host's servers
// were always in scope — so the gate must not block it.
func TestClaimTaskByRuntime_UnmanagedMcpConfigIgnoresGate(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	for _, cfg := range []string{"null", "[]"} {
		runtimeID, _ := queueMcpGateTask(t, "McpGateUnmanagedAgent-"+cfg, cfg, "")

		w := claimForOutdatedDaemon(t, runtimeID, false)
		if w.Code != http.StatusOK {
			t.Fatalf("mcp_config %s must not gate the claim, got %d: %s", cfg, w.Code, w.Body.String())
		}
	}
}
