package handler

import (
	"context"
	"testing"
	"time"
)

// The comment-list endpoint shares ListCommentsForIssue with the timeline, so the
// newest-N window (MUL-5492) can cut a thread in half here too: an old root
// outside the window, a fresh reply inside it.
//
// Two separate properties are at stake, and conflating them was the bug in the
// previous revision of this fix:
//
//   - PARENT-CHAIN CLOSURE makes a reply renderable. completeCommentParentChains
//     guarantees it, within explicit budgets.
//   - THREAD COMPLETENESS is what foldResolvedThreads needs. Closure does NOT
//     provide it: older siblings and descendants of a retained reply stay outside
//     the window. Folding a partial thread produces wrong answers rather than
//     merely incomplete ones, so the fold is suppressed on a truncated read.

// seedCommentRow inserts one comment at an exact timestamp and returns its id.
// resolvedAt marks it as the thread's resolution when non-nil.
func seedCommentRow(t *testing.T, issueID string, at time.Time, content string, parentID *string, resolvedAt *time.Time) string {
	t.Helper()
	var id string
	err := testPool.QueryRow(context.Background(), `
		INSERT INTO comment (issue_id, workspace_id, author_type, author_id, content, type,
		                     created_at, updated_at, parent_id, resolved_at, resolved_by_type, resolved_by_id)
		VALUES ($1, $2, 'member', $3, $4, 'comment', $5, $5, $6, $7,
		        CASE WHEN $7::timestamptz IS NULL THEN NULL ELSE 'member' END,
		        CASE WHEN $7::timestamptz IS NULL THEN NULL ELSE $3::uuid END)
		RETURNING id
	`, issueID, testWorkspaceID, testUserID, content, at, parentID, resolvedAt).Scan(&id)
	if err != nil {
		t.Fatalf("seed comment %q: %v", content, err)
	}
	return id
}

// assertNoOrphanCommentRows pins the invariant the UI depends on: every returned
// reply's parent is in the same response.
func assertNoOrphanCommentRows(t *testing.T, rows []CommentResponse) {
	t.Helper()
	present := make(map[string]struct{}, len(rows))
	for _, c := range rows {
		present[c.ID] = struct{}{}
	}
	for _, c := range rows {
		if c.ParentID == nil || *c.ParentID == "" {
			continue
		}
		if _, ok := present[*c.ParentID]; !ok {
			t.Errorf("comment %s references parent %s absent from the response", c.ID, *c.ParentID)
		}
	}
}

// TestListComments_ExactlyAtCapStillFolds pins the probe read. An issue holding
// exactly commentHardCap comments is complete, so the fold must still run —
// inferring truncation from the row count would silently stop folding here.
func TestListComments_ExactlyAtCapStillFolds(t *testing.T) {
	issueID := createIssueForTimeline(t, "exactly at cap still folds")

	base := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	rootID := seedCommentRow(t, issueID, base, "root question", nil, nil)
	seedCommentRow(t, issueID, base.Add(time.Second), "chatter", &rootID, nil)
	resolvedAt := base.Add(2 * time.Second)
	seedCommentRow(t, issueID, resolvedAt, "the conclusion", &rootID, &resolvedAt)
	// Fill to exactly the cap.
	bulkSeedComments(t, issueID, base.Add(time.Minute), commentHardCap-3)

	_, rows := listComments(t, issueID, "fold=true")
	if rows == nil {
		t.Fatal("ListComments returned no rows")
	}
	var root *CommentResponse
	for i := range rows {
		if rows[i].ID == rootID {
			root = &rows[i]
		}
	}
	if root == nil {
		t.Fatal("thread root missing")
	}
	if root.ThreadResolved == nil || !*root.ThreadResolved {
		t.Error("thread_resolved not set: exactly-at-cap is complete, so the fold must run")
	}
}

// TestListComments_TruncatedReadDoesNotFold covers the review finding. The
// resolution reply is inside the window while older ordinary replies on the same
// thread are outside it. Folding this set runs on a partial thread: it drops
// retained replies as "settled" and reports a folded_count that cannot account
// for the replies it never saw.
//
// The scenario is chosen so the assertions discriminate. Putting the resolution
// OUTSIDE the window instead would make the thread merely look unresolved, and a
// fold that declines to fold is indistinguishable from a fold that never ran —
// the test would pass whether or not the gate exists.
func TestListComments_TruncatedReadDoesNotFold(t *testing.T) {
	issueID := createIssueForTimeline(t, "truncated read does not fold")

	base := time.Now().UTC().Add(-4 * time.Hour).Truncate(time.Second)
	rootID := seedCommentRow(t, issueID, base, "root question", nil, nil)
	// Older ordinary replies that the window will cut away.
	for i := 0; i < 3; i++ {
		seedCommentRow(t, issueID, base.Add(time.Duration(i+1)*time.Second), "old reply", &rootID, nil)
	}

	// Push root + the old replies out of the newest-cap window.
	bulkSeedComments(t, issueID, base.Add(time.Minute), commentHardCap+50)

	// Inside the window: an ordinary reply the fold would discard, plus the
	// resolution that would make the fold fire.
	now := time.Now().UTC().Truncate(time.Second)
	keptReplyID := seedCommentRow(t, issueID, now, "fresh ordinary reply", &rootID, nil)
	resolvedAt := now.Add(time.Second)
	conclusionID := seedCommentRow(t, issueID, resolvedAt, "the conclusion", &rootID, &resolvedAt)

	_, rows := listComments(t, issueID, "fold=true")
	if rows == nil {
		t.Fatal("ListComments returned no rows")
	}

	byID := make(map[string]CommentResponse, len(rows))
	for _, c := range rows {
		byID[c.ID] = c
	}

	// Parent-chain closure still applies, so the replies are renderable.
	root, ok := byID[rootID]
	if !ok {
		t.Fatal("the old thread root was not backfilled")
	}
	if _, ok := byID[conclusionID]; !ok {
		t.Error("the resolution reply is missing")
	}
	// The fold would have dropped this as settled discussion, using a resolution
	// derived from a thread it can only partly see.
	if _, ok := byID[keptReplyID]; !ok {
		t.Error("a retained reply was folded away on a partial thread")
	}
	if root.ThreadResolved != nil {
		t.Errorf("thread_resolved = %v on a truncated read: the thread is partial, so resolution state cannot be derived", *root.ThreadResolved)
	}
	if root.FoldedCount != nil {
		t.Errorf("folded_count = %d on a truncated read: it cannot account for the 3 replies outside the window", *root.FoldedCount)
	}
	assertNoOrphanCommentRows(t, rows)
}

// TestListComments_RootResolvedTruncatedNoFalseFoldedCount is the root-resolved
// variant: the thread root itself is resolved and its older replies are outside
// the window, so a fold would report a folded_count derived from a partial set.
func TestListComments_RootResolvedTruncatedNoFalseFoldedCount(t *testing.T) {
	issueID := createIssueForTimeline(t, "root resolved truncated")

	base := time.Now().UTC().Add(-4 * time.Hour).Truncate(time.Second)
	resolvedAt := base
	rootID := seedCommentRow(t, issueID, base, "settled topic", nil, &resolvedAt)
	for i := 0; i < 5; i++ {
		seedCommentRow(t, issueID, base.Add(time.Duration(i+1)*time.Second), "old reply", &rootID, nil)
	}

	bulkSeedComments(t, issueID, base.Add(time.Minute), commentHardCap+50)
	seedCommentRow(t, issueID, time.Now().UTC().Truncate(time.Second), "fresh reply", &rootID, nil)

	_, rows := listComments(t, issueID, "fold=true")
	if rows == nil {
		t.Fatal("ListComments returned no rows")
	}
	for _, c := range rows {
		if c.ID != rootID {
			continue
		}
		if c.FoldedCount != nil {
			t.Errorf("folded_count = %d: the 5 old replies are outside the window, so any count is a fiction", *c.FoldedCount)
		}
		if c.ThreadResolved != nil {
			t.Error("thread_resolved set on a truncated read")
		}
	}
	assertNoOrphanCommentRows(t, rows)
}

// TestListComments_DeepChainBeyondBudgetIsPrunedNotOrphaned exercises the depth
// budget. A chain deeper than commentAncestorMaxDepth cannot be closed, so the
// affected reply is dropped rather than returned as an unrenderable orphan. The
// response must stay bounded and must terminate.
func TestListComments_DeepChainBeyondBudgetIsPrunedNotOrphaned(t *testing.T) {
	issueID := createIssueForTimeline(t, "deep chain beyond budget")

	base := time.Now().UTC().Add(-6 * time.Hour).Truncate(time.Second)
	// A chain deeper than the walk is allowed to climb.
	depth := commentAncestorMaxDepth + 6
	rootID := seedCommentRow(t, issueID, base, "chain root", nil, nil)
	parent := rootID
	for i := 0; i < depth; i++ {
		parent = seedCommentRow(t, issueID, base.Add(time.Duration(i+1)*time.Second), "chain link", &parent, nil)
	}
	// Push the whole chain out of the window.
	bulkSeedComments(t, issueID, base.Add(time.Hour), commentHardCap+50)
	// The newest comment is a reply at the bottom of that chain.
	deepReplyID := seedCommentRow(t, issueID, time.Now().UTC().Truncate(time.Second), "deep reply", &parent, nil)

	_, rows := listComments(t, issueID, "")
	if rows == nil {
		t.Fatal("ListComments returned no rows")
	}

	// Bounded: window plus at most the ancestor budget.
	if len(rows) > commentHardCap+commentAncestorBudget {
		t.Errorf("returned %d comments, exceeds the provable bound of %d",
			len(rows), commentHardCap+commentAncestorBudget)
	}
	// The chain could not be closed, so the deep reply is dropped rather than
	// emitted as an orphan.
	for _, c := range rows {
		if c.ID == deepReplyID {
			t.Error("the deep reply survived without a closed parent chain")
		}
	}
	assertNoOrphanCommentRows(t, rows)
}

// TestListComments_SharedAncestorFetchedOnce checks the dedup in the walk: many
// replies pointing at one old root must not duplicate that root.
func TestListComments_SharedAncestorFetchedOnce(t *testing.T) {
	issueID := createIssueForTimeline(t, "shared ancestor")

	base := time.Now().UTC().Add(-4 * time.Hour).Truncate(time.Second)
	rootID := seedCommentRow(t, issueID, base, "shared root", nil, nil)
	bulkSeedComments(t, issueID, base.Add(time.Minute), commentHardCap+50)

	now := time.Now().UTC().Truncate(time.Second)
	var replies []string
	for i := 0; i < 3; i++ {
		replies = append(replies, seedCommentRow(t, issueID, now.Add(time.Duration(i)*time.Second), "sibling reply", &rootID, nil))
	}

	_, rows := listComments(t, issueID, "")
	if rows == nil {
		t.Fatal("ListComments returned no rows")
	}

	seen := 0
	present := make(map[string]struct{}, len(rows))
	for _, c := range rows {
		present[c.ID] = struct{}{}
		if c.ID == rootID {
			seen++
		}
	}
	if seen != 1 {
		t.Errorf("shared root appears %d times, want exactly 1", seen)
	}
	for _, id := range replies {
		if _, ok := present[id]; !ok {
			t.Errorf("reply %s missing", id)
		}
	}
	assertNoOrphanCommentRows(t, rows)
}

// TestListComments_CrossIssueParentNeverCrossesBoundary is the negative tenant
// test. parent_id carries a foreign key to comment(id) but NOT to a matching
// issue, so a stray cross-issue parent reference is representable in stored data.
// The walk filters on issue_id and workspace_id at every level, so the foreign
// comment must never appear and the anomalous reply must be pruned.
func TestListComments_CrossIssueParentNeverCrossesBoundary(t *testing.T) {
	otherIssueID := createIssueForTimeline(t, "other issue")
	issueID := createIssueForTimeline(t, "cross issue parent")

	foreignID := seedCommentRow(t, otherIssueID, time.Now().UTC().Add(-time.Hour).Truncate(time.Second),
		"comment belonging to another issue", nil, nil)

	base := time.Now().UTC().Add(-4 * time.Hour).Truncate(time.Second)
	bulkSeedComments(t, issueID, base, commentHardCap+50)
	// A reply in THIS issue whose parent lives in the other issue.
	strayID := seedCommentRow(t, issueID, time.Now().UTC().Truncate(time.Second),
		"reply with a foreign parent", &foreignID, nil)

	_, rows := listComments(t, issueID, "")
	if rows == nil {
		t.Fatal("ListComments returned no rows")
	}

	for _, c := range rows {
		if c.ID == foreignID {
			t.Error("a comment from another issue leaked into this issue's response")
		}
		if c.ID == strayID {
			t.Error("the reply with an out-of-issue parent was returned as an orphan")
		}
	}
	assertNoOrphanCommentRows(t, rows)
}
