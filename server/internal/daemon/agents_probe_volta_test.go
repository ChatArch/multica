package daemon

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/multica-ai/multica/server/pkg/agent"
)

// Volta installs one trampoline binary and symlinks every managed command to
// it, dispatching on the name it was invoked as (#6183). These tests pin down
// that the daemon keeps that name, because resolving the symlink collapses
// claude/codex/pi onto the same volta-shim path and the shim then exits 126.

// voltaShimBody is a stand-in for Volta's trampoline: it reports a version only
// when invoked under a managed command's name and exits 126 otherwise, exactly
// like the real shim. It uses ${0##*/} rather than basename(1) so the fixture
// keeps working under the restricted PATHs these tests set.
const voltaShimBody = `#!/bin/sh
case "${0##*/}" in
  claude) echo "1.2.3 (Claude Code)" ;;
  codex) echo "codex-cli 0.140.0" ;;
  pi) echo "pi 0.9.0" ;;
  *) echo "volta error: no tool selected" >&2; exit 126 ;;
esac
`

// newVoltaBinDir builds a ~/.volta/bin lookalike: one volta-shim plus an alias
// symlink per command name. It returns the directory.
func newVoltaBinDir(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	shim := filepath.Join(dir, voltaShimName)
	if err := os.WriteFile(shim, []byte(voltaShimBody), 0o755); err != nil {
		t.Fatalf("write volta-shim: %v", err)
	}
	for _, name := range names {
		if err := os.Symlink(shim, filepath.Join(dir, name)); err != nil {
			t.Fatalf("symlink %s -> volta-shim: %v", name, err)
		}
	}
	return dir
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

// TestResolveAgentExecutablePath_KeepsVoltaAliasName is the direct regression
// test for #6183: all three reported commands must stay distinguishable and
// must still answer --version after resolution.
func TestResolveAgentExecutablePath_KeepsVoltaAliasName(t *testing.T) {
	skipIfNoPOSIXShell(t)

	names := []string{"claude", "codex", "pi"}
	voltaBin := newVoltaBinDir(t, names...)
	t.Setenv("PATH", voltaBin)

	wantVersion := map[string]string{
		"claude": "1.2.3 (Claude Code)",
		"codex":  "codex-cli 0.140.0",
		"pi":     "pi 0.9.0",
	}
	seen := map[string]string{}
	for _, name := range names {
		got, err := resolveAgentExecutablePath(name)
		if err != nil {
			t.Fatalf("%s: resolveAgentExecutablePath: %v", name, err)
		}
		if base := filepath.Base(got); base != name {
			t.Errorf("%s resolved to %q (basename %q); the Volta shim picks its tool from "+
				"argv[0], so collapsing the alias makes the command unrunnable", name, got, base)
		}
		if prev, dup := seen[filepath.Base(got)]; dup {
			t.Errorf("%s and this path collide: %q", prev, got)
		}
		seen[filepath.Base(got)] = name

		version, err := agent.DetectVersion(context.Background(), got)
		if err != nil {
			t.Fatalf("%s: DetectVersion(%q): %v (this is the exit-126 failure from #6183)", name, got, err)
		}
		if version != wantVersion[name] {
			t.Errorf("%s version = %q, want %q", name, version, wantVersion[name])
		}
	}
}

// TestProbeAgentCLIs_DiscoversVoltaManagedCLIs covers the same fix one level up,
// at the discovery entry point the daemon actually calls, and asserts the three
// providers get three distinct paths rather than one shared shim.
func TestProbeAgentCLIs_DiscoversVoltaManagedCLIs(t *testing.T) {
	skipIfNoPOSIXShell(t)

	voltaBin := newVoltaBinDir(t, "claude", "codex", "pi")
	t.Setenv("PATH", voltaBin)

	// Keep discovery hermetic: no login-shell fork, no Codex Desktop bundle.
	origShell := resolveAgentsViaLoginShell
	t.Cleanup(func() { resolveAgentsViaLoginShell = origShell })
	resolveAgentsViaLoginShell = func([]string) map[string]string { return nil }
	resetShellResolveCacheForTest(t)
	origBundle := codexDesktopAppBundlePaths
	t.Cleanup(func() { codexDesktopAppBundlePaths = origBundle })
	codexDesktopAppBundlePaths = func() []string { return nil }

	agents := probeAgentCLIs()

	paths := map[string]string{}
	for _, provider := range []string{"claude", "codex", "pi"} {
		entry, ok := agents[provider]
		if !ok {
			t.Fatalf("%s not discovered from a Volta install: %#v", provider, agents)
		}
		if base := filepath.Base(entry.Path); base != provider {
			t.Errorf("%s path = %q, want the alias basename %q", provider, entry.Path, provider)
		}
		if prev, dup := paths[entry.Path]; dup {
			t.Errorf("%s and %s share one path %q; the shim cannot tell them apart", prev, provider, entry.Path)
		}
		paths[entry.Path] = provider
	}
}

// TestCanonicalExecutablePath_NonVoltaSymlinkStillCanonicalized guards the blast
// radius of the fix. Ordinary symlinks must keep collapsing to the real file —
// that is what pins agents against PATH drift and powers the MUL-4486 self-heal.
func TestCanonicalExecutablePath_NonVoltaSymlinkStillCanonicalized(t *testing.T) {
	skipIfNoPOSIXShell(t)

	realDir := t.TempDir()
	realBin := filepath.Join(realDir, "claude-0.9.9")
	if err := os.WriteFile(realBin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write real binary: %v", err)
	}
	binDir := t.TempDir()
	alias := filepath.Join(binDir, "claude")
	if err := os.Symlink(realBin, alias); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	got := canonicalExecutablePath(alias)
	if filepath.Base(got) != "claude-0.9.9" {
		t.Errorf("canonicalExecutablePath(%q) = %q, want the real versioned binary; "+
			"non-Volta symlinks must still be resolved", alias, got)
	}
}

// TestCanonicalExecutablePath_ExplicitShimPathStaysCanonical documents the edge
// case: a path pointed straight at volta-shim carries no dispatch name, so there
// is nothing to preserve and it must not get special treatment.
func TestCanonicalExecutablePath_ExplicitShimPathStaysCanonical(t *testing.T) {
	skipIfNoPOSIXShell(t)

	voltaBin := newVoltaBinDir(t)
	shim := filepath.Join(voltaBin, voltaShimName)

	got := canonicalExecutablePath(shim)
	if filepath.Base(got) != voltaShimName {
		t.Errorf("canonicalExecutablePath(%q) = %q, want it to stay volta-shim", shim, got)
	}
}

// TestResolveAgentExecutablePath_VoltaAliasShadowedByHooks proves the fix reaches
// the ~/.multica/hooks branch too: that branch has its own canonicalize call, so
// a hooks-shadowed Volta command must both skip the wrapper and keep its alias.
func TestResolveAgentExecutablePath_VoltaAliasShadowedByHooks(t *testing.T) {
	skipIfNoPOSIXShell(t)

	home := t.TempDir()
	t.Setenv("HOME", home)
	hooksDir := filepath.Join(home, ".multica", "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatalf("create hooks dir: %v", err)
	}
	// A previously generated wrapper that re-execs the same command name; the
	// daemon must never pin or launch this.
	if err := os.WriteFile(filepath.Join(hooksDir, "claude"),
		[]byte("#!/bin/sh\nexec claude \"$@\"\n"), 0o755); err != nil {
		t.Fatalf("write hook wrapper: %v", err)
	}

	voltaBin := newVoltaBinDir(t, "claude")
	t.Setenv("PATH", hooksDir+string(os.PathListSeparator)+voltaBin)

	got, err := resolveAgentExecutablePath("claude")
	if err != nil {
		t.Fatalf("resolveAgentExecutablePath: %v", err)
	}
	if filepath.Dir(got) == hooksDir {
		t.Fatalf("resolved into the hooks dir (%q); the wrapper would recurse", got)
	}
	if filepath.Base(got) != "claude" {
		t.Errorf("path = %q, want the Volta alias basename claude", got)
	}
	// The version answer is what proves we landed on the Volta alias and not
	// the recursive wrapper.
	version, err := agent.DetectVersion(context.Background(), got)
	if err != nil {
		t.Fatalf("DetectVersion(%q): %v", got, err)
	}
	if version != "1.2.3 (Claude Code)" {
		t.Errorf("version = %q, want the Volta-managed claude version", version)
	}
}

func TestIsVoltaShimPath(t *testing.T) {
	for _, tc := range []struct {
		path string
		want bool
	}{
		{filepath.Join("/home/u/.volta/bin", "volta-shim"), true},
		{filepath.Join("/home/u/.volta/bin", "volta-shim.exe"), true},
		{filepath.Join("C:\\", "volta", "VOLTA-SHIM.EXE"), true},
		{filepath.Join("/home/u/.volta/bin", "claude"), false},
		{filepath.Join("/usr/local/bin", "volta"), false},
		{filepath.Join("/usr/local/bin", "volta-shim-wrapper"), false},
		{"", false},
	} {
		if got := isVoltaShimPath(tc.path); got != tc.want {
			t.Errorf("isVoltaShimPath(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}
