package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/daemonws"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"github.com/multica-ai/multica/server/pkg/taskfailure"
)

// The authoritative mcp_config semantics are enforced by the DAEMON, so handing
// a strictly-scoped task to a daemon that predates them would run the agent with
// the runtime host's MCP servers merged in — the GitHub #6283 vulnerability. The
// claim path fails the task closed instead, with a classified reason so the
// upgrade requirement reaches the operator on EVERY claim path (the default one
// is the batch endpoint, which does not surface per-task HTTP errors at all).

const mcpGateDaemonID = "mcp-gate-daemon"

// mcpGateAgent seeds an agent with the given provider / mcp_config /
// runtime_config plus one queued task. Returns the runtime and task ids.
func mcpGateAgent(t *testing.T, name, provider, mcpConfig, runtimeConfig string) (runtimeID, taskID string) {
	t.Helper()
	ctx := context.Background()

	agentID := createHandlerTestAgent(t, name, []byte(mcpConfig))
	if err := testPool.QueryRow(ctx,
		`SELECT runtime_id FROM agent WHERE id = $1`, agentID).Scan(&runtimeID); err != nil {
		t.Fatalf("get agent runtime: %v", err)
	}
	// The gate only fires for providers whose pre-#6283 daemon merged host MCP,
	// so the provider is part of the fixture rather than an incidental default.
	if _, err := testPool.Exec(ctx,
		`UPDATE agent_runtime SET provider = $2 WHERE id = $1`, runtimeID, provider); err != nil {
		t.Fatalf("set runtime provider: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(),
			`UPDATE agent_runtime SET provider = 'claude' WHERE id = $1`, runtimeID)
	})
	if runtimeConfig != "" {
		if _, err := testPool.Exec(ctx,
			`UPDATE agent SET runtime_config = $2::jsonb WHERE id = $1`, agentID, runtimeConfig); err != nil {
			t.Fatalf("set runtime_config: %v", err)
		}
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

// stripAuthoritativeMcp turns a request into a pre-#6283 daemon: the shared
// helper defaults the capability on, matching every current daemon.
func stripAuthoritativeMcp(req *http.Request) *http.Request {
	req.Header.Del("X-Client-Capabilities")
	return req
}

func mcpGatePerRuntimeClaim(t *testing.T, runtimeID string, current bool) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := newDaemonTokenRequest(http.MethodPost, "/api/daemon/runtimes/"+runtimeID+"/claim", nil, testWorkspaceID, mcpGateDaemonID)
	if !current {
		req = stripAuthoritativeMcp(req)
	}
	req = withURLParam(req, "runtimeId", runtimeID)
	testHandler.ClaimTaskByRuntime(w, req)
	return w
}

func mcpGateBatchClaim(t *testing.T, runtimeIDs []string, current bool) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := newDaemonTokenRequest(http.MethodPost, "/api/daemon/tasks/claim",
		map[string]any{"daemon_id": mcpGateDaemonID, "runtime_ids": runtimeIDs, "max_tasks": 10},
		testWorkspaceID, mcpGateDaemonID)
	if !current {
		req = stripAuthoritativeMcp(req)
	}
	testHandler.ClaimTasksByRuntime(w, req)
	return w
}

// mcpGateWSRPCClaim drives the WS RPC claim, which reuses the batch handler via
// a synthesized request whose capabilities come from the handshake identity
// rather than a live header.
func mcpGateWSRPCClaim(t *testing.T, runtimeID, capabilities string) (int, string) {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"daemon_id":   mcpGateDaemonID,
		"runtime_ids": []string{runtimeID},
		"max_tasks":   10,
	})
	if err != nil {
		t.Fatal(err)
	}
	status, raw, rerr := testHandler.DaemonRPCHandler(context.Background(), daemonws.ClientIdentity{
		DaemonID:     mcpGateDaemonID,
		WorkspaceID:  testWorkspaceID,
		WorkspaceIDs: []string{testWorkspaceID},
		RuntimeIDs:   []string{runtimeID},
		Capabilities: capabilities,
	}, "tasks.claim", body)
	if rerr != nil {
		t.Fatalf("WS RPC claim: %v", rerr)
	}
	return status, string(raw)
}

type mcpGateTaskRow struct {
	status        string
	failureReason string
	errorMessage  string
}

func mcpGateTask(t *testing.T, taskID string) mcpGateTaskRow {
	t.Helper()
	var row mcpGateTaskRow
	var reason, errMsg *string
	if err := testPool.QueryRow(context.Background(),
		`SELECT status, failure_reason, error FROM agent_task_queue WHERE id = $1`,
		taskID).Scan(&row.status, &reason, &errMsg); err != nil {
		t.Fatalf("read task: %v", err)
	}
	if reason != nil {
		row.failureReason = *reason
	}
	if errMsg != nil {
		row.errorMessage = *errMsg
	}
	return row
}

// assertRefused checks the outcome that must hold on every claim path: the task
// is failed (not silently cancelled) with the classified reason and the
// actionable message, so the operator can see WHY it did not run.
func assertRefused(t *testing.T, taskID string) {
	t.Helper()
	row := mcpGateTask(t, taskID)
	if row.status != "failed" {
		t.Fatalf("task status = %q, want failed", row.status)
	}
	if row.failureReason != string(taskfailure.ReasonMcpConfigDaemonOutdated) {
		t.Fatalf("failure_reason = %q, want %q", row.failureReason, taskfailure.ReasonMcpConfigDaemonOutdated)
	}
	for _, want := range []string{"upgrade the local daemon", "inherit_runtime"} {
		if !strings.Contains(row.errorMessage, want) {
			t.Fatalf("error_message must mention %q; got %q", want, row.errorMessage)
		}
	}
}

// Path 1 of 3 — legacy per-runtime claim. This one CAN carry the 412.
func TestMcpGate_PerRuntimeClaimRefusesOutdatedDaemon(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	runtimeID, taskID := mcpGateAgent(t, "McpGatePerRuntime", "claude", `{"mcpServers":{}}`, "")

	w := mcpGatePerRuntimeClaim(t, runtimeID, false)
	if w.Code != http.StatusPreconditionFailed {
		t.Fatalf("expected 412, got %d: %s", w.Code, w.Body.String())
	}
	var body struct{ Error string }
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if !strings.Contains(body.Error, "upgrade the local daemon") {
		t.Fatalf("412 body must be actionable; got %q", body.Error)
	}
	assertRefused(t, taskID)
}

// Path 2 of 3 — the DEFAULT machine-level batch claim. It answers 200 with a
// task list and cannot express a per-task HTTP error, which is exactly why the
// refusal has to be recorded on the task itself.
func TestMcpGate_BatchClaimRecordsRefusalOnTheTask(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	runtimeID, taskID := mcpGateAgent(t, "McpGateBatch", "claude", `{"mcpServers":{}}`, "")

	w := mcpGateBatchClaim(t, []string{runtimeID}, false)
	if w.Code != http.StatusOK {
		t.Fatalf("batch claim should still answer 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Tasks []map[string]any `json:"tasks"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode batch response: %v", err)
	}
	if len(resp.Tasks) != 0 {
		t.Fatalf("refused task must not be dispatched; got %d tasks", len(resp.Tasks))
	}
	// The whole point: the reason is visible even though the batch response
	// carried no error.
	assertRefused(t, taskID)
}

// A refusal must not cost the daemon the other tasks in the same batch —
// turning the whole batch into a 412 after finalizing siblings would drop them.
func TestMcpGate_BatchClaimStillDeliversHealthyTasks(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	refusedRuntime, refusedTask := mcpGateAgent(t, "McpGateBatchRefused", "claude", `{"mcpServers":{}}`, "")
	healthyRuntime, healthyTask := mcpGateAgent(t, "McpGateBatchHealthy", "claude", "null", "")

	w := mcpGateBatchClaim(t, []string{refusedRuntime, healthyRuntime}, false)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Tasks []struct {
			ID string `json:"id"`
		} `json:"tasks"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode batch response: %v", err)
	}
	if len(resp.Tasks) != 1 || resp.Tasks[0].ID != healthyTask {
		t.Fatalf("healthy task must still be claimed; got %+v", resp.Tasks)
	}
	assertRefused(t, refusedTask)
}

// Path 3 of 3 — the WS RPC claim, which reuses the batch handler through a
// synthesized request. Capabilities travel via the WS handshake identity, so a
// pre-#6283 daemon arrives with none and must be refused the same way.
func TestMcpGate_WSRPCClaimRefusesOutdatedDaemon(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	runtimeID, taskID := mcpGateAgent(t, "McpGateWSRPC", "claude", `{"mcpServers":{}}`, "")

	status, body := mcpGateWSRPCClaim(t, runtimeID, "")
	if status != http.StatusOK {
		t.Fatalf("WS RPC batch claim should answer 200, got %d: %s", status, body)
	}
	var resp struct {
		Tasks []map[string]any `json:"tasks"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("decode WS RPC response: %v", err)
	}
	if len(resp.Tasks) != 0 {
		t.Fatalf("refused task must not be dispatched over WS; got %d tasks", len(resp.Tasks))
	}
	assertRefused(t, taskID)
}

func TestMcpGate_WSRPCClaimAcceptsCurrentDaemon(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	runtimeID, taskID := mcpGateAgent(t, "McpGateWSRPCCurrent", "claude", `{"mcpServers":{}}`, "")

	status, body := mcpGateWSRPCClaim(t, runtimeID, protocol.DaemonCapabilityAuthoritativeMcpV1)
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, body)
	}
	var resp struct {
		Tasks []struct {
			ID string `json:"id"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("decode WS RPC response: %v", err)
	}
	if len(resp.Tasks) != 1 || resp.Tasks[0].ID != taskID {
		t.Fatalf("capability-advertising daemon must receive the task; got %+v", resp.Tasks)
	}
}

// A current daemon on the per-runtime path is unaffected.
func TestMcpGate_PerRuntimeClaimAcceptsCurrentDaemon(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	runtimeID, _ := mcpGateAgent(t, "McpGateCurrent", "claude", `{"mcpServers":{}}`, "")

	if w := mcpGatePerRuntimeClaim(t, runtimeID, true); w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

// Qwen's pre-#6283 daemon never merged host MCP (it is absent from the runtime
// MCP discovery switch), so gating it would cancel tasks that carry no risk.
func TestMcpGate_QwenOutdatedDaemonStillClaims(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	runtimeID, taskID := mcpGateAgent(t, "McpGateQwen", "qwen", `{"mcpServers":{"a":{"command":"a"}}}`, "")

	w := mcpGatePerRuntimeClaim(t, runtimeID, false)
	if w.Code != http.StatusOK {
		t.Fatalf("an outdated qwen daemon must still claim, got %d: %s", w.Code, w.Body.String())
	}
	if got := mcpGateTask(t, taskID).status; got == "failed" {
		t.Fatal("qwen task must not be failed by the MCP gate")
	}
}

// The inherit opt-in is the documented escape hatch: it declares the host's
// servers acceptable, so an old daemon already matches intent.
func TestMcpGate_InheritOptInAllowsOutdatedDaemon(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	runtimeID, _ := mcpGateAgent(t, "McpGateInherit", "claude",
		`{"mcpServers":{"a":{"command":"a"}}}`, `{"mcp":{"inherit_runtime":true}}`)

	if w := mcpGatePerRuntimeClaim(t, runtimeID, false); w.Code != http.StatusOK {
		t.Fatalf("expected 200 with inherit_runtime, got %d: %s", w.Code, w.Body.String())
	}
}

// An agent that never configured MCP, or whose config is not an object, has
// nothing an old daemon could widen.
func TestMcpGate_UnmanagedAndNonObjectConfigsIgnoreGate(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	for _, cfg := range []string{"null", "[]"} {
		runtimeID, _ := mcpGateAgent(t, "McpGateUnmanaged-"+cfg, "claude", cfg, "")
		if w := mcpGatePerRuntimeClaim(t, runtimeID, false); w.Code != http.StatusOK {
			t.Fatalf("mcp_config %s must not gate the claim, got %d: %s", cfg, w.Code, w.Body.String())
		}
	}
}
