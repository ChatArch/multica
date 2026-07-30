package main

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/handler"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// Regressions for the MUL-5483 code review, round 2.

// stagedChild inserts a child under parent with an explicit stage (0 = unstaged).
func stagedChild(t *testing.T, parent pgtype.UUID, stage int32, status string) string {
	t.Helper()
	var issueID string
	var stageArg any
	if stage > 0 {
		stageArg = stage
	}
	err := testPool.QueryRow(context.Background(), `
		INSERT INTO issue (workspace_id, title, status, priority, creator_type, creator_id,
		                   position, number, parent_issue_id, stage)
		VALUES ($1, 'staged child', $4, 'medium', 'member', $2, 0,
		        (SELECT COALESCE(MAX(number), 0) + 1 FROM issue WHERE workspace_id = $1), $3, $5)
		RETURNING id
	`, testWorkspaceID, testUserID, parent, status, stageArg).Scan(&issueID)
	if err != nil {
		t.Fatalf("stagedChild: %v", err)
	}
	t.Cleanup(func() { cleanupTestIssue(t, issueID) })
	return issueID
}

func setStatus(t *testing.T, issueID, status string) {
	t.Helper()
	if _, err := testPool.Exec(context.Background(),
		`UPDATE issue SET status=$2 WHERE id=$1`, issueID, status); err != nil {
		t.Fatalf("setStatus: %v", err)
	}
}

func rollupCount(t *testing.T, recipientID, parentID string) int {
	t.Helper()
	var n int
	if err := testPool.QueryRow(context.Background(), `
		SELECT COUNT(*) FROM inbox_item
		WHERE recipient_id=$1 AND issue_id=$2 AND type='subtree_settled' AND archived=false
	`, recipientID, parentID).Scan(&n); err != nil {
		t.Fatalf("rollupCount: %v", err)
	}
	return n
}

func agentActorEvent(t *testing.T) (events.Event, string) {
	t.Helper()
	agentID, _ := firstFixtureAgent(t)
	return events.Event{
		Type: protocol.EventIssueUpdated, WorkspaceID: testWorkspaceID,
		ActorType: "agent", ActorID: agentID,
	}, agentID
}

func settleAndRoll(t *testing.T, queries *db.Queries, bus *events.Bus, e events.Event,
	childID, parentID, from, to string) {
	t.Helper()
	setStatus(t, childID, to)
	child := handler.IssueResponse{
		ID: childID, WorkspaceID: testWorkspaceID, Status: to, ParentIssueID: &parentID,
	}
	notifyDelegatedSubtreeSettled(context.Background(), queries, bus, e, testWorkspaceID, child, from)
}

// TestRollup_ReachesDirectParentDelegatedChild is round 2's finding 1, and the
// most common relationship there is: the human CREATED the parent
// (reason='creator') while an agent filed the children (reason='delegated').
//
// The child's completion is suppressed by the delegated tier, and tierSuppressed
// then removes that user from the parent bubble. The roll-up used to target only
// reason='delegated' subscribers of the PARENT — a set this user is not in — so
// all three paths were closed and they received NO completion signal at all.
// Recipients must be the people the tier silenced: delegates on the CHILDREN.
func TestRollup_ReachesDirectParentDelegatedChild(t *testing.T) {
	ctx := context.Background()
	queries := db.New(testPool)
	bus := events.New()
	registerSubscriberListeners(bus, queries)

	e, agentID := agentActorEvent(t)
	_, runtimeID := firstFixtureAgent(t)
	parentID := createTestIssue(t, testWorkspaceID, testUserID)
	t.Cleanup(func() { cleanupTestIssue(t, parentID) })
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM inbox_item WHERE issue_id = $1`, parentID)
	})

	// Direct (creator) on the parent — the human filed the epic themselves.
	if _, err := queries.AddIssueSubscriber(ctx, db.AddIssueSubscriberParams{
		IssueID: util.MustParseUUID(parentID), UserType: "member",
		UserID: util.MustParseUUID(testUserID), Reason: "creator",
	}); err != nil {
		t.Fatalf("seed parent subscriber: %v", err)
	}

	// The agent files the child; the delegated rule subscribes the human to it.
	task := createAgentTaskWithOriginator(t, agentID, runtimeID, util.MustParseUUID(testUserID))
	childID := createAgentOriginIssue(t, agentID, "agent_create", task, util.MustParseUUID(parentID))
	publishAgentIssueCreated(bus, childID, agentID)
	if got := subscriberReason(t, queries, childID, "member", testUserID); got != "delegated" {
		t.Fatalf("child reason = %q, want delegated", got)
	}

	// Per-child delivery is suppressed...
	notifySubscribers(ctx, queries, bus, childID, "in_review", testWorkspaceID,
		e, nil, "status_changed", "info", "child", "", emptyDetails)
	// ...so the roll-up is the ONLY path left, and it has to fire.
	settleAndRoll(t, queries, bus, e, childID, parentID, "in_progress", "in_review")

	if n := rollupCount(t, testUserID, parentID); n != 1 {
		t.Fatalf("completion signals = %d, want 1 — creator-on-parent + delegated-on-child got nothing at all", n)
	}
}

// TestRollup_HonorsStageBarrier is round 2's finding 4. Requiring EVERY child to
// settle discarded the stage semantics the design promised: on a serial plan
// (stage 1 finished, stage 2 still parked in backlog) the user got no signal
// until the entire tree ended. The roll-up now shares
// handler.StageBarrierClosedFunc with the child-done path, so a closed stage
// reports even while later stages are still parked.
func TestRollup_HonorsStageBarrier(t *testing.T) {
	ctx := context.Background()
	queries := db.New(testPool)
	bus := events.New()

	e, _ := agentActorEvent(t)
	parentID := createTestIssue(t, testWorkspaceID, testUserID)
	t.Cleanup(func() { cleanupTestIssue(t, parentID) })
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM inbox_item WHERE issue_id = $1`, parentID)
	})

	s1a := stagedChild(t, util.MustParseUUID(parentID), 1, "in_progress")
	s1b := stagedChild(t, util.MustParseUUID(parentID), 1, "in_progress")
	stagedChild(t, util.MustParseUUID(parentID), 2, "backlog") // later stage, parked

	if _, err := queries.AddIssueSubscriber(ctx, db.AddIssueSubscriberParams{
		IssueID: util.MustParseUUID(parentID), UserType: "member",
		UserID: util.MustParseUUID(testUserID), Reason: "delegated",
	}); err != nil {
		t.Fatalf("seed parent subscriber: %v", err)
	}

	settleAndRoll(t, queries, bus, e, s1a, parentID, "in_progress", "in_review")
	if n := rollupCount(t, testUserID, parentID); n != 0 {
		t.Fatalf("stage 1 is not closed yet (one sibling open), got %d roll-ups", n)
	}

	settleAndRoll(t, queries, bus, e, s1b, parentID, "in_progress", "done")
	if n := rollupCount(t, testUserID, parentID); n != 1 {
		t.Fatalf("stage 1 closed but produced %d roll-ups, want 1 — a parked stage 2 must not withhold the stage-1 signal", n)
	}
}

// TestRollup_IdempotentAcrossConcurrentClose is round 2's finding 3. The barrier
// read and the inbox write are not atomic, so two siblings settling at the same
// moment can both observe a closed barrier. Only a uniqueness constraint can
// close that window; this drives it by invoking the roll-up twice against an
// already-closed barrier, which is the observable shape of that race.
func TestRollup_IdempotentAcrossConcurrentClose(t *testing.T) {
	ctx := context.Background()
	queries := db.New(testPool)
	bus := events.New()

	e, _ := agentActorEvent(t)
	parentID := createTestIssue(t, testWorkspaceID, testUserID)
	t.Cleanup(func() { cleanupTestIssue(t, parentID) })
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM inbox_item WHERE issue_id = $1`, parentID)
	})

	a := stagedChild(t, util.MustParseUUID(parentID), 0, "in_progress")
	b := stagedChild(t, util.MustParseUUID(parentID), 0, "in_progress")

	if _, err := queries.AddIssueSubscriber(ctx, db.AddIssueSubscriberParams{
		IssueID: util.MustParseUUID(parentID), UserType: "member",
		UserID: util.MustParseUUID(testUserID), Reason: "delegated",
	}); err != nil {
		t.Fatalf("seed parent subscriber: %v", err)
	}

	// Both children settle; each then reports its own close, as two concurrent
	// transactions would.
	setStatus(t, a, "done")
	setStatus(t, b, "done")
	for _, id := range []string{a, b} {
		child := handler.IssueResponse{
			ID: id, WorkspaceID: testWorkspaceID, Status: "done", ParentIssueID: &parentID,
		}
		notifyDelegatedSubtreeSettled(ctx, queries, bus, e, testWorkspaceID, child, "in_progress")
	}

	if n := rollupCount(t, testUserID, parentID); n != 1 {
		t.Fatalf("two concurrent barrier closes produced %d roll-ups, want 1", n)
	}
}

// TestRollup_ReopenAllowsLaterResettle: the dedupe must not be permanent. A child
// leaving a settled status reopens the tree and archives the prior roll-up, so
// finishing again notifies again rather than being silently swallowed forever.
func TestRollup_ReopenAllowsLaterResettle(t *testing.T) {
	ctx := context.Background()
	queries := db.New(testPool)
	bus := events.New()

	e, _ := agentActorEvent(t)
	parentID := createTestIssue(t, testWorkspaceID, testUserID)
	t.Cleanup(func() { cleanupTestIssue(t, parentID) })
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM inbox_item WHERE issue_id = $1`, parentID)
	})

	only := stagedChild(t, util.MustParseUUID(parentID), 0, "in_progress")
	if _, err := queries.AddIssueSubscriber(ctx, db.AddIssueSubscriberParams{
		IssueID: util.MustParseUUID(parentID), UserType: "member",
		UserID: util.MustParseUUID(testUserID), Reason: "delegated",
	}); err != nil {
		t.Fatalf("seed parent subscriber: %v", err)
	}

	settleAndRoll(t, queries, bus, e, only, parentID, "in_progress", "in_review")
	if n := rollupCount(t, testUserID, parentID); n != 1 {
		t.Fatalf("first settle produced %d roll-ups, want 1", n)
	}

	// Reopened: the roll-up is retired.
	settleAndRoll(t, queries, bus, e, only, parentID, "in_review", "in_progress")
	if n := rollupCount(t, testUserID, parentID); n != 0 {
		t.Fatalf("reopening the tree left %d active roll-ups, want 0", n)
	}

	// Finished again: notified again.
	settleAndRoll(t, queries, bus, e, only, parentID, "in_progress", "done")
	if n := rollupCount(t, testUserID, parentID); n != 1 {
		t.Fatalf("re-settle after a reopen produced %d roll-ups, want 1", n)
	}
}
