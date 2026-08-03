package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/multica-ai/multica/server/internal/automation"
	"github.com/multica-ai/multica/server/internal/domainevent"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Deterministic regressions for the workspace-teardown / Event-Hooks-write race
// (MUL-4332 review).
//
// The six Event Hooks tables carry no FK to workspace (the workspace DB rule
// forbids FKs), so they get none of the implicit FOR KEY SHARE protection the
// CASCADE-backed tables rely on. Without the explicit protocol a writer commits
// inside DeleteWorkspace's window — the automation sweep runs, sees nothing, and
// the writer's rows outlive their workspace as orphans. The reviewer's repro was:
//
//  1. hook-create transaction takes member FOR SHARE, then pauses;
//  2. delete transaction takes workspace FOR UPDATE, sweeps automation (0 rows),
//     then blocks deleting member;
//  3. hook transaction writes hook + revision and commits;
//  4. delete proceeds — leaving workspace=0, member=0, hook=1, revision=1.
//
// The fix makes every automation writer take LockWorkspaceForAutomationWrite
// (FOR KEY SHARE) FIRST, which conflicts with the delete's FOR UPDATE and fixes a
// consistent lock order. These tests drive that exact interleaving.

// teardownFixture is one workspace with a member, plus a neighbour workspace used
// to prove tenant isolation.
type teardownFixture struct {
	pool     *pgxpool.Pool
	svc      *HookService
	ws       string
	userID   string
	issueID  string
	neighbor string
}

func newTeardownFixture(t *testing.T) teardownFixture {
	t.Helper()
	pool := newTaskClaimRacePool(t) // skips if no DB
	ws, userID, _, issueID := seedAttributionFixture(t, pool)

	var neighbor string
	suffix := time.Now().UnixNano()
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO workspace (name, slug) VALUES ('neighbor ws', $1) RETURNING id`,
		fmt.Sprintf("neighbor-%d", suffix)).Scan(&neighbor); err != nil {
		t.Fatalf("seed neighbor workspace: %v", err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		pool.Exec(bg, `DELETE FROM hook_action_effect WHERE execution_id IN (SELECT id FROM hook_execution WHERE workspace_id = $1)`, neighbor)
		pool.Exec(bg, `DELETE FROM hook_execution WHERE workspace_id = $1`, neighbor)
		pool.Exec(bg, `DELETE FROM hook_revision WHERE hook_id IN (SELECT id FROM hook WHERE workspace_id = $1)`, neighbor)
		pool.Exec(bg, `DELETE FROM hook WHERE workspace_id = $1`, neighbor)
		pool.Exec(bg, `DELETE FROM automation_state WHERE workspace_id = $1`, neighbor)
		pool.Exec(bg, `DELETE FROM domain_event WHERE workspace_id = $1`, neighbor)
		pool.Exec(bg, `DELETE FROM workspace WHERE id = $1`, neighbor)
	})

	return teardownFixture{
		pool: pool, svc: NewHookService(db.New(pool), pool, nil),
		ws: ws, userID: userID, issueID: issueID, neighbor: neighbor,
	}
}

// seedAllSixTables writes one row into each of the six Event Hooks tables for a
// workspace, bypassing the service so it works for the neighbour too.
func (f teardownFixture) seedAllSixTables(t *testing.T, workspaceID string) {
	t.Helper()
	ctx := context.Background()
	hookID, revID, execID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	var eventID string
	if err := f.pool.QueryRow(ctx, `
		INSERT INTO domain_event (workspace_id, type, schema_version, subject_type, subject_id, actor_type, payload, correlation_id, hop_count)
		VALUES ($1, 'issue.status_changed', 1, 'issue', gen_random_uuid(), 'system', '{}'::jsonb, gen_random_uuid(), 0)
		RETURNING id`, workspaceID).Scan(&eventID); err != nil {
		t.Fatalf("seed domain_event: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `
		INSERT INTO hook (id, workspace_id, name, enabled, active_revision_id, scope_type, origin, creator_actor_type, creator_actor_id, authorization_principal_user_id)
		VALUES ($1, $2, 'teardown hook', true, $3, 'workspace', 'user', 'member', $4, $4)`,
		hookID, workspaceID, revID, f.userID); err != nil {
		t.Fatalf("seed hook: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `
		INSERT INTO hook_revision (id, hook_id, revision, event_type, match, conditions, fire_mode, actions, created_by_type, created_by_id)
		VALUES ($1, $2, 1, 'issue.status_changed', '{}'::jsonb, '[]'::jsonb, 'per_event', '[]'::jsonb, 'member', $3)`,
		revID, hookID, f.userID); err != nil {
		t.Fatalf("seed hook_revision: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `
		INSERT INTO hook_execution (id, workspace_id, hook_id, hook_revision_id, event_id, correlation_id, status)
		VALUES ($1, $2, $3, $4, $5, gen_random_uuid(), 'queued')`,
		execID, workspaceID, hookID, revID, eventID); err != nil {
		t.Fatalf("seed hook_execution: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `
		INSERT INTO hook_action_effect (effect_key, execution_id, action_index, action_type, status)
		VALUES ($1, $2, 0, 'set_issue_status', 'succeeded')`,
		"teardown:"+execID, execID); err != nil {
		t.Fatalf("seed hook_action_effect: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `
		INSERT INTO automation_state (workspace_id, state_kind, state_key, state)
		VALUES ($1, 'hook_edge', $2, '{"satisfied":true}'::jsonb)`,
		workspaceID, hookID); err != nil {
		t.Fatalf("seed automation_state: %v", err)
	}
}

// automationRowCounts returns the row count of each of the six tables for a
// workspace. hook_revision / hook_action_effect are reached through their parent.
func (f teardownFixture) automationRowCounts(t *testing.T, workspaceID string) map[string]int {
	t.Helper()
	ctx := context.Background()
	out := map[string]int{}
	queries := map[string]string{
		"domain_event":       `SELECT count(*) FROM domain_event WHERE workspace_id = $1`,
		"hook":               `SELECT count(*) FROM hook WHERE workspace_id = $1`,
		"hook_execution":     `SELECT count(*) FROM hook_execution WHERE workspace_id = $1`,
		"automation_state":   `SELECT count(*) FROM automation_state WHERE workspace_id = $1`,
		"hook_revision":      `SELECT count(*) FROM hook_revision WHERE hook_id IN (SELECT id FROM hook WHERE workspace_id = $1)`,
		"hook_action_effect": `SELECT count(*) FROM hook_action_effect WHERE execution_id IN (SELECT id FROM hook_execution WHERE workspace_id = $1)`,
	}
	for name, q := range queries {
		var n int
		if err := f.pool.QueryRow(ctx, q, workspaceID).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", name, err)
		}
		out[name] = n
	}
	return out
}

func (f teardownFixture) assertNoResidue(t *testing.T, workspaceID, label string) {
	t.Helper()
	for table, n := range f.automationRowCounts(t, workspaceID) {
		if n != 0 {
			t.Errorf("%s: %s still has %d row(s) after the workspace was deleted — orphaned automation data", label, table, n)
		}
	}
}

// deleteWorkspaceLikeHandler runs the teardown steps that matter for this race in
// the handler's order: workspace FOR UPDATE, the automation sweep, then member and
// the workspace row. Using the same queries as the handler keeps the protocol under
// test rather than a paraphrase of it.
func (f teardownFixture) deleteWorkspaceLikeHandler(ctx context.Context, workspaceID string) error {
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	qtx := db.New(f.pool).WithTx(tx)
	wsID := util.MustParseUUID(workspaceID)

	if _, err := qtx.LockWorkspaceForDelete(ctx, wsID); err != nil {
		return err
	}
	if err := qtx.DeleteWorkspaceAutomation(ctx, wsID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM member WHERE workspace_id = $1`, workspaceID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM issue WHERE workspace_id = $1`, workspaceID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM workspace WHERE id = $1`, workspaceID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (f teardownFixture) hookSpec(name string) automation.HookSpec {
	return automation.HookSpec{
		Name: name,
		When: automation.WhenSpec{Event: "issue.status_changed", Match: []byte(`{"to":"done"}`)},
		Fire: automation.FireSpec{Mode: automation.FirePerEvent},
		Do:   []automation.ActionSpec{{Type: automation.ActionSetIssueStatus, IssueID: f.issueID, Status: "done"}},
	}
}

// A hook create racing a workspace delete must not leave the hook behind. This is
// the reviewer's exact interleaving: the delete holds workspace FOR UPDATE while the
// create is in flight, so the create must block on the workspace lock and then fail
// closed, rather than slipping past the sweep.
//
// This case is genuinely exposed without the protocol: a create has no pre-existing
// row for the sweep to contend on, so nothing else blocks it. Removing the workspace
// lock makes this test fail with the reviewer's exact result — the hook commits after
// the sweep and outlives its workspace.
func TestHookCreateRacingWorkspaceDeleteLeavesNoResidue(t *testing.T) {
	f := newTeardownFixture(t)
	ctx := context.Background()

	// The delete transaction takes workspace FOR UPDATE and pauses there, holding
	// the window open exactly as the reviewer's repro did.
	deleteTx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer deleteTx.Rollback(ctx)
	dqtx := db.New(f.pool).WithTx(deleteTx)
	wsID := util.MustParseUUID(f.ws)
	if _, err := dqtx.LockWorkspaceForDelete(ctx, wsID); err != nil {
		t.Fatalf("lock workspace for delete: %v", err)
	}
	if err := dqtx.DeleteWorkspaceAutomation(ctx, wsID); err != nil {
		t.Fatalf("sweep automation: %v", err)
	}

	// The create runs concurrently; it must NOT commit while the delete holds the
	// workspace lock.
	created := make(chan error, 1)
	go func() {
		_, err := f.svc.CreateHook(ctx, wsID, f.hookSpec("racing hook"), HookAuthor{
			ActorType: "member", ActorID: util.MustParseUUID(f.userID),
			PrincipalUserID: util.MustParseUUID(f.userID),
		})
		created <- err
	}()

	select {
	case err := <-created:
		t.Fatalf("CreateHook completed (err=%v) while the delete held the workspace lock — "+
			"it did not join the teardown protocol and can commit an orphaned hook", err)
	case <-time.After(750 * time.Millisecond):
		// Expected: blocked on the workspace FOR KEY SHARE.
	}

	// Finish the delete. The blocked create then resumes and must fail closed.
	if _, err := deleteTx.Exec(ctx, `DELETE FROM member WHERE workspace_id = $1`, f.ws); err != nil {
		t.Fatal(err)
	}
	if _, err := deleteTx.Exec(ctx, `DELETE FROM issue WHERE workspace_id = $1`, f.ws); err != nil {
		t.Fatal(err)
	}
	if _, err := deleteTx.Exec(ctx, `DELETE FROM workspace WHERE id = $1`, f.ws); err != nil {
		t.Fatal(err)
	}
	if err := deleteTx.Commit(ctx); err != nil {
		t.Fatalf("commit delete: %v", err)
	}

	select {
	case err := <-created:
		if err == nil {
			t.Error("CreateHook succeeded against a deleted workspace")
		} else if !errors.Is(err, ErrWorkspaceGone) && !errors.Is(err, ErrHookNoPrincipal) {
			t.Logf("CreateHook failed as expected: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("CreateHook never unblocked after the delete committed")
	}

	f.assertNoResidue(t, f.ws, "hook create race")
}

// The same protocol must hold for an update, which appends a hook_revision row.
//
// NOTE on what this proves: unlike the create and outbox cases below, an update is
// ALSO protected incidentally — it takes the hook row FOR UPDATE, and the sweep has
// already deleted that row uncommitted, so it blocks there even without the
// workspace lock. This test therefore pins the end-to-end outcome (no residue), not
// the workspace lock in isolation; the explicit lock makes that protection a stated
// protocol instead of a side effect of which rows happen to be locked.
func TestHookUpdateRacingWorkspaceDeleteLeavesNoResidue(t *testing.T) {
	f := newTeardownFixture(t)
	ctx := context.Background()
	wsID := util.MustParseUUID(f.ws)
	author := HookAuthor{
		ActorType: "member", ActorID: util.MustParseUUID(f.userID),
		PrincipalUserID: util.MustParseUUID(f.userID),
	}

	existing, err := f.svc.CreateHook(ctx, wsID, f.hookSpec("hook to update"), author)
	if err != nil {
		t.Fatalf("seed hook: %v", err)
	}

	deleteTx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer deleteTx.Rollback(ctx)
	dqtx := db.New(f.pool).WithTx(deleteTx)
	if _, err := dqtx.LockWorkspaceForDelete(ctx, wsID); err != nil {
		t.Fatal(err)
	}
	if err := dqtx.DeleteWorkspaceAutomation(ctx, wsID); err != nil {
		t.Fatal(err)
	}

	updated := make(chan error, 1)
	go func() {
		spec := f.hookSpec("hook to update")
		spec.Name = "renamed during teardown"
		_, err := f.svc.UpdateHook(ctx, wsID, existing.Hook.ID, spec, author)
		updated <- err
	}()

	select {
	case err := <-updated:
		t.Fatalf("UpdateHook completed (err=%v) while the delete held the workspace lock — "+
			"it can append an orphaned hook_revision", err)
	case <-time.After(750 * time.Millisecond):
	}

	for _, stmt := range []string{
		`DELETE FROM member WHERE workspace_id = $1`,
		`DELETE FROM issue WHERE workspace_id = $1`,
		`DELETE FROM workspace WHERE id = $1`,
	} {
		if _, err := deleteTx.Exec(ctx, stmt, f.ws); err != nil {
			t.Fatal(err)
		}
	}
	if err := deleteTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-updated:
	case <-time.After(5 * time.Second):
		t.Fatal("UpdateHook never unblocked")
	}

	f.assertNoResidue(t, f.ws, "hook update race")
}

// A domain-event write (the always-on outbox producer, independent of any feature
// flag) racing a workspace delete must not leave an event behind.
//
// Like the create case, this is genuinely exposed without the protocol: domain_event
// has no FK and the writer takes no row lock the sweep contends on, so without the
// workspace lock the event commits after the sweep and is orphaned. Removing the lock
// makes this test fail.
func TestDomainEventWriteRacingWorkspaceDeleteLeavesNoResidue(t *testing.T) {
	f := newTeardownFixture(t)
	ctx := context.Background()
	wsID := util.MustParseUUID(f.ws)

	deleteTx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer deleteTx.Rollback(ctx)
	dqtx := db.New(f.pool).WithTx(deleteTx)
	if _, err := dqtx.LockWorkspaceForDelete(ctx, wsID); err != nil {
		t.Fatal(err)
	}
	if err := dqtx.DeleteWorkspaceAutomation(ctx, wsID); err != nil {
		t.Fatal(err)
	}

	written := make(chan error, 1)
	go func() {
		written <- domainevent.WriteInTx(ctx, f.pool, db.New(f.pool), func(qtx *db.Queries) ([]domainevent.Event, error) {
			return []domainevent.Event{
				domainevent.IssueStatusChanged(wsID, util.MustParseUUID(f.issueID),
					domainevent.SystemActor(),
					domainevent.IssueStatusChangedPayload{From: "todo", To: "done"}),
			}, nil
		})
	}()

	select {
	case err := <-written:
		t.Fatalf("the outbox write completed (err=%v) while the delete held the workspace "+
			"lock — a domain event can outlive its workspace", err)
	case <-time.After(750 * time.Millisecond):
	}

	for _, stmt := range []string{
		`DELETE FROM member WHERE workspace_id = $1`,
		`DELETE FROM issue WHERE workspace_id = $1`,
		`DELETE FROM workspace WHERE id = $1`,
	} {
		if _, err := deleteTx.Exec(ctx, stmt, f.ws); err != nil {
			t.Fatal(err)
		}
	}
	if err := deleteTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-written:
		if err == nil {
			t.Error("the outbox write succeeded against a deleted workspace")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the outbox write never unblocked")
	}

	f.assertNoResidue(t, f.ws, "domain event race")
}

// The matcher's decision transaction writes hook_execution / automation_state; it
// must join the same protocol.
//
// Same caveat as the update case: the matcher locks each candidate hook row
// (LockHookForDecision), which the sweep has already deleted uncommitted, so it also
// blocks without the workspace lock. This pins the outcome; the explicit lock is what
// makes the guarantee independent of that incidental row contention.
func TestMatcherWriteRacingWorkspaceDeleteLeavesNoResidue(t *testing.T) {
	f := newTeardownFixture(t)
	ctx := context.Background()
	wsID := util.MustParseUUID(f.ws)

	// A hook and a pending event for it, so the matcher has real work to do.
	if _, err := f.svc.CreateHook(ctx, wsID, f.hookSpec("matcher hook"), HookAuthor{
		ActorType: "member", ActorID: util.MustParseUUID(f.userID),
		PrincipalUserID: util.MustParseUUID(f.userID),
	}); err != nil {
		t.Fatalf("seed hook: %v", err)
	}
	var eventID string
	if err := f.pool.QueryRow(ctx, `
		INSERT INTO domain_event (workspace_id, type, schema_version, subject_type, subject_id, actor_type, actor_id, payload, correlation_id, hop_count)
		VALUES ($1, 'issue.status_changed', 1, 'issue', $2, 'member', $3, '{"from":"todo","to":"done"}'::jsonb, gen_random_uuid(), 0)
		RETURNING id`, f.ws, f.issueID, f.userID).Scan(&eventID); err != nil {
		t.Fatalf("seed domain_event: %v", err)
	}
	event, err := f.svc.Queries.GetDomainEvent(ctx, util.MustParseUUID(eventID))
	if err != nil {
		t.Fatal(err)
	}

	deleteTx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer deleteTx.Rollback(ctx)
	dqtx := db.New(f.pool).WithTx(deleteTx)
	if _, err := dqtx.LockWorkspaceForDelete(ctx, wsID); err != nil {
		t.Fatal(err)
	}
	if err := dqtx.DeleteWorkspaceAutomation(ctx, wsID); err != nil {
		t.Fatal(err)
	}

	matched := make(chan error, 1)
	go func() { matched <- f.svc.MatchEvent(ctx, event) }()

	select {
	case err := <-matched:
		t.Fatalf("the matcher decision completed (err=%v) while the delete held the workspace "+
			"lock — it can write an orphaned hook_execution / automation_state", err)
	case <-time.After(750 * time.Millisecond):
	}

	for _, stmt := range []string{
		`DELETE FROM member WHERE workspace_id = $1`,
		`DELETE FROM issue WHERE workspace_id = $1`,
		`DELETE FROM workspace WHERE id = $1`,
	} {
		if _, err := deleteTx.Exec(ctx, stmt, f.ws); err != nil {
			t.Fatal(err)
		}
	}
	if err := deleteTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-matched:
	case <-time.After(5 * time.Second):
		t.Fatal("the matcher never unblocked")
	}

	f.assertNoResidue(t, f.ws, "matcher race")
}

// Deleting one workspace must leave an adjacent workspace's automation data — all
// six tables — completely intact.
func TestWorkspaceDeleteLeavesNeighborAutomationIntact(t *testing.T) {
	f := newTeardownFixture(t)
	ctx := context.Background()

	f.seedAllSixTables(t, f.ws)
	f.seedAllSixTables(t, f.neighbor)

	before := f.automationRowCounts(t, f.neighbor)
	for table, n := range before {
		if n != 1 {
			t.Fatalf("neighbour fixture wrong: %s = %d, want 1", table, n)
		}
	}

	if err := f.deleteWorkspaceLikeHandler(ctx, f.ws); err != nil {
		t.Fatalf("delete workspace: %v", err)
	}

	f.assertNoResidue(t, f.ws, "target workspace")
	for table, n := range f.automationRowCounts(t, f.neighbor) {
		if n != 1 {
			t.Errorf("neighbour %s = %d, want 1 — deleting one workspace touched another's automation data", table, n)
		}
	}
}

var _ = pgx.ErrNoRows
