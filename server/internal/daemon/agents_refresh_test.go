package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"testing"
	"time"
)

// stubAgentProbe replaces CLI discovery for the duration of a test. The returned
// setter swaps in the next probe result, simulating the user installing or
// uninstalling a CLI while the daemon runs.
//
// Goroutine-safe: agentDiscoveryLoop calls the probe from its own goroutine
// while the test body swaps the result.
func stubAgentProbe(t *testing.T, initial map[string]AgentEntry) func(map[string]AgentEntry) {
	t.Helper()
	orig := probeAgentCLIs
	t.Cleanup(func() { probeAgentCLIs = orig })
	var (
		mu      sync.Mutex
		current = initial
	)
	probeAgentCLIs = func() map[string]AgentEntry {
		mu.Lock()
		defer mu.Unlock()
		// Copy so the caller can iterate without holding the lock.
		out := make(map[string]AgentEntry, len(current))
		for name, entry := range current {
			out[name] = entry
		}
		return out
	}
	return func(next map[string]AgentEntry) {
		mu.Lock()
		defer mu.Unlock()
		current = next
	}
}

// TestRefreshAgentAvailability_RegistersCLIInstalledAfterStartup is the MUL-5439
// regression (GH #6077): the availability set used to be built once in
// LoadConfig, so a CLI installed while the daemon was running never registered
// — and on Desktop, quitting the app does not restart the daemon, so the user
// had no way to recover short of an explicit daemon restart.
func TestRefreshAgentAvailability_RegistersCLIInstalledAfterStartup(t *testing.T) {
	fx := newBatchFixture(t)
	d := fx.daemon
	d.cfg.Agents = map[string]AgentEntry{"codex": {Path: "/fake/codex"}}
	fx.setWorkspaces(WorkspaceInfo{ID: "ws-1", Name: "one"})

	// Startup: only codex exists.
	setProbe := stubAgentProbe(t, map[string]AgentEntry{"codex": {Path: "/fake/codex"}})
	if err := d.syncWorkspacesFromAPI(context.Background(), false); err != nil {
		t.Fatalf("syncWorkspacesFromAPI: %v", err)
	}
	types, _ := fx.registrationFor("ws-1")
	if len(types) != 1 || types[0] != "codex" {
		t.Fatalf("initial registration = %v, want [codex]", types)
	}

	// The user now installs Antigravity.
	setProbe(map[string]AgentEntry{
		"codex":       {Path: "/fake/codex"},
		"antigravity": {Path: "/fake/agy"},
	})

	gained := d.refreshAgentAvailability(context.Background())
	if len(gained) != 1 || gained[0] != "antigravity" {
		t.Fatalf("gained = %v, want [antigravity]", gained)
	}
	if _, ok := d.agents()["antigravity"]; !ok {
		t.Error("antigravity missing from the availability set after refresh")
	}

	types, calls := fx.registrationFor("ws-1")
	if calls != 2 {
		t.Fatalf("ws-1 registered %d times, want 2 (initial + refresh)", calls)
	}
	sort.Strings(types)
	if len(types) != 2 || types[0] != "antigravity" || types[1] != "codex" {
		t.Errorf("refreshed registration = %v, want [antigravity codex]", types)
	}
}

// TestRefreshAgentAvailability_NoopWhenNothingNew keeps the discovery loop cheap
// and silent: an unchanged probe result must not re-register anything.
func TestRefreshAgentAvailability_NoopWhenNothingNew(t *testing.T) {
	fx := newBatchFixture(t)
	d := fx.daemon
	d.cfg.Agents = map[string]AgentEntry{"codex": {Path: "/fake/codex"}}
	fx.setWorkspaces(WorkspaceInfo{ID: "ws-1", Name: "one"})
	stubAgentProbe(t, map[string]AgentEntry{"codex": {Path: "/fake/codex"}})

	if err := d.syncWorkspacesFromAPI(context.Background(), false); err != nil {
		t.Fatalf("syncWorkspacesFromAPI: %v", err)
	}
	before := fx.registerCallCount()

	if gained := d.refreshAgentAvailability(context.Background()); gained != nil {
		t.Fatalf("gained = %v, want none", gained)
	}
	if after := fx.registerCallCount(); after != before {
		t.Errorf("register calls went %d -> %d, want no change", before, after)
	}
}

// TestRefreshAgentAvailability_KeepsProviderThatStoppedResolving pins the
// one-directional contract. A provider vanishing from a probe is usually an
// environment difference or a version manager mid-upgrade, not an uninstall, so
// dropping it would tear down a runtime that may be executing a task. Removal
// is left to an explicit restart.
func TestRefreshAgentAvailability_KeepsProviderThatStoppedResolving(t *testing.T) {
	fx := newBatchFixture(t)
	d := fx.daemon
	d.cfg.Agents = map[string]AgentEntry{
		"codex":  {Path: "/fake/codex"},
		"claude": {Path: "/fake/claude"},
	}
	fx.setWorkspaces(WorkspaceInfo{ID: "ws-1", Name: "one"})
	setProbe := stubAgentProbe(t, map[string]AgentEntry{
		"codex":  {Path: "/fake/codex"},
		"claude": {Path: "/fake/claude"},
	})
	if err := d.syncWorkspacesFromAPI(context.Background(), false); err != nil {
		t.Fatalf("syncWorkspacesFromAPI: %v", err)
	}
	before := fx.registerCallCount()

	// claude disappears from the probe.
	setProbe(map[string]AgentEntry{"codex": {Path: "/fake/codex"}})

	if gained := d.refreshAgentAvailability(context.Background()); gained != nil {
		t.Fatalf("gained = %v, want none", gained)
	}
	if _, ok := d.agents()["claude"]; !ok {
		t.Error("claude was dropped from the availability set; refresh must be additive only")
	}
	if after := fx.registerCallCount(); after != before {
		t.Errorf("register calls went %d -> %d, want no change on a lost provider", before, after)
	}
}

// TestRefreshAgentAvailability_GainWithNoWorkspacesStillPublishes covers a
// bootstrap daemon: the provider must join the availability set (so /health and
// the next registration see it) even when there is no workspace to register yet.
func TestRefreshAgentAvailability_GainWithNoWorkspacesStillPublishes(t *testing.T) {
	fx := newBatchFixture(t)
	d := fx.daemon
	d.cfg.Agents = map[string]AgentEntry{}
	stubAgentProbe(t, map[string]AgentEntry{"antigravity": {Path: "/fake/agy"}})

	gained := d.refreshAgentAvailability(context.Background())
	if len(gained) != 1 || gained[0] != "antigravity" {
		t.Fatalf("gained = %v, want [antigravity]", gained)
	}
	if _, ok := d.agents()["antigravity"]; !ok {
		t.Error("antigravity missing from the availability set")
	}
	if got := fx.registerCallCount(); got != 0 {
		t.Errorf("made %d register calls with no tracked workspaces, want 0", got)
	}
}

// TestHealth_ReportsSkippedAgents covers the diagnostic half of MUL-5439: a CLI
// that IS installed but gets dropped at registration (version undetectable,
// below minimum) used to be indistinguishable from a CLI that is not installed
// — both simply produced no runtime.
func TestHealth_ReportsSkippedAgents(t *testing.T) {
	fx := newBatchFixture(t)
	d := fx.daemon
	d.cfg.Agents = map[string]AgentEntry{
		"codex":       {Path: "/fake/codex"},
		"antigravity": {Path: "/fake/agy"},
	}
	fx.setProbeErr(func(path string, _ int) error {
		if path == "/fake/agy" {
			return context.DeadlineExceeded
		}
		return nil
	})

	d.detectBuiltinRuntimes(context.Background())

	rec := httptest.NewRecorder()
	d.healthHandler(time.Now())(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("health status = %d, want 200", rec.Code)
	}
	var resp HealthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	reason, ok := resp.SkippedAgents["antigravity"]
	if !ok {
		t.Fatalf("skipped_agents = %v, want an antigravity entry", resp.SkippedAgents)
	}
	if reason == "" {
		t.Error("antigravity skip reason is empty; the UI needs something to show")
	}
	if _, ok := resp.SkippedAgents["codex"]; ok {
		t.Error("codex registered successfully but is listed as skipped")
	}

	// A later round where the CLI works must clear the entry, not accumulate.
	fx.setProbeErr(nil)
	d.detectBuiltinRuntimes(context.Background())
	if got := d.skippedAgentsSnapshot(); len(got) != 0 {
		t.Errorf("skipped agents = %v after a clean round, want empty", got)
	}
}

// TestCachedShellResolvedAgents_ReusesResultWithinTTL guards the cost side of
// running discovery periodically: resolveAgentsViaLoginShell forks the user's
// login shell and runs their rc files, so repeated probes must not repeat it.
func TestCachedShellResolvedAgents_ReusesResultWithinTTL(t *testing.T) {
	var calls int
	orig := resolveAgentsViaLoginShell
	t.Cleanup(func() { resolveAgentsViaLoginShell = orig })
	resolveAgentsViaLoginShell = func([]string) map[string]string {
		calls++
		return map[string]string{"agy": "/fake/agy"}
	}
	resetShellResolveCacheForTest(t)

	for i := 0; i < 5; i++ {
		if got := cachedShellResolvedAgents()["agy"]; got != "/fake/agy" {
			t.Fatalf("resolution %d = %q, want /fake/agy", i, got)
		}
	}
	if calls != 1 {
		t.Errorf("forked the login shell %d times, want 1 within the TTL", calls)
	}

	// An expired TTL must re-resolve, otherwise a CLI installed into a
	// login-shell-only PATH dir would never be discovered.
	shellResolveMu.Lock()
	shellResolvedAt = time.Now().Add(-2 * shellResolveTTL)
	shellResolveMu.Unlock()
	cachedShellResolvedAgents()
	if calls != 2 {
		t.Errorf("forked the login shell %d times, want 2 after the TTL expired", calls)
	}
}

// TestCachedShellResolvedAgents_InvalidatesOnEnvChange keeps the cache honest
// when the resolution-relevant environment changes.
func TestCachedShellResolvedAgents_InvalidatesOnEnvChange(t *testing.T) {
	var calls int
	orig := resolveAgentsViaLoginShell
	t.Cleanup(func() { resolveAgentsViaLoginShell = orig })
	resolveAgentsViaLoginShell = func([]string) map[string]string {
		calls++
		return map[string]string{}
	}
	resetShellResolveCacheForTest(t)

	t.Setenv("PATH", "/one")
	cachedShellResolvedAgents()
	t.Setenv("PATH", "/two")
	cachedShellResolvedAgents()
	if calls != 2 {
		t.Errorf("resolved %d times across a PATH change, want 2", calls)
	}
}

// TestAgentDiscoveryLoop_PicksUpInstallWithoutRestart covers the loop wiring:
// the fix is only useful if something actually calls the refresh on a schedule.
func TestAgentDiscoveryLoop_PicksUpInstallWithoutRestart(t *testing.T) {
	fx := newBatchFixture(t)
	d := fx.daemon
	d.cfg.Agents = map[string]AgentEntry{"codex": {Path: "/fake/codex"}}
	fx.setWorkspaces(WorkspaceInfo{ID: "ws-1", Name: "one"})
	setProbe := stubAgentProbe(t, map[string]AgentEntry{"codex": {Path: "/fake/codex"}})
	if err := d.syncWorkspacesFromAPI(context.Background(), false); err != nil {
		t.Fatalf("syncWorkspacesFromAPI: %v", err)
	}

	origInterval := agentDiscoveryInterval
	agentDiscoveryInterval = 5 * time.Millisecond
	t.Cleanup(func() { agentDiscoveryInterval = origInterval })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	// Stop the loop and WAIT for it before the test returns: the fixture's
	// t.Cleanup restores the global version-probe stub, which would otherwise
	// race a probe still in flight inside the loop goroutine.
	defer func() {
		cancel()
		<-done
	}()
	go func() {
		defer close(done)
		d.agentDiscoveryLoop(ctx)
	}()

	setProbe(map[string]AgentEntry{
		"codex":       {Path: "/fake/codex"},
		"antigravity": {Path: "/fake/agy"},
	})

	// Poll on the REGISTRATION, not just the availability set: the set is
	// published before the re-registration call, so asserting on it alone would
	// race the register round.
	deadline := time.Now().Add(3 * time.Second)
	for {
		types, _ := fx.registrationFor("ws-1")
		sort.Strings(types)
		if len(types) == 2 && types[0] == "antigravity" && types[1] == "codex" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("discovery loop never registered the newly installed CLI; last registration = %v", types)
		}
		time.Sleep(5 * time.Millisecond)
	}

	if _, ok := d.agents()["antigravity"]; !ok {
		t.Error("antigravity missing from the availability set")
	}
}

func resetShellResolveCacheForTest(t *testing.T) {
	t.Helper()
	reset := func() {
		shellResolveMu.Lock()
		shellResolveCache = nil
		shellResolveKey = ""
		shellResolvedAt = time.Time{}
		shellResolveMu.Unlock()
	}
	reset()
	t.Cleanup(reset)
}
