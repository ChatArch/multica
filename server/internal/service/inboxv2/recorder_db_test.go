package inboxv2

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func (f *fixture) recorder(enabled bool) *Recorder {
	return NewRecorderWithSwitch(f.store, func() bool { return enabled })
}

func (f *fixture) wsStr() string   { return uuid.UUID(f.ws.Bytes).String() }
func (f *fixture) userStr() string { return uuid.UUID(f.user.Bytes).String() }

func (f *fixture) countEvents(t *testing.T) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM inbox_event WHERE workspace_id = $1`, f.ws).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// The switch defaults off, and off must mean genuinely nothing: no group, no
// event, no partially-created row. That is what makes deploying the dual write
// a reversible, behaviour-free change.
func TestRecorderWritesNothingWhenTheSwitchIsOff(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	r := f.recorder(false)

	if r.Enabled() {
		t.Fatal("recorder must report itself disabled")
	}
	r.Record(ctx, f.wsStr(), f.userStr(), Delivery{
		Type:       TypeNewComment,
		Identity:   IdentityInput{CommentID: uuid.New().String()},
		SourceKind: SourceIssue,
		SourceID:   uuid.UUID(f.issue.Bytes).String(),
		TargetKind: TargetComment,
		TargetID:   uuid.New().String(),
	}, f.now)

	if got := f.countEvents(t); got != 0 {
		t.Fatalf("switch off wrote %d events, want 0", got)
	}
	var groups int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM inbox_group WHERE workspace_id = $1`, f.ws).Scan(&groups); err != nil {
		t.Fatal(err)
	}
	if groups != 0 {
		t.Fatalf("switch off created %d groups, want 0", groups)
	}
}

func TestRecorderWritesGroupAndEventWhenOn(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	r := f.recorder(true)

	commentID := uuid.New().String()
	targetID := uuid.New().String()
	r.Record(ctx, f.wsStr(), f.userStr(), Delivery{
		Type:       TypeNewComment,
		Identity:   IdentityInput{CommentID: commentID},
		SourceKind: SourceIssue,
		SourceID:   uuid.UUID(f.issue.Bytes).String(),
		TargetKind: TargetComment,
		TargetID:   targetID,
		ActorType:  "member",
		ActorID:    uuid.New().String(),
	}, f.now)

	var gotType, gotTargetKind, gotKey string
	var gotSeq int64
	if err := f.pool.QueryRow(ctx, `
SELECT type, target_kind, delivery_key, event_seq FROM inbox_event WHERE workspace_id = $1
`, f.ws).Scan(&gotType, &gotTargetKind, &gotKey, &gotSeq); err != nil {
		t.Fatalf("expected exactly one event: %v", err)
	}
	if gotType != string(TypeNewComment) || gotTargetKind != "comment" || gotSeq != 1 {
		t.Fatalf("unexpected event: type=%s kind=%s seq=%d", gotType, gotTargetKind, gotSeq)
	}
	expectedKey, err := DeliveryKey(f.wsStr(), f.userStr(), TypeNewComment, IdentityInput{CommentID: commentID})
	if err != nil {
		t.Fatal(err)
	}
	if gotKey != expectedKey {
		t.Fatal("stored delivery key does not match the one the producer would recompute on retry")
	}
}

// The producer is allowed to be called twice for one logical delivery — the
// event bus dispatches synchronously inside request goroutines and retries are
// real. Recording twice must leave one event.
func TestRecorderIsIdempotentAcrossRetries(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	r := f.recorder(true)

	d := Delivery{
		Type:       TypeMentioned,
		Identity:   IdentityInput{CommentID: uuid.New().String()},
		SourceKind: SourceIssue,
		SourceID:   uuid.UUID(f.issue.Bytes).String(),
		TargetKind: TargetComment,
		TargetID:   uuid.New().String(),
	}
	r.Record(ctx, f.wsStr(), f.userStr(), d, f.now)
	r.Record(ctx, f.wsStr(), f.userStr(), d, f.now)

	if got := f.countEvents(t); got != 1 {
		t.Fatalf("a retried delivery produced %d events, want 1", got)
	}
}

// A malformed delivery must not panic or propagate: the legacy write already
// succeeded, and the user's notification must not be lost because the shadow
// write disagreed with the contract.
func TestRecorderSwallowsBadDeliveries(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	r := f.recorder(true)

	cases := []struct {
		name string
		d    Delivery
	}{
		{"comment type with no comment target", Delivery{
			Type:       TypeNewComment,
			Identity:   IdentityInput{CommentID: uuid.New().String()},
			SourceKind: SourceIssue,
			SourceID:   uuid.UUID(f.issue.Bytes).String(),
		}},
		{"missing identity input", Delivery{
			Type:       TypeNewComment,
			SourceKind: SourceIssue,
			SourceID:   uuid.UUID(f.issue.Bytes).String(),
			TargetKind: TargetComment,
			TargetID:   uuid.New().String(),
		}},
		{"unparseable source id", Delivery{
			Type:       TypeStatusChanged,
			Identity:   IdentityInput{IssueID: "x", Field: "status", ChangeID: "t"},
			SourceKind: SourceIssue,
			SourceID:   "not-a-uuid",
		}},
		{"unknown type", Delivery{Type: EventType("nope")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r.Record(ctx, f.wsStr(), f.userStr(), tc.d, f.now) // must not panic
		})
	}
	if got := f.countEvents(t); got != 0 {
		t.Fatalf("malformed deliveries wrote %d events, want 0", got)
	}
}

// Standalone notifications have no parent entity, so their group id comes from
// the delivery key. A retry must reuse the group rather than opening a new one.
func TestRecorderStandaloneRetryReusesTheSameGroup(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	r := f.recorder(true)

	d := Delivery{
		Type: TypeAutopilotPaused,
		Identity: IdentityInput{
			AutopilotID:  uuid.New().String(),
			OccurrenceID: "run-42",
		},
		SourceKind: SourceStandalone,
		TargetKind: TargetAutopilot,
		TargetID:   uuid.New().String(),
		ActorType:  "system",
	}
	r.Record(ctx, f.wsStr(), f.userStr(), d, f.now)
	r.Record(ctx, f.wsStr(), f.userStr(), d, f.now)

	var groups int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM inbox_group WHERE workspace_id = $1`, f.ws).Scan(&groups); err != nil {
		t.Fatal(err)
	}
	if groups != 1 {
		t.Fatalf("standalone retry opened %d groups, want 1", groups)
	}
	if got := f.countEvents(t); got != 1 {
		t.Fatalf("standalone retry wrote %d events, want 1", got)
	}
}

// Two events about the same issue fold into one group even when they are
// different types — the property the old schema could not express.
func TestRecorderFoldsDifferentTypesOnOneIssue(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	r := f.recorder(true)
	issueID := uuid.UUID(f.issue.Bytes).String()

	r.Record(ctx, f.wsStr(), f.userStr(), Delivery{
		Type:       TypeNewComment,
		Identity:   IdentityInput{CommentID: uuid.New().String()},
		SourceKind: SourceIssue, SourceID: issueID,
		TargetKind: TargetComment, TargetID: uuid.New().String(),
	}, f.now)
	r.Record(ctx, f.wsStr(), f.userStr(), Delivery{
		Type: TypeStatusChanged,
		Identity: IdentityInput{
			IssueID: issueID, Field: "status", ChangeID: "2026-08-04T07:00:00Z",
		},
		SourceKind: SourceIssue, SourceID: issueID,
	}, f.now)

	var groups int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM inbox_group WHERE workspace_id = $1`, f.ws).Scan(&groups); err != nil {
		t.Fatal(err)
	}
	if groups != 1 {
		t.Fatalf("two events on one issue produced %d groups, want 1", groups)
	}
	if got := f.countEvents(t); got != 2 {
		t.Fatalf("expected 2 events, got %d", got)
	}
}

// A field change flipped back to its original value must stay two
// notifications: this is the case a (issue, field, from, to) key would collapse.
func TestRecorderKeepsRepeatedFieldFlipsDistinct(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	r := f.recorder(true)
	issueID := uuid.UUID(f.issue.Bytes).String()

	for _, changeAt := range []string{"2026-08-04T07:00:00Z", "2026-08-04T07:05:00Z"} {
		r.Record(ctx, f.wsStr(), f.userStr(), Delivery{
			Type: TypeStatusChanged,
			Identity: IdentityInput{
				IssueID: issueID, Field: "status", ChangeID: changeAt,
			},
			SourceKind: SourceIssue, SourceID: issueID,
		}, f.now)
	}
	if got := f.countEvents(t); got != 2 {
		t.Fatalf("two distinct edits produced %d events, want 2", got)
	}
}
