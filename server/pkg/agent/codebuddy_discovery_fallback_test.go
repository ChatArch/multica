//go:build !windows

package agent

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// writeCodebuddyStub writes an executable stub at <dir>/codebuddy whose
// `--help` behaviour is supplied by body. Tests use a stub rather than the
// real CLI so the default suite never executes a user-installed agent binary.
func writeCodebuddyStub(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "codebuddy")
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write codebuddy stub: %v", err)
	}
	return path
}

// resetCodebuddyHelpCache drops any memoised --help output for path so each
// case actually re-runs the stub. codebuddyHelpStore is a package global.
func resetCodebuddyHelpCache(t *testing.T, path string) {
	t.Helper()
	drop := func() {
		codebuddyHelpMu.Lock()
		delete(codebuddyHelpStore, path)
		codebuddyHelpMu.Unlock()
	}
	drop()
	t.Cleanup(drop)
}

// TestCodebuddyHelpOutputRejectsFailedExec is the MUL-5549 regression: a
// `#!/usr/bin/env node` codebuddy whose interpreter is not on a GUI-launched
// daemon's PATH exits 127 and prints `env: node: No such file or directory` on
// stderr. codebuddyHelpOutput used to discard the exit status and hand that
// stderr back as if it were help text, so every parser downstream silently
// fell back — and the failure was then memoised for codebuddyHelpTTL.
func TestCodebuddyHelpOutputRejectsFailedExec(t *testing.T) {
	path := writeCodebuddyStub(t, "#!/bin/sh\necho 'env: node: No such file or directory' >&2\nexit 127\n")
	resetCodebuddyHelpCache(t, path)

	if got := codebuddyHelpOutput(context.Background(), path); got != "" {
		t.Fatalf("a non-zero exit must yield no help text, got %q", got)
	}

	codebuddyHelpMu.Lock()
	_, cached := codebuddyHelpStore[path]
	codebuddyHelpMu.Unlock()
	if cached {
		t.Error("a failed --help must not be cached; it would pin the failure for codebuddyHelpTTL")
	}
}

// TestDiscoverCodebuddyModelsMarksFallback pins that every degraded path is
// reported as Fallback. Before MUL-5549 all three returned (staticModels, nil),
// which the daemon reported as a successful discovery and the server then
// cached as this runtime's real catalog for 24h.
func TestDiscoverCodebuddyModelsMarksFallback(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
	}{
		{"binary missing", missingAgentExecutable(t, "codebuddy")},
		{"help exec fails", writeCodebuddyStub(t, "#!/bin/sh\necho 'env: node: No such file or directory' >&2\nexit 127\n")},
		{"help has no model line", writeCodebuddyStub(t, "#!/bin/sh\necho 'Usage: codebuddy [options]'\n")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetCodebuddyHelpCache(t, tc.path)

			catalog, err := discoverCodebuddyModels(context.Background(), tc.path)
			if err != nil {
				t.Fatalf("discoverCodebuddyModels: %v", err)
			}
			if !catalog.Fallback {
				t.Error("a degraded discovery must be marked Fallback")
			}
			// The models are still returned — the picker stays populated.
			if len(catalog.Models) == 0 {
				t.Error("expected the static stand-in to still be offered to the UI")
			}
		})
	}
}

// TestDiscoverCodebuddyModelsRealHelpIsNotFallback is the other half: a stub
// emitting the real v2.130.0 `--model` line must parse and must NOT be marked
// Fallback, so a genuine catalog still reaches the cache.
func TestDiscoverCodebuddyModelsRealHelpIsNotFallback(t *testing.T) {
	const helpLine = `  --model <model>    Model for the current session. Please provide the model ID. ` +
		`Currently supported: (hy3, glm-5.2, minimax-m3, kimi-k3-1, deepseek-v4-pro)`
	path := writeCodebuddyStub(t, "#!/bin/sh\ncat <<'EOF'\nUsage: codebuddy [options]\n"+helpLine+"\nEOF\n")
	resetCodebuddyHelpCache(t, path)

	catalog, err := discoverCodebuddyModels(context.Background(), path)
	if err != nil {
		t.Fatalf("discoverCodebuddyModels: %v", err)
	}
	if catalog.Fallback {
		t.Error("a parsed catalog must not be marked Fallback")
	}
	if len(catalog.Models) != 5 || catalog.Models[0].ID != "hy3" {
		t.Fatalf("unexpected catalog: %+v", catalog.Models)
	}
	// The static stand-in shares no IDs with the real catalog, which is what
	// made the silent fallback user-visible in the first place.
	for _, m := range catalog.Models {
		for _, static := range codebuddyStaticModels() {
			if m.ID == static.ID {
				t.Fatalf("real and fallback catalogs must stay distinguishable, both have %q", m.ID)
			}
		}
	}
}

// countingCodebuddyStub writes a codebuddy stub that appends a line to a
// counter file on every `--help` invocation, so a test can assert how many
// times the (slow, 35s-capped) command actually ran. helpBody is emitted on
// stdout for --help; --version always succeeds so runtime registration and the
// thinking-cache key lookup behave normally.
func countingCodebuddyStub(t *testing.T, helpBody string, helpExit int) (path, counter string) {
	t.Helper()
	dir := t.TempDir()
	counter = filepath.Join(dir, "help-calls")
	path = filepath.Join(dir, "codebuddy")
	script := "#!/bin/sh\n" +
		"case \"$1\" in\n" +
		"  --version) echo '2.130.0'; exit 0 ;;\n" +
		"  --help) echo call >> " + counter + "\n" +
		"          cat <<'HELPEOF'\n" + helpBody + "\nHELPEOF\n" +
		"          exit " + itoa(helpExit) + " ;;\n" +
		"esac\n" +
		"exit 0\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write counting stub: %v", err)
	}
	return path, counter
}

func itoa(n int) string { return strconv.Itoa(n) }

func helpCallCount(t *testing.T, counter string) int {
	t.Helper()
	b, err := os.ReadFile(counter)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatalf("read counter: %v", err)
	}
	return len(strings.Fields(string(b)))
}

// TestListModelsCodebuddyRunsHelpAtMostOnce is the MUL-5549 follow-up: model
// discovery and effort discovery both read `codebuddy --help`, and the effort
// pass used to call it independently. That was masked while a failed --help was
// wrongly memoised; once failures correctly stopped being cached, the failure
// path ran the 35s command TWICE in one request — past the server's 60s running
// timeout, so the request timed out and the late report was discarded as stale.
// The user then got no list at all, not even the fallback.
//
// Both paths must therefore run --help exactly once. This goes through the full
// ListModels entry point, since that is where the second call was introduced.
func TestListModelsCodebuddyRunsHelpAtMostOnce(t *testing.T) {
	const realHelp = "Usage: codebuddy [options]\n" +
		"  --model <model>   Currently supported: (hy3, glm-5.2, kimi-k3-1)\n" +
		"  --effort <level>  Reasoning effort level (low, medium, high, xhigh)"

	for _, tc := range []struct {
		name         string
		helpBody     string
		helpExit     int
		wantFallback bool
	}{
		{name: "help succeeds", helpBody: realHelp, helpExit: 0},
		// The original #6180 failure: interpreter missing, exit 127.
		{name: "help fails", helpBody: "env: node: No such file or directory", helpExit: 127, wantFallback: true},
		// Help runs but carries no model line (older//unauthenticated CLI).
		{name: "help unparseable", helpBody: "Usage: codebuddy [options]", helpExit: 0, wantFallback: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, counter := countingCodebuddyStub(t, tc.helpBody, tc.helpExit)
			resetCodebuddyHelpCache(t, path)
			// cachedDiscovery and the thinking cache are package globals; clear
			// both so this case actually executes the stub.
			modelCacheMu.Lock()
			delete(modelCache, "codebuddy")
			modelCacheMu.Unlock()
			resetThinkingCacheForTests()
			t.Cleanup(resetThinkingCacheForTests)

			catalog, err := ListModels(context.Background(), "codebuddy", path)
			if err != nil {
				t.Fatalf("ListModels(codebuddy): %v", err)
			}
			if catalog.Fallback != tc.wantFallback {
				t.Errorf("Fallback = %v, want %v", catalog.Fallback, tc.wantFallback)
			}
			if got := helpCallCount(t, counter); got != 1 {
				t.Errorf("`codebuddy --help` ran %d times, want exactly 1 — "+
					"two 35s attempts in one request exceed the server's 60s running timeout", got)
			}
			// Whichever path was taken, the picker still gets models and the
			// thinking picker still gets levels.
			if len(catalog.Models) == 0 {
				t.Fatal("expected a non-empty catalog")
			}
			if catalog.Models[0].Thinking == nil || len(catalog.Models[0].Thinking.SupportedLevels) == 0 {
				t.Error("expected effort levels to be annotated on both the real and fallback paths")
			}
		})
	}
}

// TestCachedDiscoveryDoesNotCacheFallback pins that a fallback never occupies
// the daemon's 60s discovery cache: the next request must be free to retry.
func TestCachedDiscoveryDoesNotCacheFallback(t *testing.T) {
	const key = "test-cache-fallback"
	reset := func() {
		modelCacheMu.Lock()
		delete(modelCache, key)
		modelCacheMu.Unlock()
	}
	reset()
	t.Cleanup(reset)

	calls := 0
	fn := func() (Catalog, error) {
		calls++
		return Catalog{Models: []Model{{ID: "stand-in"}}, Fallback: true}, nil
	}
	for i := 0; i < 2; i++ {
		got, err := cachedDiscovery(key, fn)
		if err != nil {
			t.Fatalf("cachedDiscovery: %v", err)
		}
		if !got.Fallback {
			t.Error("cachedDiscovery must preserve the Fallback marker")
		}
	}
	if calls != 2 {
		t.Fatalf("a fallback must not be cached: expected fn called 2x, got %d", calls)
	}
}
