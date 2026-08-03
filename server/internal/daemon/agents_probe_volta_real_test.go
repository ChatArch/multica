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
// The fixture tests in agents_probe_volta_test.go model Volta's shim behavior;
// these prove the model matches the real thing, which is the one gap a fixture
// cannot close by construction.
//
// Skipped unless MULTICA_VERIFY_VOLTA_HOME points at a Volta home (the directory
// containing bin/volta-shim), so CI and ordinary `go test` runs are unaffected:
//
//	export VOLTA_HOME=/tmp/volta-check
//	curl -fsSL https://get.volta.sh | bash -s -- --skip-setup   # no profile edits
//	export PATH="$VOLTA_HOME/bin:$PATH"
//	volta install node
//	volta install @anthropic-ai/claude-code @openai/codex
//	MULTICA_VERIFY_VOLTA_HOME="$VOLTA_HOME" go test ./internal/daemon -run TestRealVolta -v
//
// Verified on macOS arm64 with Volta 2.0.2, claude-code 2.1.220, codex 0.146.0.
// Note those two differ in a way that matters: claude ships a native binary,
// while codex is a `#!/usr/bin/env node` script — which is why resolution has to
// carry Volta's Node platform and not just a path.

// requireRealVolta skips unless a real Volta home is configured.
//
// Deliberately does NOT put Volta's bin dir on PATH. Doing so hides the whole
// point of item 3: the shim dir contains a `node` shim, so a package script would
// find node by accident and the missing execution environment would go unnoticed.
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
	// The daemon inherits VOLTA_HOME; a default ~/.volta install needs nothing.
	t.Setenv("VOLTA_HOME", voltaHome)
	resetVoltaResolveCache(t)
	return voltaHome, binDir
}

// systemOnlyPath is a PATH with no node and no Volta on it, modelling the
// GUI-launched daemon this fix has to work under.
func systemOnlyPath() string {
	return "/usr/bin" + string(os.PathListSeparator) + "/bin"
}

// TestRealVolta_PlainSymlinkResolutionIsUnrunnable is the counterfactual: plain
// canonicalization collapses every managed command onto one volta-shim, which
// refuses to run.
func TestRealVolta_PlainSymlinkResolutionIsUnrunnable(t *testing.T) {
	_, binDir := requireRealVolta(t)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+systemOnlyPath())

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

// TestRealVolta_ResolvesConcreteBinaryWithEnvironment asserts the daemon pins
// what Volta itself reports AND carries an environment that makes it runnable
// from a PATH that has no node at all.
func TestRealVolta_ResolvesConcreteBinaryWithEnvironment(t *testing.T) {
	_, binDir := requireRealVolta(t)
	// Volta's own bin dir is on PATH only so LookPath can find the alias; the
	// version probe's environment is what the resolution supplies.
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+systemOnlyPath())

	for _, command := range []string{"claude", "codex"} {
		out, err := exec.Command(filepath.Join(binDir, "volta"), "which", command).Output()
		if err != nil {
			t.Fatalf("volta which %s: %v", command, err)
		}
		want := strings.TrimSpace(string(out))

		resolved, err := resolveAgentExecutable(command)
		if err != nil {
			t.Fatalf("%s: resolveAgentExecutable: %v", command, err)
		}
		if resolved.Path != want {
			t.Errorf("%s path = %q, want the `volta which` answer %q", command, resolved.Path, want)
		}
		if len(resolved.PathDirs) == 0 {
			t.Errorf("%s carries no PATH dirs; a node-script package bin would be unrunnable", command)
		}

		version, err := agent.DetectVersionWithPathDirs(context.Background(), resolved.Path, resolved.PathDirs)
		if err != nil {
			t.Fatalf("%s: DetectVersionWithPathDirs(%q): %v", command, resolved.Path, err)
		}
		if err := agent.CheckMinVersion(command, version); err != nil {
			t.Errorf("%s version %q failed the real min-version gate: %v", command, version, err)
		}
		t.Logf("%s -> %s (version %q, pathDirs %v)", command, resolved.Path, version, resolved.PathDirs)
	}
}

// TestRealVolta_ResolvedEnvironmentIsRequired proves the carried environment is
// load-bearing on a real install rather than decorative: at least one of the real
// CLIs must fail without it. codex 0.146.0 is a `#!/usr/bin/env node` script and
// does; claude ships a native binary and does not, so this asserts on the set.
func TestRealVolta_ResolvedEnvironmentIsRequired(t *testing.T) {
	_, binDir := requireRealVolta(t)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+systemOnlyPath())

	needsEnv := 0
	for _, command := range []string{"claude", "codex"} {
		resolved, err := resolveAgentExecutable(command)
		if err != nil {
			t.Fatalf("%s: resolveAgentExecutable: %v", command, err)
		}
		// Bare PATH, no node, no Volta: what a GUI-launched daemon looks like.
		t.Setenv("PATH", systemOnlyPath())
		_, withoutEnv := agent.DetectVersion(context.Background(), resolved.Path)
		_, withEnv := agent.DetectVersionWithPathDirs(context.Background(), resolved.Path, resolved.PathDirs)
		t.Setenv("PATH", binDir+string(os.PathListSeparator)+systemOnlyPath())

		if withEnv != nil {
			t.Errorf("%s failed even with the resolved environment: %v", command, withEnv)
		}
		if withoutEnv != nil {
			needsEnv++
			t.Logf("%s requires the resolved environment (without it: %v)", command, withoutEnv)
		}
	}
	if needsEnv == 0 {
		t.Error("no real CLI required the resolved environment; if the installed packages " +
			"are all native binaries this check cannot prove anything — install a node-script CLI (codex)")
	}
}

// TestRealVolta_RegistersRuntimes is the user-visible outcome from the report:
// Volta-managed CLIs must reach the registration payload.
func TestRealVolta_RegistersRuntimes(t *testing.T) {
	_, binDir := requireRealVolta(t)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+systemOnlyPath())
	isolateAgentDiscovery(t)

	d := freshDaemon("")
	d.cfg.Agents = probeAgentCLIs()

	registered := map[string]string{}
	for _, rt := range d.detectBuiltinRuntimes(context.Background()) {
		registered[rt["type"]] = rt["version"]
	}
	t.Logf("registered: %#v", registered)
	for _, command := range []string{"claude", "codex"} {
		if _, ok := registered[command]; !ok {
			t.Errorf("%s missing from the registration payload; skipped reasons: %#v",
				command, d.skippedAgentsSnapshot())
		}
	}
}
