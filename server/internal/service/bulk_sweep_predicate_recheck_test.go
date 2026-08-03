package service

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// The bulk sweep's candidate peek takes NO lock (that is what lets it lock the
// workspace first), so between the peek and the per-workspace transaction a
// candidate can legitimately stop being eligible. Re-locking on a broad "status is
// one of the active values" predicate would then kill a task that just became
// healthy — the exact window MUL-4332's review reproduced on the queued-TTL sweeper:
// the peek reads an expired `queued` task, a daemon commits queued -> dispatched,
// and the sweep flips that just-started task to `failed` with a `queued_expired`
// event.
//
// This drives the REAL production pair — PeekExpiredQueuedTasks for the candidate
// read and LockExpiredQueuedTasksByIDsForFail for the re-lock — and lands the REAL
// daemon claim (ClaimAgentTask) in between. With the predicate-specific re-lock the
// dispatched task drops out untouched; with the old shared status set it is failed.
func TestExpiredQueuedSweepSkipsTaskDispatchedAfterThePeek(t *testing.T) {
	pool := newTaskClaimRacePool(t) // skips if no DB
	ctx := context.Background()
	queries := db.New(pool)
	svc := NewTaskService(queries, pool, nil, events.New())

	_, _, agentID, issueID := seedAttributionFixture(t, pool)

	// One task that is genuinely TTL-expired in `queued` — a real sweep candidate.
	var taskID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority, created_at)
		VALUES ($1, (SELECT runtime_id FROM agent WHERE id = $1), $2, 'queued', 0, now() - interval '10 minutes')
		RETURNING id`, agentID, issueID).Scan(&taskID); err != nil {
		t.Fatalf("seed queued task: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, taskID)
		pool.Exec(context.Background(), `DELETE FROM domain_event WHERE subject_id = $1`, taskID)
	})

	claimed := false
	failed, err := svc.FailBulkTasksWithEvents(ctx,
		func(q *db.Queries) ([]db.AgentTaskQueue, error) {
			// The real, unlocked candidate read. Scoped to this test's task so a
			// neighbouring test's backlog cannot join the batch.
			rows, e := q.PeekExpiredQueuedTasks(ctx, db.PeekExpiredQueuedTasksParams{
				TtlSecs:    60,
				MaxPerTick: 100,
			})
			if e != nil {
				return nil, e
			}
			mine := make([]db.AgentTaskQueue, 0, 1)
			for _, r := range rows {
				if util.UUIDToString(r.ID) == taskID {
					mine = append(mine, r)
				}
			}
			if len(mine) != 1 {
				t.Errorf("peek returned %d rows for the seeded task, want 1", len(mine))
			}

			// THE RACE: a daemon claims the task for real, committing
			// queued -> dispatched, after the peek has already banked it as a
			// candidate and before the sweep re-locks it.
			c, cErr := queries.ClaimAgentTask(ctx, db.ClaimAgentTaskParams{
				AgentID:          util.MustParseUUID(agentID),
				PrepareLeaseSecs: 300,
			})
			if cErr != nil {
				t.Errorf("daemon claim: %v", cErr)
			} else if util.UUIDToString(c.ID) != taskID {
				t.Errorf("daemon claimed %s, want the seeded task %s", util.UUIDToString(c.ID), taskID)
			} else {
				claimed = true
			}
			return mine, nil
		},
		// The production re-lock: the same queued-TTL predicate, now under the task
		// row lock.
		func(qtx *db.Queries, ids []pgtype.UUID) ([]db.AgentTaskQueue, error) {
			return qtx.LockExpiredQueuedTasksByIDsForFail(ctx, db.LockExpiredQueuedTasksByIDsForFailParams{
				Ids:     ids,
				TtlSecs: 60,
			})
		},
		func(qtx *db.Queries, ids []pgtype.UUID) ([]db.AgentTaskQueue, error) {
			return qtx.FailAgentTasksByIDs(ctx, db.FailAgentTasksByIDsParams{
				Ids:           ids,
				Error:         pgtype.Text{String: "task expired in queue", Valid: true},
				FailureReason: pgtype.Text{String: "queued_expired", Valid: true},
			})
		})
	if err != nil {
		t.Fatalf("bulk sweep: %v", err)
	}
	if !claimed {
		t.Fatal("the daemon claim did not land — the race was not reproduced")
	}

	if len(failed) != 0 {
		t.Errorf("sweep failed %d task(s), want 0 — a task dispatched after the peek is no longer queued-expired and must not be reaped", len(failed))
	}
	if s := taskStatusForTest(t, pool, taskID); s != "dispatched" {
		t.Errorf("task status = %q, want dispatched (the sweep killed a task a daemon had just started)", s)
	}
	if n := subjectEventCount(t, pool, taskID); n != 0 {
		t.Errorf("task events = %d, want 0 — no queued_expired event may be emitted for a dispatched task", n)
	}
}

// A bulk sweep tick spans many workspaces and runs ONE transaction per workspace,
// so it can succeed partially: workspace A's batch errors while workspace B's fact
// + event are already committed. Those committed rows are terminal and will never be
// re-selected by a later tick, so their post-commit side effects — auto-retry, stuck
// issue reset, agent reconcile, realtime — are the sweep's only chance to run them.
//
// FailBulkTasksWithEvents therefore returns BOTH the committed rows and the
// aggregate error, and every caller must dispatch the rows BEFORE acting on the
// error. This pins that contract end to end: workspace A's transaction fails for
// real, workspace B commits, and running the production dispatch step on the
// returned rows produces B's real, DB-observable side effect (its stuck issue is
// reset to todo with an issue.status_changed event). A caller that returned early
// on the error would lose that reset permanently.
func TestBulkSweepDispatchesCommittedWorkspaceWhenAnotherWorkspaceFails(t *testing.T) {
	pool := newTaskClaimRacePool(t) // skips if no DB
	ctx := context.Background()
	queries := db.New(pool)
	svc := NewTaskService(queries, pool, nil, events.New())

	// Two INDEPENDENT workspaces, each with its own agent, runtime and issue.
	_, _, agentA, issueA := seedAttributionFixture(t, pool)
	_, _, agentB, issueB := seedAttributionFixture(t, pool)

	// Both issues are stuck in_progress, so the failed task's post-commit pipeline
	// has a real reset to perform.
	for _, id := range []string{issueA, issueB} {
		if _, err := pool.Exec(ctx, `UPDATE issue SET status = 'in_progress' WHERE id = $1`, id); err != nil {
			t.Fatalf("set issue in_progress: %v", err)
		}
	}

	// One TTL-expired queued task per workspace. A is older so it is swept first —
	// the error lands before B's workspace is even reached.
	seed := func(agentID, issueID, age string) string {
		var id string
		if err := pool.QueryRow(ctx, `
			INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority, created_at)
			VALUES ($1, (SELECT runtime_id FROM agent WHERE id = $1), $2, 'queued', 0, now() - $3::interval)
			RETURNING id`, agentID, issueID, age).Scan(&id); err != nil {
			t.Fatalf("seed queued task: %v", err)
		}
		return id
	}
	taskA := seed(agentA, issueA, "20 minutes")
	taskB := seed(agentB, issueB, "10 minutes")
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = ANY($1::uuid[])`, []string{taskA, taskB})
		pool.Exec(context.Background(), `DELETE FROM domain_event WHERE subject_id = ANY($1::uuid[])`,
			[]string{taskA, taskB, issueA, issueB})
	})

	// Workspace A's transaction blows up (a transient DB failure inside that one
	// workspace's batch); workspace B's runs the real fail.
	boom := errors.New("transient DB blip in workspace A")
	failed, err := svc.FailBulkTasksWithEvents(ctx,
		func(q *db.Queries) ([]db.AgentTaskQueue, error) {
			rows, e := q.PeekExpiredQueuedTasks(ctx, db.PeekExpiredQueuedTasksParams{
				TtlSecs:    60,
				MaxPerTick: 100,
			})
			if e != nil {
				return nil, e
			}
			mine := make([]db.AgentTaskQueue, 0, 2)
			for _, r := range rows {
				switch util.UUIDToString(r.ID) {
				case taskA, taskB:
					mine = append(mine, r)
				}
			}
			return mine, nil
		},
		func(qtx *db.Queries, ids []pgtype.UUID) ([]db.AgentTaskQueue, error) {
			return qtx.LockExpiredQueuedTasksByIDsForFail(ctx, db.LockExpiredQueuedTasksByIDsForFailParams{
				Ids:     ids,
				TtlSecs: 60,
			})
		},
		func(qtx *db.Queries, ids []pgtype.UUID) ([]db.AgentTaskQueue, error) {
			for _, id := range ids {
				if util.UUIDToString(id) == taskA {
					return nil, boom
				}
			}
			return qtx.FailAgentTasksByIDs(ctx, db.FailAgentTasksByIDsParams{
				Ids:           ids,
				Error:         pgtype.Text{String: "task expired in queue", Valid: true},
				FailureReason: pgtype.Text{String: "queued_expired", Valid: true},
			})
		})

	// Both return values are meaningful AT THE SAME TIME.
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the workspace A failure to surface", err)
	}
	if len(failed) != 1 || util.UUIDToString(failed[0].ID) != taskB {
		t.Fatalf("returned rows = %v, want exactly workspace B's task %s — a failing workspace must not swallow a committed one",
			failed, taskB)
	}

	// Workspace A rolled back entirely; workspace B committed fact + event.
	if s := taskStatusForTest(t, pool, taskA); s != "queued" {
		t.Errorf("workspace A task = %q, want queued (its transaction rolled back)", s)
	}
	if n := subjectEventCount(t, pool, taskA); n != 0 {
		t.Errorf("workspace A events = %d, want 0", n)
	}
	if s := taskStatusForTest(t, pool, taskB); s != "failed" {
		t.Errorf("workspace B task = %q, want failed", s)
	}
	if n := subjectEventCount(t, pool, taskB); n != 1 {
		t.Errorf("workspace B events = %d, want 1", n)
	}

	// The caller's contract: dispatch the committed rows BEFORE surfacing the error.
	// This is the exact production ordering the sweepers and recoverOrphansPage use.
	svc.CaptureQueuedExpiredTasks(ctx, failed)
	svc.HandleFailedTasks(ctx, failed)

	// B's post-commit side effect actually landed — the reset an early return would
	// have dropped forever, since taskB is already terminal and no tick re-selects it.
	if s := issueStatusForTest(t, pool, issueB); s != "todo" {
		t.Errorf("workspace B issue = %q, want todo — the committed row's post-commit reset was lost", s)
	}
	if n := subjectEventCount(t, pool, issueB); n != 1 {
		t.Errorf("workspace B issue events = %d, want 1 issue.status_changed", n)
	}
	// A's task never failed, so nothing about A may have moved.
	if s := issueStatusForTest(t, pool, issueA); s != "in_progress" {
		t.Errorf("workspace A issue = %q, want in_progress (its sweep rolled back)", s)
	}
}

// The same peek-then-lock window on the offline-runtime sweeper: a daemon that
// re-registers between the peek and the transaction flips its runtime back to
// `online`, which makes its in-flight tasks ineligible again. Killing them would
// fail work a live daemon is actively running.
func TestOfflineRuntimeSweepSkipsTaskWhoseRuntimeCameBackOnline(t *testing.T) {
	pool := newTaskClaimRacePool(t) // skips if no DB
	ctx := context.Background()
	queries := db.New(pool)
	svc := NewTaskService(queries, pool, nil, events.New())

	_, _, agentID, issueID := seedAttributionFixture(t, pool)
	runtimeID := runtimeIDForAgentTest(t, pool, agentID)

	// The runtime is offline with a task still in flight — a real sweep candidate.
	if _, err := pool.Exec(ctx, `UPDATE agent_runtime SET status = 'offline' WHERE id = $1`, runtimeID); err != nil {
		t.Fatalf("take runtime offline: %v", err)
	}
	var taskID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority, started_at)
		VALUES ($1, $2, $3, 'running', 0, now())
		RETURNING id`, agentID, runtimeID, issueID).Scan(&taskID); err != nil {
		t.Fatalf("seed running task: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, taskID)
		pool.Exec(context.Background(), `DELETE FROM domain_event WHERE subject_id = $1`, taskID)
	})

	failed, err := svc.FailBulkTasksWithEvents(ctx,
		func(q *db.Queries) ([]db.AgentTaskQueue, error) {
			rows, e := q.PeekTasksForOfflineRuntimes(ctx, 100)
			if e != nil {
				return nil, e
			}
			mine := make([]db.AgentTaskQueue, 0, 1)
			for _, r := range rows {
				if util.UUIDToString(r.ID) == taskID {
					mine = append(mine, r)
				}
			}
			if len(mine) != 1 {
				t.Errorf("peek returned %d rows for the seeded task, want 1", len(mine))
			}
			// THE RACE: the daemon re-registers and its runtime is online again.
			if _, uErr := pool.Exec(ctx, `UPDATE agent_runtime SET status = 'online', last_seen_at = now() WHERE id = $1`, runtimeID); uErr != nil {
				t.Errorf("bring runtime back online: %v", uErr)
			}
			return mine, nil
		},
		func(qtx *db.Queries, ids []pgtype.UUID) ([]db.AgentTaskQueue, error) {
			return qtx.LockOfflineRuntimeTasksByIDsForFail(ctx, ids)
		},
		func(qtx *db.Queries, ids []pgtype.UUID) ([]db.AgentTaskQueue, error) {
			return qtx.FailAgentTasksByIDs(ctx, db.FailAgentTasksByIDsParams{
				Ids:           ids,
				Error:         pgtype.Text{String: "runtime went offline", Valid: true},
				FailureReason: pgtype.Text{String: "runtime_offline", Valid: true},
			})
		})
	if err != nil {
		t.Fatalf("bulk sweep: %v", err)
	}
	if len(failed) != 0 {
		t.Errorf("sweep failed %d task(s), want 0 — the runtime is online again", len(failed))
	}
	if s := taskStatusForTest(t, pool, taskID); s != "running" {
		t.Errorf("task status = %q, want running (the sweep killed a task on a live runtime)", s)
	}
	if n := subjectEventCount(t, pool, taskID); n != 0 {
		t.Errorf("task events = %d, want 0", n)
	}
}

// The same peek-then-lock window on the stale sweeper: the daemon refreshes the
// task's prepare lease (ExtendAgentTaskPrepareLease, renewed every ~15s between
// claim and StartTask) between the peek and the transaction, which proves it is
// alive and makes the task ineligible again.
func TestStaleSweepSkipsTaskWhosePrepareLeaseWasRefreshed(t *testing.T) {
	pool := newTaskClaimRacePool(t) // skips if no DB
	ctx := context.Background()
	queries := db.New(pool)
	svc := NewTaskService(queries, pool, nil, events.New())

	_, _, agentID, issueID := seedAttributionFixture(t, pool)
	runtimeID := runtimeIDForAgentTest(t, pool, agentID)

	// Dispatched long ago with an already-expired prepare lease — a real candidate.
	var taskID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority, dispatched_at, prepare_lease_expires_at)
		VALUES ($1, $2, $3, 'dispatched', 0, now() - interval '30 minutes', now() - interval '5 minutes')
		RETURNING id`, agentID, runtimeID, issueID).Scan(&taskID); err != nil {
		t.Fatalf("seed dispatched task: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, taskID)
		pool.Exec(context.Background(), `DELETE FROM domain_event WHERE subject_id = $1`, taskID)
	})

	failed, err := svc.FailBulkTasksWithEvents(ctx,
		func(q *db.Queries) ([]db.AgentTaskQueue, error) {
			rows, e := q.PeekStaleTasksToFail(ctx, db.PeekStaleTasksToFailParams{
				DispatchedTimeoutSecs: 60,
				RunningTimeoutSecs:    60,
				RuntimeStaleSecs:      60,
				MaxPerTick:            100,
			})
			if e != nil {
				return nil, e
			}
			mine := make([]db.AgentTaskQueue, 0, 1)
			for _, r := range rows {
				if util.UUIDToString(r.ID) == taskID {
					mine = append(mine, r)
				}
			}
			if len(mine) != 1 {
				t.Errorf("peek returned %d rows for the seeded task, want 1", len(mine))
			}
			// THE RACE: the daemon proves it is alive by extending the prepare lease.
			if _, eErr := queries.ExtendAgentTaskPrepareLease(ctx, db.ExtendAgentTaskPrepareLeaseParams{
				ID:        util.MustParseUUID(taskID),
				RuntimeID: runtimeID,
				LeaseSecs: 300,
			}); eErr != nil {
				t.Errorf("extend prepare lease: %v", eErr)
			}
			return mine, nil
		},
		func(qtx *db.Queries, ids []pgtype.UUID) ([]db.AgentTaskQueue, error) {
			return qtx.LockStaleTasksByIDsForFail(ctx, db.LockStaleTasksByIDsForFailParams{
				Ids:                   ids,
				DispatchedTimeoutSecs: 60,
				RunningTimeoutSecs:    60,
				RuntimeStaleSecs:      60,
			})
		},
		func(qtx *db.Queries, ids []pgtype.UUID) ([]db.AgentTaskQueue, error) {
			return qtx.FailAgentTasksByIDs(ctx, db.FailAgentTasksByIDsParams{
				Ids:           ids,
				Error:         pgtype.Text{String: "task timed out", Valid: true},
				FailureReason: pgtype.Text{String: "timeout", Valid: true},
			})
		})
	if err != nil {
		t.Fatalf("bulk sweep: %v", err)
	}
	if len(failed) != 0 {
		t.Errorf("sweep failed %d task(s), want 0 — the prepare lease is live again", len(failed))
	}
	if s := taskStatusForTest(t, pool, taskID); s != "dispatched" {
		t.Errorf("task status = %q, want dispatched (the sweep killed a task whose daemon is alive)", s)
	}
	if n := subjectEventCount(t, pool, taskID); n != 0 {
		t.Errorf("task events = %d, want 0", n)
	}
}
