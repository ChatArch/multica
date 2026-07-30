package handler

import (
	"context"
	"testing"
	"time"
)

// The comment-list endpoint shares ListCommentsForIssue with the timeline, so
// the newest-N window (MUL-5492) can cut a thread in half here too: an old root
// outside the window, a fresh reply inside it.
//
// The consequence differs from the timeline's. Data is not invisible — but
// foldResolvedThreads documents a COMPLETE-thread set as a precondition, and
// comment.go asserts the default list mode satisfies it. A half thread violates
// that: rootOf stops at the highest ancestor it holds, so a reply is treated as
// its own thread root and a resolved thread stops folding correctly.

// seedCommentRow inserts one comment at an exact timestamp and returns its id.
// Separate from the timeline helper so this file can also set resolved_at.
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

// TestListComments_BackfillsParentsBeyondCap pins the invariant on the
// comment-list endpoint: no returned reply may reference a parent that is absent.
func TestListComments_BackfillsParentsBeyondCap(t *testing.T) {
	issueID := createIssueForTimeline(t, "comment list backfills parents")

	base := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Second)
	rootID := seedCommentRow(t, issueID, base, "old root", nil, nil)
	// Push the root out of the newest-commentHardCap window.
	bulkSeedComments(t, issueID, base.Add(time.Minute), commentHardCap+50)
	replyID := seedCommentRow(t, issueID, time.Now().UTC().Truncate(time.Second), "fresh reply", &rootID, nil)

	_, rows := listComments(t, issueID, "")
	if rows == nil {
		t.Fatal("ListComments returned no rows")
	}

	present := make(map[string]struct{}, len(rows))
	for _, c := range rows {
		present[c.ID] = struct{}{}
	}
	if _, ok := present[replyID]; !ok {
		t.Fatal("the fresh reply is missing entirely")
	}
	if _, ok := present[rootID]; !ok {
		t.Error("the old thread root was not backfilled")
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

// TestListComments_FoldStillWorksWhenRootBeyondCap covers the fold precondition
// directly. A reply-resolved thread whose root sits outside the comment window
// must still fold to "root + resolution reply". Without the ancestor backfill
// the root is missing, rootOf treats the resolution reply as its own root, and
// the fold silently degrades.
func TestListComments_FoldStillWorksWhenRootBeyondCap(t *testing.T) {
	issueID := createIssueForTimeline(t, "fold with root beyond cap")

	base := time.Now().UTC().Add(-4 * time.Hour).Truncate(time.Second)
	rootID := seedCommentRow(t, issueID, base, "old root question", nil, nil)
	// Two replies on the old thread, the later one carrying the resolution.
	seedCommentRow(t, issueID, base.Add(time.Second), "old chatter reply", &rootID, nil)
	resolvedAt := time.Now().UTC().Truncate(time.Second)
	conclusionID := seedCommentRow(t, issueID, resolvedAt, "the conclusion", &rootID, &resolvedAt)

	// Bulk comments in between push the root (and the chatter reply) out of the window.
	bulkSeedComments(t, issueID, base.Add(time.Minute), commentHardCap+50)

	_, rows := listComments(t, issueID, "fold=true")
	if rows == nil {
		t.Fatal("ListComments returned no rows")
	}

	byID := make(map[string]CommentResponse, len(rows))
	for _, c := range rows {
		byID[c.ID] = c
	}

	root, haveRoot := byID[rootID]
	if !haveRoot {
		t.Fatal("thread root absent: fold cannot compute the thread at all")
	}
	if _, haveConclusion := byID[conclusionID]; !haveConclusion {
		t.Error("the resolution reply is missing")
	}
	// The fold marks the thread root, which is only possible when the root is
	// in the set. This is the assertion that fails if the precondition is broken.
	if root.ThreadResolved == nil || !*root.ThreadResolved {
		t.Error("thread root not marked thread_resolved: the fold did not see a complete thread")
	}
}
