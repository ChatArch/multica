package main

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/handler"
	"github.com/multica-ai/multica/server/internal/service/inboxv2"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// These tests drive the real listener path with the dual-write switch on.
//
// Compiling is not evidence that a producer is wired correctly: every call site
// hands the writer a delivery descriptor, and a wrong type, a missing target or
// an unstable identity would all still build. What is checked here is that the
// rows the producers actually write match the frozen contract.

// publishComment fires a comment:created event and returns the comment id, so a
// test can assert the v2 target points at exactly that comment.
func publishComment(bus *events.Bus, issueID, authorID, content string) string {
	commentID := uuid.NewString()
	bus.Publish(events.Event{
		Type:        protocol.EventCommentCreated,
		WorkspaceID: testWorkspaceID,
		ActorType:   "member",
		ActorID:     authorID,
		Payload: map[string]any{
			"comment": handler.CommentResponse{
				ID:         commentID,
				IssueID:    issueID,
				AuthorType: "member",
				AuthorID:   authorID,
				Content:    content,
				Type:       "comment",
			},
			"issue_title":  "dual write test issue",
			"issue_status": "todo",
		},
	})
	return commentID
}

// enableDualWrite points the listeners at a writer whose switch is forced on,
// without touching the process environment (which would leak across tests).
func enableDualWrite(t *testing.T) {
	t.Helper()
	t.Setenv(inboxv2.DualWriteEnvVar, "true")
}

func v2Rows(t *testing.T, issueID string) []db.InboxEvent {
	t.Helper()
	rows, err := testPool.Query(context.Background(), `
SELECT e.id, e.group_id, e.workspace_id, e.event_seq, e.type, e.actor_type, e.actor_id,
       e.target_kind, e.target_id, e.payload, e.payload_version, e.delivery_key, e.created_at
FROM inbox_event e
JOIN inbox_group g ON g.id = e.group_id
WHERE g.source_kind = 'issue' AND g.source_id = $1
ORDER BY e.event_seq
`, issueID)
	if err != nil {
		t.Fatalf("read v2 events: %v", err)
	}
	defer rows.Close()
	var out []db.InboxEvent
	for rows.Next() {
		var e db.InboxEvent
		if err := rows.Scan(&e.ID, &e.GroupID, &e.WorkspaceID, &e.EventSeq, &e.Type,
			&e.ActorType, &e.ActorID, &e.TargetKind, &e.TargetID, &e.Payload,
			&e.PayloadVersion, &e.DeliveryKey, &e.CreatedAt); err != nil {
			t.Fatalf("scan v2 event: %v", err)
		}
		out = append(out, e)
	}
	return out
}

func cleanupV2ForIssue(t *testing.T, issueID string) {
	t.Helper()
	ctx := context.Background()
	_, _ = testPool.Exec(ctx, `
DELETE FROM inbox_event WHERE group_id IN (
    SELECT id FROM inbox_group WHERE source_kind = 'issue' AND source_id = $1
)`, issueID)
	_, _ = testPool.Exec(ctx, `DELETE FROM inbox_group WHERE source_kind = 'issue' AND source_id = $1`, issueID)
}

// The default. Producers are wired, but nothing reaches the v2 tables until the
// switch is flipped — so shipping this code is not the same event as changing
// behaviour.
func TestDualWriteSwitchOffLeavesV2Empty(t *testing.T) {
	queries := db.New(testPool)
	bus := newNotificationBus(t, queries)

	issueID := createTestIssue(t, testWorkspaceID, testUserID)
	subscriberID := createTestUser(t, "dualwrite-off@multica.ai")
	t.Cleanup(func() {
		cleanupInboxForIssue(t, issueID)
		cleanupV2ForIssue(t, issueID)
		cleanupTestIssue(t, issueID)
		cleanupTestUser(t, "dualwrite-off@multica.ai")
	})
	addTestSubscriber(t, issueID, "member", subscriberID, "creator")

	publishComment(bus, issueID, testUserID, "no v2 rows expected")

	if got := len(inboxItemsForRecipient(t, queries, subscriberID)); got == 0 {
		t.Fatal("the legacy notification must still be delivered with the switch off")
	}
	if got := v2Rows(t, issueID); len(got) != 0 {
		t.Fatalf("switch off produced %d v2 events, want 0", len(got))
	}
}

// A comment notification must land in v2 as a new_comment event that points at
// the comment — the shape that makes "open the thing that changed" possible and
// that the old details blob could not guarantee.
func TestDualWriteCommentCarriesItsCommentTarget(t *testing.T) {
	enableDualWrite(t)
	queries := db.New(testPool)
	bus := newNotificationBus(t, queries)

	issueID := createTestIssue(t, testWorkspaceID, testUserID)
	subscriberID := createTestUser(t, "dualwrite-comment@multica.ai")
	t.Cleanup(func() {
		cleanupInboxForIssue(t, issueID)
		cleanupV2ForIssue(t, issueID)
		cleanupTestIssue(t, issueID)
		cleanupTestUser(t, "dualwrite-comment@multica.ai")
	})
	addTestSubscriber(t, issueID, "member", subscriberID, "creator")

	commentID := publishComment(bus, issueID, testUserID, "hello")

	rows := v2Rows(t, issueID)
	if len(rows) != 1 {
		t.Fatalf("expected 1 v2 event, got %d", len(rows))
	}
	e := rows[0]
	if e.Type != "new_comment" {
		t.Fatalf("type = %q, want new_comment", e.Type)
	}
	if e.TargetKind.String != "comment" {
		t.Fatalf("target_kind = %q, want comment", e.TargetKind.String)
	}
	if util.UUIDToString(e.TargetID) != commentID {
		t.Fatalf("target_id = %q, want the comment %q", util.UUIDToString(e.TargetID), commentID)
	}
	if e.EventSeq != 1 {
		t.Fatalf("event_seq = %d, want 1", e.EventSeq)
	}
	if len(inboxItemsForRecipient(t, queries, subscriberID)) != 1 {
		t.Fatal("the legacy row must be written too — this is a dual write")
	}
}

// Several notifications about one issue must fold into one group. This is the
// property the old schema could not express and every client re-implemented.
func TestDualWriteFoldsIssueActivityIntoOneGroup(t *testing.T) {
	enableDualWrite(t)
	queries := db.New(testPool)
	bus := newNotificationBus(t, queries)

	issueID := createTestIssue(t, testWorkspaceID, testUserID)
	subscriberID := createTestUser(t, "dualwrite-fold@multica.ai")
	t.Cleanup(func() {
		cleanupInboxForIssue(t, issueID)
		cleanupV2ForIssue(t, issueID)
		cleanupTestIssue(t, issueID)
		cleanupTestUser(t, "dualwrite-fold@multica.ai")
	})
	addTestSubscriber(t, issueID, "member", subscriberID, "creator")

	publishComment(bus, issueID, testUserID, "first")
	publishComment(bus, issueID, testUserID, "second")

	rows := v2Rows(t, issueID)
	if len(rows) != 2 {
		t.Fatalf("expected 2 v2 events, got %d", len(rows))
	}
	if rows[0].GroupID != rows[1].GroupID {
		t.Fatal("two comments on one issue must land in one group")
	}
	if rows[0].EventSeq != 1 || rows[1].EventSeq != 2 {
		t.Fatalf("sequence must be dense: %d, %d", rows[0].EventSeq, rows[1].EventSeq)
	}

	var groups int
	if err := testPool.QueryRow(context.Background(),
		`SELECT count(*) FROM inbox_group WHERE source_kind='issue' AND source_id=$1`,
		issueID).Scan(&groups); err != nil {
		t.Fatal(err)
	}
	if groups != 1 {
		t.Fatalf("groups = %d, want 1", groups)
	}
}

// A status change must carry no target: it is about the issue, so opening it
// should land on the issue overview rather than some borrowed comment.
func TestDualWriteStatusChangeCarriesNoTarget(t *testing.T) {
	enableDualWrite(t)
	queries := db.New(testPool)
	bus := newNotificationBus(t, queries)

	issueID := createTestIssue(t, testWorkspaceID, testUserID)
	subscriberID := createTestUser(t, "dualwrite-status@multica.ai")
	t.Cleanup(func() {
		cleanupInboxForIssue(t, issueID)
		cleanupV2ForIssue(t, issueID)
		cleanupTestIssue(t, issueID)
		cleanupTestUser(t, "dualwrite-status@multica.ai")
	})
	addTestSubscriber(t, issueID, "member", subscriberID, "creator")

	bus.Publish(events.Event{
		Type:        protocol.EventIssueUpdated,
		WorkspaceID: testWorkspaceID,
		ActorType:   "member",
		ActorID:     testUserID,
		Payload: map[string]any{
			"issue": handler.IssueResponse{
				ID: issueID, WorkspaceID: testWorkspaceID, Title: "status test",
				Status: "in_progress", UpdatedAt: "2026-08-04T08:00:00Z",
			},
			"status_changed": true,
			"prev_status":    "todo",
		},
	})

	rows := v2Rows(t, issueID)
	if len(rows) != 1 {
		t.Fatalf("expected 1 v2 event, got %d", len(rows))
	}
	if rows[0].Type != "status_changed" {
		t.Fatalf("type = %q, want status_changed", rows[0].Type)
	}
	if rows[0].TargetKind.Valid || rows[0].TargetID.Valid {
		t.Fatal("a status change must not carry a jump target")
	}
}

// Re-delivering the same event — the bus dispatches synchronously and retries
// are real — must not produce a second v2 row.
func TestDualWriteIsIdempotentForARepeatedEvent(t *testing.T) {
	enableDualWrite(t)
	queries := db.New(testPool)
	bus := newNotificationBus(t, queries)

	issueID := createTestIssue(t, testWorkspaceID, testUserID)
	subscriberID := createTestUser(t, "dualwrite-retry@multica.ai")
	t.Cleanup(func() {
		cleanupInboxForIssue(t, issueID)
		cleanupV2ForIssue(t, issueID)
		cleanupTestIssue(t, issueID)
		cleanupTestUser(t, "dualwrite-retry@multica.ai")
	})
	addTestSubscriber(t, issueID, "member", subscriberID, "creator")

	payload := map[string]any{
		"issue": handler.IssueResponse{
			ID: issueID, WorkspaceID: testWorkspaceID, Title: "retry test",
			Status: "in_progress", UpdatedAt: "2026-08-04T08:30:00Z",
		},
		"status_changed": true,
		"prev_status":    "todo",
	}
	for i := 0; i < 2; i++ {
		bus.Publish(events.Event{
			Type:        protocol.EventIssueUpdated,
			WorkspaceID: testWorkspaceID,
			ActorType:   "member",
			ActorID:     testUserID,
			Payload:     payload,
		})
	}

	if got := v2Rows(t, issueID); len(got) != 1 {
		t.Fatalf("a repeated event produced %d v2 events, want 1", len(got))
	}
}
