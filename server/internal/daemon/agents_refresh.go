package daemon

import (
	"context"
	"sort"
	"time"
)

// agentDiscoveryInterval is how often a running daemon re-checks which agent
// CLIs are installed. Each round is a handful of exec.LookPath calls — the
// login-shell fallback is separately rate-limited by the much longer
// shellResolveTTL — so this can be short enough that installing a CLI feels
// immediate. Overridable for tests.
var agentDiscoveryInterval = 2 * time.Minute

// agentDiscoveryLoop periodically re-runs CLI discovery so a provider installed
// while the daemon is running comes online without a restart (MUL-5439).
func (d *Daemon) agentDiscoveryLoop(ctx context.Context) {
	ticker := time.NewTicker(agentDiscoveryInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.refreshAgentAvailability(ctx)
		}
	}
}

// agents returns the current built-in agent availability set.
//
// The returned map is shared and MUST NOT be mutated: refreshAgentAvailability
// publishes a whole new map rather than editing this one, which is what makes
// unlocked reads from task-execution paths safe.
func (d *Daemon) agents() map[string]AgentEntry {
	if m := d.agentsAvailable.Load(); m != nil {
		return *m
	}
	// Zero-value Daemon (tests construct one directly): fall back to the
	// startup config so behavior matches the pre-refresh code path.
	return d.cfg.Agents
}

// setSkippedAgents replaces the diagnostic "discovered but not registered" set
// reported on /health. Called at the end of every registration round with the
// providers that round dropped.
func (d *Daemon) setSkippedAgents(skipped map[string]string) {
	d.skippedAgentsMu.Lock()
	defer d.skippedAgentsMu.Unlock()
	d.skippedAgents = skipped
}

// skippedAgentsSnapshot copies the current skip reasons for the health handler.
func (d *Daemon) skippedAgentsSnapshot() map[string]string {
	d.skippedAgentsMu.RLock()
	defer d.skippedAgentsMu.RUnlock()
	if len(d.skippedAgents) == 0 {
		return nil
	}
	out := make(map[string]string, len(d.skippedAgents))
	for name, reason := range d.skippedAgents {
		out[name] = reason
	}
	return out
}

// refreshAgentAvailability re-runs CLI discovery on a live daemon and registers
// any provider that appeared since the last probe.
//
// Why this exists: the availability set used to be built exactly once, in
// LoadConfig, so a CLI installed while the daemon was running was invisible
// until the daemon restarted. On Desktop that is worse than it sounds — the app
// defaults to autoStop=false and auto-start only compares CLI versions, so
// quitting and reopening the app does not restart the daemon, and the user is
// left with a CLI that runs fine in their shell but never appears under
// Runtimes (MUL-5439, GH #6077).
//
// Deliberately one-directional: only providers GAINED since the last probe
// trigger a refresh. A provider that stops resolving is kept, because a
// transient PATH difference (a daemon restarted from a narrower environment, a
// version manager mid-upgrade) would otherwise tear down a runtime that is
// working — and possibly executing a task. Removal stays the job of an explicit
// restart, where the user chose the environment.
//
// Returns the providers that were added, for logging and tests.
func (d *Daemon) refreshAgentAvailability(ctx context.Context) []string {
	current := d.agents()
	probed := probeAgentCLIs()

	var gained []string
	for name := range probed {
		if _, known := current[name]; !known {
			gained = append(gained, name)
		}
	}
	if len(gained) == 0 {
		return nil
	}
	sort.Strings(gained)

	// Copy-on-write: build the union, then publish. Entries for providers we
	// already knew about are preserved as-is so this never fights the pinned
	// path / self-heal bookkeeping (resolvedPaths) for a running provider.
	merged := make(map[string]AgentEntry, len(current)+len(gained))
	for name, entry := range current {
		merged[name] = entry
	}
	for _, name := range gained {
		merged[name] = probed[name]
	}
	d.agentsAvailable.Store(&merged)
	d.logger.Info("agent CLI discovered after startup; registering", "providers", gained)

	d.registerNewlyAvailableRuntimes(ctx)
	return gained
}

// registerNewlyAvailableRuntimes re-registers every tracked workspace with the
// current availability set, so providers added by refreshAgentAvailability get
// runtime rows without restarting the daemon.
//
// One probe round serves every workspace (same reasoning as the sync loop:
// built-in CLIs are per machine, not per workspace). RecoverOrphans is
// deliberately NOT called here — unlike the runtime_gone recovery path, the
// surviving runtime IDs may still be executing tasks for the user, and failing
// those as orphans would kill live work (MUL-3332).
func (d *Daemon) registerNewlyAvailableRuntimes(ctx context.Context) {
	d.mu.Lock()
	workspaceIDs := make([]string, 0, len(d.workspaces))
	for id := range d.workspaces {
		workspaceIDs = append(workspaceIDs, id)
	}
	d.mu.Unlock()
	if len(workspaceIDs) == 0 {
		return
	}
	sort.Strings(workspaceIDs)

	builtins := d.detectBuiltinRuntimes(ctx)
	if len(builtins) == 0 {
		return
	}

	var changed bool
	for _, id := range workspaceIDs {
		resp, profileSig, err := d.registerRuntimesForWorkspaceBatch(ctx, id, builtins)
		if err != nil {
			d.logger.Warn("register newly available runtimes failed", "workspace_id", id, "error", err)
			continue
		}
		newIDs, _, ok := d.applyRegisterResponseInPlace(id, resp, profileSig)
		if !ok {
			continue
		}
		if len(newIDs) > 0 {
			changed = true
		}
	}
	if changed {
		d.notifyRuntimeSetChanged()
	}
}
