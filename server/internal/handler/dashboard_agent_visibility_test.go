package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// dashboardAgentPresence is the expected membership of the three agent ids that
// matter in a per-agent dashboard response.
type dashboardAgentPresence struct {
	privateAgent bool
	publicAgent  bool
	sentinel     bool
}

// TestDashboardPerAgentRollupsFoldRestrictedAgents is the regression guard for
// MUL-5409: the three per-agent dashboard rollups used to authorize on
// workspace membership alone and return a bare agent_id for EVERY agent in the
// workspace, telling a plain member that someone else's private agent exists,
// how much it spends, how long it runs, and what it fails on.
//
// The contract now: rows for agents the caller may not view are folded onto the
// `__restricted_agents__` sentinel — never dropped — so
//
//   - no private agent's UUID appears in a plain member's response, and
//   - every aggregate still sums to exactly what the privileged view sums to,
//     which is what keeps the per-agent breakdown reconciling with the
//     workspace-level KPIs rendered beside it.
//
// A public_to agent is seeded alongside the private one so the test also proves
// the fold is selective rather than "the member sees nothing".
func TestDashboardPerAgentRollupsFoldRestrictedAgents(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	privateAgentID, _, memberID := privateAgentTestFixture(t)
	runtimeID := handlerTestRuntimeID(t)

	// Second agent, public_to the whole workspace: the plain member CAN see
	// this one, so its rows must survive with their real UUID.
	var publicAgentID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent (
			workspace_id, name, description, runtime_mode, runtime_config,
			runtime_id, visibility, permission_mode, max_concurrent_tasks, owner_id,
			instructions, custom_env, custom_args
		)
		VALUES ($1, 'dashboard-visibility-public-agent', '', 'cloud', '{}'::jsonb,
		        $2, 'workspace', 'public_to', 1, $3, '', '{}'::jsonb, '[]'::jsonb)
		RETURNING id
	`, testWorkspaceID, runtimeID, testUserID).Scan(&publicAgentID); err != nil {
		t.Fatalf("create public agent: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent WHERE id = $1`, publicAgentID)
	})
	if _, err := testPool.Exec(ctx, `
		INSERT INTO agent_invocation_target (agent_id, target_type, target_id)
		VALUES ($1, 'workspace', $2)
	`, publicAgentID, testWorkspaceID); err != nil {
		t.Fatalf("grant workspace invocation target: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(),
			`DELETE FROM agent_invocation_target WHERE agent_id = $1`, publicAgentID)
	})

	var issueID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, title, creator_id, creator_type, number)
		VALUES ($1, 'dashboard agent visibility', $2, 'member',
		        (SELECT COALESCE(MAX(number), 0) + 1 FROM issue WHERE workspace_id = $1))
		RETURNING id
	`, testWorkspaceID, testUserID).Scan(&issueID); err != nil {
		t.Fatalf("create issue: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, issueID) })

	started := time.Now().UTC().Add(-30 * time.Minute)
	completed := started.Add(10 * time.Minute) // 600s run

	// One completed + one failed run per agent, each with usage, so all three
	// endpoints have something to fold: tokens/cost, run time, and a failure
	// reason alongside its succeeded denominator.
	seedRun := func(agentID, status, failureReason string, tokens int64) {
		var taskID string
		if err := testPool.QueryRow(ctx, `
			INSERT INTO agent_task_queue (
				agent_id, issue_id, runtime_id, status, failure_reason,
				started_at, completed_at, created_at
			)
			VALUES ($1, $2, $3, $4, NULLIF($5, ''), $6, $7, now())
			RETURNING id
		`, agentID, issueID, runtimeID, status, failureReason, started, completed).Scan(&taskID); err != nil {
			t.Fatalf("insert %s task: %v", status, err)
		}
		t.Cleanup(func() {
			testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, taskID)
		})
		if _, err := testPool.Exec(ctx, `
			INSERT INTO task_usage (task_id, provider, model, input_tokens, output_tokens, created_at)
			VALUES ($1, 'claude', 'agent-visibility-model', $2, 0, now())
		`, taskID, tokens); err != nil {
			t.Fatalf("insert task_usage: %v", err)
		}
	}
	seedRun(privateAgentID, "completed", "", 1000)
	seedRun(privateAgentID, "failed", "runtime_offline", 200)
	seedRun(publicAgentID, "completed", "", 300)
	seedRun(publicAgentID, "failed", "timeout", 50)

	t.Cleanup(func() {
		testPool.Exec(context.Background(),
			`DELETE FROM task_usage_hourly WHERE model = 'agent-visibility-model'`)
	})
	if _, err := testPool.Exec(ctx, `
		SELECT rollup_task_usage_hourly_window('1970-01-01'::timestamptz, now() + interval '1 hour')
	`); err != nil {
		t.Fatalf("rollup window: %v", err)
	}

	// `days=7` rather than 1: failures/by-agent closes its window at exactly N
	// calendar days in the viewer tz, and a run seeded 20 minutes ago falls
	// outside that when the suite happens to run just after local midnight.
	// The assertions below compare the two views of the same data, so a wider
	// window costs nothing.
	read := func(
		name string,
		handler func(http.ResponseWriter, *http.Request),
		path, userID string,
		out any,
	) string {
		t.Helper()
		w := httptest.NewRecorder()
		handler(w, newRequestAs(userID, "GET", path, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("%s as %s: expected 200, got %d: %s", name, userID, w.Code, w.Body.String())
		}
		body := w.Body.String()
		if err := json.Unmarshal([]byte(body), out); err != nil {
			t.Fatalf("%s: decode response: %v", name, err)
		}
		// Structural assertions only cover the fields this test knows about; a
		// substring scan also catches an agent id smuggled in by a field added
		// later.
		if userID == memberID && strings.Contains(body, privateAgentID) {
			t.Errorf("%s: member response leaked private agent id %s: %s", name, privateAgentID, body)
		}
		return body
	}

	// ---- usage/by-agent ----------------------------------------------------

	type usageRow struct {
		AgentID      string `json:"agent_id"`
		Model        string `json:"model"`
		InputTokens  int64  `json:"input_tokens"`
		CostUSDTicks int64  `json:"cost_usd_ticks"`
	}
	var ownerUsage, memberUsage []usageRow
	const usagePath = "/api/dashboard/usage/by-agent?days=7"
	read("usage/by-agent", testHandler.GetDashboardUsageByAgent, usagePath, testUserID, &ownerUsage)
	read("usage/by-agent", testHandler.GetDashboardUsageByAgent, usagePath, memberID, &memberUsage)

	usageID := func(r usageRow) string { return r.AgentID }
	assertDashboardAgentPresence(t, "usage/by-agent (owner)", dashboardAgentIDSet(ownerUsage, usageID),
		dashboardAgentPresence{privateAgent: true, publicAgent: true, sentinel: false}, privateAgentID, publicAgentID)
	assertDashboardAgentPresence(t, "usage/by-agent (member)", dashboardAgentIDSet(memberUsage, usageID),
		dashboardAgentPresence{privateAgent: false, publicAgent: true, sentinel: true}, privateAgentID, publicAgentID)

	sumUsage := func(rows []usageRow) (tokens, ticks int64) {
		for _, r := range rows {
			tokens += r.InputTokens
			ticks += r.CostUSDTicks
		}
		return
	}
	ownerTokens, ownerTicks := sumUsage(ownerUsage)
	memberTokens, memberTicks := sumUsage(memberUsage)
	if ownerTokens != memberTokens || ownerTicks != memberTicks {
		t.Errorf("usage/by-agent: folding changed the totals — owner (%d tokens, %d ticks), member (%d tokens, %d ticks)",
			ownerTokens, ownerTicks, memberTokens, memberTicks)
	}
	// The restricted bucket must stay split by (provider, model) or the client
	// cannot price it, and the leaderboard stops summing to the Cost KPI.
	for _, r := range memberUsage {
		if r.AgentID == restrictedAgentsRowID && r.Model == "" {
			t.Errorf("usage/by-agent: restricted bucket lost its model dimension: %+v", r)
		}
	}

	// ---- agent-runtime -----------------------------------------------------

	type runTimeRow struct {
		AgentID      string `json:"agent_id"`
		TotalSeconds int64  `json:"total_seconds"`
		TaskCount    int32  `json:"task_count"`
		FailedCount  int32  `json:"failed_count"`
	}
	var ownerRunTime, memberRunTime []runTimeRow
	const runTimePath = "/api/dashboard/agent-runtime?days=7"
	read("agent-runtime", testHandler.GetDashboardAgentRunTime, runTimePath, testUserID, &ownerRunTime)
	read("agent-runtime", testHandler.GetDashboardAgentRunTime, runTimePath, memberID, &memberRunTime)

	runTimeID := func(r runTimeRow) string { return r.AgentID }
	assertDashboardAgentPresence(t, "agent-runtime (owner)", dashboardAgentIDSet(ownerRunTime, runTimeID),
		dashboardAgentPresence{privateAgent: true, publicAgent: true, sentinel: false}, privateAgentID, publicAgentID)
	assertDashboardAgentPresence(t, "agent-runtime (member)", dashboardAgentIDSet(memberRunTime, runTimeID),
		dashboardAgentPresence{privateAgent: false, publicAgent: true, sentinel: true}, privateAgentID, publicAgentID)

	sumRunTime := func(rows []runTimeRow) (secs int64, tasks, failed int32) {
		for _, r := range rows {
			secs += r.TotalSeconds
			tasks += r.TaskCount
			failed += r.FailedCount
		}
		return
	}
	ownerSecs, ownerTasks, ownerFailed := sumRunTime(ownerRunTime)
	memberSecs, memberTasks, memberFailed := sumRunTime(memberRunTime)
	if ownerSecs != memberSecs || ownerTasks != memberTasks || ownerFailed != memberFailed {
		t.Errorf("agent-runtime: folding changed the totals — owner (%ds, %d tasks, %d failed), member (%ds, %d tasks, %d failed)",
			ownerSecs, ownerTasks, ownerFailed, memberSecs, memberTasks, memberFailed)
	}
	// One bucket, not one row per hidden agent: how MANY private agents exist
	// is itself part of what "private" hides.
	restrictedRows := 0
	for _, r := range memberRunTime {
		if r.AgentID == restrictedAgentsRowID {
			restrictedRows++
		}
	}
	if restrictedRows != 1 {
		t.Errorf("agent-runtime: expected exactly 1 restricted bucket row, got %d", restrictedRows)
	}

	// ---- failures/by-agent -------------------------------------------------

	type failureRow struct {
		AgentID       string `json:"agent_id"`
		FailureReason string `json:"failure_reason"`
		TaskCount     int32  `json:"task_count"`
	}
	var ownerFailures, memberFailures []failureRow
	const failuresPath = "/api/dashboard/failures/by-agent?days=7"
	read("failures/by-agent", testHandler.GetDashboardFailuresByAgent, failuresPath, testUserID, &ownerFailures)
	read("failures/by-agent", testHandler.GetDashboardFailuresByAgent, failuresPath, memberID, &memberFailures)

	failureID := func(r failureRow) string { return r.AgentID }
	assertDashboardAgentPresence(t, "failures/by-agent (owner)", dashboardAgentIDSet(ownerFailures, failureID),
		dashboardAgentPresence{privateAgent: true, publicAgent: true, sentinel: false}, privateAgentID, publicAgentID)
	assertDashboardAgentPresence(t, "failures/by-agent (member)", dashboardAgentIDSet(memberFailures, failureID),
		dashboardAgentPresence{privateAgent: false, publicAgent: true, sentinel: true}, privateAgentID, publicAgentID)

	// Per-reason totals must be identical across the two views: the Errors card
	// derives the workspace failure rate from these rows, and a fold that lost
	// or double-counted a bucket would silently skew it.
	byReason := func(rows []failureRow) map[string]int32 {
		out := map[string]int32{}
		for _, r := range rows {
			out[r.FailureReason] += r.TaskCount
		}
		return out
	}
	ownerByReason, memberByReason := byReason(ownerFailures), byReason(memberFailures)
	if len(ownerByReason) != len(memberByReason) {
		t.Errorf("failures/by-agent: reason set changed — owner %v, member %v", ownerByReason, memberByReason)
	}
	for reason, count := range ownerByReason {
		if memberByReason[reason] != count {
			t.Errorf("failures/by-agent: reason %q total changed — owner %d, member %d",
				reason, count, memberByReason[reason])
		}
	}
	// The private agent's failure still has to be COUNTED — it rides inside the
	// bucket — just not be attributable to it.
	if ownerByReason["runtime_offline"] < 1 {
		t.Fatalf("test setup: expected the private agent's runtime_offline failure in the owner view, got %v", ownerByReason)
	}
}

func dashboardAgentIDSet[T any](rows []T, idOf func(T) string) map[string]struct{} {
	out := make(map[string]struct{}, len(rows))
	for _, r := range rows {
		out[idOf(r)] = struct{}{}
	}
	return out
}

func assertDashboardAgentPresence(
	t *testing.T,
	label string,
	ids map[string]struct{},
	want dashboardAgentPresence,
	privateAgentID, publicAgentID string,
) {
	t.Helper()
	check := func(what, id string, expected bool) {
		_, got := ids[id]
		if got != expected {
			t.Errorf("%s: %s (%s) present=%v, want %v", label, what, id, got, expected)
		}
	}
	check("private agent", privateAgentID, want.privateAgent)
	check("public agent", publicAgentID, want.publicAgent)
	check("restricted bucket", restrictedAgentsRowID, want.sentinel)
}
