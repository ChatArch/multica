package daemon

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const testRepoURL = "https://example.com/acme/widgets.git"

// newEvictTestRepo clones a bare repo into the exact cache path the daemon
// would use for repoURL, so the live-set lookup (which is keyed on that path)
// lines up with what is on disk.
func newEvictTestRepo(t *testing.T, d *Daemon, workspaceID, repoURL string) string {
	t.Helper()
	source := createGCGitRepo(t)
	barePath := d.repoCache.BarePath(workspaceID, repoURL)
	if err := os.MkdirAll(filepath.Dir(barePath), 0o755); err != nil {
		t.Fatal(err)
	}
	runGitForGC(t, "", "clone", "--bare", source, barePath)
	return barePath
}

// writeLastUsed pins the on-disk stamp format that repocache.LastUsed reads.
func writeLastUsed(t *testing.T, barePath string, at time.Time) {
	t.Helper()
	path := filepath.Join(barePath, ".multica_last_used")
	if err := os.WriteFile(path, []byte(at.UTC().Format(time.RFC3339Nano)), 0o644); err != nil {
		t.Fatal(err)
	}
}

// attachRepo registers a workspace that claims repoURL, which is what makes the
// repo "live" and therefore ineligible for eviction.
func attachRepo(d *Daemon, workspaceID, repoURL string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.workspaces[workspaceID] = &workspaceState{
		workspaceID:     workspaceID,
		allowedRepoURLs: map[string]struct{}{repoURL: {}},
	}
}

func runRepoGC(d *Daemon) *gcStats {
	stats := &gcStats{byPattern: map[string]int{}}
	d.pruneRepoWorktrees(d.cfg.WorkspacesRoot, stats)
	return stats
}

// TestEvictRepoCache_RemovesIdleDetachedRepo is the happy path: a cache no
// workspace claims, with no worktrees, untouched past the TTL.
func TestEvictRepoCache_RemovesIdleDetachedRepo(t *testing.T) {
	d := newGCTestDaemon(t, http.NewServeMux())
	d.cfg.GCRepoTTL = 24 * time.Hour

	wsID := "11111111-1111-1111-1111-111111111111"
	barePath := newEvictTestRepo(t, d, wsID, testRepoURL)
	writeLastUsed(t, barePath, time.Now().Add(-48*time.Hour))

	stats := runRepoGC(d)

	if _, err := os.Stat(barePath); !os.IsNotExist(err) {
		t.Fatalf("expected bare repo to be evicted, stat err = %v", err)
	}
	if stats.repoCachesReclaimed != 1 {
		t.Errorf("repo_caches_reclaimed = %d, want 1", stats.repoCachesReclaimed)
	}
	if stats.bytesReclaimed <= 0 {
		t.Errorf("bytes_reclaimed = %d, want > 0", stats.bytesReclaimed)
	}
	// The per-workspace directory is now empty and should go with it.
	wsDir := filepath.Join(d.cfg.WorkspacesRoot, reposDirName, wsID)
	if _, err := os.Stat(wsDir); !os.IsNotExist(err) {
		t.Errorf("expected empty workspace cache dir to be removed, stat err = %v", err)
	}
}

// TestEvictRepoCache_KeepsRepoStillAttachedToWorkspace is the load-bearing
// guard. Sync re-clones every listed repo that is missing whenever a workspace
// registers, which happens on every daemon start — so evicting a repo the
// workspace still claims would just pay a full re-clone on the next restart.
func TestEvictRepoCache_KeepsRepoStillAttachedToWorkspace(t *testing.T) {
	d := newGCTestDaemon(t, http.NewServeMux())
	d.cfg.GCRepoTTL = 24 * time.Hour

	wsID := "11111111-1111-1111-1111-111111111111"
	barePath := newEvictTestRepo(t, d, wsID, testRepoURL)
	writeLastUsed(t, barePath, time.Now().Add(-90*24*time.Hour))
	attachRepo(d, wsID, testRepoURL)

	stats := runRepoGC(d)

	if _, err := os.Stat(barePath); err != nil {
		t.Fatalf("attached repo must survive eviction however idle: %v", err)
	}
	if stats.repoCachesReclaimed != 0 {
		t.Errorf("repo_caches_reclaimed = %d, want 0", stats.repoCachesReclaimed)
	}
}

// TestEvictRepoCache_KeepsRepoWithLiveWorktree guards the checkout that would
// break: a linked worktree's .git points into this directory.
func TestEvictRepoCache_KeepsRepoWithLiveWorktree(t *testing.T) {
	d := newGCTestDaemon(t, http.NewServeMux())
	d.cfg.GCRepoTTL = 24 * time.Hour

	wsID := "11111111-1111-1111-1111-111111111111"
	barePath := newEvictTestRepo(t, d, wsID, testRepoURL)
	writeLastUsed(t, barePath, time.Now().Add(-90*24*time.Hour))

	worktree := filepath.Join(t.TempDir(), "checkout")
	runGitForGC(t, "", "-C", barePath, "worktree", "add", "-b", "agent/live/1234", worktree, "HEAD")

	stats := runRepoGC(d)

	if _, err := os.Stat(barePath); err != nil {
		t.Fatalf("repo with a live worktree must survive: %v", err)
	}
	if stats.repoCachesReclaimed != 0 {
		t.Errorf("repo_caches_reclaimed = %d, want 0", stats.repoCachesReclaimed)
	}
}

// TestEvictRepoCache_MissingStampIsBackfilledNotEvicted is the upgrade-safety
// case. Every cache that predates the stamp reports "unknown"; reading that as
// "infinitely old" would wipe every repo cache on the machine in the first
// cycle after an upgrade and force a full re-clone of each.
func TestEvictRepoCache_MissingStampIsBackfilledNotEvicted(t *testing.T) {
	d := newGCTestDaemon(t, http.NewServeMux())
	d.cfg.GCRepoTTL = time.Nanosecond // any real stamp would be past this

	wsID := "11111111-1111-1111-1111-111111111111"
	barePath := newEvictTestRepo(t, d, wsID, testRepoURL)

	stats := runRepoGC(d)

	if _, err := os.Stat(barePath); err != nil {
		t.Fatalf("a cache with no stamp must not be evicted on first sight: %v", err)
	}
	if stats.repoCachesReclaimed != 0 {
		t.Errorf("repo_caches_reclaimed = %d, want 0", stats.repoCachesReclaimed)
	}
	if _, err := os.Stat(filepath.Join(barePath, ".multica_last_used")); err != nil {
		t.Errorf("expected the missing stamp to be backfilled so the clock starts now: %v", err)
	}

	// Second cycle: the backfilled stamp is now older than the 1ns TTL, so the
	// repo becomes eligible — the grace period is one cycle, not permanent.
	time.Sleep(2 * time.Millisecond)
	if stats := runRepoGC(d); stats.repoCachesReclaimed != 1 {
		t.Errorf("second cycle repo_caches_reclaimed = %d, want 1", stats.repoCachesReclaimed)
	}
}

// TestEvictRepoCache_DisabledWhenTTLZero keeps the feature opt-out.
func TestEvictRepoCache_DisabledWhenTTLZero(t *testing.T) {
	d := newGCTestDaemon(t, http.NewServeMux())
	d.cfg.GCRepoTTL = 0

	wsID := "11111111-1111-1111-1111-111111111111"
	barePath := newEvictTestRepo(t, d, wsID, testRepoURL)
	writeLastUsed(t, barePath, time.Now().Add(-365*24*time.Hour))

	stats := runRepoGC(d)

	if _, err := os.Stat(barePath); err != nil {
		t.Fatalf("MULTICA_GC_REPO_TTL=0 must disable eviction entirely: %v", err)
	}
	if stats.repoCachesReclaimed != 0 {
		t.Errorf("repo_caches_reclaimed = %d, want 0", stats.repoCachesReclaimed)
	}
}

// TestEvictRepoCache_KeepsRecentlyUsedRepo covers the ordinary in-use case: a
// detached repo that a task checked out inside the TTL window.
func TestEvictRepoCache_KeepsRecentlyUsedRepo(t *testing.T) {
	d := newGCTestDaemon(t, http.NewServeMux())
	d.cfg.GCRepoTTL = 24 * time.Hour

	wsID := "11111111-1111-1111-1111-111111111111"
	barePath := newEvictTestRepo(t, d, wsID, testRepoURL)
	writeLastUsed(t, barePath, time.Now().Add(-1*time.Hour))

	stats := runRepoGC(d)

	if _, err := os.Stat(barePath); err != nil {
		t.Fatalf("repo used within the TTL must survive: %v", err)
	}
	if stats.repoCachesReclaimed != 0 {
		t.Errorf("repo_caches_reclaimed = %d, want 0", stats.repoCachesReclaimed)
	}
}

// TestLinkedWorktreeCount checks the porcelain parsing directly: the bare
// repo's own block carries a `bare` marker and must not be counted, or every
// cache would look permanently in use.
func TestLinkedWorktreeCount(t *testing.T) {
	d := newGCTestDaemon(t, http.NewServeMux())
	barePath := newEvictTestRepo(t, d, "11111111-1111-1111-1111-111111111111", testRepoURL)

	count, err := linkedWorktreeCount(barePath)
	if err != nil {
		t.Fatalf("linkedWorktreeCount: %v", err)
	}
	if count != 0 {
		t.Fatalf("bare repo with no checkouts = %d, want 0 (its own block is marked bare)", count)
	}

	first := filepath.Join(t.TempDir(), "wt1")
	second := filepath.Join(t.TempDir(), "wt2")
	runGitForGC(t, "", "-C", barePath, "worktree", "add", "-b", "agent/a/1", first, "HEAD")
	runGitForGC(t, "", "-C", barePath, "worktree", "add", "-b", "agent/b/2", second, "HEAD")

	count, err = linkedWorktreeCount(barePath)
	if err != nil {
		t.Fatalf("linkedWorktreeCount: %v", err)
	}
	if count != 2 {
		t.Errorf("linked worktrees = %d, want 2", count)
	}
}
