package inboxv2

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (f *fixture) recorder(enabled bool) *Recorder {
	return NewRecorderWithSwitch(f.store, func() bool { return enabled })
}

func (f *fixture) wsStr() string   { return uuid.UUID(f.ws.Bytes).String() }
func (f *fixture) userStr() string { return uuid.UUID(f.user.Bytes).String() }

func (f *fixture) countEvents(t *testing.T) int {
	t.Helper()
	return f.count(t, `SELECT count(*) FROM inbox_event WHERE workspace_id = $1`)
}

func (f *fixture) countGroups(t *testing.T) int {
	t.Helper()
	return f.count(t, `SELECT count(*) FROM inbox_group WHERE workspace_id = $1`)
}

func (f *fixture) countLegacy(t *testing.T) int {
	t.Helper()
	return f.count(t, `SELECT count(*) FROM inbox_item WHERE workspace_id = $1`)
}

func (f *fixture) count(t *testing.T, q string) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(), q, f.ws).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// seedWorkspace makes f.ws a real workspace row. inbox_item predates the no-FK
// convention and still carries workspace_id REFERENCES workspace(id), so the
// dual-write tests — the only ones that touch the legacy table — need one.
func (f *fixture) seedWorkspace(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	slug := "inboxv2-dualwrite-" + uuid.New().String()[:8]
	if _, err := f.pool.Exec(ctx, `
INSERT INTO workspace (id, name, slug) VALUES ($1, 'inboxv2 dual write', $2)
`, f.ws, slug); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, f.ws)
	})
}

// writeLegacy performs the inbox_item insert a producer does today.
func writeLegacy(ctx context.Context, tx pgx.Tx, f *fixture, title string) error {
	_, err := tx.Exec(ctx, `
INSERT INTO inbox_item (workspace_id, recipient_type, recipient_id, type, title)
VALUES ($1, 'member', $2, 'new_comment', $3)
`, f.ws, f.user, title)
	return err
}

// dualWrite is the shape every producer will take: one transaction, legacy row
// first, v2 second, commit once. A v2 error aborts the whole thing.
func dualWrite(ctx context.Context, f *fixture, r *Recorder, d Delivery, title string) error {
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if err := writeLegacy(ctx, tx, f, title); err != nil {
		return err
	}
	if err := r.RecordInTx(ctx, tx, f.wsStr(), f.userStr(), d, f.now); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (f *fixture) validDelivery() Delivery {
	return Delivery{
		Type:       TypeNewComment,
		Identity:   IdentityInput{CommentID: uuid.New().String()},
		SourceKind: SourceIssue,
		SourceID:   uuid.UUID(f.issue.Bytes).String(),
		TargetKind: TargetComment,
		TargetID:   uuid.New().String(),
		ActorType:  "member",
	}
}

// --- the switch --------------------------------------------------------------

// The switch defaults off, and off must mean genuinely nothing: the legacy row
// still lands, and no v2 row of any kind appears. That is what makes shipping
// the producer wiring a behaviour-free deploy.
func TestSwitchOffWritesLegacyOnly(t *testing.T) {
	f := newFixture(t)
	f.seedWorkspace(t)
	ctx := context.Background()
	r := f.recorder(false)

	if r.Enabled() {
		t.Fatal("recorder must report itself disabled")
	}
	if err := dualWrite(ctx, f, r, f.validDelivery(), "switch off"); err != nil {
		t.Fatalf("dual write with the switch off must still deliver: %v", err)
	}
	if got := f.countLegacy(t); got != 1 {
		t.Fatalf("legacy rows = %d, want 1", got)
	}
	if got := f.countEvents(t); got != 0 {
		t.Fatalf("switch off wrote %d events, want 0", got)
	}
	if got := f.countGroups(t); got != 0 {
		t.Fatalf("switch off created %d groups, want 0", got)
	}
}

// --- the atomicity contract --------------------------------------------------

func TestDualWriteCommitsLegacyAndV2Together(t *testing.T) {
	f := newFixture(t)
	f.seedWorkspace(t)
	ctx := context.Background()

	if err := dualWrite(ctx, f, f.recorder(true), f.validDelivery(), "atomic commit"); err != nil {
		t.Fatal(err)
	}
	if got := f.countLegacy(t); got != 1 {
		t.Fatalf("legacy rows = %d, want 1", got)
	}
	if got := f.countGroups(t); got != 1 {
		t.Fatalf("groups = %d, want 1", got)
	}
	if got := f.countEvents(t); got != 1 {
		t.Fatalf("events = %d, want 1", got)
	}
}

// The half that matters most: if the v2 write cannot be made, the legacy row
// must not survive either. Before this contract the producer would have kept
// the inbox_item and left the tables disagreeing until a backfill ran.
func TestV2FailureRollsBackTheLegacyRow(t *testing.T) {
	f := newFixture(t)
	f.seedWorkspace(t)
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
		{"identity the delivery key cannot be built from", Delivery{
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
			err := dualWrite(ctx, f, r, tc.d, "should roll back")
			if err == nil {
				t.Fatal("expected the dual write to fail")
			}
			if got := f.countLegacy(t); got != 0 {
				t.Fatalf("legacy row survived a v2 failure: %d rows", got)
			}
			if got := f.countEvents(t); got != 0 {
				t.Fatalf("v2 events = %d, want 0", got)
			}
		})
	}
}

// A retried delivery is not a failure. The duplicate must roll back only the v2
// attempt — a unique violation would otherwise abort the caller's transaction
// and take the legacy row with it, turning an ordinary retry into a dropped
// notification. This is why the append runs inside a savepoint.
func TestDuplicateV2DeliveryDoesNotRollBackLegacy(t *testing.T) {
	f := newFixture(t)
	f.seedWorkspace(t)
	ctx := context.Background()
	r := f.recorder(true)
	d := f.validDelivery()

	if err := dualWrite(ctx, f, r, d, "first"); err != nil {
		t.Fatal(err)
	}
	if err := dualWrite(ctx, f, r, d, "retry"); err != nil {
		t.Fatalf("a retried delivery must not fail the transaction: %v", err)
	}

	// Two legacy rows: the legacy table has no idempotency and this test does
	// not add one. One v2 event: the delivery key deduplicated it.
	if got := f.countLegacy(t); got != 2 {
		t.Fatalf("legacy rows = %d, want 2", got)
	}
	if got := f.countEvents(t); got != 1 {
		t.Fatalf("a retried delivery produced %d events, want 1", got)
	}
	if got := f.countGroups(t); got != 1 {
		t.Fatalf("groups = %d, want 1", got)
	}
}

// The savepoint must leave the caller's transaction usable, not merely
// un-aborted: a producer may write more rows after the duplicate is absorbed.
func TestTransactionStaysUsableAfterADuplicateV2Delivery(t *testing.T) {
	f := newFixture(t)
	f.seedWorkspace(t)
	ctx := context.Background()
	r := f.recorder(true)
	d := f.validDelivery()

	if err := dualWrite(ctx, f, r, d, "first"); err != nil {
		t.Fatal(err)
	}

	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)

	if err := writeLegacy(ctx, tx, f, "before duplicate"); err != nil {
		t.Fatal(err)
	}
	if err := r.RecordInTx(ctx, tx, f.wsStr(), f.userStr(), d, f.now); err != nil {
		t.Fatalf("duplicate must be absorbed: %v", err)
	}
	// The transaction must still accept work after the savepoint rollback.
	if err := writeLegacy(ctx, tx, f, "after duplicate"); err != nil {
		t.Fatalf("transaction was left unusable by the duplicate: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit after a duplicate failed: %v", err)
	}

	if got := f.countLegacy(t); got != 3 {
		t.Fatalf("legacy rows = %d, want 3", got)
	}
	if got := f.countEvents(t); got != 1 {
		t.Fatalf("events = %d, want 1", got)
	}
}

// A rollback for reasons of the caller's own must take the v2 rows with it.
func TestCallerRollbackDiscardsV2Rows(t *testing.T) {
	f := newFixture(t)
	f.seedWorkspace(t)
	ctx := context.Background()
	r := f.recorder(true)

	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeLegacy(ctx, tx, f, "doomed"); err != nil {
		t.Fatal(err)
	}
	if err := r.RecordInTx(ctx, tx, f.wsStr(), f.userStr(), f.validDelivery(), f.now); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}

	if got := f.countLegacy(t); got != 0 {
		t.Fatalf("legacy rows = %d, want 0", got)
	}
	if got := f.countEvents(t); got != 0 {
		t.Fatalf("v2 events survived the caller's rollback: %d", got)
	}
	if got := f.countGroups(t); got != 0 {
		t.Fatalf("v2 groups survived the caller's rollback: %d", got)
	}
}

// --- grouping and identity, now through the transactional path ---------------

func TestRecorderStandaloneRetryReusesTheSameGroup(t *testing.T) {
	f := newFixture(t)
	f.seedWorkspace(t)
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
	for i := 0; i < 2; i++ {
		if err := dualWrite(ctx, f, r, d, "standalone"); err != nil {
			t.Fatal(err)
		}
	}
	if got := f.countGroups(t); got != 1 {
		t.Fatalf("standalone retry opened %d groups, want 1", got)
	}
	if got := f.countEvents(t); got != 1 {
		t.Fatalf("standalone retry wrote %d events, want 1", got)
	}
}

func TestRecorderFoldsDifferentTypesOnOneIssue(t *testing.T) {
	f := newFixture(t)
	f.seedWorkspace(t)
	ctx := context.Background()
	r := f.recorder(true)
	issueID := uuid.UUID(f.issue.Bytes).String()

	if err := dualWrite(ctx, f, r, f.validDelivery(), "comment"); err != nil {
		t.Fatal(err)
	}
	if err := dualWrite(ctx, f, r, Delivery{
		Type: TypeStatusChanged,
		Identity: IdentityInput{
			IssueID: issueID, Field: "status", ChangeID: "2026-08-04T07:00:00Z",
		},
		SourceKind: SourceIssue, SourceID: issueID,
	}, "status"); err != nil {
		t.Fatal(err)
	}

	if got := f.countGroups(t); got != 1 {
		t.Fatalf("two events on one issue produced %d groups, want 1", got)
	}
	if got := f.countEvents(t); got != 2 {
		t.Fatalf("expected 2 events, got %d", got)
	}
}

// A field change flipped back to its original value must stay two
// notifications: this is the case a (issue, field, from, to) key would collapse.
func TestRecorderKeepsRepeatedFieldFlipsDistinct(t *testing.T) {
	f := newFixture(t)
	f.seedWorkspace(t)
	ctx := context.Background()
	r := f.recorder(true)
	issueID := uuid.UUID(f.issue.Bytes).String()

	for _, changeAt := range []string{"2026-08-04T07:00:00Z", "2026-08-04T07:05:00Z"} {
		if err := dualWrite(ctx, f, r, Delivery{
			Type: TypeStatusChanged,
			Identity: IdentityInput{
				IssueID: issueID, Field: "status", ChangeID: changeAt,
			},
			SourceKind: SourceIssue, SourceID: issueID,
		}, "flip"); err != nil {
			t.Fatal(err)
		}
	}
	if got := f.countEvents(t); got != 2 {
		t.Fatalf("two distinct edits produced %d events, want 2", got)
	}
}
