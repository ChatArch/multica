package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/service/inboxv2"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// These tests exercise the legacy endpoints with the read switch on, so the
// response an old client would receive is produced by the projection rather
// than by inbox_item.
//
// The point is the switch: the same handler, the same route, the same JSON
// shape, and the only difference is which table answered.

func withWorkspaceCtx(req *http.Request) *http.Request {
	return req.WithContext(
		middleware.SetMemberContext(req.Context(), testWorkspaceID, db.Member{}))
}

// seedV2Group inserts one group plus one event directly, which is what the
// dual write would have produced.
func seedV2Group(t *testing.T, recipientID, issueID, evType, title string, readThrough int64) (groupID, eventID string) {
	t.Helper()
	ctx := context.Background()

	payload := inboxv2.LegacyPayload{
		Title:       title,
		Severity:    "info",
		IssueStatus: "todo",
		Details:     map[string]string{"comment_id": uuid.NewString()},
	}.Encode()

	if err := testPool.QueryRow(ctx, `
INSERT INTO inbox_group (workspace_id, recipient_id, source_kind, source_id,
                         latest_seq, read_through_seq, latest_event_at, surfaced_at)
VALUES ($1, $2, 'issue', $3, 1, $4, now(), now())
RETURNING id
`, testWorkspaceID, recipientID, issueID, readThrough).Scan(&groupID); err != nil {
		t.Fatalf("seed group: %v", err)
	}
	if err := testPool.QueryRow(ctx, `
INSERT INTO inbox_event (group_id, workspace_id, event_seq, type, actor_type,
                         target_kind, target_id, payload, delivery_key, created_at)
VALUES ($1, $2, 1, $3, 'member', 'comment', gen_random_uuid(), $4, $5, now())
RETURNING id
`, groupID, testWorkspaceID, evType, payload, "v1:proj-"+uuid.NewString()).Scan(&eventID); err != nil {
		t.Fatalf("seed event: %v", err)
	}
	if _, err := testPool.Exec(ctx,
		`UPDATE inbox_group SET latest_event_id = $1 WHERE id = $2`, eventID, groupID); err != nil {
		t.Fatalf("link latest event: %v", err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = testPool.Exec(bg, `DELETE FROM inbox_event WHERE group_id = $1`, groupID)
		_, _ = testPool.Exec(bg, `DELETE FROM inbox_group WHERE id = $1`, groupID)
	})
	return groupID, eventID
}

func listInbox(t *testing.T) []InboxItemResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	testHandler.ListInbox(rec, withWorkspaceCtx(newRequest(http.MethodGet, "/api/inbox", nil)))
	if rec.Code != http.StatusOK {
		t.Fatalf("ListInbox returned %d: %s", rec.Code, rec.Body.String())
	}
	var out []InboxItemResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

// With the switch off the endpoint must still be the old one, reading
// inbox_item — otherwise turning the code on would already have changed
// behaviour.
func TestInboxReadSwitchOffIgnoresV2Rows(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	issueID := uuid.NewString()
	seedV2Group(t, testUserID, issueID, "new_comment", "v2 only row", 0)

	for _, row := range listInbox(t) {
		if row.Title == "v2 only row" {
			t.Fatal("the switch is off, so a v2 row must not appear in the legacy list")
		}
	}
}

func TestInboxReadSwitchOnServesTheProjection(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	t.Setenv(inboxv2.ReadEnvVar, "true")

	issueID := uuid.NewString()
	_, eventID := seedV2Group(t, testUserID, issueID, "new_comment", "projected row", 0)

	rows := listInbox(t)
	var found *InboxItemResponse
	for i := range rows {
		if rows[i].ID == eventID {
			found = &rows[i]
		}
	}
	if found == nil {
		t.Fatal("the projected row is missing from the legacy list")
	}
	// id is the EVENT id, which is what an old client round-trips back into the
	// single-item endpoints.
	if found.ID != eventID {
		t.Fatalf("id = %q, want the event id %q", found.ID, eventID)
	}
	if found.Title != "projected row" || found.Type != "new_comment" {
		t.Fatalf("unexpected projection: %+v", found)
	}
	if found.IssueID == nil || *found.IssueID != issueID {
		t.Fatal("issue_id must come from the group's source")
	}
	if found.Read {
		t.Fatal("an event above the cursor must project as unread")
	}
	if found.Details == nil || string(found.Details) == "null" {
		t.Fatal("details must project as an object")
	}
}

// The write endpoints address a row by event id and must resolve it to the
// group the state actually lives on.
func TestInboxMarkReadThroughTheProjection(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	t.Setenv(inboxv2.ReadEnvVar, "true")

	issueID := uuid.NewString()
	groupID, eventID := seedV2Group(t, testUserID, issueID, "new_comment", "mark me read", 0)

	rec := httptest.NewRecorder()
	req := withWorkspaceCtx(withURLParam(
		newRequest(http.MethodPost, "/api/inbox/"+eventID+"/read", nil), "id", eventID))
	testHandler.MarkInboxRead(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("MarkInboxRead returned %d: %s", rec.Code, rec.Body.String())
	}

	var resp InboxItemResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.Read {
		t.Fatal("the response must report the row as read")
	}

	// The cursor, not a boolean, is what actually moved.
	var cursor int64
	if err := testPool.QueryRow(context.Background(),
		`SELECT read_through_seq FROM inbox_group WHERE id = $1`, groupID).Scan(&cursor); err != nil {
		t.Fatal(err)
	}
	if cursor != 1 {
		t.Fatalf("read_through_seq = %d, want 1", cursor)
	}
}

// An event id belonging to someone else must 404 rather than reveal that it
// exists — the same answer as a made-up id.
func TestInboxProjectionWriteIsScopedToTheRecipient(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	t.Setenv(inboxv2.ReadEnvVar, "true")

	// A different recipient. The v2 tables carry no foreign key to a user
	// (repo rule), so a bare id is enough to own a group.
	otherUser := uuid.NewString()

	issueID := uuid.NewString()
	_, eventID := seedV2Group(t, otherUser, issueID, "new_comment", "not yours", 0)

	for _, tc := range []struct {
		name string
		id   string
	}{
		{"another recipient's event", eventID},
		{"an id that does not exist", uuid.NewString()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := withWorkspaceCtx(withURLParam(
				newRequest(http.MethodPost, "/api/inbox/"+tc.id+"/read", nil), "id", tc.id))
			testHandler.MarkInboxRead(rec, req)
			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404 (body: %s)", rec.Code, rec.Body.String())
			}
		})
	}
}

// The unread count is the folded one: several events on one issue are one
// unread row, which is what every badge wanted and what the old raw-row count
// got wrong.
func TestInboxUnreadCountThroughTheProjection(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	t.Setenv(inboxv2.ReadEnvVar, "true")

	issueID := uuid.NewString()
	groupID, _ := seedV2Group(t, testUserID, issueID, "new_comment", "unread", 0)

	// A second event on the same group.
	if _, err := testPool.Exec(context.Background(), `
INSERT INTO inbox_event (group_id, workspace_id, event_seq, type, actor_type,
                         target_kind, target_id, payload, delivery_key, created_at)
VALUES ($1, $2, 2, 'new_comment', 'member', 'comment', gen_random_uuid(), '{}', $3, now())
`, groupID, testWorkspaceID, "v1:proj-second-"+uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(context.Background(),
		`UPDATE inbox_group SET latest_seq = 2 WHERE id = $1`, groupID); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	testHandler.CountUnreadInbox(rec, withWorkspaceCtx(newRequest(http.MethodGet, "/api/inbox/unread-count", nil)))
	if rec.Code != http.StatusOK {
		t.Fatalf("CountUnreadInbox returned %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Count int64 `json:"count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Count < 1 {
		t.Fatalf("count = %d, want at least the seeded group", body.Count)
	}

	// Reading through the group must take it out of the count entirely, not
	// decrement it by one event.
	before := body.Count
	if _, err := testPool.Exec(context.Background(),
		`UPDATE inbox_group SET read_through_seq = latest_seq WHERE id = $1`, groupID); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	testHandler.CountUnreadInbox(rec, withWorkspaceCtx(newRequest(http.MethodGet, "/api/inbox/unread-count", nil)))
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Count != before-1 {
		t.Fatalf("count = %d, want %d — two events on one issue are one unread row", body.Count, before-1)
	}
}
