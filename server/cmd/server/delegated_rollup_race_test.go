package main

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/handler"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Regressions for the MUL-5483 code review, round 5.

// pauseOnGetIssueDB blocks the first GetIssue call until release is closed,
// parking the close path after it has snapshotted the barrier's children but
// before it archives and writes. That is the window an out-of-band hook cannot
// cover, which is why the write itself re-checks membership.
type pauseOnGetIssueDB struct {
	db.DBTX
	release <-chan struct{}
	paused  chan struct{}
	once    sync.Once
}

func (p *pauseOnGetIssueDB) QueryRow(ctx context.Context, sql string, args ...interface{}) pgx.Row {
	// sqlc keeps the `-- name: <Query>` header in the generated constant.
	if strings.Contains(sql, "name: GetIssue ") || strings.Contains(sql, "name: GetIssue\n") {
		p.once.Do(func() {
			close(p.paused)
			<-p.release
		})
	}
	return p.DBTX.QueryRow(ctx, sql, args...)
}

// TestRollupRace_StaleSnapshotIsNotAnnounced: a child created while the close
// path is mid-flight must not let it announce "the batch is finished" from the
// snapshot it took before that child existed. The create hook cannot fix this on
// its own — it runs before the row exists, so it archives nothing, and the close
// then inserts anyway. Only re-checking membership inside the INSERT closes it.
func TestRollupRace_StaleSnapshotIsNotAnnounced(t *testing.T) {
	queries := db.New(testPool)
	bus := events.New()
	e, agentID := agentActorEvent(t)

	parentID := createTestIssue(t, testWorkspaceID, testUserID)
	t.Cleanup(func() { cleanupTestIssue(t, parentID) })
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM inbox_item WHERE issue_id = $1`, parentID)
	})
	seedParentDelegate(t, queries, parentID)

	c1 := stagedChild(t, util.MustParseUUID(parentID), 0, "in_progress")
	setStatus(t, c1, "in_review")

	release := make(chan struct{})
	paused := make(chan struct{})
	pausing := db.New(&pauseOnGetIssueDB{DBTX: testPool, release: release, paused: paused})

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		child := handler.IssueResponse{
			ID: c1, WorkspaceID: testWorkspaceID, Status: "in_review", ParentIssueID: &parentID,
		}
		notifyDelegatedSubtreeSettled(context.Background(), pausing, bus, e, testWorkspaceID, child, "in_progress")
	}()

	<-paused // the close now holds a snapshot of {c1}

	// New work lands, and the real reopen hook runs while the close is parked.
	c2 := stagedChild(t, util.MustParseUUID(parentID), 0, "in_progress")
	registerNotificationListeners(bus, queries)
	publishChildCreated(bus, c2, parentID, agentID)

	close(release)
	wg.Wait()

	if d := rollupDetails(t, testUserID, parentID); len(d) != 0 {
		t.Fatalf("announced completion from a stale snapshot: %v — %s is still in_progress", d, c2)
	}

	// And once the new child really finishes, the summary lands with the right count.
	settleAndRoll(t, queries, bus, e, c2, parentID, "in_progress", "done")
	d := rollupDetails(t, testUserID, parentID)
	if len(d) != 1 || d[0]["child_count"] != "2" {
		t.Fatalf("want one summary with child_count=2 once the tree is really done, got %v", d)
	}
}

// TestRollupRace_MovedScopeSummaryIsCollected: a child moving OUT of a barrier
// leaves that barrier with no members. The mover's old scope cannot be named from
// the new state — issue:updated carries no prev_stage / prev_parent — but "no
// child carries stage 1 any more" is derivable from current state, so the orphaned
// summary is collected rather than sitting next to a contradicting one.
func TestRollupRace_MovedScopeSummaryIsCollected(t *testing.T) {
	ctx := context.Background()
	queries := db.New(testPool)
	bus := events.New()
	e, _ := agentActorEvent(t)

	parentID := createTestIssue(t, testWorkspaceID, testUserID)
	t.Cleanup(func() { cleanupTestIssue(t, parentID) })
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM inbox_item WHERE issue_id = $1`, parentID)
	})
	seedParentDelegate(t, queries, parentID)

	s1 := stagedChild(t, util.MustParseUUID(parentID), 1, "in_progress")
	settleAndRoll(t, queries, bus, e, s1, parentID, "in_progress", "done")
	if n := rollupCount(t, testUserID, parentID); n != 1 {
		t.Fatalf("setup: want 1 roll-up for stage 1, got %d", n)
	}

	// The child is unstaged, so the sibling set becomes unstaged and stage 1
	// ceases to exist.
	if _, err := testPool.Exec(ctx, `UPDATE issue SET stage = NULL WHERE id = $1`, s1); err != nil {
		t.Fatalf("unstage: %v", err)
	}
	c2 := stagedChild(t, util.MustParseUUID(parentID), 0, "in_progress")
	settleAndRoll(t, queries, bus, e, c2, parentID, "in_progress", "done")

	d := rollupDetails(t, testUserID, parentID)
	if len(d) != 1 {
		var got []string
		for _, x := range d {
			got = append(got, x["barrier_scope"].(string)+"/"+x["child_count"].(string))
		}
		t.Fatalf("want exactly 1 active summary, got %d (%v) — a defunct scope was left contradicting the current one", len(d), got)
	}
	if d[0]["barrier_scope"] != "all" || d[0]["child_count"] != "2" {
		t.Fatalf("surviving summary = %v, want scope=all child_count=2", d[0])
	}
}

// TestRollupRace_LiveScopeSurvivesCollection guards the other side: the defunct
// collector must not sweep a barrier that still has children. Stage 2 really did
// finish, so writing stage 1's summary must leave it alone.
func TestRollupRace_LiveScopeSurvivesCollection(t *testing.T) {
	queries := db.New(testPool)
	bus := events.New()
	e, _ := agentActorEvent(t)

	parentID := createTestIssue(t, testWorkspaceID, testUserID)
	t.Cleanup(func() { cleanupTestIssue(t, parentID) })
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM inbox_item WHERE issue_id = $1`, parentID)
	})
	seedParentDelegate(t, queries, parentID)

	s1 := stagedChild(t, util.MustParseUUID(parentID), 1, "in_progress")
	s2 := stagedChild(t, util.MustParseUUID(parentID), 2, "in_progress")

	// Stage 2 finishes first (its own frontier is stage <= 2, so it needs stage 1
	// settled too); settle stage 1 first, then stage 2, and both must survive.
	settleAndRoll(t, queries, bus, e, s1, parentID, "in_progress", "done")
	settleAndRoll(t, queries, bus, e, s2, parentID, "in_progress", "done")

	scopes := map[string]bool{}
	for _, d := range rollupDetails(t, testUserID, parentID) {
		scopes[d["barrier_scope"].(string)] = true
	}
	if !scopes["1"] || !scopes["2"] {
		t.Fatalf("scopes = %v, want both stages to keep their summary — both stages really finished", scopes)
	}
}
