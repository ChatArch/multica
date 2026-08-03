package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/pkg/agent"
)

// Opt-in integration coverage for #6183 against a REAL Volta installation.
// The fixture-based tests in agents_probe_volta_test.go model Volta's shim
// behavior; these prove the model matches the real thing, which is the one gap
// a fixture cannot close by construction.
//
// Skipped unless MULTICA_VERIFY_VOLTA_HOME points at a Volta home (the
// directory containing bin/volta-shim), so CI and ordinary `go test` runs are
// unaffected. To run it:
//
//	export VOLTA_HOME=/tmp/volta-check
//	curl -fsSL https://get.volta.sh | bash -s -- --skip-setup   # --skip-setup: no profile edits
//	export PATH="$VOLTA_HOME/bin:$PATH"
//	volta install node
//	volta install @anthropic-ai/claude-code @openai/codex
//	MULTICA_VERIFY_VOLTA_HOME="$VOLTA_HOME" go test ./internal/daemon -run TestRealVolta -v
//
// Verified on macOS arm64 with Volta 2.0.2, claude-code 2.1.220, codex 0.146.0.

// requireRealVolta skips unless a real Volta home is configured, and points the
// resolution environment at it.
func requireRealVolta(t *testing.T) (voltaHome, binDir string) {
	t.Helper()
	voltaHome = strings.TrimSpace(os.Getenv("MULTICA_VERIFY_VOLTA_HOME"))
	if voltaHome == "" {
		t.Skip("set MULTICA_VERIFY_VOLTA_HOME to a Volta home to run the real-Volta checks")
	}
	binDir = filepath.Join(voltaHome, "bin")
	if _, err := os.Stat(filepath.Join(binDir, voltaShimName)); err != nil {
		t.Fatalf("no %s in %s: %v", voltaShimName, binDir, err)
	}
	// The daemon inherits VOLTA_HOME from its environment; a default ~/.volta
	// install needs nothing set. /usr/bin stays on PATH because the real CLIs
	// are `#!/usr/bin/env node` scripts.
	t.Setenv("VOLTA_HOME", voltaHome)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+"/usr/bin"+string(os.PathListSeparator)+"/bin")
	resetVoltaResolveCache(t)
	return voltaHome, binDir
}

// TestRealVolta_PlainSymlinkResolutionIsUnrunnable is the counterfactual: it
// pins why plain canonicalization cannot be used for a Volta alias. Every
// managed command collapses onto one volta-shim, which refuses to run.
func TestRealVolta_PlainSymlinkResolutionIsUnrunnable(t *testing.T) {
	_, binDir := requireRealVolta(t)

	alias := filepath.Join(binDir, "claude")
	collapsed, err := filepath.EvalSymlinks(alias)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", alias, err)
	}
	if filepath.Base(collapsed) != voltaShimName {
		t.Fatalf("expected %q to collapse onto %s, got %q", alias, voltaShimName, collapsed)
	}
	if _, err := agent.DetectVersion(context.Background(), collapsed); err == nil {
		t.Fatal("collapsed shim path unexpectedly answered --version; " +
			"the whole exception exists because it exits 126")
	}
}

// TestRealVolta_ResolvesConcreteBinary asserts the daemon pins exactly what
// Volta itself reports, so the path we gate is the path we launch.
func TestRealVolta_ResolvesConcreteBinary(t *testing.T) {
	_, binDir := requireRealVolta(t)

	for _, command := range []string{"claude", "codex"} {
		out, err := exec.Command(filepath.Join(binDir, "volta"), "which", command).Output()
		if err != nil {
			t.Fatalf("volta which %s: %v", command, err)
		}
		want := strings.TrimSpace(string(out))

		got, err := resolveAgentExecutablePath(command)
		if err != nil {
			t.Fatalf("%s: resolveAgentExecutablePath: %v", command, err)
		}
		if got != want {
			t.Errorf("%s path = %q, want the `volta which` answer %q", command, got, want)
		}

		version, err := agent.DetectVersion(context.Background(), got)
		if err != nil {
			t.Fatalf("%s: DetectVersion(%q): %v", command, got, err)
		}
		if err := agent.CheckMinVersion(command, version); err != nil {
			t.Errorf("%s version %q failed the real min-version gate: %v", command, version, err)
		}
	}
}

// TestRealVolta_RegistersRuntimes is the user-visible outcome from the report:
// Volta-managed CLIs must reach the registration payload.
func TestRealVolta_RegistersRuntimes(t *testing.T) {
	requireRealVolta(t)
	isolateAgentDiscovery(t)

	d := freshDaemon("")
	d.cfg.Agents = probeAgentCLIs()

	registered := map[string]string{}
	for _, rt := range d.detectBuiltinRuntimes(context.Background()) {
		registered[rt["type"]] = rt["version"]
	}
	for _, command := range []string{"claude", "codex"} {
		if _, ok := registered[command]; !ok {
			t.Errorf("%s missing from the registration payload; skipped reasons: %#v",
				command, d.skippedAgentsSnapshot())
		}
	}
}
