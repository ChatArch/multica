package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/multica-ai/multica/server/pkg/agent"
)

// Volta installs one trampoline binary and symlinks every managed command to
// it, dispatching on the name it was invoked as (#6183). Resolving that symlink
// collapses claude/codex/pi onto the same volta-shim path and the shim then
// exits 126, so the daemon asks Volta for the concrete binary instead. These
// tests cover that resolution, the registration it has to survive, and the
// blast radius of the exception.

// voltaFixture is a ~/.volta lookalike.
type voltaFixture struct {
	binDir   string // the shim dir: volta, volta-shim, and one symlink per command
	imageDir string // where the concrete per-tool binaries live
}

func (f voltaFixture) alias(command string) string    { return filepath.Join(f.binDir, command) }
func (f voltaFixture) shim() string                   { return filepath.Join(f.binDir, voltaShimName) }
func (f voltaFixture) concrete(command string) string { return filepath.Join(f.imageDir, command) }

// voltaFixtureVersions are the versions the concrete fixture binaries report.
// They must clear agent.MinVersions (claude >= 2.0.0, codex >= 0.100.0) or the
// registration-level test below would prove nothing: the provider would be
// dropped by the min-version gate rather than by the bug under test.
var voltaFixtureVersions = map[string]string{
	"claude": "2.1.0 (Claude Code)",
	"codex":  "codex-cli 0.140.0",
	"pi":     "pi 0.9.0",
}

// newVoltaFixture builds the shim dir, the concrete binaries, a faithful
// argv[0]-dispatching volta-shim, and a `volta` that answers `which`.
//
// withVolta=false omits the `volta` binary, modelling an install where the
// daemon cannot ask Volta for the concrete path.
func newVoltaFixture(t *testing.T, withVolta bool, commands ...string) voltaFixture {
	t.Helper()
	root := t.TempDir()
	f := voltaFixture{
		binDir:   filepath.Join(root, "bin"),
		imageDir: filepath.Join(root, "image"),
	}
	for _, dir := range []string{f.binDir, f.imageDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	for _, command := range commands {
		version, ok := voltaFixtureVersions[command]
		if !ok {
			t.Fatalf("no fixture version defined for %q", command)
		}
		writeScript(t, f.concrete(command), fmt.Sprintf("#!/bin/sh\necho %q\n", version))
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

	for _, command := range commands {
		if err := os.Symlink(f.shim(), f.alias(command)); err != nil {
			t.Fatalf("symlink %s -> volta-shim: %v", command, err)
		}
	}

	if withVolta {
		// `volta which <tool>` prints the concrete path on stdout and exits
		// non-zero without output when it cannot resolve, as upstream does.
		writeScript(t, filepath.Join(f.binDir, "volta"), fmt.Sprintf(`#!/bin/sh
if [ "$1" = "which" ] && [ -n "$2" ]; then
  p=%q/"$2"
  if [ -x "$p" ]; then echo "$p"; exit 0; fi
fi
exit 1
`, f.imageDir))
	}
	return f
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

// resetVoltaResolveCache clears the process-wide `volta which` cache so tests
// cannot observe each other's answers.
func resetVoltaResolveCache(t *testing.T) {
	t.Helper()
	clear := func() {
		voltaResolveMu.Lock()
		defer voltaResolveMu.Unlock()
		voltaResolveCache = map[string]string{}
	}
	clear()
	t.Cleanup(clear)
}

// TestResolveAgentExecutablePath_PinsVoltaConcreteBinary is the primary
// regression test for #6183. The pinned path must be the concrete binary, so
// that the path we version-check is the path we later execute.
func TestResolveAgentExecutablePath_PinsVoltaConcreteBinary(t *testing.T) {
	skipIfNoPOSIXShell(t)
	resetVoltaResolveCache(t)

	commands := []string{"claude", "codex", "pi"}
	f := newVoltaFixture(t, true, commands...)
	t.Setenv("PATH", f.binDir)

	for _, command := range commands {
		got, err := resolveAgentExecutablePath(command)
		if err != nil {
			t.Fatalf("%s: resolveAgentExecutablePath: %v", command, err)
		}
		if got == f.shim() {
			t.Fatalf("%s pinned the shared shim %q; the version probe exits 126 there (#6183)", command, got)
		}
		if got == f.alias(command) {
			t.Errorf("%s pinned the Volta alias %q; that path dispatches per working directory, "+
				"so the gated version would not be the executed version", command, got)
		}
		if want := f.concrete(command); got != want {
			t.Errorf("%s path = %q, want the concrete binary %q", command, got, want)
		}

		version, err := agent.DetectVersion(context.Background(), got)
		if err != nil {
			t.Fatalf("%s: DetectVersion(%q): %v", command, got, err)
		}
		if version != voltaFixtureVersions[command] {
			t.Errorf("%s version = %q, want %q", command, version, voltaFixtureVersions[command])
		}
	}
}

// TestProbeAgentCLIs_DiscoversVoltaManagedCLIs covers the discovery entry point
// the daemon actually calls and asserts the three providers get three distinct
// concrete paths rather than one shared shim.
func TestProbeAgentCLIs_DiscoversVoltaManagedCLIs(t *testing.T) {
	skipIfNoPOSIXShell(t)
	resetVoltaResolveCache(t)

	commands := []string{"claude", "codex", "pi"}
	f := newVoltaFixture(t, true, commands...)
	t.Setenv("PATH", f.binDir)
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

// TestDetectBuiltinRuntimes_RegistersVoltaManagedCLIs is the end-result test:
// availability alone was never the user-visible outcome. This runs the real
// version probe and the real minimum-version gate over a Volta install and
// asserts all three providers reach the registration payload.
func TestDetectBuiltinRuntimes_RegistersVoltaManagedCLIs(t *testing.T) {
	skipIfNoPOSIXShell(t)
	resetVoltaResolveCache(t)

	commands := []string{"claude", "codex", "pi"}
	f := newVoltaFixture(t, true, commands...)
	t.Setenv("PATH", f.binDir)
	isolateAgentDiscovery(t)

	// Deliberately no detectAgentVersion / checkAgentMinVersion stubs: the point
	// is that the real gate accepts what discovery pinned.
	d := freshDaemon("")
	d.cfg.Agents = probeAgentCLIs()

	runtimes := d.detectBuiltinRuntimes(context.Background())

	registered := map[string]string{}
	for _, rt := range runtimes {
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

// TestResolveAgentExecutablePath_FailsClosedWithoutVoltaResolution pins the
// deliberate failure mode: with no way to ask Volta for the concrete binary we
// must NOT fall back to the alias, because that path is only version-checkable
// under conditions we cannot reproduce at launch. Staying unregistered is the
// correct outcome; MULTICA_*_PATH remains the escape hatch.
func TestResolveAgentExecutablePath_FailsClosedWithoutVoltaResolution(t *testing.T) {
	skipIfNoPOSIXShell(t)
	resetVoltaResolveCache(t)

	f := newVoltaFixture(t, false, "claude")
	t.Setenv("PATH", f.binDir)

	got, err := resolveAgentExecutablePath("claude")
	if err != nil {
		t.Fatalf("resolveAgentExecutablePath: %v", err)
	}
	if got == f.alias("claude") {
		t.Fatalf("fell back to the alias %q; that reintroduces an ungated launch path", got)
	}
	// The shim path itself is still canonicalized, so compare against the
	// resolved form (on macOS /tmp is a symlink to /private/tmp).
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

// TestVoltaConcreteExecutable_ReresolvesAfterBinaryReplaced covers the cache's
// revalidation-by-existence: when Volta swaps the underlying tool and the old
// concrete path disappears, the next resolution must not keep serving it.
func TestVoltaConcreteExecutable_ReresolvesAfterBinaryReplaced(t *testing.T) {
	skipIfNoPOSIXShell(t)
	resetVoltaResolveCache(t)

	f := newVoltaFixture(t, true, "claude")
	t.Setenv("PATH", f.binDir)

	first, ok := voltaConcreteExecutable(f.shim(), "claude")
	if !ok {
		t.Fatal("initial resolution failed")
	}

	// Cached answer is reused while it is still runnable.
	if again, ok := voltaConcreteExecutable(f.shim(), "claude"); !ok || again != first {
		t.Fatalf("cached resolution = (%q, %v), want (%q, true)", again, ok, first)
	}

	// Volta replaces the tool: the old path is gone and `volta which` now
	// answers with a different one.
	if err := os.Remove(first); err != nil {
		t.Fatalf("remove concrete binary: %v", err)
	}
	newImage := filepath.Join(t.TempDir(), "image")
	if err := os.MkdirAll(newImage, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeScript(t, filepath.Join(newImage, "claude"), "#!/bin/sh\necho \"3.0.0 (Claude Code)\"\n")
	writeScript(t, filepath.Join(f.binDir, "volta"), fmt.Sprintf(`#!/bin/sh
if [ "$1" = "which" ] && [ -n "$2" ]; then
  p=%q/"$2"
  if [ -x "$p" ]; then echo "$p"; exit 0; fi
fi
exit 1
`, newImage))

	second, ok := voltaConcreteExecutable(f.shim(), "claude")
	if !ok {
		t.Fatal("re-resolution failed after the pinned binary disappeared")
	}
	if second == first {
		t.Errorf("still serving the removed path %q", first)
	}
	if want := filepath.Join(newImage, "claude"); second != want {
		t.Errorf("re-resolved path = %q, want %q", second, want)
	}
}

// TestResolveAgentExecutablePath_VoltaAliasShadowedByHooks proves the fix
// reaches the ~/.multica/hooks branch, which canonicalizes independently: a
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

	f := newVoltaFixture(t, true, "claude")
	t.Setenv("PATH", hooksDir+string(os.PathListSeparator)+f.binDir)

	got, err := resolveAgentExecutablePath("claude")
	if err != nil {
		t.Fatalf("resolveAgentExecutablePath: %v", err)
	}
	if filepath.Dir(got) == hooksDir {
		t.Fatalf("resolved into the hooks dir (%q); the wrapper would recurse", got)
	}
	if want := f.concrete("claude"); got != want {
		t.Errorf("path = %q, want the concrete binary %q", got, want)
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

	f := newVoltaFixture(t, true)
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
