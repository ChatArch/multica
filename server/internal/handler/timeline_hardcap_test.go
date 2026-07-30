package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Regression tests for MUL-5492: the per-issue timeline cap used to be applied
// with ORDER BY created_at ASC, so once an issue accumulated more than
// timelineHardCap rows the cap discarded the NEWEST ones and the timeline
// silently appeared to stop at some point in the past.
//
// These tests pin three properties:
//  1. the cap keeps the newest window, not the oldest;
//  2. the window is contiguous — no region where only one of
//     (comments, activities) survives, which would look like a complete
//     history while missing half of it;
//  3. truncation is reported instead of being silent.

// fetchTimelineRecorder issues GET /timeline and returns the raw recorder so
// tests can assert on response headers as well as the body.
func fetchTimelineRecorder(t *testing.T, issueID, rawQuery string) *httptest.ResponseRecorder {
	t.Helper()
	target := "/api/issues/" + issueID + "/timeline"
	if rawQuery != "" {
		target += "?" + rawQuery
	}
	w := httptest.NewRecorder()
	req := newRequest("GET", target, nil)
	req = withURLParam(req, "id", issueID)
	testHandler.ListTimeline(w, req)
	return w
}

func decodeTimelineEntries(t *testing.T, w *httptest.ResponseRecorder) []TimelineEntry {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var entries []TimelineEntry
	if err := json.Unmarshal(w.Body.Bytes(), &entries); err != nil {
		t.Fatalf("decode timeline: %v", err)
	}
	return entries
}

// bulkSeedActivities inserts n activities one second apart starting at start.
// One statement, because these tests need thousands of rows.
func bulkSeedActivities(t *testing.T, issueID string, start time.Time, n int) {
	t.Helper()
	_, err := testPool.Exec(context.Background(), `
		INSERT INTO activity_log (workspace_id, issue_id, actor_type, actor_id, action, details, created_at)
		SELECT $1, $2, 'member', $3, 'status_changed', '{"from":"todo","to":"in_progress"}'::jsonb,
		       $4::timestamptz + (g * interval '1 second')
		FROM generate_series(0, $5::int - 1) AS g
	`, testWorkspaceID, issueID, testUserID, start, n)
	if err != nil {
		t.Fatalf("bulk seed %d activities: %v", n, err)
	}
}

// bulkSeedComments inserts n comments one second apart starting at start.
func bulkSeedComments(t *testing.T, issueID string, start time.Time, n int) {
	t.Helper()
	_, err := testPool.Exec(context.Background(), `
		INSERT INTO comment (issue_id, workspace_id, author_type, author_id, content, type, created_at, updated_at)
		SELECT $1, $2, 'member', $3, 'bulk comment ' || g, 'comment',
		       $4::timestamptz + (g * interval '1 second'),
		       $4::timestamptz + (g * interval '1 second')
		FROM generate_series(0, $5::int - 1) AS g
	`, issueID, testWorkspaceID, testUserID, start, n)
	if err != nil {
		t.Fatalf("bulk seed %d comments: %v", n, err)
	}
}

func countByType(entries []TimelineEntry) (comments, activities int) {
	for _, e := range entries {
		switch e.Type {
		case "comment":
			comments++
		case "activity":
			activities++
		}
	}
	return
}

// mustParseTS parses a timeline timestamp into an instant. Tests compare
// instants, never strings: the API renders timestamps in the DB session's
// offset, so two equal instants can have different textual forms and a string
// comparison would silently pass for the wrong reason.
func mustParseTS(t *testing.T, label, raw string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		t.Fatalf("parse %s timestamp %q: %v", label, raw, err)
	}
	return parsed
}

// TestListTimeline_HardCapKeepsNewestActivities is the direct regression: with
// more activities than the cap, the response must be the newest window. Before
// the fix the LAST seeded activity was missing and the FIRST one was present.
func TestListTimeline_HardCapKeepsNewestActivities(t *testing.T) {
	issueID := createIssueForTimeline(t, "hard cap keeps newest")

	// 100 rows past the cap, ending "now" so the newest row is unambiguous.
	const total = timelineHardCap + 100
	start := time.Now().UTC().Add(-time.Duration(total) * time.Second).Truncate(time.Second)
	bulkSeedActivities(t, issueID, start, total)

	oldest := start
	newest := start.Add(time.Duration(total-1) * time.Second)

	w := fetchTimelineRecorder(t, issueID, "")
	entries := decodeTimelineEntries(t, w)
	_, activityCount := countByType(entries)

	if activityCount != timelineHardCap {
		t.Errorf("activity count = %d, want %d (the cap)", activityCount, timelineHardCap)
	}

	// The newest row must be present: this is the assertion that failed before.
	if got := mustParseTS(t, "last entry", entries[len(entries)-1].CreatedAt); !got.Equal(newest) {
		t.Errorf("last entry created_at = %s, want newest seeded %s", got, newest)
	}
	// ...and the oldest must have been the one dropped.
	if got := mustParseTS(t, "first entry", entries[0].CreatedAt); got.Equal(oldest) {
		t.Errorf("oldest seeded row %s survived the cap; the cap is still trimming the wrong end", got)
	}

	if got := w.Header().Get(HeaderTimelineTruncated); got != "true" {
		t.Errorf("%s = %q, want \"true\": truncation must not be silent", HeaderTimelineTruncated, got)
	}
	// The floor header must be directly comparable with entry timestamps, so
	// assert on the exact bytes as well as the instant.
	if got, want := w.Header().Get(HeaderTimelineWindowFrom), entries[0].CreatedAt; got != want {
		t.Errorf("%s = %q, want the oldest returned entry %q (identical formatting)", HeaderTimelineWindowFrom, got, want)
	}
}

// TestListTimeline_JointWindowHasNoOneSidedRegion covers the failure mode that
// a naive ASC→DESC flip introduces. The two lists are capped independently, so
// each has its own floor. Activity is machine-paced and hits the cap first;
// without a shared floor the region below the activity floor would come back as
// comments with zero interleaved activity — a history that looks complete and
// is not.
func TestListTimeline_JointWindowHasNoOneSidedRegion(t *testing.T) {
	issueID := createIssueForTimeline(t, "joint window")

	// Comments reach much further back in time than the activity window can.
	const activityTotal = timelineHardCap + 100
	activityStart := time.Now().UTC().Add(-time.Duration(activityTotal) * time.Second).Truncate(time.Second)
	commentStart := activityStart.Add(-2 * time.Hour)

	bulkSeedActivities(t, issueID, activityStart, activityTotal)
	bulkSeedComments(t, issueID, commentStart, 50)

	w := fetchTimelineRecorder(t, issueID, "")
	entries := decodeTimelineEntries(t, w)

	if got := w.Header().Get(HeaderTimelineTruncated); got != "true" {
		t.Fatalf("%s = %q, want \"true\"", HeaderTimelineTruncated, got)
	}
	windowFrom := w.Header().Get(HeaderTimelineWindowFrom)
	if windowFrom == "" {
		t.Fatal("window floor header missing")
	}
	floor := mustParseTS(t, "window floor", windowFrom)

	// Every returned entry must sit at or after the reported floor. The 50 old
	// comments are all older than the activity floor, so all of them must be
	// clamped away rather than presented as a gap-free history.
	for i, e := range entries {
		if at := mustParseTS(t, "entry", e.CreatedAt); at.Before(floor) {
			t.Fatalf("entry %d (%s at %s) is older than the reported window floor %s: the window is not contiguous",
				i, e.Type, at, floor)
		}
	}
	commentCount, _ := countByType(entries)
	if commentCount != 0 {
		t.Errorf("comment count = %d, want 0: every seeded comment predates the activity floor", commentCount)
	}
}

// TestListTimeline_ExactlyAtCapIsNotTruncated pins the cap+1 probe read. An
// issue that happens to hold exactly timelineHardCap rows is complete, and
// reporting it as truncated would also drag the other list's window down to
// this floor for no reason.
func TestListTimeline_ExactlyAtCapIsNotTruncated(t *testing.T) {
	issueID := createIssueForTimeline(t, "exactly at cap")

	start := time.Now().UTC().Add(-time.Duration(timelineHardCap) * time.Second).Truncate(time.Second)
	bulkSeedActivities(t, issueID, start, timelineHardCap)
	// A comment far older than every activity: it must survive, because the
	// activity list is at the cap but not past it.
	bulkSeedComments(t, issueID, start.Add(-time.Hour), 1)

	w := fetchTimelineRecorder(t, issueID, "")
	entries := decodeTimelineEntries(t, w)

	if got := w.Header().Get(HeaderTimelineTruncated); got != "" {
		t.Errorf("%s = %q, want unset: exactly-at-cap is a complete timeline", HeaderTimelineTruncated, got)
	}
	commentCount, activityCount := countByType(entries)
	if activityCount != timelineHardCap {
		t.Errorf("activity count = %d, want %d", activityCount, timelineHardCap)
	}
	if commentCount != 1 {
		t.Errorf("comment count = %d, want 1: nothing should have been clamped", commentCount)
	}
}

// TestListTimeline_WrappedShapeReportsHasMoreBefore checks the legacy wrapped
// response also stops claiming the timeline is complete. has_more_before was
// hardcoded false.
func TestListTimeline_WrappedShapeReportsHasMoreBefore(t *testing.T) {
	issueID := createIssueForTimeline(t, "wrapped has_more_before")

	const total = timelineHardCap + 10
	start := time.Now().UTC().Add(-time.Duration(total) * time.Second).Truncate(time.Second)
	bulkSeedActivities(t, issueID, start, total)

	w := fetchTimelineRecorder(t, issueID, "limit=50")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var resp timelinePaginatedResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode wrapped response: %v", err)
	}
	if !resp.HasMoreBefore {
		t.Error("has_more_before = false, want true when the cap clamped the window")
	}
	if resp.HasMoreAfter {
		t.Error("has_more_after = true, want false: the newest end is always complete now")
	}
	// The wrapped shape is newest-first, so entry 0 is the newest row.
	newest := start.Add(time.Duration(total-1) * time.Second)
	if len(resp.Entries) == 0 {
		t.Fatal("wrapped response returned no entries")
	}
	if got := mustParseTS(t, "newest entry", resp.Entries[0].CreatedAt); !got.Equal(newest) {
		t.Errorf("first (newest-first) entry created_at = %s, want %s", got, newest)
	}
}

// TestListCommentsForIssue_KeepsNewestWindow covers the same query from the
// comment-list endpoint's side, since ListCommentsForIssue is shared. The
// ascending contract must be preserved while the cap now bites at the old end.
func TestListCommentsForIssue_KeepsNewestWindow(t *testing.T) {
	issueID := createIssueForTimeline(t, "comment cap keeps newest")

	const total = 30
	start := time.Now().UTC().Add(-time.Duration(total) * time.Second).Truncate(time.Second)
	bulkSeedComments(t, issueID, start, total)

	issueUUID := parseUUID(issueID)
	wsUUID := parseUUID(testWorkspaceID)

	const limit = 10
	rows, err := testHandler.Queries.ListCommentsForIssue(context.Background(), db.ListCommentsForIssueParams{
		IssueID:     issueUUID,
		WorkspaceID: wsUUID,
		Limit:       limit,
	})
	if err != nil {
		t.Fatalf("ListCommentsForIssue: %v", err)
	}
	if len(rows) != limit {
		t.Fatalf("row count = %d, want %d", len(rows), limit)
	}
	// Newest window: the last `limit` rows of the seeded range.
	wantFirst := start.Add(time.Duration(total-limit) * time.Second)
	wantLast := start.Add(time.Duration(total-1) * time.Second)
	if got := rows[0].CreatedAt.Time.UTC(); !got.Equal(wantFirst) {
		t.Errorf("first row created_at = %s, want %s (newest window, ascending)", got, wantFirst)
	}
	if got := rows[len(rows)-1].CreatedAt.Time.UTC(); !got.Equal(wantLast) {
		t.Errorf("last row created_at = %s, want %s (newest row)", got, wantLast)
	}
	// Ascending order preserved for every existing caller.
	for i := 1; i < len(rows); i++ {
		if rows[i].CreatedAt.Time.Before(rows[i-1].CreatedAt.Time) {
			t.Fatalf("row %d is older than row %d: ascending contract broken", i, i-1)
		}
	}
}
