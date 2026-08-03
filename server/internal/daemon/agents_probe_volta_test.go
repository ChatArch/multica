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

// Volta installs one trampoline binary and symlinks every managed command to it,
// dispatching on the name it was invoked as (#6183). Resolving that symlink
// collapses claude/codex/pi onto the same volta-shim path and the shim then exits
// 126, so the daemon asks Volta for the concrete binary instead — together with
// the execution context Volta itself would supply: the Node platform bound to the
// package at install time, plus the shared NODE_PATH.

// voltaFixture mirrors a real Volta layout, including the Homebrew-style split
// where the binaries (volta, volta-shim) live outside $VOLTA_HOME.
type voltaFixture struct {
	home       string // $VOLTA_HOME
	installDir string // holds volta + volta-shim
	boundNode  string // node version recorded in each bin config
	otherNode  string // a different, "current default" node
}

func (f voltaFixture) binDir() string          { return filepath.Join(f.home, "bin") }
func (f voltaFixture) alias(cmd string) string { return filepath.Join(f.binDir(), cmd) }
func (f voltaFixture) shim() string            { return filepath.Join(f.installDir, voltaShimName) }
func (f voltaFixture) voltaBin() string        { return filepath.Join(f.installDir, "volta") }
func (f voltaFixture) concrete(cmd string) string {
	return filepath.Join(f.home, "tools", "image", "packages", cmd, "bin", cmd)
}
func (f voltaFixture) nodeDir(version string) string {
	return filepath.Join(f.home, "tools", "image", "node", version, "bin")
}
func (f voltaFixture) boundNodeDir() string { return f.nodeDir(f.boundNode) }
func (f voltaFixture) sharedLibDir() string {
	return filepath.Join(f.home, "tools", "shared")
}

// voltaFixtureVersions must clear agent.MinVersions (claude >= 2.0.0,
// codex >= 0.100.0) or the registration-level tests prove nothing.
var voltaFixtureVersions = map[string]string{
	"claude": "2.1.0 (Claude Code)",
	"codex":  "codex-cli 0.140.0",
	"pi":     "pi 0.9.0",
}

type voltaFixtureOpts struct {
	commands []string
	// omitVolta models an install whose `volta` binary cannot be run.
	omitVolta bool
	// omitBinConfig drops the bin config, so the bound Node cannot be determined.
	omitBinConfig bool
	// projectDir, when set, makes `volta which` answer with a project-local
	// binary when invoked from inside that directory, as real Volta does.
	projectDir string
}

func newVoltaFixture(t *testing.T, opts voltaFixtureOpts) voltaFixture {
	t.Helper()
	root := t.TempDir()
	f := voltaFixture{
		home:       filepath.Join(root, "home"),
		installDir: filepath.Join(root, "install"),
		boundNode:  "20.11.0",
		otherNode:  "24.18.1",
	}
	mkdirs(t, f.binDir(), f.installDir, f.sharedLibDir(),
		f.nodeDir(f.boundNode), f.nodeDir(f.otherNode),
		filepath.Join(f.home, "tools", "user", "bins"))

	// Only the bound Node carries a working `node`; the "default" one is a decoy
	// that fails, so a resolution using it cannot pass unnoticed.
	writeScript(t, filepath.Join(f.nodeDir(f.boundNode), "node"), "#!/bin/sh\nexit 0\n")
	writeScript(t, filepath.Join(f.nodeDir(f.otherNode), "node"),
		"#!/bin/sh\necho 'wrong node: this is the current default, not the bound one' >&2\nexit 1\n")

	for _, cmd := range opts.commands {
		version, ok := voltaFixtureVersions[cmd]
		if !ok {
			t.Fatalf("no fixture version defined for %q", cmd)
		}
		mkdirs(t, filepath.Dir(f.concrete(cmd)))
		// Mirrors a real package bin: a node script, so it is unrunnable without
		// the Node platform Volta bound to it. Verified against @openai/codex
		// 0.146.0, which is `#!/usr/bin/env node`.
		writeScript(t, f.concrete(cmd), fmt.Sprintf(`#!/bin/sh
node --check-marker >/dev/null 2>&1 || { echo "env: node: No such file or directory" >&2; exit 127; }
echo %q
`, version))
		if !opts.omitBinConfig {
			writeVoltaConfigFile(t, filepath.Join(f.home, "tools", "user", "bins", cmd+".json"), fmt.Sprintf(
				`{"name":%q,"package":%q,"version":"1.0.0","platform":{"node":%q,"npm":null,"pnpm":null,"yarn":null},"manager":"Npm"}`,
				cmd, cmd, f.boundNode))
		}
	}

	// Faithful shim: refuses to run under its own name (upstream
	// ErrorKind::RunShimDirectly -> exit 126) and otherwise dispatches on argv[0].
	writeScript(t, f.shim(), fmt.Sprintf(`#!/bin/sh
n=${0##*/}
if [ "$n" = %q ]; then
  echo "volta error: 'volta-shim' should not be called directly" >&2
  exit 126
fi
exec %q/tools/image/packages/"$n"/bin/"$n" "$@"
`, voltaShimName, f.home))

	for _, cmd := range opts.commands {
		if err := os.Symlink(f.shim(), f.alias(cmd)); err != nil {
			t.Fatalf("symlink %s: %v", cmd, err)
		}
	}
	if !opts.omitVolta {
		writeScript(t, f.voltaBin(), voltaFixtureScript(f, opts))
	}
	return f
}

// voltaFixtureScript renders the fake `volta`. It resolves everything relative to
// $VOLTA_HOME and fails without it, so a caller that does not pass the right home
// cannot silently get the right answer. `which node` deliberately reports the
// *current default* Node, which is not the one a bound package must run under.
func voltaFixtureScript(f voltaFixture, opts voltaFixtureOpts) string {
	var project string
	if opts.projectDir != "" {
		project = fmt.Sprintf(`
case "$(pwd -P)" in
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
[ -n "$VOLTA_HOME" ] || { echo "volta: VOLTA_HOME not set" >&2; exit 1; }
%s
if [ "$1" = "which" ] && [ "$2" = "node" ]; then
  echo "$VOLTA_HOME/tools/image/node/%s/bin/node"; exit 0
fi
if [ "$1" = "which" ] && [ -n "$2" ]; then
  p="$VOLTA_HOME/tools/image/packages/$2/bin/$2"
  if [ -x "$p" ]; then echo "$p"; exit 0; fi
fi
exit 1
`, project, f.otherNode)
}

func mkdirs(t *testing.T, dirs ...string) {
	t.Helper()
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
}

func writeScript(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func writeVoltaConfigFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
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

// unsetEnvForTest removes a variable for the duration of the test and restores
// the original afterwards. t.Setenv with an empty string is NOT equivalent: the
// variable stays present-but-empty, and a real Volta then treats its home as a
// relative path and writes layout files into the working directory.
func unsetEnvForTest(t *testing.T, name string) {
	t.Helper()
	t.Setenv(name, "") // registers restoration of the original value
	if err := os.Unsetenv(name); err != nil {
		t.Fatalf("unset %s: %v", name, err)
	}
}

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

// systemPath is a PATH with neither Volta nor any node on it: what a
// GUI-launched daemon sees.
func systemPath(extra ...string) string {
	return strings.Join(append(append([]string{}, extra...), "/usr/bin", "/bin"),
		string(os.PathListSeparator))
}

// probeVersion runs the pinned executable under its resolved environment.
func probeVersion(t *testing.T, resolved resolvedExecutable) (string, error) {
	t.Helper()
	return agent.DetectVersionWithEnv(context.Background(), resolved.Path, resolved.Env)
}

// TestResolveAgentExecutable_PinsConcreteBinaryWithBoundNode is the primary
// regression test for #6183 plus the environment requirement: the pinned path must
// be the concrete binary, and it must carry the Node platform Volta bound to the
// package at install time — NOT the current default Node.
func TestResolveAgentExecutable_PinsConcreteBinaryWithBoundNode(t *testing.T) {
	skipIfNoPOSIXShell(t)
	resetVoltaResolveCache(t)

	commands := []string{"claude", "codex", "pi"}
	f := newVoltaFixture(t, voltaFixtureOpts{commands: commands})
	t.Setenv("PATH", systemPath(f.binDir()))

	for _, cmd := range commands {
		resolved, err := resolveAgentExecutable(cmd)
		if err != nil {
			t.Fatalf("%s: resolveAgentExecutable: %v", cmd, err)
		}
		if resolved.Path == f.shim() {
			t.Fatalf("%s pinned the shared shim; the version probe exits 126 there (#6183)", cmd)
		}
		if resolved.Path == f.alias(cmd) {
			t.Errorf("%s pinned the Volta alias; that dispatches per working directory", cmd)
		}
		if want := f.concrete(cmd); resolved.Path != want {
			t.Errorf("%s path = %q, want %q", cmd, resolved.Path, want)
		}

		pathDirs := resolved.Env.PrefixPaths["PATH"]
		if len(pathDirs) == 0 || pathDirs[0] != f.boundNodeDir() {
			t.Errorf("%s PATH dirs = %v, want the install-time bound Node %q; using the "+
				"current default Node means switching defaults silently changes which "+
				"Node a previously installed CLI runs under", cmd, pathDirs, f.boundNodeDir())
		}
		if nodePath := resolved.Env.PrefixPaths["NODE_PATH"]; len(nodePath) == 0 || nodePath[0] != f.sharedLibDir() {
			t.Errorf("%s NODE_PATH = %v, want Volta's shared lib dir %q so a global bin can "+
				"require other global libs", cmd, nodePath, f.sharedLibDir())
		}

		version, err := probeVersion(t, resolved)
		if err != nil {
			t.Fatalf("%s: version probe under the resolved env: %v", cmd, err)
		}
		if version != voltaFixtureVersions[cmd] {
			t.Errorf("%s version = %q, want %q", cmd, version, voltaFixtureVersions[cmd])
		}
	}
}

// TestResolveAgentExecutable_EnvironmentIsRequired proves the carried environment
// is load-bearing: without it the pinned path is not runnable at all.
func TestResolveAgentExecutable_EnvironmentIsRequired(t *testing.T) {
	skipIfNoPOSIXShell(t)
	resetVoltaResolveCache(t)

	f := newVoltaFixture(t, voltaFixtureOpts{commands: []string{"codex"}})
	t.Setenv("PATH", systemPath(f.binDir()))

	resolved, err := resolveAgentExecutable("codex")
	if err != nil {
		t.Fatalf("resolveAgentExecutable: %v", err)
	}
	if _, err := probeVersion(t, resolved); err != nil {
		t.Errorf("probe with the resolved environment failed: %v", err)
	}
	if _, err := agent.DetectVersion(context.Background(), resolved.Path); err == nil {
		t.Error("probe WITHOUT the resolved environment unexpectedly succeeded; the fixture " +
			"no longer models the `#!/usr/bin/env node` dependency")
	}
}

// TestVoltaResolve_UsesDerivedVoltaHomeNotAmbient covers the GUI daemon with a
// custom VOLTA_HOME. The daemon does not inherit the user's VOLTA_HOME, and Volta
// reads all package data relative to it, so resolution must derive the home from
// the alias instead of trusting (or defaulting) the environment.
func TestVoltaResolve_UsesDerivedVoltaHomeNotAmbient(t *testing.T) {
	skipIfNoPOSIXShell(t)
	resetVoltaResolveCache(t)

	f := newVoltaFixture(t, voltaFixtureOpts{commands: []string{"claude"}})
	t.Setenv("PATH", systemPath(f.binDir()))

	// The daemon's environment knows nothing about this install: no VOLTA_HOME at
	// all (GUI launch), which is what the original report ran into.
	unsetEnvForTest(t, "VOLTA_HOME")
	resolved, ok := voltaResolve(f.alias("claude"), f.shim(), "claude")
	if !ok {
		t.Fatal("resolution failed with no VOLTA_HOME in the environment; a GUI-launched " +
			"daemon never inherits it, so the home must come from the alias path")
	}
	if want := f.concrete("claude"); resolved.Path != want {
		t.Errorf("path = %q, want %q", resolved.Path, want)
	}

	// And a *wrong* inherited value must not win either.
	resetVoltaResolveCache(t)
	decoy := t.TempDir()
	t.Setenv("VOLTA_HOME", decoy)
	resolved, ok = voltaResolve(f.alias("claude"), f.shim(), "claude")
	if !ok {
		t.Fatal("resolution failed with a decoy VOLTA_HOME set")
	}
	if strings.HasPrefix(resolved.Path, decoy) {
		t.Errorf("resolved into the decoy home %q; the ambient VOLTA_HOME overrode the "+
			"install the alias actually belongs to", decoy)
	}
	if want := f.concrete("claude"); resolved.Path != want {
		t.Errorf("path = %q, want %q", resolved.Path, want)
	}
}

func TestVoltaHomeFromAlias(t *testing.T) {
	skipIfNoPOSIXShell(t)
	f := newVoltaFixture(t, voltaFixtureOpts{commands: []string{"claude"}})

	got, ok := voltaHomeFromAlias(f.alias("claude"))
	if !ok {
		t.Fatal("could not derive VOLTA_HOME from the alias")
	}
	if got != f.home {
		t.Errorf("voltaHomeFromAlias = %q, want %q", got, f.home)
	}

	// A non-Volta symlink yields nothing rather than a wrong guess.
	other := filepath.Join(t.TempDir(), "claude")
	target := filepath.Join(t.TempDir(), "real")
	writeScript(t, target, "#!/bin/sh\nexit 0\n")
	if err := os.Symlink(target, other); err != nil {
		t.Fatal(err)
	}
	if home, ok := voltaHomeFromAlias(other); ok {
		t.Errorf("derived %q from a non-Volta symlink; want no answer", home)
	}
}

// TestVoltaResolve_FailsClosedWithoutBoundNode covers the partial-answer case: if
// the bound Node cannot be determined we cannot reproduce Volta's environment, so
// the resolution must fail rather than cache a path with an empty environment.
func TestVoltaResolve_FailsClosedWithoutBoundNode(t *testing.T) {
	skipIfNoPOSIXShell(t)
	resetVoltaResolveCache(t)

	f := newVoltaFixture(t, voltaFixtureOpts{commands: []string{"claude"}, omitBinConfig: true})
	t.Setenv("PATH", systemPath(f.binDir()))

	if resolved, ok := voltaResolve(f.alias("claude"), f.shim(), "claude"); ok {
		t.Fatalf("resolved without a bound Node (%+v); an incomplete environment must not "+
			"be cached or launched", resolved)
	}
	// And the overall resolution must not silently fall back to the alias.
	got, err := resolveAgentExecutablePath("claude")
	if err != nil {
		t.Fatalf("resolveAgentExecutablePath: %v", err)
	}
	if got == f.alias("claude") {
		t.Errorf("fell back to the alias %q", got)
	}
}

// TestVoltaResolve_RevalidatesWhenNodeImageRemoved makes cache validity cover the
// whole resolution, not just the tool path: if the Node image is deleted (a Volta
// upgrade prunes old images) the cached entry is unrunnable and must be dropped.
func TestVoltaResolve_RevalidatesWhenNodeImageRemoved(t *testing.T) {
	skipIfNoPOSIXShell(t)
	resetVoltaResolveCache(t)

	f := newVoltaFixture(t, voltaFixtureOpts{commands: []string{"claude"}})
	t.Setenv("PATH", systemPath(f.binDir()))

	first, ok := voltaResolve(f.alias("claude"), f.shim(), "claude")
	if !ok {
		t.Fatal("initial resolution failed")
	}
	if !voltaResolutionUsable(first) {
		t.Fatal("fresh resolution reported unusable")
	}

	if err := os.RemoveAll(filepath.Dir(f.boundNodeDir())); err != nil {
		t.Fatalf("remove node image: %v", err)
	}
	if voltaResolutionUsable(first) {
		t.Error("cached resolution still considered usable after its Node image was removed; " +
			"validity must cover the environment, not only the tool path")
	}
}

// TestVoltaResolve_BudgetResetsAfterCooldown is the recovery case: the spend
// budget bounds one breaker window, not the process lifetime. Repeated slow
// failures separated by expired cooldowns must each get a fresh attempt, or Volta
// resolution latches off for good after a couple of transient outages.
func TestVoltaResolve_BudgetResetsAfterCooldown(t *testing.T) {
	skipIfNoPOSIXShell(t)
	resetVoltaResolveCache(t)

	origRun, origCooldown, origBudget := runVolta, voltaFailureCooldown, voltaResolveBudget
	t.Cleanup(func() {
		runVolta, voltaFailureCooldown, voltaResolveBudget = origRun, origCooldown, origBudget
	})
	voltaFailureCooldown = 20 * time.Millisecond
	voltaResolveBudget = 60 * time.Millisecond

	var mu sync.Mutex
	calls := 0
	// Each attempt costs most of the budget, so two of them exceed it.
	runVolta = func(string, string, ...string) (string, bool) {
		mu.Lock()
		calls++
		mu.Unlock()
		time.Sleep(40 * time.Millisecond)
		return "", false
	}

	f := newVoltaFixture(t, voltaFixtureOpts{commands: []string{"claude"}})
	t.Setenv("PATH", systemPath(f.binDir()))

	for i := 0; i < 3; i++ {
		if i > 0 {
			time.Sleep(2 * voltaFailureCooldown)
		}
		if _, ok := voltaResolve(f.alias("claude"), f.shim(), "claude"); ok {
			t.Fatalf("attempt %d unexpectedly resolved", i+1)
		}
	}

	mu.Lock()
	got := calls
	mu.Unlock()
	if got != 3 {
		t.Errorf("`volta` was invoked %d times across 3 expired cooldowns, want 3; the spend "+
			"budget is accumulating across windows and permanently trips the breaker", got)
	}
}

// TestVoltaResolve_BoundsTotalCostWhenVoltaHangs covers the additive-timeout
// problem: a wedged install used to cost one full timeout PER command.
func TestVoltaResolve_BoundsTotalCostWhenVoltaHangs(t *testing.T) {
	skipIfNoPOSIXShell(t)
	resetVoltaResolveCache(t)

	origRun, origTimeout, origWait := runVolta, voltaResolveTimeout, voltaResolveWaitDelay
	t.Cleanup(func() {
		runVolta, voltaResolveTimeout, voltaResolveWaitDelay = origRun, origTimeout, origWait
	})
	voltaResolveTimeout = 100 * time.Millisecond
	voltaResolveWaitDelay = 50 * time.Millisecond

	var mu sync.Mutex
	calls := 0
	runVolta = func(string, string, ...string) (string, bool) {
		mu.Lock()
		calls++
		mu.Unlock()
		time.Sleep(voltaResolveTimeout + voltaResolveWaitDelay)
		return "", false
	}

	commands := []string{"claude", "codex", "pi"}
	f := newVoltaFixture(t, voltaFixtureOpts{commands: commands})
	t.Setenv("PATH", systemPath(f.binDir()))

	start := time.Now()
	for _, cmd := range commands {
		if _, ok := voltaResolve(f.alias(cmd), f.shim(), cmd); ok {
			t.Fatalf("%s unexpectedly resolved against a hung volta", cmd)
		}
	}
	elapsed := time.Since(start)

	mu.Lock()
	got := calls
	mu.Unlock()
	if got != 1 {
		t.Errorf("`volta` was invoked %d times for %d commands, want 1; a broken install must "+
			"be asked once per cooldown, not once per command", got, len(commands))
	}
	singleCall := voltaResolveTimeout + voltaResolveWaitDelay
	if additive := time.Duration(len(commands)) * singleCall; elapsed >= additive {
		t.Errorf("resolving %d hung aliases took %v, at least the additive bound %v",
			len(commands), elapsed, additive)
	}
}

// TestVoltaResolve_CoalescesConcurrentResolutions covers the in-flight merge:
// releasing the lock for the subprocess must not let N concurrent callers each
// spawn their own `volta`.
func TestVoltaResolve_CoalescesConcurrentResolutions(t *testing.T) {
	skipIfNoPOSIXShell(t)
	resetVoltaResolveCache(t)

	origRun := runVolta
	t.Cleanup(func() { runVolta = origRun })

	f := newVoltaFixture(t, voltaFixtureOpts{commands: []string{"claude"}})
	t.Setenv("PATH", systemPath(f.binDir()))

	var mu sync.Mutex
	calls := 0
	runVolta = func(voltaBin, voltaHome string, args ...string) (string, bool) {
		mu.Lock()
		calls++
		mu.Unlock()
		time.Sleep(30 * time.Millisecond)
		return origRun(voltaBin, voltaHome, args...)
	}

	const concurrency = 8
	var wg sync.WaitGroup
	results := make([]resolvedExecutable, concurrency)
	oks := make([]bool, concurrency)
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], oks[i] = voltaResolve(f.alias("claude"), f.shim(), "claude")
		}(i)
	}
	wg.Wait()

	for i, ok := range oks {
		if !ok {
			t.Fatalf("goroutine %d failed to resolve", i)
		}
		if results[i].Path != results[0].Path {
			t.Errorf("goroutine %d resolved %q, want %q", i, results[i].Path, results[0].Path)
		}
	}
	mu.Lock()
	got := calls
	mu.Unlock()
	// One `volta which` for the tool. Without coalescing every goroutine spawns
	// its own, which is what the released lock would otherwise allow.
	if got > 1 {
		t.Errorf("`volta` was invoked %d times for %d concurrent resolutions of the same "+
			"command, want 1", got, concurrency)
	}
}

// TestVoltaResolve_IgnoresProjectDirectory covers the working-directory
// sensitivity: Volta resolves a project-local binary before the user default, so
// asking it from wherever the daemon started would pin one project's dependency as
// the machine-wide runtime.
func TestVoltaResolve_IgnoresProjectDirectory(t *testing.T) {
	skipIfNoPOSIXShell(t)
	resetVoltaResolveCache(t)

	projectDir := t.TempDir()
	projectBin := filepath.Join(projectDir, "node_modules", ".bin")
	mkdirs(t, projectBin)
	writeScript(t, filepath.Join(projectBin, "claude"), "#!/bin/sh\necho \"0.0.1 (project local)\"\n")

	physicalProject, err := filepath.EvalSymlinks(projectDir)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", projectDir, err)
	}
	f := newVoltaFixture(t, voltaFixtureOpts{commands: []string{"claude"}, projectDir: physicalProject})
	t.Setenv("PATH", systemPath(f.binDir()))

	// Sanity: the fixture really is cwd-sensitive, so this test can fail.
	cmd := exec.Command(f.voltaBin(), "which", "claude")
	cmd.Dir = projectDir
	cmd.Env = append(os.Environ(), "VOLTA_HOME="+f.home)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("fixture volta failed inside the project dir: %v", err)
	}
	physicalProjectBin := filepath.Join(physicalProject, "node_modules", ".bin", "claude")
	if got := strings.TrimSpace(string(out)); got != physicalProjectBin {
		t.Fatalf("fixture is not cwd-sensitive: got %q, want %q", got, physicalProjectBin)
	}

	t.Chdir(projectDir)
	resolved, ok := voltaResolve(f.alias("claude"), f.shim(), "claude")
	if !ok {
		t.Fatal("voltaResolve failed")
	}
	if resolved.Path == physicalProjectBin {
		t.Errorf("resolved the project-local binary; a daemon started inside a JS project " +
			"would pin that project's dependency as the machine runtime")
	}
	if want := f.concrete("claude"); resolved.Path != want {
		t.Errorf("path = %q, want the default toolchain binary %q", resolved.Path, want)
	}
}

// TestProbeAgentCLIs_ShellFallbackCarriesEnvironment covers the GUI/login-shell
// leg: it must apply the same resolution, environment included.
func TestProbeAgentCLIs_ShellFallbackCarriesEnvironment(t *testing.T) {
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
		t.Errorf("path = %q, want %q", entry.Path, want)
	}
	if dirs := entry.Env.PrefixPaths["PATH"]; len(dirs) == 0 || dirs[0] != f.boundNodeDir() {
		t.Errorf("PATH dirs = %v, want the bound Node %q", dirs, f.boundNodeDir())
	}
}

// TestReresolveAgentCommand_ShellFallbackCarriesEnvironment is the same for the
// self-heal path, which has its own shell branch.
func TestReresolveAgentCommand_ShellFallbackCarriesEnvironment(t *testing.T) {
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
	if want := f.concrete("claude"); resolved.Path != want {
		t.Errorf("path = %q, want %q", resolved.Path, want)
	}
	if resolved.Env.IsZero() {
		t.Error("self-heal dropped the execution environment")
	}
}

// TestDetectBuiltinRuntimes_RegistersVoltaManagedCLIs is the end-result test:
// through the real version probe and the real minimum-version gate, under a PATH
// that has no node of its own.
func TestDetectBuiltinRuntimes_RegistersVoltaManagedCLIs(t *testing.T) {
	skipIfNoPOSIXShell(t)
	resetVoltaResolveCache(t)

	commands := []string{"claude", "codex", "pi"}
	f := newVoltaFixture(t, voltaFixtureOpts{commands: commands})
	t.Setenv("PATH", systemPath(f.binDir()))
	isolateAgentDiscovery(t)

	d := freshDaemon("")
	d.cfg.Agents = probeAgentCLIs()

	registered := map[string]string{}
	for _, rt := range d.detectBuiltinRuntimes(context.Background()) {
		registered[rt["type"]] = rt["version"]
	}
	for _, cmd := range commands {
		version, ok := registered[cmd]
		if !ok {
			t.Errorf("%s missing from the registration payload; skipped reasons: %#v",
				cmd, d.skippedAgentsSnapshot())
			continue
		}
		if version != voltaFixtureVersions[cmd] {
			t.Errorf("%s registered version = %q, want %q", cmd, version, voltaFixtureVersions[cmd])
		}
	}
}

// TestProbeAgentCLIs_DiscoversVoltaManagedCLIs asserts the providers get distinct
// concrete paths rather than one shared shim.
func TestProbeAgentCLIs_DiscoversVoltaManagedCLIs(t *testing.T) {
	skipIfNoPOSIXShell(t)
	resetVoltaResolveCache(t)

	commands := []string{"claude", "codex", "pi"}
	f := newVoltaFixture(t, voltaFixtureOpts{commands: commands})
	t.Setenv("PATH", systemPath(f.binDir()))
	isolateAgentDiscovery(t)

	agents := probeAgentCLIs()
	seen := map[string]string{}
	for _, cmd := range commands {
		entry, ok := agents[cmd]
		if !ok {
			t.Fatalf("%s not discovered: %#v", cmd, agents)
		}
		if want := f.concrete(cmd); entry.Path != want {
			t.Errorf("%s path = %q, want %q", cmd, entry.Path, want)
		}
		if prev, dup := seen[entry.Path]; dup {
			t.Errorf("%s and %s share one path %q", prev, cmd, entry.Path)
		}
		seen[entry.Path] = cmd
	}
}

// TestResolveAgentExecutablePath_FailsClosedWithoutVoltaResolution pins the
// deliberate failure mode: with no way to ask Volta we must NOT fall back to the
// alias, whose environment we cannot reproduce at launch.
func TestResolveAgentExecutablePath_FailsClosedWithoutVoltaResolution(t *testing.T) {
	skipIfNoPOSIXShell(t)
	resetVoltaResolveCache(t)

	f := newVoltaFixture(t, voltaFixtureOpts{commands: []string{"claude"}, omitVolta: true})
	t.Setenv("PATH", systemPath(f.binDir()))

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

// TestVoltaResolve_ReresolvesAfterBinaryReplaced covers revalidation by existence.
func TestVoltaResolve_ReresolvesAfterBinaryReplaced(t *testing.T) {
	skipIfNoPOSIXShell(t)
	resetVoltaResolveCache(t)

	f := newVoltaFixture(t, voltaFixtureOpts{commands: []string{"claude"}})
	t.Setenv("PATH", systemPath(f.binDir()))

	first, ok := voltaResolve(f.alias("claude"), f.shim(), "claude")
	if !ok {
		t.Fatal("initial resolution failed")
	}
	if again, ok := voltaResolve(f.alias("claude"), f.shim(), "claude"); !ok || again.Path != first.Path {
		t.Fatalf("cached resolution = (%q, %v), want (%q, true)", again.Path, ok, first.Path)
	}
	if err := os.Remove(first.Path); err != nil {
		t.Fatalf("remove concrete binary: %v", err)
	}
	if voltaResolutionUsable(first) {
		t.Error("cached resolution still usable after the binary was removed")
	}
}

// TestResolveAgentExecutable_VoltaAliasShadowedByHooks proves the fix reaches the
// ~/.multica/hooks branch, which canonicalizes independently.
func TestResolveAgentExecutable_VoltaAliasShadowedByHooks(t *testing.T) {
	skipIfNoPOSIXShell(t)
	resetVoltaResolveCache(t)

	home := t.TempDir()
	t.Setenv("HOME", home)
	hooksDir := filepath.Join(home, ".multica", "hooks")
	mkdirs(t, hooksDir)
	writeScript(t, filepath.Join(hooksDir, "claude"), "#!/bin/sh\nexec claude \"$@\"\n")

	f := newVoltaFixture(t, voltaFixtureOpts{commands: []string{"claude"}})
	t.Setenv("PATH", systemPath(hooksDir, f.binDir()))

	resolved, err := resolveAgentExecutable("claude")
	if err != nil {
		t.Fatalf("resolveAgentExecutable: %v", err)
	}
	if filepath.Dir(resolved.Path) == hooksDir {
		t.Fatalf("resolved into the hooks dir (%q); the wrapper would recurse", resolved.Path)
	}
	if want := f.concrete("claude"); resolved.Path != want {
		t.Errorf("path = %q, want %q", resolved.Path, want)
	}
	if resolved.Env.IsZero() {
		t.Error("hooks branch dropped the execution environment")
	}
}

// TestCanonicalExecutablePath_NonVoltaSymlinkStillCanonicalized guards the blast
// radius: ordinary symlinks must keep collapsing to the real file.
func TestCanonicalExecutablePath_NonVoltaSymlinkStillCanonicalized(t *testing.T) {
	skipIfNoPOSIXShell(t)

	realBin := filepath.Join(t.TempDir(), "claude-0.9.9")
	writeScript(t, realBin, "#!/bin/sh\nexit 0\n")
	alias := filepath.Join(t.TempDir(), "claude")
	if err := os.Symlink(realBin, alias); err != nil {
		t.Fatal(err)
	}
	if got := canonicalExecutablePath(alias); filepath.Base(got) != "claude-0.9.9" {
		t.Errorf("canonicalExecutablePath(%q) = %q, want the real versioned binary", alias, got)
	}
}

// TestCanonicalExecutablePath_SymlinkedParentDirIsCanonicalized covers the other
// half of canonicalization: a symlinked directory must be collapsed too.
func TestCanonicalExecutablePath_SymlinkedParentDirIsCanonicalized(t *testing.T) {
	skipIfNoPOSIXShell(t)

	root := t.TempDir()
	realDir := filepath.Join(root, "versions", "20.1.0", "bin")
	mkdirs(t, realDir)
	writeScript(t, filepath.Join(realDir, "claude"), "#!/bin/sh\nexit 0\n")
	linkDir := filepath.Join(root, "current")
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Fatal(err)
	}

	got := canonicalExecutablePath(filepath.Join(linkDir, "claude"))
	wantDir, err := filepath.EvalSymlinks(realDir)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(wantDir, "claude"); got != want {
		t.Errorf("canonicalExecutablePath through a symlinked dir = %q, want %q", got, want)
	}
}

// TestCanonicalExecutablePath_ExplicitShimPathStaysCanonical documents the edge
// case: a path pointed straight at volta-shim has no dispatch name to recover.
func TestCanonicalExecutablePath_ExplicitShimPathStaysCanonical(t *testing.T) {
	skipIfNoPOSIXShell(t)
	resetVoltaResolveCache(t)

	f := newVoltaFixture(t, voltaFixtureOpts{})
	if got := canonicalExecutablePath(f.shim()); filepath.Base(got) != voltaShimName {
		t.Errorf("canonicalExecutablePath(%q) = %q, want it to stay volta-shim", f.shim(), got)
	}
}

// TestIsVoltaShimPath keeps the exception minimal: only Volta's actual shim names
// may opt out of plain symlink resolution.
func TestIsVoltaShimPath(t *testing.T) {
	for _, tc := range []struct {
		path string
		want bool
	}{
		{filepath.Join("/home/u/.volta/bin", "volta-shim"), true},
		{filepath.Join("/home/u/.volta/bin", "volta-shim.exe"), true},
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

func TestVoltaNonProjectDir(t *testing.T) {
	got := voltaNonProjectDir(filepath.Join("/home", "u", ".volta", "bin", "volta"))
	if runtime.GOOS != "windows" && got != "/" {
		t.Errorf("voltaNonProjectDir = %q, want the filesystem root", got)
	}
	if got == "" {
		t.Error("voltaNonProjectDir returned an empty directory")
	}
}

func TestEnvWithVoltaHome(t *testing.T) {
	got := envWithVoltaHome([]string{"PATH=/bin", "VOLTA_HOME=/old", "FOO=bar"}, "/new")
	var seen []string
	for _, kv := range got {
		if strings.HasPrefix(kv, "VOLTA_HOME=") {
			seen = append(seen, kv)
		}
	}
	if len(seen) != 1 || seen[0] != "VOLTA_HOME=/new" {
		t.Errorf("VOLTA_HOME entries = %v, want exactly [VOLTA_HOME=/new]", seen)
	}
}
