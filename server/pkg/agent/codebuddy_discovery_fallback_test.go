//go:build !windows

package agent

import (
	"context"
	"os"
	"path/filepath"
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
