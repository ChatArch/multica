package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Regressions for the MUL-5483 code review, round 3. All three findings were
// consequences of round 2 adding the stage barrier and the dedupe key in the same
// commit without checking how they interact.

func rollupDetails(t *testing.T, recipientID, parentID string) []map[string]any {
	t.Helper()
	rows, err := testPool.Query(context.Background(), `
		SELECT details FROM inbox_item
		WHERE recipient_id=$1 AND issue_id=$2 AND type='subtree_settled' AND archived=false
		ORDER BY created_at
	`, recipientID, parentID)
	if err != nil {
		t.Fatalf("query details: %v", err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			t.Fatalf("scan details: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("unmarshal details: %v", err)
		}
		out = append(out, m)
	}
	return out
}

func rollupBodies(t *testing.T, recipientID, parentID string) []string {
	t.Helper()
	rows, err := testPool.Query(context.Background(), `
		SELECT COALESCE(body,'') FROM inbox_item
		WHERE recipient_id=$1 AND issue_id=$2 AND type='subtree_settled' AND archived=false
		ORDER BY created_at
	`, recipientID, parentID)
	if err != nil {
		t.Fatalf("query bodies: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var b string
		if err := rows.Scan(&b); err != nil {
			t.Fatalf("scan body: %v", err)
		}
		out = append(out, b)
	}
	return out
}

// TestRollupBarrier_EachStageNotifiesOnce is round 3's finding 1. The dedupe key
// was (recipient, parent), so stage 1's roll-up occupied the only slot and stage
// 2 closing was swallowed by ON CONFLICT DO NOTHING. Nothing freed the slot
// either: archiving only happened on settled -> non-settled, while normal stage
// promotion is backlog -> todo. Two closed stages produced one notification.
func TestRollupBarrier_EachStageNotifiesOnce(t *testing.T) {
	ctx := context.Background()
	queries := db.New(testPool)
	bus := events.New()
	e, _ := agentActorEvent(t)

	parentID := createTestIssue(t, testWorkspaceID, testUserID)
	t.Cleanup(func() { cleanupTestIssue(t, parentID) })
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM inbox_item WHERE issue_id = $1`, parentID)
	})
	s1 := stagedChild(t, util.MustParseUUID(parentID), 1, "in_progress")
	s2 := stagedChild(t, util.MustParseUUID(parentID), 2, "backlog")

	if _, err := queries.AddIssueSubscriber(ctx, db.AddIssueSubscriberParams{
		IssueID: util.MustParseUUID(parentID), UserType: "member",
		UserID: util.MustParseUUID(testUserID), Reason: "delegated",
	}); err != nil {
		t.Fatalf("seed subscriber: %v", err)
	}

	settleAndRoll(t, queries, bus, e, s1, parentID, "in_progress", "in_review")
	if n := rollupCount(t, testUserID, parentID); n != 1 {
		t.Fatalf("stage 1 close produced %d roll-ups, want 1", n)
	}

	// Stage 2 is promoted the normal way (backlog -> todo), which never archives.
	setStatus(t, s2, "todo")
	settleAndRoll(t, queries, bus, e, s2, parentID, "todo", "done")

	if n := rollupCount(t, testUserID, parentID); n != 2 {
		t.Fatalf("roll-ups = %d, want 2 — the later stage was swallowed by the earlier stage's dedupe slot", n)
	}
	keys := map[string]bool{}
	for _, d := range rollupDetails(t, testUserID, parentID) {
		keys[d["barrier_key"].(string)] = true
	}
	if !keys["1"] || !keys["2"] {
		t.Fatalf("barrier keys = %v, want one per stage", keys)
	}
}

// TestRollupBarrier_CopyDescribesTheClosedBarrier: the body and child_count used
// to describe the WHOLE sibling set, so closing stage 1 claimed "All N
// sub-issues have finished" while stage 2 still sat in backlog.
func TestRollupBarrier_CopyDescribesTheClosedBarrier(t *testing.T) {
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
	stagedChild(t, util.MustParseUUID(parentID), 1, "in_review")
	stagedChild(t, util.MustParseUUID(parentID), 2, "backlog")
	stagedChild(t, util.MustParseUUID(parentID), 2, "backlog")

	if _, err := queries.AddIssueSubscriber(ctx, db.AddIssueSubscriberParams{
		IssueID: util.MustParseUUID(parentID), UserType: "member",
		UserID: util.MustParseUUID(testUserID), Reason: "delegated",
	}); err != nil {
		t.Fatalf("seed subscriber: %v", err)
	}

	settleAndRoll(t, queries, bus, e, s1a, parentID, "in_progress", "done")

	details := rollupDetails(t, testUserID, parentID)
	if len(details) != 1 {
		t.Fatalf("roll-ups = %d, want 1", len(details))
	}
	// Stage 1 has 2 children; the tree has 4. Reporting 4 would be a lie.
	if got := details[0]["child_count"]; got != "2" {
		t.Fatalf("child_count = %v, want '2' (stage 1 only, not the whole tree)", got)
	}
	if got := details[0]["stage"]; got != "1" {
		t.Fatalf("stage = %v, want '1'", got)
	}
	bodies := rollupBodies(t, testUserID, parentID)
	if len(bodies) != 1 || bodies[0] != "Stage 1 is complete — 2 sub-issues finished." {
		t.Fatalf("body = %q, want the stage-scoped wording", bodies)
	}
}

// TestRollupBarrier_DetailsValuesAreAllStrings is the Go half of round 3's
// finding 2. inbox details are string-valued across the whole notification path,
// and mobile encodes that as z.record(z.string(), z.string()); because the
// endpoint parses an ARRAY, one numeric value fails the entire parse and drops
// mobile's whole inbox to the empty fallback. A compile check on the label map
// could not catch it — only asserting the emitted payload can.
func TestRollupBarrier_DetailsValuesAreAllStrings(t *testing.T) {
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
		t.Fatalf("seed subscriber: %v", err)
	}

	settleAndRoll(t, queries, bus, e, only, parentID, "in_progress", "in_review")

	details := rollupDetails(t, testUserID, parentID)
	if len(details) != 1 {
		t.Fatalf("roll-ups = %d, want 1", len(details))
	}
	for k, v := range details[0] {
		if _, ok := v.(string); !ok {
			t.Fatalf("details[%q] = %#v is not a string — mobile's inbox list parse would fail and render EMPTY", k, v)
		}
	}
	if details[0]["barrier_key"] != "all" {
		t.Fatalf("barrier_key = %v, want 'all' for an unstaged set", details[0]["barrier_key"])
	}
}

// TestRollupBarrier_RecipientsScopedToClosedStage is round 3's finding 3.
// Recipients were unioned across the parent and EVERY sibling, so a delegate who
// only watches a still-parked stage-2 child was told stage 1 had finished.
func TestRollupBarrier_RecipientsScopedToClosedStage(t *testing.T) {
	ctx := context.Background()
	queries := db.New(testPool)
	bus := events.New()
	e, _ := agentActorEvent(t)

	const otherEmail = "rollup-frontier-scope@multica.ai"
	other := createTestUser(t, otherEmail)
	t.Cleanup(func() { cleanupTestUser(t, otherEmail) })

	parentID := createTestIssue(t, testWorkspaceID, testUserID)
	t.Cleanup(func() { cleanupTestIssue(t, parentID) })
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM inbox_item WHERE issue_id = $1`, parentID)
	})
	s1 := stagedChild(t, util.MustParseUUID(parentID), 1, "in_progress")
	s2 := stagedChild(t, util.MustParseUUID(parentID), 2, "backlog")

	// A delegated on the stage-1 child; B delegated only on the stage-2 child.
	for _, seed := range []struct{ issueID, userID string }{
		{s1, testUserID},
		{s2, other},
	} {
		if _, err := queries.AddIssueSubscriber(ctx, db.AddIssueSubscriberParams{
			IssueID: util.MustParseUUID(seed.issueID), UserType: "member",
			UserID: util.MustParseUUID(seed.userID), Reason: "delegated",
		}); err != nil {
			t.Fatalf("seed subscriber: %v", err)
		}
	}

	settleAndRoll(t, queries, bus, e, s1, parentID, "in_progress", "in_review")

	if n := rollupCount(t, testUserID, parentID); n != 1 {
		t.Fatalf("stage-1 delegate got %d roll-ups, want 1", n)
	}
	if n := rollupCount(t, other, parentID); n != 0 {
		t.Fatalf("stage-2-only delegate got %d roll-ups, want 0 — told about a stage they are not part of", n)
	}
}

// TestRollupBarrier_ReopenScopedToItsOwnBarrier: archiving on reopen must not
// wipe a sibling barrier's summary. Reopening stage 1 clears stage 1 only —
// clearing stage 2 as well would lose it permanently, since its children are
// already settled and no further transition INTO settled will fire for them.
func TestRollupBarrier_ReopenScopedToItsOwnBarrier(t *testing.T) {
	ctx := context.Background()
	queries := db.New(testPool)
	bus := events.New()
	e, _ := agentActorEvent(t)

	parentID := createTestIssue(t, testWorkspaceID, testUserID)
	t.Cleanup(func() { cleanupTestIssue(t, parentID) })
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM inbox_item WHERE issue_id = $1`, parentID)
	})
	s1 := stagedChild(t, util.MustParseUUID(parentID), 1, "in_progress")
	s2 := stagedChild(t, util.MustParseUUID(parentID), 2, "backlog")

	if _, err := queries.AddIssueSubscriber(ctx, db.AddIssueSubscriberParams{
		IssueID: util.MustParseUUID(parentID), UserType: "member",
		UserID: util.MustParseUUID(testUserID), Reason: "delegated",
	}); err != nil {
		t.Fatalf("seed subscriber: %v", err)
	}

	settleAndRoll(t, queries, bus, e, s1, parentID, "in_progress", "in_review")
	setStatus(t, s2, "todo")
	settleAndRoll(t, queries, bus, e, s2, parentID, "todo", "done")
	if n := rollupCount(t, testUserID, parentID); n != 2 {
		t.Fatalf("setup: want 2 roll-ups, got %d", n)
	}

	// Stage 1 reopens: only its own summary is retired.
	settleAndRoll(t, queries, bus, e, s1, parentID, "in_review", "in_progress")

	keys := map[string]bool{}
	for _, d := range rollupDetails(t, testUserID, parentID) {
		keys[d["barrier_key"].(string)] = true
	}
	if keys["1"] {
		t.Fatal("reopening stage 1 must retire its own roll-up")
	}
	if !keys["2"] {
		t.Fatal("reopening stage 1 must NOT retire stage 2's roll-up — it could never be re-sent")
	}
}
