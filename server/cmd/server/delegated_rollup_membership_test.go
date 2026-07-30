package main

import (
	"context"
	"testing"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/handler"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// Regressions for the MUL-5483 code review, round 4: a barrier's MEMBERSHIP
// changes over its life, so a dedupe identity that ignores membership goes stale.
// These pin the behavior for each way membership can move.

func seedParentDelegate(t *testing.T, queries *db.Queries, parentID string) {
	t.Helper()
	if _, err := queries.AddIssueSubscriber(context.Background(), db.AddIssueSubscriberParams{
		IssueID: util.MustParseUUID(parentID), UserType: "member",
		UserID: util.MustParseUUID(testUserID), Reason: "delegated",
	}); err != nil {
		t.Fatalf("seed parent delegate: %v", err)
	}
}

// publishChildCreated fires the event the create path publishes, so the reopen
// hook runs through the real listener chain.
func publishChildCreated(bus *events.Bus, childID, parentID, agentID string) {
	bus.Publish(events.Event{
		Type:        protocol.EventIssueCreated,
		WorkspaceID: testWorkspaceID,
		ActorType:   "agent",
		ActorID:     agentID,
		Payload: map[string]any{
			"issue": handler.IssueResponse{
				ID:            childID,
				WorkspaceID:   testWorkspaceID,
				Title:         "new child",
				Status:        "in_progress",
				Priority:      "medium",
				CreatorType:   "agent",
				CreatorID:     agentID,
				ParentIssueID: &parentID,
			},
		},
	})
}

// TestRollupMembership_AddChildToSettledTreeNotifiesAgain is round 4's finding.
// An agent that keeps decomposing under an already-settled tree is the whole
// point of this feature, and it was the one case that silently broke: the old
// active roll-up held the unique slot (the key was per-parent, not per
// membership) and nothing archived it, because archiving only ran on
// settled -> non-settled while a NEW child never touches an existing child's
// status. The next completion was swallowed and the inbox kept a stale count.
func TestRollupMembership_AddChildToSettledTreeNotifiesAgain(t *testing.T) {
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
	settleAndRoll(t, queries, bus, e, c1, parentID, "in_progress", "in_review")
	if d := rollupDetails(t, testUserID, parentID); len(d) != 1 || d[0]["child_count"] != "1" {
		t.Fatalf("setup: want one roll-up with child_count=1, got %v", d)
	}

	// The agent files another sub-issue under the settled tree.
	c2 := stagedChild(t, util.MustParseUUID(parentID), 0, "in_progress")

	// Creation alone retires the now-false summary, so the inbox is not left
	// claiming the batch is done while new work is in flight.
	registerNotificationListeners(bus, queries)
	publishChildCreated(bus, c2, parentID, agentID)
	if n := rollupCount(t, testUserID, parentID); n != 0 {
		t.Fatalf("adding a child left %d active roll-up(s) claiming the tree was finished, want 0", n)
	}

	// And the new completion produces a fresh, accurate summary.
	settleAndRoll(t, queries, bus, e, c2, parentID, "in_progress", "done")
	d := rollupDetails(t, testUserID, parentID)
	if len(d) != 1 {
		t.Fatalf("want exactly 1 active roll-up after the second round, got %d: %v", len(d), d)
	}
	if d[0]["child_count"] != "2" {
		t.Fatalf("child_count = %v, want '2' — the new completion was swallowed by the stale row", d[0]["child_count"])
	}
}

// TestRollupMembership_AddChildWithoutHookStillNotifies pins the structural half
// of the fix: correctness of the NEXT roll-up must not depend on the child-create
// hook firing. The dedupe key is derived from barrier membership, so even with no
// hook at all (an enqueue path that does not publish issue:created, a dropped
// event) a changed membership is a different row and the summary still lands.
func TestRollupMembership_AddChildWithoutHookStillNotifies(t *testing.T) {
	queries := db.New(testPool)
	bus := events.New()
	e, _ := agentActorEvent(t)

	parentID := createTestIssue(t, testWorkspaceID, testUserID)
	t.Cleanup(func() { cleanupTestIssue(t, parentID) })
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM inbox_item WHERE issue_id = $1`, parentID)
	})
	seedParentDelegate(t, queries, parentID)

	c1 := stagedChild(t, util.MustParseUUID(parentID), 0, "in_progress")
	settleAndRoll(t, queries, bus, e, c1, parentID, "in_progress", "in_review")

	// No issue:created event is published for this one.
	c2 := stagedChild(t, util.MustParseUUID(parentID), 0, "in_progress")
	settleAndRoll(t, queries, bus, e, c2, parentID, "in_progress", "done")

	d := rollupDetails(t, testUserID, parentID)
	if len(d) != 1 || d[0]["child_count"] != "2" {
		t.Fatalf("want one accurate roll-up (child_count=2) even with no create hook, got %v", d)
	}
}

// TestRollupMembership_DeletedChildChangesTheBarrier: removing a child also moves
// membership, so the next close must be treated as a new barrier rather than
// deduped against the summary that counted the deleted one.
func TestRollupMembership_DeletedChildChangesTheBarrier(t *testing.T) {
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

	c1 := stagedChild(t, util.MustParseUUID(parentID), 0, "in_progress")
	c2 := stagedChild(t, util.MustParseUUID(parentID), 0, "in_progress")
	setStatus(t, c2, "done")
	settleAndRoll(t, queries, bus, e, c1, parentID, "in_progress", "done")
	if d := rollupDetails(t, testUserID, parentID); len(d) != 1 || d[0]["child_count"] != "2" {
		t.Fatalf("setup: want child_count=2, got %v", d)
	}

	// c2 is deleted and a replacement is filed and finished.
	if _, err := testPool.Exec(ctx, `DELETE FROM issue WHERE id = $1`, c2); err != nil {
		t.Fatalf("delete child: %v", err)
	}
	c3 := stagedChild(t, util.MustParseUUID(parentID), 0, "in_progress")
	settleAndRoll(t, queries, bus, e, c3, parentID, "in_progress", "in_review")

	d := rollupDetails(t, testUserID, parentID)
	if len(d) != 1 {
		t.Fatalf("want exactly 1 active roll-up, got %d: %v", len(d), d)
	}
	if d[0]["child_count"] != "2" {
		t.Fatalf("child_count = %v, want '2' (c1 + c3)", d[0]["child_count"])
	}
	// The summary must be a NEW row, not the one that counted the deleted child.
	if len(rollupBodies(t, testUserID, parentID)) != 1 {
		t.Fatal("expected the superseded summary to be retired")
	}
}

// TestRollupMembership_AddedChildRetiresOnlyItsOwnStage: a new child in a later
// stage says nothing about whether an earlier stage finished, so it must not
// retire that earlier stage's summary.
func TestRollupMembership_AddedChildRetiresOnlyItsOwnStage(t *testing.T) {
	queries := db.New(testPool)
	bus := events.New()
	e, agentID := agentActorEvent(t)

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

	// A new stage-2 child appears. Stage 1 is still legitimately finished.
	s2 := stagedChild(t, util.MustParseUUID(parentID), 2, "in_progress")
	registerNotificationListeners(bus, queries)
	publishChildCreated(bus, s2, parentID, agentID)

	scopes := map[string]bool{}
	for _, d := range rollupDetails(t, testUserID, parentID) {
		scopes[d["barrier_scope"].(string)] = true
	}
	if !scopes["1"] {
		t.Fatal("a new stage-2 child must not retire stage 1's summary — stage 1 really did finish")
	}
}
