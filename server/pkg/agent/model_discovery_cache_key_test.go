package agent

import (
	"context"
	"testing"
)

// TestDiscoveryCacheKeyIsPathScoped pins that the 60s discovery memo is keyed
// by (provider, executable path). One host can run a built-in provider CLI and
// a custom runtime profile (MUL-3284) of the same protocol family pointing at a
// different, often differently versioned, binary; a provider-only key served
// one binary's catalog for the other for the full TTL (MUL-5471).
func TestDiscoveryCacheKeyIsPathScoped(t *testing.T) {
	builtIn := discoveryCacheKey("codex", "/usr/local/bin/codex")
	custom := discoveryCacheKey("codex", "/opt/bin/company-codex")
	if builtIn == custom {
		t.Fatalf("cache keys collide: %q", builtIn)
	}
	// An empty path (caller wants the provider default on PATH) keeps the bare
	// provider key so existing callers and tests stay compatible.
	if got := discoveryCacheKey("codex", ""); got != "codex" {
		t.Errorf("discoveryCacheKey(codex, \"\") = %q, want codex", got)
	}
}

// TestCachedDiscoveryDoesNotShareAcrossPaths exercises the memo itself: two
// distinct paths must each run their own discovery instead of the second one
// inheriting the first one's cached models.
func TestCachedDiscoveryDoesNotShareAcrossPaths(t *testing.T) {
	keyA := discoveryCacheKey("codex", t.TempDir()+"/codex-a")
	keyB := discoveryCacheKey("codex", t.TempDir()+"/codex-b")
	t.Cleanup(func() {
		modelCacheMu.Lock()
		delete(modelCache, keyA)
		delete(modelCache, keyB)
		modelCacheMu.Unlock()
	})

	calls := 0
	discover := func(id string) func() ([]Model, error) {
		return func() ([]Model, error) {
			calls++
			return []Model{{ID: id}}, nil
		}
	}

	gotA, err := cachedDiscovery(keyA, discover("model-a"))
	if err != nil {
		t.Fatalf("cachedDiscovery(A): %v", err)
	}
	gotB, err := cachedDiscovery(keyB, discover("model-b"))
	if err != nil {
		t.Fatalf("cachedDiscovery(B): %v", err)
	}
	if calls != 2 {
		t.Fatalf("discovery calls = %d, want 2 (one per binary)", calls)
	}
	if len(gotA) != 1 || gotA[0].ID != "model-a" {
		t.Errorf("path A models = %+v, want [model-a]", gotA)
	}
	if len(gotB) != 1 || gotB[0].ID != "model-b" {
		t.Errorf("path B models = %+v, want [model-b]", gotB)
	}

	// Same key again is served from the memo.
	if _, err := cachedDiscovery(keyA, discover("model-a")); err != nil {
		t.Fatalf("cachedDiscovery(A, cached): %v", err)
	}
	if calls != 2 {
		t.Errorf("discovery calls = %d after cached re-read, want 2", calls)
	}
}

// TestListModelsStaticProviderIgnoresCache is a light guard that switching the
// dynamic branches to a path-scoped key did not disturb static catalogs.
func TestListModelsStaticProviderIgnoresCache(t *testing.T) {
	models, err := ListModels(context.Background(), "claude", missingAgentExecutable(t, "claude"))
	if err != nil {
		t.Fatalf("ListModels(claude): %v", err)
	}
	if len(models) == 0 {
		t.Fatal("claude static catalog is empty")
	}
}
