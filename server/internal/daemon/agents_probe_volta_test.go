package daemon

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/agent"
)

// Volta installs one trampoline binary and symlinks every managed command to
// it, dispatching on the name it was invoked as (#6183). Resolving that symlink
// collapses claude/codex/pi onto the same volta-shim path and the shim then
// exits 126, so the daemon asks Volta for the concrete binary instead — plus the
// Node platform directory that binary needs to run.

// voltaFixture is a ~/.volta lookalike.
type voltaFixture struct {
	binDir   string // shim dir: volta, volta-shim, one symlink per command
	imageDir string // concrete per-tool binaries
	nodeDir  string // Volta's Node platform bin dir
}

func (f voltaFixture) alias(command string) string    { return filepath.Join(f.binDir, command) }
func (f voltaFixture) shim() string                   { return filepath.Join(f.binDir, voltaShimName) }
func (f voltaFixture) concrete(command string) string { return filepath.Join(f.imageDir, command) }

// voltaFixtureVersions are the versions the concrete fixture binaries report.
// They must clear agent.MinVersions (claude >= 2.0.0, codex >= 0.100.0) or the
// registration-level tests would prove nothing: the provider would be dropped by
// the min-version gate rather than by the bug under test.
var voltaFixtureVersions = map[string]string{
	"claude": "2.1.0 (Claude Code)",
	"codex":  "codex-cli 0.140.0",
	"pi":     "pi 0.9.0",
}

type voltaFixtureOpts struct {
	commands []string
	// omitVolta models an install whose `volta` binary cannot be run.
	omitVolta bool
	// projectDir, when set, makes `volta which` answer with a project-local
	// binary whenever it is invoked from inside that directory — which is what
	// real Volta does (upstream src/command/which.rs prefers the project bin).
	projectDir string
}

// newVoltaFixture builds the shim dir, concrete binaries, a Node platform dir, a
// faithful argv[0]-dispatching volta-shim, and a `volta` answering `which`.
//
// Every concrete tool here needs `node` on PATH, mirroring the real install
// where package bins are `#!/usr/bin/env node` scripts (verified against
// @openai/codex 0.146.0). `node` exists ONLY in the Node platform dir, so a
// resolution that forgets to carry that dir produces an unrunnable path.
func newVoltaFixture(t *testing.T, opts voltaFixtureOpts) voltaFixture {
	t.Helper()
	root := t.TempDir()
	f := voltaFixture{
		binDir:   filepath.Join(root, "bin"),
		imageDir: filepath.Join(root, "image"),
		nodeDir:  filepath.Join(root, "node-image", "bin"),
	}
	for _, dir := range []string{f.binDir, f.imageDir, f.nodeDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	writeScript(t, filepath.Join(f.nodeDir, "node"), "#!/bin/sh\nexit 0\n")

	for _, command := range opts.commands {
		version, ok := voltaFixtureVersions[command]
		if !ok {
			t.Fatalf("no fixture version defined for %q", command)
		}
		writeScript(t, f.concrete(command), fmt.Sprintf(`#!/bin/sh
command -v node >/dev/null 2>&1 || { echo "env: node: No such file or directory" >&2; exit 127; }
echo %q
`, version))
	}

	// Faithful shim: refuses to run under its own name exactly like upstream
	// (ErrorKind::RunShimDirectly -> exit 126), and otherwise dispatches on
	// argv[0]. Uses ${0##*/} instead of basename(1) so it survives the
	// restricted PATHs these tests set.
	writeScript(t, f.shim(), fmt.Sprintf(`#!/bin/sh
n=${0##*/}
if [ "$n" = %q ]; then
  echo "volta error: 'volta-shim' should not be called directly" >&2
  exit 126
fi
exec %q/"$n" "$@"
`, voltaShimName, f.imageDir))

	for _, command := range opts.commands {
		if err := os.Symlink(f.shim(), f.alias(command)); err != nil {
			t.Fatalf("symlink %s -> volta-shim: %v", command, err)
		}
	}

	if !opts.omitVolta {
		writeScript(t, filepath.Join(f.binDir, "volta"), voltaFixtureScript(f, opts))
	}
	return f
}

// voltaFixtureScript renders the fake `volta`. It answers `which <tool>` and
// `which node`, prints nothing and exits non-zero otherwise, like upstream.
func voltaFixtureScript(f voltaFixture, opts voltaFixtureOpts) string {
	var body string
	if opts.projectDir != "" {
		// Project-local preference: only triggered when invoked from inside the
		// project, which is exactly the cwd sensitivity the caller must avoid.
		body = fmt.Sprintf(`
case "$PWD" in
  %q*)
    if [ "$1" = "which" ] && [ -n "$2" ]; then
      p=%q/"$2"
      if [ -x "$p" ]; then echo "$p"; exit 0; fi
    fi
    ;;
esac
`, opts.projectDir, filepath.Join(opts.projectDir, "node_modules", ".bin"))
	}
	return fmt.Sprintf(`#!/bin/sh
%s
if [ "$1" = "which" ] && [ "$2" = "node" ]; then
  echo %q; exit 0
fi
if [ "$1" = "which" ] && [ -n "$2" ]; then
  p=%q/"$2"
  if [ -x "$p" ]; then echo "$p"; exit 0; fi
fi
exit 1
`, body, filepath.Join(f.nodeDir, "node"), f.imageDir)
}

func writeScript(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func skipIfNoPOSIXShell(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shim fixture needs a /bin/sh")
	}
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skipf("no /bin/sh available: %v", err)
	}
}

// isolateAgentDiscovery keeps probeAgentCLIs hermetic: no login-shell fork and
// no Codex Desktop bundle fallback, so the only thing that can resolve is the
// fixture on PATH.
func isolateAgentDiscovery(t *testing.T) {
	t.Helper()
	origShell := resolveAgentsViaLoginShell
	t.Cleanup(func() { resolveAgentsViaLoginShell = origShell })
	resolveAgentsViaLoginShell = func([]string) map[string]string { return nil }
	resetShellResolveCacheForTest(t)

	origBundle := codexDesktopAppBundlePaths
	t.Cleanup(func() { codexDesktopAppBundlePaths = origBundle })
	codexDesktopAppBundlePaths = func() []string { return nil }
}

// resetVoltaResolveCache clears the process-wide Volta resolution state (cached
// answers, failure cooldown and spend budget) so tests cannot observe each
// other's results.
func resetVoltaResolveCache(t *testing.T) {
	t.Helper()
	clear := func() {
		voltaMu.Lock()
		defer voltaMu.Unlock()
		voltaStates = map[string]*voltaInstallState{}
	}
	clear()
	t.Cleanup(clear)
}

// runVoltaFromDir runs the fixture `volta` with an explicit working directory,
// so a test can confirm the fixture is genuinely cwd-sensitive before asserting
// that production code is not.
func runVoltaFromDir(t *testing.T, voltaBin, dir string, args ...string) (string, bool) {
	t.Helper()
	cmd := exec.Command(voltaBin, args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(out)), true
}

// pathWithout returns a PATH containing only dirs (plus /bin and /usr/bin for
// the shell itself), deliberately excluding Volta's Node platform dir.
func pathWithout(dirs ...string) string {
	all := append([]string{}, dirs...)
	all = append(all, "/usr/bin", "/bin")
	out := ""
	for _, d := range all {
		if out != "" {
			out += string(os.PathListSeparator)
		}
		out += d
	}
	return out
}

// TestResolveAgentExecutablePath_PinsVoltaConcreteBinary is the primary
// regression test for #6183: the pinned path must be the concrete binary, so the
// path we version-check is the path we later execute.
func TestResolveAgentExecutablePath_PinsVoltaConcreteBinary(t *testing.T) {
	skipIfNoPOSIXShell(t)
	resetVoltaResolveCache(t)

	commands := []string{"claude", "codex", "pi"}
	f := newVoltaFixture(t, voltaFixtureOpts{commands: commands})
	t.Setenv("PATH", pathWithout(f.binDir))

	for _, command := range commands {
		resolved, err := resolveAgentExecutable(command)
		if err != nil {
			t.Fatalf("%s: resolveAgentExecutable: %v", command, err)
		}
		if resolved.Path == f.shim() {
			t.Fatalf("%s pinned the shared shim; the version probe exits 126 there (#6183)", command)
		}
		if resolved.Path == f.alias(command) {
			t.Errorf("%s pinned the Volta alias %q; that path dispatches per working "+
				"directory, so the gated version would not be the executed version", command, resolved.Path)
		}
		if want := f.concrete(command); resolved.Path != want {
			t.Errorf("%s path = %q, want the concrete binary %q", command, resolved.Path, want)
		}

		version, err := agent.DetectVersionWithPathDirs(context.Background(), resolved.Path, resolved.PathDirs)
		if err != nil {
			t.Fatalf("%s: DetectVersionWithPathDirs(%q): %v", command, resolved.Path, err)
		}
		if version != voltaFixtureVersions[command] {
			t.Errorf("%s version = %q, want %q", command, version, voltaFixtureVersions[command])
		}
	}
}

// TestResolveAgentExecutable_CarriesVoltaNodePlatform covers review item 3: the
// concrete binary is a node script, so the resolution must also hand back the
// Node platform dir. Without it the pinned path is unrunnable under a PATH that
// has no node — the GUI-launched case — and the version probe and the task
// launch could disagree about which node runs the CLI.
func TestResolveAgentExecutable_CarriesVoltaNodePlatform(t *testing.T) {
	skipIfNoPOSIXShell(t)
	resetVoltaResolveCache(t)

	f := newVoltaFixture(t, voltaFixtureOpts{commands: []string{"codex"}})
	// Note: no node anywhere on PATH, only inside Volta's platform dir.
	t.Setenv("PATH", pathWithout(f.binDir))

	resolved, err := resolveAgentExecutable("codex")
	if err != nil {
		t.Fatalf("resolveAgentExecutable: %v", err)
	}
	if len(resolved.PathDirs) == 0 {
		t.Fatal("resolution returned no PATH dirs; a node-script package binary cannot run without Volta's Node platform")
	}
	if resolved.PathDirs[0] != f.nodeDir {
		t.Errorf("PathDirs[0] = %q, want Volta's node dir %q", resolved.PathDirs[0], f.nodeDir)
	}

	// With the environment: runnable.
	if _, err := agent.DetectVersionWithPathDirs(context.Background(), resolved.Path, resolved.PathDirs); err != nil {
		t.Errorf("version probe with the resolved environment failed: %v", err)
	}
	// Without it: not runnable. This is what makes carrying the dirs necessary
	// rather than cosmetic.
	if _, err := agent.DetectVersion(context.Background(), resolved.Path); err == nil {
		t.Error("version probe without the resolved environment unexpectedly succeeded; " +
			"the fixture no longer models the `#!/usr/bin/env node` dependency")
	}
}

// TestProbeAgentCLIs_ShellFallbackResolvesVoltaAlias covers review item 1. A
// GUI-launched daemon does not see Volta on its own PATH and falls back to the
// login shell, whose script preserves the file name — so the alias arrives here
// still pointing at the shared shim. That leg must apply the same resolution as
// the LookPath leg, otherwise Desktop users keep the ungated alias.
func TestProbeAgentCLIs_ShellFallbackResolvesVoltaAlias(t *testing.T) {
	skipIfNoPOSIXShell(t)
	resetVoltaResolveCache(t)

	f := newVoltaFixture(t, voltaFixtureOpts{commands: []string{"claude"}})
	// Empty PATH: the LookPath leg must miss so the shell fallback is the only
	// way claude can resolve, exactly as on a GUI-launched daemon.
	t.Setenv("PATH", "")

	origShell := resolveAgentsViaLoginShell
	t.Cleanup(func() { resolveAgentsViaLoginShell = origShell })
	resolveAgentsViaLoginShell = func([]string) map[string]string {
		return map[string]string{"claude": f.alias("claude")}
	}
	resetShellResolveCacheForTest(t)
	origBundle := codexDesktopAppBundlePaths
	t.Cleanup(func() { codexDesktopAppBundlePaths = origBundle })
	codexDesktopAppBundlePaths = func() []string { return nil }

	entry, ok := probeAgentCLIs()["claude"]
	if !ok {
		t.Fatal("claude not discovered via the login-shell fallback")
	}
	if entry.Path == f.alias("claude") {
		t.Fatalf("shell fallback pinned the Volta alias %q; the GUI path bypassed resolution", entry.Path)
	}
	if want := f.concrete("claude"); entry.Path != want {
		t.Errorf("path = %q, want the concrete binary %q", entry.Path, want)
	}
	if len(entry.PathDirs) == 0 || entry.PathDirs[0] != f.nodeDir {
		t.Errorf("PathDirs = %v, want Volta's node dir %q", entry.PathDirs, f.nodeDir)
	}
}

// TestReresolveAgentCommand_ShellFallbackResolvesVoltaAlias is item 1 for the
// self-heal path, which has its own shell branch.
func TestReresolveAgentCommand_ShellFallbackResolvesVoltaAlias(t *testing.T) {
	skipIfNoPOSIXShell(t)
	resetVoltaResolveCache(t)

	f := newVoltaFixture(t, voltaFixtureOpts{commands: []string{"claude"}})
	t.Setenv("PATH", "")

	origShell := resolveAgentsViaLoginShell
	t.Cleanup(func() { resolveAgentsViaLoginShell = origShell })
	resolveAgentsViaLoginShell = func([]string) map[string]string {
		return map[string]string{"claude": f.alias("claude")}
	}

	resolved, ok := reresolveAgentCommand("claude")
	if !ok {
		t.Fatal("reresolveAgentCommand did not resolve claude")
	}
	if resolved.Path == f.alias("claude") {
		t.Fatalf("self-heal pinned the Volta alias %q", resolved.Path)
	}
	if want := f.concrete("claude"); resolved.Path != want {
		t.Errorf("path = %q, want %q", resolved.Path, want)
	}
}

// TestVoltaResolve_IgnoresProjectDirectory covers review item 2. Volta resolves a
// project-local binary before the user default, so asking it from whatever
// directory the daemon happens to be started in would pin one project's
// dependency as the machine-wide runtime.
func TestVoltaResolve_IgnoresProjectDirectory(t *testing.T) {
	skipIfNoPOSIXShell(t)
	resetVoltaResolveCache(t)

	projectDir := t.TempDir()
	projectBin := filepath.Join(projectDir, "node_modules", ".bin")
	if err := os.MkdirAll(projectBin, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeScript(t, filepath.Join(projectBin, "claude"), "#!/bin/sh\necho \"0.0.1 (project local)\"\n")

	f := newVoltaFixture(t, voltaFixtureOpts{
		commands:   []string{"claude"},
		projectDir: projectDir,
	})
	t.Setenv("PATH", pathWithout(f.binDir))

	// Sanity: the fixture really is cwd-sensitive, so this test can fail.
	if out, ok := runVoltaFromDir(t, filepath.Join(f.binDir, "volta"), projectDir, "which", "claude"); !ok {
		t.Fatalf("fixture volta failed inside the project dir")
	} else if out != filepath.Join(projectBin, "claude") {
		t.Fatalf("fixture is not cwd-sensitive: got %q", out)
	}

	// Run the daemon's resolution with the process cwd inside the project.
	t.Chdir(projectDir)

	resolved, ok := voltaResolve(f.shim(), "claude")
	if !ok {
		t.Fatal("voltaResolve failed")
	}
	if got := resolved.Path; got == filepath.Join(projectBin, "claude") {
		t.Errorf("resolved the project-local binary %q; a daemon started inside a JS "+
			"project would pin that project's dependency as the machine runtime", got)
	} else if want := f.concrete("claude"); got != want {
		t.Errorf("path = %q, want the default toolchain binary %q", got, want)
	}
}

// TestVoltaResolve_BoundsTotalCostWhenVoltaHangs covers review item 4. A wedged
// Volta install used to cost one full timeout PER command, so a machine with
// several Volta-managed CLIs stalled startup for the sum of them.
//
// The invocation count is the real invariant ("ask a broken install once, not
// once per command"), so it is asserted directly rather than inferred from wall
// clock. runVolta is stubbed instead of hanging a real subprocess: a killed child
// reports its side effects asynchronously, which made a file-based counter race
// with the kill and read one call behind.
func TestVoltaResolve_BoundsTotalCostWhenVoltaHangs(t *testing.T) {
	skipIfNoPOSIXShell(t)
	resetVoltaResolveCache(t)

	origTimeout, origWait := voltaResolveTimeout, voltaResolveWaitDelay
	origRun := runVolta
	t.Cleanup(func() {
		voltaResolveTimeout, voltaResolveWaitDelay = origTimeout, origWait
		runVolta = origRun
	})
	voltaResolveTimeout = 100 * time.Millisecond
	voltaResolveWaitDelay = 50 * time.Millisecond

	var mu sync.Mutex
	calls := 0
	// Models a wedged install: every invocation burns the whole per-call budget
	// (timeout + the wait for a hung child's pipes) and then fails.
	runVolta = func(string, ...string) (string, bool) {
		mu.Lock()
		calls++
		mu.Unlock()
		time.Sleep(voltaResolveTimeout + voltaResolveWaitDelay)
		return "", false
	}

	commands := []string{"claude", "codex", "pi"}
	f := newVoltaFixture(t, voltaFixtureOpts{commands: commands})
	t.Setenv("PATH", pathWithout(f.binDir))

	start := time.Now()
	for _, command := range commands {
		if _, ok := voltaResolve(f.shim(), command); ok {
			t.Fatalf("%s unexpectedly resolved against a hung volta", command)
		}
	}
	elapsed := time.Since(start)

	mu.Lock()
	got := calls
	mu.Unlock()
	if got != 1 {
		t.Errorf("`volta` was invoked %d times for %d commands, want 1; a broken install "+
			"must be asked once per cooldown, not once per command", got, len(commands))
	}

	singleCall := voltaResolveTimeout + voltaResolveWaitDelay
	if additive := time.Duration(len(commands)) * singleCall; elapsed >= additive {
		t.Errorf("resolving %d hung aliases took %v, at least the additive bound %v; "+
			"the per-command timeout can still stall daemon startup", len(commands), elapsed, additive)
	}
}

// TestVoltaResolve_FailureCooldownExpires is the other half of the circuit
// breaker: a temporarily broken install must be retried once the cooldown is up,
// not written off for the daemon's lifetime.
func TestVoltaResolve_FailureCooldownExpires(t *testing.T) {
	skipIfNoPOSIXShell(t)
	resetVoltaResolveCache(t)

	origRun, origCooldown := runVolta, voltaFailureCooldown
	t.Cleanup(func() { runVolta, voltaFailureCooldown = origRun, origCooldown })
	voltaFailureCooldown = 20 * time.Millisecond

	var mu sync.Mutex
	calls := 0
	runVolta = func(string, ...string) (string, bool) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		return "", false
	}

	f := newVoltaFixture(t, voltaFixtureOpts{commands: []string{"claude"}})
	t.Setenv("PATH", pathWithout(f.binDir))

	if _, ok := voltaResolve(f.shim(), "claude"); ok {
		t.Fatal("unexpectedly resolved")
	}
	// Inside the cooldown: no new invocation.
	if _, ok := voltaResolve(f.shim(), "claude"); ok {
		t.Fatal("unexpectedly resolved")
	}
	mu.Lock()
	during := calls
	mu.Unlock()
	if during != 1 {
		t.Errorf("invocations inside the cooldown = %d, want 1", during)
	}

	time.Sleep(2 * voltaFailureCooldown)
	if _, ok := voltaResolve(f.shim(), "claude"); ok {
		t.Fatal("unexpectedly resolved")
	}
	mu.Lock()
	after := calls
	mu.Unlock()
	if after != 2 {
		t.Errorf("invocations after the cooldown expired = %d, want 2; a transient failure "+
			"must not disable Volta resolution permanently", after)
	}
}

// TestDetectBuiltinRuntimes_RegistersVoltaManagedCLIs is the end-result test:
// availability alone was never the user-visible outcome. This runs the real
// version probe and the real minimum-version gate over a Volta install and
// asserts all three providers reach the registration payload.
func TestDetectBuiltinRuntimes_RegistersVoltaManagedCLIs(t *testing.T) {
	skipIfNoPOSIXShell(t)
	resetVoltaResolveCache(t)

	commands := []string{"claude", "codex", "pi"}
	f := newVoltaFixture(t, voltaFixtureOpts{commands: commands})
	t.Setenv("PATH", pathWithout(f.binDir))
	isolateAgentDiscovery(t)

	// Deliberately no detectAgentVersion / checkAgentMinVersion stubs: the point
	// is that the real gate accepts what discovery pinned.
	d := freshDaemon("")
	d.cfg.Agents = probeAgentCLIs()

	registered := map[string]string{}
	for _, rt := range d.detectBuiltinRuntimes(context.Background()) {
		registered[rt["type"]] = rt["version"]
	}
	for _, command := range commands {
		version, ok := registered[command]
		if !ok {
			t.Errorf("%s missing from the registration payload; skipped reasons: %#v",
				command, d.skippedAgentsSnapshot())
			continue
		}
		if version != voltaFixtureVersions[command] {
			t.Errorf("%s registered version = %q, want %q", command, version, voltaFixtureVersions[command])
		}
	}
}

// TestProbeAgentCLIs_DiscoversVoltaManagedCLIs covers the discovery entry point
// and asserts the three providers get three distinct concrete paths.
func TestProbeAgentCLIs_DiscoversVoltaManagedCLIs(t *testing.T) {
	skipIfNoPOSIXShell(t)
	resetVoltaResolveCache(t)

	commands := []string{"claude", "codex", "pi"}
	f := newVoltaFixture(t, voltaFixtureOpts{commands: commands})
	t.Setenv("PATH", pathWithout(f.binDir))
	isolateAgentDiscovery(t)

	agents := probeAgentCLIs()
	seen := map[string]string{}
	for _, command := range commands {
		entry, ok := agents[command]
		if !ok {
			t.Fatalf("%s not discovered from a Volta install: %#v", command, agents)
		}
		if want := f.concrete(command); entry.Path != want {
			t.Errorf("%s path = %q, want %q", command, entry.Path, want)
		}
		if prev, dup := seen[entry.Path]; dup {
			t.Errorf("%s and %s share one path %q", prev, command, entry.Path)
		}
		seen[entry.Path] = command
	}
}

// TestResolveAgentExecutablePath_FailsClosedWithoutVoltaResolution pins the
// deliberate failure mode: with no way to ask Volta we must NOT fall back to the
// alias, because that path is only version-checkable under conditions we cannot
// reproduce at launch. Staying unregistered is correct; MULTICA_*_PATH remains
// the escape hatch.
func TestResolveAgentExecutablePath_FailsClosedWithoutVoltaResolution(t *testing.T) {
	skipIfNoPOSIXShell(t)
	resetVoltaResolveCache(t)

	f := newVoltaFixture(t, voltaFixtureOpts{commands: []string{"claude"}, omitVolta: true})
	t.Setenv("PATH", pathWithout(f.binDir))

	got, err := resolveAgentExecutablePath("claude")
	if err != nil {
		t.Fatalf("resolveAgentExecutablePath: %v", err)
	}
	if got == f.alias("claude") {
		t.Fatalf("fell back to the alias %q; that reintroduces an ungated launch path", got)
	}
	wantShim, err := filepath.EvalSymlinks(f.shim())
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	if got != wantShim {
		t.Errorf("path = %q, want the canonicalized shim %q", got, wantShim)
	}
	if _, err := agent.DetectVersion(context.Background(), got); err == nil {
		t.Error("expected version detection to fail, so the provider stays unregistered")
	}
}

// TestVoltaResolve_ReresolvesAfterBinaryReplaced covers the cache's
// revalidation-by-existence: when Volta swaps the underlying tool and the old
// concrete path disappears, the next resolution must not keep serving it.
func TestVoltaResolve_ReresolvesAfterBinaryReplaced(t *testing.T) {
	skipIfNoPOSIXShell(t)
	resetVoltaResolveCache(t)

	f := newVoltaFixture(t, voltaFixtureOpts{commands: []string{"claude"}})
	t.Setenv("PATH", pathWithout(f.binDir))

	first, ok := voltaResolve(f.shim(), "claude")
	if !ok {
		t.Fatal("initial resolution failed")
	}
	if again, ok := voltaResolve(f.shim(), "claude"); !ok || again.Path != first.Path {
		t.Fatalf("cached resolution = (%q, %v), want (%q, true)", again.Path, ok, first.Path)
	}

	if err := os.Remove(first.Path); err != nil {
		t.Fatalf("remove concrete binary: %v", err)
	}
	newImage := filepath.Join(t.TempDir(), "image")
	if err := os.MkdirAll(newImage, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeScript(t, filepath.Join(newImage, "claude"), "#!/bin/sh\necho \"3.0.0 (Claude Code)\"\n")
	writeScript(t, filepath.Join(f.binDir, "volta"), fmt.Sprintf(`#!/bin/sh
if [ "$1" = "which" ] && [ "$2" = "node" ]; then echo %q; exit 0; fi
if [ "$1" = "which" ] && [ -n "$2" ]; then
  p=%q/"$2"
  if [ -x "$p" ]; then echo "$p"; exit 0; fi
fi
exit 1
`, filepath.Join(f.nodeDir, "node"), newImage))

	second, ok := voltaResolve(f.shim(), "claude")
	if !ok {
		t.Fatal("re-resolution failed after the pinned binary disappeared")
	}
	if second.Path == first.Path {
		t.Errorf("still serving the removed path %q", first.Path)
	}
	if want := filepath.Join(newImage, "claude"); second.Path != want {
		t.Errorf("re-resolved path = %q, want %q", second.Path, want)
	}
}

// TestResolveAgentExecutablePath_VoltaAliasShadowedByHooks proves the fix reaches
// the ~/.multica/hooks branch, which canonicalizes independently: a
// hooks-shadowed Volta command must skip the recursive wrapper AND still land on
// the concrete binary.
func TestResolveAgentExecutablePath_VoltaAliasShadowedByHooks(t *testing.T) {
	skipIfNoPOSIXShell(t)
	resetVoltaResolveCache(t)

	home := t.TempDir()
	t.Setenv("HOME", home)
	hooksDir := filepath.Join(home, ".multica", "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatalf("create hooks dir: %v", err)
	}
	writeScript(t, filepath.Join(hooksDir, "claude"), "#!/bin/sh\nexec claude \"$@\"\n")

	f := newVoltaFixture(t, voltaFixtureOpts{commands: []string{"claude"}})
	t.Setenv("PATH", pathWithout(hooksDir, f.binDir))

	resolved, err := resolveAgentExecutable("claude")
	if err != nil {
		t.Fatalf("resolveAgentExecutable: %v", err)
	}
	if filepath.Dir(resolved.Path) == hooksDir {
		t.Fatalf("resolved into the hooks dir (%q); the wrapper would recurse", resolved.Path)
	}
	if want := f.concrete("claude"); resolved.Path != want {
		t.Errorf("path = %q, want the concrete binary %q", resolved.Path, want)
	}
	if len(resolved.PathDirs) == 0 {
		t.Error("hooks branch dropped the Volta Node platform dir")
	}
}

// TestCanonicalExecutablePath_NonVoltaSymlinkStillCanonicalized guards the blast
// radius: ordinary symlinks must keep collapsing to the real file, which is what
// pins agents against PATH drift and powers the MUL-4486 self-heal.
func TestCanonicalExecutablePath_NonVoltaSymlinkStillCanonicalized(t *testing.T) {
	skipIfNoPOSIXShell(t)

	realDir := t.TempDir()
	realBin := filepath.Join(realDir, "claude-0.9.9")
	writeScript(t, realBin, "#!/bin/sh\nexit 0\n")
	alias := filepath.Join(t.TempDir(), "claude")
	if err := os.Symlink(realBin, alias); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	if got := canonicalExecutablePath(alias); filepath.Base(got) != "claude-0.9.9" {
		t.Errorf("canonicalExecutablePath(%q) = %q, want the real versioned binary; "+
			"non-Volta symlinks must still be resolved", alias, got)
	}
}

// TestCanonicalExecutablePath_SymlinkedParentDirIsCanonicalized covers the other
// half of canonicalization: a symlinked *directory* on the way to the binary
// must also be collapsed, so a moving prefix dir cannot redirect a pinned path.
func TestCanonicalExecutablePath_SymlinkedParentDirIsCanonicalized(t *testing.T) {
	skipIfNoPOSIXShell(t)

	root := t.TempDir()
	realDir := filepath.Join(root, "versions", "20.1.0", "bin")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeScript(t, filepath.Join(realDir, "claude"), "#!/bin/sh\nexit 0\n")
	linkDir := filepath.Join(root, "current")
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Fatalf("symlink dir: %v", err)
	}

	got := canonicalExecutablePath(filepath.Join(linkDir, "claude"))
	wantDir, err := filepath.EvalSymlinks(realDir)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	if want := filepath.Join(wantDir, "claude"); got != want {
		t.Errorf("canonicalExecutablePath through a symlinked dir = %q, want %q", got, want)
	}
}

// TestCanonicalExecutablePath_ExplicitShimPathStaysCanonical documents the edge
// case: a path pointed straight at volta-shim carries no dispatch name, so there
// is nothing to resolve and it must not get special treatment.
func TestCanonicalExecutablePath_ExplicitShimPathStaysCanonical(t *testing.T) {
	skipIfNoPOSIXShell(t)
	resetVoltaResolveCache(t)

	f := newVoltaFixture(t, voltaFixtureOpts{})
	if got := canonicalExecutablePath(f.shim()); filepath.Base(got) != voltaShimName {
		t.Errorf("canonicalExecutablePath(%q) = %q, want it to stay volta-shim", f.shim(), got)
	}
}

// TestIsVoltaShimPath keeps the PATH-pinning exception minimal: only Volta's
// actual shim names may opt out of plain symlink resolution.
func TestIsVoltaShimPath(t *testing.T) {
	for _, tc := range []struct {
		path string
		want bool
	}{
		{filepath.Join("/home/u/.volta/bin", "volta-shim"), true},
		{filepath.Join("/home/u/.volta/bin", "volta-shim.exe"), true},
		// Near misses: an extension-insensitive match would swallow these.
		{filepath.Join("/home/u/.volta/bin", "volta-shim.bak"), false},
		{filepath.Join("/home/u/.volta/bin", "volta-shim.wrapper"), false},
		{filepath.Join("/home/u/.volta/bin", "volta-shim.exe.bak"), false},
		{filepath.Join("/usr/local/bin", "volta-shim-wrapper"), false},
		{filepath.Join("/usr/local/bin", "my-volta-shim"), false},
		{filepath.Join("/home/u/.volta/bin", "claude"), false},
		{filepath.Join("/usr/local/bin", "volta"), false},
		{"", false},
	} {
		if got := isVoltaShimPath(tc.path); got != tc.want {
			t.Errorf("isVoltaShimPath(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// TestVoltaNonProjectDir keeps the deterministic working directory an ancestor
// nothing can shadow: any directory with a package.json above it would let Volta
// pick a project tool.
func TestVoltaNonProjectDir(t *testing.T) {
	got := voltaNonProjectDir(filepath.Join("/home", "u", ".volta", "bin", "volta"))
	if runtime.GOOS != "windows" && got != "/" {
		t.Errorf("voltaNonProjectDir = %q, want the filesystem root", got)
	}
	if got == "" {
		t.Error("voltaNonProjectDir returned an empty directory")
	}
}
