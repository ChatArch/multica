package inboxv2

import (
	"context"
	"os"
	"regexp"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// These tests run against a real database because every contract they check is
// enforced by Postgres: the CHECK constraints, the unique keys, the row lock
// that serialises sequence allocation. A mock would only assert that the Go
// code calls the queries it calls.

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://multica:multica@localhost:5432/multica?sslmode=disable"
	}
	pool, err := pgxpool.New(context.Background(), dbURL)
	if err != nil {
		t.Skipf("skipping DB test: %v", err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		pool.Close()
		t.Skipf("skipping DB test: database not reachable: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

type fixture struct {
	store *Store
	pool  *pgxpool.Pool
	ws    pgtype.UUID
	user  pgtype.UUID
	issue pgtype.UUID
	now   time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	pool := testPool(t)
	f := &fixture{
		store: NewStore(db.New(pool), pool),
		pool:  pool,
		ws:    newUUID(),
		user:  newUUID(),
		issue: newUUID(),
		now:   time.Now().UTC().Truncate(time.Microsecond),
	}
	// inbox_event / inbox_group carry no foreign keys (repo rule), so the test
	// needs no seeded workspace — random ids are enough to isolate a run.
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM inbox_event WHERE workspace_id = $1`, f.ws)
		_, _ = pool.Exec(ctx, `DELETE FROM inbox_group WHERE workspace_id = $1`, f.ws)
	})
	return f
}

func newUUID() pgtype.UUID {
	return pgtype.UUID{Bytes: uuid.New(), Valid: true}
}

// comment builds a valid new_comment delivery for a distinct comment.
func (f *fixture) comment(t *testing.T, commentID string) AppendParams {
	t.Helper()
	key, err := DeliveryKey(uuid.UUID(f.ws.Bytes).String(), uuid.UUID(f.user.Bytes).String(),
		TypeNewComment, IdentityInput{CommentID: commentID})
	if err != nil {
		t.Fatal(err)
	}
	return AppendParams{
		WorkspaceID: f.ws,
		RecipientID: f.user,
		SourceKind:  SourceIssue,
		SourceID:    f.issue,
		Type:        TypeNewComment,
		ActorType:   "member",
		TargetKind:  TargetComment,
		TargetID:    newUUID(),
		DeliveryKey: key,
		Now:         f.now,
	}
}

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

// --- schema / registry drift -------------------------------------------------

// The Go registry and the database CHECK are two copies of the same list. If
// they drift, a producer either writes a type the database rejects at runtime,
// or the database silently accepts a type nothing knows how to render.
func TestEventTypeRegistryMatchesDatabaseCheck(t *testing.T) {
	f := newFixture(t)
	var def string
	err := f.pool.QueryRow(context.Background(),
		`SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conname = 'inbox_event_type_known'`).Scan(&def)
	if err != nil {
		t.Fatalf("read constraint: %v", err)
	}

	inDB := map[string]bool{}
	for _, m := range regexp.MustCompile(`'([a-z_]+)'`).FindAllStringSubmatch(def, -1) {
		inDB[m[1]] = true
	}
	inGo := map[string]bool{}
	for typ := range EventTypes {
		inGo[string(typ)] = true
	}

	var missingInDB, missingInGo []string
	for typ := range inGo {
		if !inDB[typ] {
			missingInDB = append(missingInDB, typ)
		}
	}
	for typ := range inDB {
		if !inGo[typ] {
			missingInGo = append(missingInGo, typ)
		}
	}
	sort.Strings(missingInDB)
	sort.Strings(missingInGo)
	if len(missingInDB) > 0 {
		t.Errorf("types in the Go registry but not in the database CHECK: %v", missingInDB)
	}
	if len(missingInGo) > 0 {
		t.Errorf("types in the database CHECK but not in the Go registry: %v", missingInGo)
	}
}

// Each registered type must actually be insertable in its declared target
// shape. This is what catches a CHECK whose per-type clause contradicts the
// registry, which a set comparison alone would miss.
func TestEveryRegisteredTypeIsInsertableInItsDeclaredShape(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	types := make([]EventType, 0, len(EventTypes))
	for typ := range EventTypes {
		types = append(types, typ)
	}
	sort.Slice(types, func(i, j int) bool { return types[i] < types[j] })

	for i, typ := range types {
		spec := EventTypes[typ]
		p := AppendParams{
			WorkspaceID: f.ws,
			RecipientID: f.user,
			SourceKind:  SourceStandalone,
			SourceID:    newUUID(),
			Type:        typ,
			ActorType:   "system",
			DeliveryKey: "v1:shape-" + string(typ),
			Now:         f.now.Add(time.Duration(i) * time.Second),
		}
		if spec.Rule == TargetRequired || spec.Rule == TargetOptional {
			if spec.Kind != "" {
				p.TargetKind = spec.Kind
				p.TargetID = newUUID()
			}
		}
		if _, err := f.store.Append(ctx, p); err != nil {
			t.Errorf("type %q is not insertable in its declared shape: %v", typ, err)
		}
	}
}

// The borrowed-target bug made unrepresentable: a comment notification without
// a comment, and a status change carrying one, are both rejected by the
// database rather than by convention.
func TestDatabaseRejectsTargetContractViolations(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	// Bypass the Go validator on purpose — the point is that the database is
	// the backstop even if a producer reaches the query layer directly.
	q := db.New(f.pool)

	group, err := q.AcquireInboxGroup(ctx, db.AcquireInboxGroupParams{
		WorkspaceID: f.ws, RecipientID: f.user,
		SourceKind: string(SourceIssue), SourceID: f.issue, Now: ts(f.now),
	})
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name       string
		typ        EventType
		targetKind pgtype.Text
		targetID   pgtype.UUID
	}{
		{"comment notification without a comment", TypeNewComment, pgtype.Text{}, pgtype.UUID{}},
		{"mention pointing at a run", TypeMentioned, pgtype.Text{String: "run", Valid: true}, newUUID()},
		{"status change carrying a target", TypeStatusChanged, pgtype.Text{String: "comment", Valid: true}, newUUID()},
		{"assignment carrying a target", TypeIssueAssigned, pgtype.Text{String: "comment", Valid: true}, newUUID()},
		{"kind without id", TypeTaskFailed, pgtype.Text{String: "run", Valid: true}, pgtype.UUID{}},
		{"id without kind", TypeTaskFailed, pgtype.Text{}, newUUID()},
		{"unknown type", EventType("totally_made_up"), pgtype.Text{}, pgtype.UUID{}},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := q.InsertInboxEvent(ctx, db.InsertInboxEventParams{
				GroupID: group.ID, WorkspaceID: f.ws,
				EventSeq: int64(100 + i), Type: string(tc.typ),
				TargetKind: tc.targetKind, TargetID: tc.targetID,
				Payload: []byte("{}"), PayloadVersion: 1,
				DeliveryKey: "v1:violation-" + tc.name, Now: ts(f.now),
			})
			if err == nil {
				t.Fatal("expected the database to reject this row")
			}
		})
	}
}

// --- append: ordering and idempotency ---------------------------------------

func TestAppendAllocatesGaplessSequence(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	for i := 1; i <= 5; i++ {
		res, err := f.store.Append(ctx, f.comment(t, uuid.New().String()))
		if err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
		if res.Event.EventSeq != int64(i) {
			t.Fatalf("append %d got seq %d, want %d", i, res.Event.EventSeq, i)
		}
		if res.Group.LatestSeq != int64(i) {
			t.Fatalf("append %d left latest_seq %d, want %d", i, res.Group.LatestSeq, i)
		}
	}
}

func TestAppendIsIdempotentForARepeatedDelivery(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	p := f.comment(t, "same-comment")

	first, err := f.store.Append(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if first.Deduplicated {
		t.Fatal("the first delivery is not a duplicate")
	}

	second, err := f.store.Append(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Deduplicated {
		t.Fatal("a repeated delivery must be reported as deduplicated so the caller suppresses its websocket event")
	}
	if second.Event.ID != first.Event.ID {
		t.Fatal("a retry must return the original event, not a new one")
	}
	if second.Group.LatestSeq != 1 {
		t.Fatalf("a retry must not advance latest_seq, got %d", second.Group.LatestSeq)
	}
	if second.Group.StateVersion != first.Group.StateVersion {
		t.Fatalf("a retry must not advance state_version: %d -> %d",
			first.Group.StateVersion, second.Group.StateVersion)
	}
}

// Two transactions can both miss the delivery-key probe. The unique constraint
// decides, the loser rolls back whole, and both callers end up describing the
// same single notification.
func TestAppendConcurrentSameDeliveryKeyProducesOneEvent(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	p := f.comment(t, "contended-comment")

	const racers = 8
	var wg sync.WaitGroup
	results := make([]AppendResult, racers)
	errs := make([]error, racers)
	start := make(chan struct{})

	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i], errs[i] = f.store.Append(ctx, p)
		}(i)
	}
	close(start)
	wg.Wait()

	fresh := 0
	var eventID pgtype.UUID
	for i := 0; i < racers; i++ {
		if errs[i] != nil {
			t.Fatalf("racer %d: %v", i, errs[i])
		}
		if !results[i].Deduplicated {
			fresh++
		}
		if !eventID.Valid {
			eventID = results[i].Event.ID
		} else if results[i].Event.ID != eventID {
			t.Fatal("racers observed different events for one delivery key")
		}
	}
	if fresh != 1 {
		t.Fatalf("exactly one racer may create the event, got %d", fresh)
	}

	var count int64
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM inbox_event WHERE delivery_key = $1`, p.DeliveryKey).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected exactly one stored event, got %d", count)
	}

	group, err := f.store.Queries.GetInboxGroupForRecipient(ctx, db.GetInboxGroupForRecipientParams{
		ID: results[0].Event.GroupID, WorkspaceID: f.ws, RecipientID: f.user,
	})
	if err != nil {
		t.Fatal(err)
	}
	if group.LatestSeq != 1 {
		t.Fatalf("latest_seq advanced %d times for one delivery", group.LatestSeq)
	}
}

// Concurrent distinct deliveries into one group must serialise on the group
// row lock, producing a dense sequence rather than duplicates or holes.
func TestAppendConcurrentDistinctDeliveriesSerialise(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	const n = 10
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make([]error, n)
	seqs := make([]int64, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			p := f.comment(t, uuid.New().String())
			<-start
			res, err := f.store.Append(ctx, p)
			errs[i] = err
			if err == nil {
				seqs[i] = res.Event.EventSeq
			}
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	sort.Slice(seqs, func(i, j int) bool { return seqs[i] < seqs[j] })
	for i, got := range seqs {
		if got != int64(i+1) {
			t.Fatalf("sequence is not dense: %v", seqs)
		}
	}
}

// Archive is not unsubscribe: a new event pulls the group back into the inbox.
func TestAppendUnarchivesTheGroup(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	first, err := f.store.Append(ctx, f.comment(t, uuid.New().String()))
	if err != nil {
		t.Fatal(err)
	}
	archived, err := f.store.Queries.ArchiveInboxGroup(ctx, db.ArchiveInboxGroupParams{
		ID: first.Group.ID, WorkspaceID: f.ws, RecipientID: f.user, Now: ts(f.now),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !archived.ArchivedAt.Valid {
		t.Fatal("archive did not stick")
	}

	second, err := f.store.Append(ctx, f.comment(t, uuid.New().String()))
	if err != nil {
		t.Fatal(err)
	}
	if second.Group.ArchivedAt.Valid {
		t.Fatal("a new event must bring an archived group back into the inbox")
	}
	if !IsUnread(second.Group) {
		t.Fatal("a group woken by a new event must be unread")
	}
}

// --- read cursor and state_version ------------------------------------------

// The race the old React ref was papering over: an automatic read is issued,
// the user marks the group unread before it lands, and the late read must not
// undo them. Re-opening afterwards must still work.
func TestManualUnreadSurvivesALateAutomaticRead(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	appended, err := f.store.Append(ctx, f.comment(t, uuid.New().String()))
	if err != nil {
		t.Fatal(err)
	}
	g := appended.Group

	// The client opens the group and captures the snapshot its read will carry.
	staleSeq, staleVersion := g.LatestSeq, g.StateVersion

	// Before that read lands, the user marks the group unread.
	g, err = f.store.Queries.MarkInboxGroupUnread(ctx, db.MarkInboxGroupUnreadParams{
		ID: g.ID, WorkspaceID: f.ws, RecipientID: f.user, Now: ts(f.now),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !g.ManualUnread || !IsUnread(g) {
		t.Fatal("mark unread did not take effect")
	}
	if g.StateVersion == staleVersion {
		t.Fatal("mark unread must advance state_version, otherwise a late read cannot be told apart from a fresh one")
	}

	// The late automatic read arrives carrying the pre-unread snapshot.
	g, err = f.store.Queries.MarkInboxGroupRead(ctx, db.MarkInboxGroupReadParams{
		ID: g.ID, WorkspaceID: f.ws, RecipientID: f.user,
		ObservedSeq: staleSeq, ObservedStateVersion: staleVersion, Now: ts(f.now),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !g.ManualUnread {
		t.Fatal("a late read must not clear a manual unread the user set after it was issued")
	}
	if !IsUnread(g) {
		t.Fatal("the group must still read as unread")
	}
	if g.ReadThroughSeq != staleSeq {
		t.Fatalf("the cursor should still advance (harmlessly), got %d want %d", g.ReadThroughSeq, staleSeq)
	}

	// The user opens it again, now holding the current version.
	g, err = f.store.Queries.MarkInboxGroupRead(ctx, db.MarkInboxGroupReadParams{
		ID: g.ID, WorkspaceID: f.ws, RecipientID: f.user,
		ObservedSeq: g.LatestSeq, ObservedStateVersion: g.StateVersion, Now: ts(f.now),
	})
	if err != nil {
		t.Fatal(err)
	}
	if g.ManualUnread {
		t.Fatal("a fresh read holding the current state_version must clear the manual unread")
	}
	if IsUnread(g) {
		t.Fatal("the group must read as read")
	}
}

// An inflated observed_seq must not mark events that have not happened yet.
func TestReadClampsObservedSeqToLatest(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	appended, err := f.store.Append(ctx, f.comment(t, uuid.New().String()))
	if err != nil {
		t.Fatal(err)
	}
	g, err := f.store.Queries.MarkInboxGroupRead(ctx, db.MarkInboxGroupReadParams{
		ID: appended.Group.ID, WorkspaceID: f.ws, RecipientID: f.user,
		ObservedSeq: 9999, ObservedStateVersion: appended.Group.StateVersion, Now: ts(f.now),
	})
	if err != nil {
		t.Fatal(err)
	}
	if g.ReadThroughSeq != 1 {
		t.Fatalf("cursor must clamp to latest_seq, got %d", g.ReadThroughSeq)
	}

	next, err := f.store.Append(ctx, f.comment(t, uuid.New().String()))
	if err != nil {
		t.Fatal(err)
	}
	if !IsUnread(next.Group) {
		t.Fatal("an event arriving after an inflated read must still be unread")
	}
}

// The cursor is monotonic: an out-of-order read cannot rewind it and resurrect
// notifications the user already cleared.
func TestReadCursorNeverRewinds(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	var g db.InboxGroup
	for i := 0; i < 3; i++ {
		res, err := f.store.Append(ctx, f.comment(t, uuid.New().String()))
		if err != nil {
			t.Fatal(err)
		}
		g = res.Group
	}
	g, err := f.store.Queries.MarkInboxGroupRead(ctx, db.MarkInboxGroupReadParams{
		ID: g.ID, WorkspaceID: f.ws, RecipientID: f.user,
		ObservedSeq: 3, ObservedStateVersion: g.StateVersion, Now: ts(f.now),
	})
	if err != nil {
		t.Fatal(err)
	}
	g, err = f.store.Queries.MarkInboxGroupRead(ctx, db.MarkInboxGroupReadParams{
		ID: g.ID, WorkspaceID: f.ws, RecipientID: f.user,
		ObservedSeq: 1, ObservedStateVersion: g.StateVersion, Now: ts(f.now),
	})
	if err != nil {
		t.Fatal(err)
	}
	if g.ReadThroughSeq != 3 {
		t.Fatalf("cursor rewound to %d", g.ReadThroughSeq)
	}
}

// Archive means handled, so the "archived but unread" limbo state cannot exist.
func TestArchiveClearsUnread(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	res, err := f.store.Append(ctx, f.comment(t, uuid.New().String()))
	if err != nil {
		t.Fatal(err)
	}
	g, err := f.store.Queries.MarkInboxGroupUnread(ctx, db.MarkInboxGroupUnreadParams{
		ID: res.Group.ID, WorkspaceID: f.ws, RecipientID: f.user, Now: ts(f.now),
	})
	if err != nil {
		t.Fatal(err)
	}
	g, err = f.store.Queries.ArchiveInboxGroup(ctx, db.ArchiveInboxGroupParams{
		ID: g.ID, WorkspaceID: f.ws, RecipientID: f.user, Now: ts(f.now),
	})
	if err != nil {
		t.Fatal(err)
	}
	if g.ManualUnread || IsUnread(g) {
		t.Fatal("archiving must clear the unread state, including an explicit manual unread")
	}
	if InInbox(g, f.now) {
		t.Fatal("an archived group is not in the inbox")
	}
}

// --- counting, listing, scoping ---------------------------------------------

func TestUnreadCountIsPerGroupNotPerEvent(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	for i := 0; i < 4; i++ {
		if _, err := f.store.Append(ctx, f.comment(t, uuid.New().String())); err != nil {
			t.Fatal(err)
		}
	}
	got, err := f.store.Queries.CountUnreadInboxGroups(ctx, db.CountUnreadInboxGroupsParams{
		WorkspaceID: f.ws, RecipientID: f.user, Now: ts(f.now),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != 1 {
		t.Fatalf("four events on one issue must count as one unread row, got %d", got)
	}
}

// One person plus one source is exactly one group, no matter how the events
// arrive. This is the constraint the old schema could not express.
func TestGroupIdentityIsUnique(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	a, err := f.store.Append(ctx, f.comment(t, uuid.New().String()))
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.store.Append(ctx, f.comment(t, uuid.New().String()))
	if err != nil {
		t.Fatal(err)
	}
	if a.Group.ID != b.Group.ID {
		t.Fatal("two events about the same issue must land in one group")
	}

	var count int64
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM inbox_group WHERE workspace_id=$1 AND recipient_id=$2`,
		f.ws, f.user).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected exactly one group, got %d", count)
	}
}

// Group reads and writes are scoped by recipient, so knowing a UUID is not
// enough to reach someone else's inbox.
func TestGroupAccessIsScopedToTheRecipient(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	res, err := f.store.Append(ctx, f.comment(t, uuid.New().String()))
	if err != nil {
		t.Fatal(err)
	}
	intruder := newUUID()

	if _, err := f.store.Queries.GetInboxGroupForRecipient(ctx, db.GetInboxGroupForRecipientParams{
		ID: res.Group.ID, WorkspaceID: f.ws, RecipientID: intruder,
	}); err == nil {
		t.Fatal("another user must not be able to read this group")
	}
	if _, err := f.store.Queries.MarkInboxGroupRead(ctx, db.MarkInboxGroupReadParams{
		ID: res.Group.ID, WorkspaceID: f.ws, RecipientID: intruder,
		ObservedSeq: 1, ObservedStateVersion: res.Group.StateVersion, Now: ts(f.now),
	}); err == nil {
		t.Fatal("another user must not be able to mark this group read")
	}
	if _, err := f.store.Queries.ArchiveInboxGroup(ctx, db.ArchiveInboxGroupParams{
		ID: res.Group.ID, WorkspaceID: f.ws, RecipientID: intruder, Now: ts(f.now),
	}); err == nil {
		t.Fatal("another user must not be able to archive this group")
	}

	// A foreign workspace id is equally insufficient.
	if _, err := f.store.Queries.GetInboxGroupForRecipient(ctx, db.GetInboxGroupForRecipientParams{
		ID: res.Group.ID, WorkspaceID: newUUID(), RecipientID: f.user,
	}); err == nil {
		t.Fatal("a group must not be reachable from another workspace")
	}
}

// The keyset cursor must not repeat or skip a row when groups share a
// surfaced_at, which is exactly what an OFFSET would do.
func TestListPaginationIsStableAcrossEqualSurfacedAt(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	const total = 5
	for i := 0; i < total; i++ {
		p := f.comment(t, uuid.New().String())
		p.SourceID = newUUID() // distinct groups, identical surfaced_at
		p.DeliveryKey = "v1:page-" + uuid.New().String()
		if _, err := f.store.Append(ctx, p); err != nil {
			t.Fatal(err)
		}
	}

	seen := map[string]bool{}
	cursorAt := pgtype.Timestamptz{}
	cursorID := pgtype.UUID{}
	for page := 0; page < total+1; page++ {
		rows, err := f.store.Queries.ListInboxGroups(ctx, db.ListInboxGroupsParams{
			WorkspaceID: f.ws, RecipientID: f.user, Now: ts(f.now.Add(time.Hour)),
			CursorSurfacedAt: cursorAt, CursorID: cursorID, PageSize: 2,
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) == 0 {
			break
		}
		for _, r := range rows {
			id := uuid.UUID(r.ID.Bytes).String()
			if seen[id] {
				t.Fatalf("group %s returned on more than one page", id)
			}
			seen[id] = true
		}
		last := rows[len(rows)-1]
		cursorAt, cursorID = last.SurfacedAt, last.ID
	}
	if len(seen) != total {
		t.Fatalf("pagination returned %d of %d groups", len(seen), total)
	}
}

// A snoozed group leaves the inbox and its badge without any sweeper running.
func TestSnoozedGroupLeavesTheInboxAndComesBack(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	res, err := f.store.Append(ctx, f.comment(t, uuid.New().String()))
	if err != nil {
		t.Fatal(err)
	}
	until := f.now.Add(time.Hour)
	if _, err := f.pool.Exec(ctx,
		`UPDATE inbox_group SET snoozed_until = $1 WHERE id = $2`, ts(until), res.Group.ID); err != nil {
		t.Fatal(err)
	}

	count, err := f.store.Queries.CountUnreadInboxGroups(ctx, db.CountUnreadInboxGroupsParams{
		WorkspaceID: f.ws, RecipientID: f.user, Now: ts(f.now),
	})
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("a snoozed group must not be counted, got %d", count)
	}

	// No job runs in between; the timestamp expiring is the whole mechanism.
	count, err = f.store.Queries.CountUnreadInboxGroups(ctx, db.CountUnreadInboxGroupsParams{
		WorkspaceID: f.ws, RecipientID: f.user, Now: ts(until.Add(time.Minute)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("an expired snooze must return the group, got %d", count)
	}
}

// The unread event list is what a click uses to pick its jump target.
func TestUnreadEventsForGroup(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	var g db.InboxGroup
	for i := 0; i < 3; i++ {
		res, err := f.store.Append(ctx, f.comment(t, uuid.New().String()))
		if err != nil {
			t.Fatal(err)
		}
		g = res.Group
	}
	g, err := f.store.Queries.MarkInboxGroupRead(ctx, db.MarkInboxGroupReadParams{
		ID: g.ID, WorkspaceID: f.ws, RecipientID: f.user,
		ObservedSeq: 1, ObservedStateVersion: g.StateVersion, Now: ts(f.now),
	})
	if err != nil {
		t.Fatal(err)
	}
	events, err := f.store.Queries.ListUnreadInboxEventsForGroup(ctx, db.ListUnreadInboxEventsForGroupParams{
		GroupID: g.ID, WorkspaceID: f.ws, RecipientID: f.user, ReadThroughSeq: g.ReadThroughSeq,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 unread events, got %d", len(events))
	}
	if events[0].EventSeq != 2 || events[1].EventSeq != 3 {
		t.Fatalf("unread events must be oldest-first, got %d,%d", events[0].EventSeq, events[1].EventSeq)
	}
}

// A group id on its own must not be enough to read someone's notification
// history. Without this the events endpoint would be a plain IDOR: knowing a
// UUID would return another person's inbox contents.
func TestEventQueriesAreScopedToTheOwner(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	res, err := f.store.Append(ctx, f.comment(t, uuid.New().String()))
	if err != nil {
		t.Fatal(err)
	}
	groupID := res.Group.ID
	intruder := newUUID()
	otherWS := newUUID()

	cases := []struct {
		name        string
		workspaceID pgtype.UUID
		recipientID pgtype.UUID
		wantRows    int
	}{
		{"owner", f.ws, f.user, 1},
		{"another recipient", f.ws, intruder, 0},
		{"another workspace", otherWS, f.user, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			history, err := f.store.Queries.ListInboxEventsForGroup(ctx, db.ListInboxEventsForGroupParams{
				GroupID: groupID, WorkspaceID: tc.workspaceID, RecipientID: tc.recipientID, PageSize: 50,
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(history) != tc.wantRows {
				t.Errorf("ListInboxEventsForGroup returned %d rows, want %d", len(history), tc.wantRows)
			}
			unread, err := f.store.Queries.ListUnreadInboxEventsForGroup(ctx, db.ListUnreadInboxEventsForGroupParams{
				GroupID: groupID, WorkspaceID: tc.workspaceID, RecipientID: tc.recipientID, ReadThroughSeq: 0,
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(unread) != tc.wantRows {
				t.Errorf("ListUnreadInboxEventsForGroup returned %d rows, want %d", len(unread), tc.wantRows)
			}
		})
	}
}

// The frozen producer contract, checked against the database rather than
// against the registry alone. Each case is a real producer shape.
func TestFrozenProducerTargetContract(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	q := db.New(f.pool)

	group, err := q.AcquireInboxGroup(ctx, db.AcquireInboxGroupParams{
		WorkspaceID: f.ws, RecipientID: f.user,
		SourceKind: string(SourceIssue), SourceID: f.issue, Now: ts(f.now),
	})
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name       string
		typ        EventType
		targetKind string
		withID     bool
		wantOK     bool
	}{
		// reaction_added covers both anchors, which is why its target is
		// optional rather than required.
		{"reaction on a comment", TypeReactionAdded, "comment", true, true},
		{"reaction on the issue itself", TypeReactionAdded, "", false, true},
		{"reaction pointing at a run", TypeReactionAdded, "run", true, false},

		// mentioned covers both anchors too: a comment body, or the issue
		// description (issue:created / issue:updated pass no comment).
		{"mention in a comment", TypeMentioned, "comment", true, true},
		{"mention in the issue description", TypeMentioned, "", false, true},
		{"mention pointing at a run", TypeMentioned, "run", true, false},

		// Agent/task outcomes with a verified producer: run target required.
		{"task_failed with its run", TypeTaskFailed, "run", true, true},
		{"task_failed without a run", TypeTaskFailed, "", false, false},
		{"quick_create_done with its run", TypeQuickCreateDone, "run", true, true},
		{"quick_create_done without a run", TypeQuickCreateDone, "", false, false},
		{"quick_create_failed without a run", TypeQuickCreateFailed, "", false, false},

		{"autopilot_paused with its autopilot", TypeAutopilotPaused, "autopilot", true, true},
		{"autopilot_paused without one", TypeAutopilotPaused, "", false, false},

		// No producer exists for these, so they stay permissive.
		{"task_completed without a run", TypeTaskCompleted, "", false, true},
		{"agent_blocked pointing at a comment", TypeAgentBlocked, "comment", true, false},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var kind pgtype.Text
			if tc.targetKind != "" {
				kind = pgtype.Text{String: tc.targetKind, Valid: true}
			}
			var id pgtype.UUID
			if tc.withID {
				id = newUUID()
			}
			_, err := q.InsertInboxEvent(ctx, db.InsertInboxEventParams{
				GroupID: group.ID, WorkspaceID: f.ws,
				EventSeq: int64(500 + i), Type: string(tc.typ),
				TargetKind: kind, TargetID: id,
				Payload: []byte("{}"), PayloadVersion: 1,
				DeliveryKey: "v1:frozen-" + tc.name, Now: ts(f.now),
			})
			if tc.wantOK && err != nil {
				t.Fatalf("database rejected a valid producer shape: %v", err)
			}
			if !tc.wantOK && err == nil {
				t.Fatal("database accepted a shape the frozen contract forbids")
			}

			// The Go validator must agree with the database, in both
			// directions — that is the whole point of having both.
			goErr := ValidateTarget(tc.typ, TargetKind(tc.targetKind), tc.withID)
			if (goErr == nil) != tc.wantOK {
				t.Errorf("ValidateTarget disagrees with the database: err=%v, database accepted=%v", goErr, tc.wantOK)
			}
		})
	}
}
