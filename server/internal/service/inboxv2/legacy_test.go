package inboxv2

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func uuidVal(s string) pgtype.UUID {
	return pgtype.UUID{Bytes: uuid.MustParse(s), Valid: true}
}

func tstz(s string) pgtype.Timestamptz {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return pgtype.Timestamptz{Time: t, Valid: true}
}

// StringDetails is the single choke point that keeps a producer from putting a
// number into `details`. It matters because the legacy inbox endpoint returns an
// ARRAY: one non-string value fails the whole parse on mobile and the entire
// list renders empty, not just the offending row.
func TestStringDetailsFlattensEveryScalar(t *testing.T) {
	got := StringDetails(map[string]any{
		"str":     "text",
		"int":     3,
		"int64":   int64(9223372036854775807),
		"float":   12.5,
		"bool":    true,
		"nothing": nil,
	})
	want := map[string]string{
		"str":   "text",
		"int":   "3",
		"int64": "9223372036854775807",
		"float": "12.5",
		"bool":  "true",
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("details[%q] = %q, want %q", k, got[k], v)
		}
	}
	if _, ok := got["nothing"]; ok {
		t.Error("a nil value must be dropped, not rendered as \"null\"")
	}
	if StringDetails(nil) != nil {
		t.Error("no details must project as nil, not an empty map")
	}
}

// The exact numbers the autopilot pause monitor puts in its details map today.
// Before the projection these reached clients as JSON numbers.
func TestStringDetailsCoversTheAutopilotPauseShape(t *testing.T) {
	got := StringDetails(map[string]any{
		"autopilot_id":         "8ba1f4b6-0000-4000-8000-000000000001",
		"autopilot_title":      "Nightly triage",
		"failed_runs":          int64(7),
		"total_runs":           int64(10),
		"fail_pct":             70.0,
		"lookback_seconds":     int64(3600),
		"threshold_min_runs":   5,
		"threshold_fail_ratio": 0.5,
		"reason":               "auto_paused_high_failure_rate",
	})
	for k, v := range got {
		if v == "" {
			t.Errorf("details[%q] projected empty", k)
		}
	}
	if got["failed_runs"] != "7" || got["fail_pct"] != "70" || got["threshold_fail_ratio"] != "0.5" {
		t.Fatalf("numeric details did not flatten as expected: %v", got)
	}
}

// --- §8 field mapping --------------------------------------------------------

func sampleEvent(seq int64, payload LegacyPayload) db.InboxEvent {
	return db.InboxEvent{
		ID:          uuidVal("11111111-1111-4111-8111-111111111111"),
		GroupID:     uuidVal("22222222-2222-4222-8222-222222222222"),
		WorkspaceID: uuidVal("33333333-3333-4333-8333-333333333333"),
		EventSeq:    seq,
		Type:        "new_comment",
		ActorType:   pgtype.Text{String: "member", Valid: true},
		ActorID:     uuidVal("44444444-4444-4444-8444-444444444444"),
		TargetKind:  pgtype.Text{String: "comment", Valid: true},
		TargetID:    uuidVal("55555555-5555-4555-8555-555555555555"),
		Payload:     payload.Encode(),
		CreatedAt:   tstz("2026-08-04T09:00:00Z"),
	}
}

func sampleGroup(readThrough int64, manualUnread bool, archived bool) db.InboxGroup {
	g := db.InboxGroup{
		ID:             uuidVal("22222222-2222-4222-8222-222222222222"),
		WorkspaceID:    uuidVal("33333333-3333-4333-8333-333333333333"),
		RecipientID:    uuidVal("66666666-6666-4666-8666-666666666666"),
		SourceKind:     string(SourceIssue),
		SourceID:       uuidVal("77777777-7777-4777-8777-777777777777"),
		LatestSeq:      3,
		ReadThroughSeq: readThrough,
		ManualUnread:   manualUnread,
	}
	if archived {
		g.ArchivedAt = tstz("2026-08-04T10:00:00Z")
	}
	return g
}

// id is the EVENT id, because old clients round-trip it back into the
// single-item write endpoints, which resolve event -> group.
func TestLegacyRowUsesTheEventID(t *testing.T) {
	row := RenderLegacy(sampleEvent(2, LegacyPayload{Title: "t", Severity: "info"}), sampleGroup(0, false, false), "member")
	if row.ID != "11111111-1111-4111-8111-111111111111" {
		t.Fatalf("id = %q, want the event id", row.ID)
	}
	if row.IssueID == nil || *row.IssueID != "77777777-7777-4777-8777-777777777777" {
		t.Fatal("issue_id must come from an issue group's source_id")
	}
}

// read = covered by the cursor AND not explicitly marked unread. Both halves
// matter: the cursor alone would render a manually-unread group as read.
func TestLegacyReadFlagCombinesCursorAndManualUnread(t *testing.T) {
	cases := []struct {
		name        string
		seq         int64
		readThrough int64
		manual      bool
		want        bool
	}{
		{"below the cursor", 2, 3, false, true},
		{"exactly at the cursor", 3, 3, false, true},
		{"above the cursor", 3, 2, false, false},
		{"below the cursor but manually unread", 2, 3, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			row := RenderLegacy(
				sampleEvent(tc.seq, LegacyPayload{Title: "t", Severity: "info"}),
				sampleGroup(tc.readThrough, tc.manual, false), "member")
			if row.Read != tc.want {
				t.Fatalf("read = %v, want %v", row.Read, tc.want)
			}
		})
	}
}

func TestLegacyArchivedFollowsArchivedAt(t *testing.T) {
	if RenderLegacy(sampleEvent(1, LegacyPayload{}), sampleGroup(0, false, false), "member").Archived {
		t.Fatal("an active group must not project as archived")
	}
	if !RenderLegacy(sampleEvent(1, LegacyPayload{}), sampleGroup(0, false, true), "member").Archived {
		t.Fatal("an archived group must project as archived")
	}
}

// The legacy column was never absent and clients index into it directly.
func TestLegacyDetailsIsNeverNull(t *testing.T) {
	row := RenderLegacy(sampleEvent(1, LegacyPayload{Title: "t"}), sampleGroup(0, false, false), "member")
	if row.Details == nil {
		t.Fatal("details must project as an object, never null")
	}
	blob, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(blob, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["details"] == nil {
		t.Fatal("serialised details must be {} rather than null")
	}
	if decoded["severity"] != "info" {
		t.Fatalf("severity must default to info, got %v", decoded["severity"])
	}
}

// Rendering must not need the database: the list path would otherwise become
// one query per row.
func TestLegacyRenderIsPure(t *testing.T) {
	p := LegacyPayload{
		Title: "Nightly triage", Body: "auto-paused", Severity: "attention",
		IssueStatus: "in_progress",
		Details:     map[string]string{"reason": "auto_paused_high_failure_rate"},
	}
	e := sampleEvent(1, p)
	g := sampleGroup(0, false, false)
	first := RenderLegacy(e, g, "member")
	second := RenderLegacy(e, g, "member")
	a, _ := json.Marshal(first)
	b, _ := json.Marshal(second)
	if string(a) != string(b) {
		t.Fatal("rendering the same rows twice must produce the same output")
	}
	if first.Body == nil || *first.Body != "auto-paused" {
		t.Fatal("body must come from the snapshot")
	}
	if first.IssueStatus == nil || *first.IssueStatus != "in_progress" {
		t.Fatal("issue_status must come from the snapshot")
	}
}

// TestWriteLegacyProjectionGolden emits the fixture that mobile's zod schema is
// run against in apps/mobile/data/inbox-schema.test.ts.
//
// The point of a shared file rather than two hand-written copies: the TS test
// could previously only pin how mobile reacts to a payload someone typed by
// hand, so a change on the Go side could not fail it. Now the Go projection
// produces the fixture, and any drift in it fails the mobile parse in CI.
func TestWriteLegacyProjectionGolden(t *testing.T) {
	rows := []LegacyRow{
		RenderLegacy(sampleEvent(3, LegacyPayload{
			Title:       "P0: delegated subscription rule",
			Severity:    "info",
			IssueStatus: "in_review",
			Details:     map[string]string{"comment_id": "55555555-5555-4555-8555-555555555555"},
		}), sampleGroup(2, false, false), "member"),

		// The autopilot pause, whose details were numeric before the projection.
		func() LegacyRow {
			e := sampleEvent(1, LegacyPayload{
				Title:    "Autopilot paused: Nightly triage",
				Body:     "Auto-paused after 7 of 10 runs failed (70.0%).",
				Severity: "attention",
				Details: StringDetails(map[string]any{
					"autopilot_id":         "8ba1f4b6-0000-4000-8000-000000000001",
					"failed_runs":          int64(7),
					"total_runs":           int64(10),
					"fail_pct":             70.0,
					"threshold_fail_ratio": 0.5,
					"reason":               "auto_paused_high_failure_rate",
				}),
			})
			e.Type = "autopilot_paused"
			e.ActorType = pgtype.Text{String: "system", Valid: true}
			e.ActorID = pgtype.UUID{}
			e.TargetKind = pgtype.Text{String: "autopilot", Valid: true}
			g := sampleGroup(0, false, false)
			g.SourceKind = string(SourceStandalone)
			return RenderLegacy(e, g, "member")
		}(),

		// A manually-unread row, and an archived one.
		RenderLegacy(sampleEvent(1, LegacyPayload{Title: "manual unread", Severity: "info"}),
			sampleGroup(3, true, false), "member"),
		RenderLegacy(sampleEvent(1, LegacyPayload{Title: "archived", Severity: "info"}),
			sampleGroup(3, false, true), "member"),
	}

	blob, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	blob = append(blob, '\n')

	path := filepath.Join("testdata", "legacy_projection.json")
	existing, err := os.ReadFile(path)
	if err == nil && string(existing) == string(blob) {
		return
	}
	if err := os.MkdirAll("testdata", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, blob, 0o644); err != nil {
		t.Fatal(err)
	}
	if err == nil {
		t.Fatalf("%s was stale and has been regenerated — commit it so the mobile schema test sees the current projection", path)
	}
}

// Every value under details must serialise as a JSON string. This is the exact
// property mobile's z.record(z.string(), z.string()) enforces, asserted on the
// producing side so a break is caught in the Go package that caused it.
func TestLegacyProjectionDetailsAreAllStrings(t *testing.T) {
	blob, err := os.ReadFile(filepath.Join("testdata", "legacy_projection.json"))
	if err != nil {
		t.Skipf("golden not generated yet: %v", err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(blob, &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		t.Fatal("golden is empty")
	}
	for i, row := range rows {
		details, ok := row["details"].(map[string]any)
		if !ok {
			t.Fatalf("row %d: details is not an object", i)
		}
		for k, v := range details {
			if _, isString := v.(string); !isString {
				t.Errorf("row %d: details[%q] = %v (%T), but every value must be a string", i, k, v, v)
			}
		}
	}
}
