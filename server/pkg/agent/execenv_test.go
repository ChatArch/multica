package agent

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// ExecEnv exists so a resolved executable travels with the environment it needs.
// These tests pin the mechanism (prefix semantics, cache-key distinctness) and
// prove the environment actually reaches the subprocess of a consumer that
// launches the CLI.

func skipIfNoPOSIXShell(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fixture needs a POSIX shell")
	}
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skipf("no /bin/sh: %v", err)
	}
}

func TestExecEnv_ZeroValueIsNoOp(t *testing.T) {
	var e ExecEnv
	if !e.IsZero() {
		t.Error("zero ExecEnv should be a no-op")
	}
	if got := e.CacheKey(); got != "" {
		t.Errorf("CacheKey = %q, want empty", got)
	}
	environ := []string{"PATH=/bin", "FOO=bar"}
	if got := e.ApplyToEnviron(environ); len(got) != 2 {
		t.Errorf("ApplyToEnviron changed a no-op environment: %v", got)
	}
	// An ExecEnv whose lists are all empty is still a no-op.
	empty := ExecEnv{PrefixPaths: map[string][]string{"PATH": {}}}
	if !empty.IsZero() {
		t.Error("ExecEnv with empty lists should be a no-op")
	}
}

func TestExecEnv_ApplyToEnvironPrefixes(t *testing.T) {
	sep := string(os.PathListSeparator)
	e := ExecEnv{PrefixPaths: map[string][]string{
		"PATH":      {"/node/bin"},
		"NODE_PATH": {"/shared"},
	}}
	got := e.ApplyToEnviron([]string{"PATH=/usr/bin" + sep + "/bin", "FOO=bar"})

	vals := map[string]string{}
	for _, kv := range got {
		if name, value, ok := strings.Cut(kv, "="); ok {
			if _, dup := vals[name]; dup {
				t.Errorf("duplicate entry for %s in %v", name, got)
			}
			vals[name] = value
		}
	}
	if want := "/node/bin" + sep + "/usr/bin" + sep + "/bin"; vals["PATH"] != want {
		t.Errorf("PATH = %q, want %q (prefix, existing value preserved)", vals["PATH"], want)
	}
	if vals["NODE_PATH"] != "/shared" {
		t.Errorf("NODE_PATH = %q, want /shared when previously unset", vals["NODE_PATH"])
	}
	if vals["FOO"] != "bar" {
		t.Errorf("unrelated var lost: %v", vals)
	}
}

func TestExecEnv_ApplyToMapPrefixes(t *testing.T) {
	sep := string(os.PathListSeparator)
	e := ExecEnv{PrefixPaths: map[string][]string{"PATH": {"/node/bin"}}}

	env := map[string]string{"PATH": "/existing"}
	e.ApplyToMap(env)
	if want := "/node/bin" + sep + "/existing"; env["PATH"] != want {
		t.Errorf("PATH = %q, want %q", env["PATH"], want)
	}

	// A map that does not carry PATH yet falls back to the process value, so the
	// task environment does not lose the inherited PATH.
	t.Setenv("PATH", "/from-process")
	env2 := map[string]string{}
	e.ApplyToMap(env2)
	if want := "/node/bin" + sep + "/from-process"; env2["PATH"] != want {
		t.Errorf("PATH = %q, want %q", env2["PATH"], want)
	}
}

func TestExecEnv_CacheKeyDistinguishesEnvironments(t *testing.T) {
	a := ExecEnv{PrefixPaths: map[string][]string{"PATH": {"/node/20/bin"}}}
	b := ExecEnv{PrefixPaths: map[string][]string{"PATH": {"/node/24/bin"}}}
	if a.CacheKey() == b.CacheKey() {
		t.Error("two different Node platforms share a cache key")
	}
	// Stable across map iteration order.
	multi1 := ExecEnv{PrefixPaths: map[string][]string{"PATH": {"/a"}, "NODE_PATH": {"/b"}}}
	multi2 := ExecEnv{PrefixPaths: map[string][]string{"NODE_PATH": {"/b"}, "PATH": {"/a"}}}
	if multi1.CacheKey() != multi2.CacheKey() {
		t.Errorf("cache key is not order-stable: %q vs %q", multi1.CacheKey(), multi2.CacheKey())
	}
}

// TestDiscoveryCacheKey_IncludesEnv is the review's "environment must be part of
// the discovery cache key": otherwise the same binary under two different
// toolchains would share one cached catalog.
func TestDiscoveryCacheKey_IncludesEnv(t *testing.T) {
	base := discoveryCacheKey("codex", "/bin/codex", ExecEnv{})
	withEnv := discoveryCacheKey("codex", "/bin/codex",
		ExecEnv{PrefixPaths: map[string][]string{"PATH": {"/node/20/bin"}}})
	other := discoveryCacheKey("codex", "/bin/codex",
		ExecEnv{PrefixPaths: map[string][]string{"PATH": {"/node/24/bin"}}})

	if base == withEnv {
		t.Error("cache key ignores the execution environment")
	}
	if withEnv == other {
		t.Error("cache key does not distinguish two execution environments")
	}
}

// newEnvDependentCLI writes a fake CLI that only works when a marker executable
// is reachable on PATH, mirroring a Volta package binary that needs the Node
// platform Volta bound to it. It returns the CLI path and the ExecEnv that makes
// it runnable.
func newEnvDependentCLI(t *testing.T, stdout string) (string, ExecEnv) {
	t.Helper()
	root := t.TempDir()
	envDir := filepath.Join(root, "toolchain", "bin")
	if err := os.MkdirAll(envDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(envDir, "fixture-node"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cli := filepath.Join(root, "cli")
	body := "#!/bin/sh\ncommand -v fixture-node >/dev/null 2>&1 || { echo 'env: node: No such file or directory' >&2; exit 127; }\n" + stdout
	if err := os.WriteFile(cli, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return cli, ExecEnv{PrefixPaths: map[string][]string{"PATH": {envDir}}}
}

// TestExecEnv_ReachesSubprocess proves the environment is actually applied at the
// exec boundary, not merely carried around.
func TestExecEnv_ReachesSubprocess(t *testing.T) {
	skipIfNoPOSIXShell(t)
	cli, env := newEnvDependentCLI(t, "echo ok\n")
	t.Setenv("PATH", "/usr/bin:/bin")

	if out, err := env.command(context.Background(), cli).Output(); err != nil {
		t.Errorf("with the environment applied: %v", err)
	} else if strings.TrimSpace(string(out)) != "ok" {
		t.Errorf("stdout = %q, want ok", out)
	}
	if _, err := (ExecEnv{}).command(context.Background(), cli).Output(); err == nil {
		t.Error("without the environment the fixture should fail; it no longer models the dependency")
	}
}

// TestDetectVersionWithEnv_UsesEnvironment covers the registration probe.
func TestDetectVersionWithEnv_UsesEnvironment(t *testing.T) {
	skipIfNoPOSIXShell(t)
	cli, env := newEnvDependentCLI(t, "echo '1.2.3'\n")
	t.Setenv("PATH", "/usr/bin:/bin")

	if v, err := DetectVersionWithEnv(context.Background(), cli, env); err != nil {
		t.Errorf("DetectVersionWithEnv: %v", err)
	} else if v != "1.2.3" {
		t.Errorf("version = %q, want 1.2.3", v)
	}
	if _, err := DetectVersion(context.Background(), cli); err == nil {
		t.Error("DetectVersion without the environment should fail")
	}
}

// TestListModelsWithEnv_UsesEnvironment is the review's item 2 for model
// discovery: a Volta-managed CLI is unrunnable unless ListModels applies the same
// environment, and pi's discovery shells out to the CLI.
func TestListModelsWithEnv_UsesEnvironment(t *testing.T) {
	skipIfNoPOSIXShell(t)
	// pi discovery runs `<cli> --list-models` and parses `provider model` lines.
	// It degrades to an empty catalog when the process cannot run, so the model
	// count is what distinguishes "ran" from "could not run".
	cli, env := newEnvDependentCLI(t, "echo 'anthropic claude-sonnet-4'\n")
	t.Setenv("PATH", "/usr/bin:/bin")

	withEnv, err := discoverPiModels(context.Background(), cli, env)
	if err != nil {
		t.Fatalf("discovery with the environment: %v", err)
	}
	if len(withEnv) == 0 {
		t.Error("discovery with the environment found no models; ListModels did not apply it, " +
			"so a Volta-managed CLI would come back with an empty catalog")
	}

	without, err := discoverPiModels(context.Background(), cli, ExecEnv{})
	if err != nil {
		t.Fatalf("discovery without the environment: %v", err)
	}
	if len(without) != 0 {
		t.Errorf("discovery without the environment found %d models; the fixture no longer "+
			"models the toolchain dependency", len(without))
	}
}

// TestValidatorsWithEnv_ReachListModelsWithEnv covers the service-tier and
// thinking validators, which called the env-less ListModels and so silently
// dropped the toolchain for every Volta-managed CLI.
//
// The verdict is not a usable signal (a provider may simply not support the
// value), so this asserts on the discovery cache: the entry the validator
// populates must be keyed with the environment's fingerprint. With the env-less
// call it would land under a key that has no fingerprint at all.
func TestValidatorsWithEnv_ReachListModelsWithEnv(t *testing.T) {
	skipIfNoPOSIXShell(t)
	cli, env := newEnvDependentCLI(t, "echo 'anthropic claude-sonnet-4'\n")
	t.Setenv("PATH", "/usr/bin:/bin")

	resetModelCacheForTest()
	if _, err := ValidateThinkingLevelWithEnv(context.Background(), "pi", cli,
		"anthropic/claude-sonnet-4", "high", env); err != nil {
		t.Fatalf("ValidateThinkingLevelWithEnv: %v", err)
	}
	wantKey := discoveryCacheKey("pi", cli, env)
	if !modelCacheHasKey(wantKey) {
		t.Errorf("thinking validator did not populate the cache under the env-aware key %q "+
			"(keys present: %v); it is still calling the env-less ListModels",
			wantKey, modelCacheKeys())
	}

	// ValidateServiceTier only consults a catalog for codex (it short-circuits for
	// every other provider), and a codex fallback catalog is deliberately not
	// cached — so assert the negative instead: nothing may be cached under a key
	// that lacks the environment fingerprint.
	resetModelCacheForTest()
	if _, err := ValidateServiceTierWithEnv(context.Background(), "codex", cli,
		"gpt-5-codex", "flex", env); err != nil {
		t.Logf("ValidateServiceTierWithEnv error (fails open by design): %v", err)
	}
	for _, key := range modelCacheKeys() {
		if !strings.Contains(key, env.CacheKey()) {
			t.Errorf("service-tier validator cached %q, which lacks the environment "+
				"fingerprint %q; it is still calling the env-less ListModels", key, env.CacheKey())
		}
	}

	// The zero-env wrappers must remain callable for every existing caller.
	if _, err := ValidateServiceTier(context.Background(), "codex", cli, "gpt-5", "flex"); err != nil {
		t.Logf("ValidateServiceTier error (fails open by design): %v", err)
	}
}

func resetModelCacheForTest() {
	modelCacheMu.Lock()
	defer modelCacheMu.Unlock()
	modelCache = map[string]modelCacheEntry{}
}

func modelCacheHasKey(key string) bool {
	modelCacheMu.Lock()
	defer modelCacheMu.Unlock()
	_, ok := modelCache[key]
	return ok
}

func modelCacheKeys() []string {
	modelCacheMu.Lock()
	defer modelCacheMu.Unlock()
	keys := make([]string, 0, len(modelCache))
	for k := range modelCache {
		keys = append(keys, k)
	}
	return keys
}
