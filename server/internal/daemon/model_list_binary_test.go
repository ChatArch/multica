package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/multica-ai/multica/server/pkg/agent"
)

// modelListFixture stands up a Daemon whose model-list reports are captured and
// whose agent.ListModels call is stubbed, so a test can assert exactly which
// executable path model discovery enumerated.
type modelListFixture struct {
	daemon *Daemon

	mu             sync.Mutex
	listedProvider string
	listedPath     string
	listCalls      int
	report         map[string]any
}

func newModelListFixture(t *testing.T) *modelListFixture {
	t.Helper()
	withFastLocalSkillReportBackoffs(t)

	fx := &modelListFixture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		fx.mu.Lock()
		fx.report = body
		fx.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	t.Cleanup(srv.Close)

	d := freshDaemon(srv.URL)
	d.profileLaunchSpecs = make(map[string]profileLaunchSpec)
	fx.daemon = d

	orig := listModels
	listModels = func(_ context.Context, provider, execPath string) ([]agent.Model, error) {
		fx.mu.Lock()
		fx.listedProvider = provider
		fx.listedPath = execPath
		fx.listCalls++
		fx.mu.Unlock()
		return []agent.Model{{ID: "m-1", Label: "M 1"}}, nil
	}
	t.Cleanup(func() { listModels = orig })

	return fx
}

// addCustomRuntime registers a custom-profile runtime the way
// registerRuntimesForWorkspace would: a runtime row carrying profile_id plus
// the profile's resolved executable path.
func (fx *modelListFixture) addCustomRuntime(runtimeID, provider, profileID, path string) Runtime {
	rt := Runtime{ID: runtimeID, Provider: provider, ProfileID: profileID, Status: "online"}
	fx.daemon.mu.Lock()
	fx.daemon.runtimeIndex[runtimeID] = rt
	fx.daemon.mu.Unlock()
	fx.daemon.recordProfileLaunch(profileID, path, "1.2.3", nil)
	return rt
}

func (fx *modelListFixture) listed() (provider, path string, calls int) {
	fx.mu.Lock()
	defer fx.mu.Unlock()
	return fx.listedProvider, fx.listedPath, fx.listCalls
}

func (fx *modelListFixture) reported() map[string]any {
	fx.mu.Lock()
	defer fx.mu.Unlock()
	return fx.report
}

// TestHandleModelList_CustomProfileEnumeratesProfileBinary pins MUL-5471: a
// custom runtime profile executes its own command_name, so the model picker
// must enumerate that binary — not the built-in provider binary that happens to
// share the protocol family on this host.
func TestHandleModelList_CustomProfileEnumeratesProfileBinary(t *testing.T) {
	fx := newModelListFixture(t)
	// Built-in codex is installed too, at a different (newer) path. Before the
	// fix this path is what discovery enumerated.
	fx.daemon.cfg.Agents = map[string]AgentEntry{
		"codex": {Path: "/usr/local/bin/codex", Command: "codex"},
	}
	rt := fx.addCustomRuntime("rt-1", "codex", "prof-1", "/opt/bin/company-codex")

	fx.daemon.handleModelList(context.Background(), rt, "req-1")

	provider, path, calls := fx.listed()
	if calls != 1 {
		t.Fatalf("ListModels calls = %d, want 1", calls)
	}
	if provider != "codex" {
		t.Errorf("ListModels provider = %q, want codex", provider)
	}
	if path != "/opt/bin/company-codex" {
		t.Errorf("ListModels executable path = %q, want the profile command /opt/bin/company-codex", path)
	}
	if got := fx.reported()["status"]; got != "completed" {
		t.Errorf("reported status = %v, want completed", got)
	}
}

// TestHandleModelList_CustomProfileWithoutBuiltInProvider pins the harsher
// variant of MUL-5471: on a host with no built-in agent of the profile's
// protocol family, discovery used to fail with "no agent configured for
// provider" even though the profile's binary exists and is what would run.
func TestHandleModelList_CustomProfileWithoutBuiltInProvider(t *testing.T) {
	fx := newModelListFixture(t)
	// Custom-only host: no built-in agents at all.
	fx.daemon.cfg.Agents = map[string]AgentEntry{}
	rt := fx.addCustomRuntime("rt-2", "codex", "prof-2", "/opt/bin/company-codex")

	fx.daemon.handleModelList(context.Background(), rt, "req-2")

	_, path, calls := fx.listed()
	if calls != 1 {
		t.Fatalf("ListModels calls = %d, want 1 (custom profile must not fail the built-in lookup)", calls)
	}
	if path != "/opt/bin/company-codex" {
		t.Errorf("ListModels executable path = %q, want /opt/bin/company-codex", path)
	}
	report := fx.reported()
	if got := report["status"]; got != "completed" {
		t.Fatalf("reported status = %v (error=%v), want completed", got, report["error"])
	}
}

// TestHandleModelList_BuiltInRuntimeUsesProviderBinary guards the unchanged
// half of the contract: a runtime with no profile still enumerates the
// configured built-in provider binary.
func TestHandleModelList_BuiltInRuntimeUsesProviderBinary(t *testing.T) {
	fx := newModelListFixture(t)
	// Command is intentionally empty: resolveAgentEntry then returns the pinned
	// entry as-is instead of re-resolving `codex` on the test host, so the
	// assertion below is about branch selection, not MUL-4486 self-healing.
	fx.daemon.cfg.Agents = map[string]AgentEntry{
		"codex": {Path: "/usr/local/bin/codex"},
	}
	// A stale profile spec for an unrelated profile must not leak into a
	// built-in runtime's discovery.
	fx.daemon.recordProfileLaunch("prof-other", "/opt/bin/company-codex", "1.2.3", nil)
	rt := Runtime{ID: "rt-3", Provider: "codex", Status: "online"}
	fx.daemon.mu.Lock()
	fx.daemon.runtimeIndex[rt.ID] = rt
	fx.daemon.mu.Unlock()

	fx.daemon.handleModelList(context.Background(), rt, "req-3")

	_, path, calls := fx.listed()
	if calls != 1 {
		t.Fatalf("ListModels calls = %d, want 1", calls)
	}
	if path != "/usr/local/bin/codex" {
		t.Errorf("ListModels executable path = %q, want the built-in /usr/local/bin/codex", path)
	}
}

// TestHandleModelList_UnknownProviderStillFails keeps the genuine
// misconfiguration loud: no profile and no built-in entry means there is no
// binary to enumerate, and the UI should say so rather than show an empty list
// silently.
func TestHandleModelList_UnknownProviderStillFails(t *testing.T) {
	fx := newModelListFixture(t)
	fx.daemon.cfg.Agents = map[string]AgentEntry{}
	rt := Runtime{ID: "rt-4", Provider: "codex", Status: "online"}

	fx.daemon.handleModelList(context.Background(), rt, "req-4")

	if _, _, calls := fx.listed(); calls != 0 {
		t.Fatalf("ListModels calls = %d, want 0", calls)
	}
	report := fx.reported()
	if got := report["status"]; got != "failed" {
		t.Fatalf("reported status = %v, want failed", got)
	}
	if got, _ := report["error"].(string); got != `no agent configured for provider "codex"` {
		t.Errorf("reported error = %q", got)
	}
}
