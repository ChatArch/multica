package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// GH #6340: a user stops a chat turn the agent had already started answering,
// then sends the next message and the agent replies with no memory of the
// conversation.
//
// The cancelled turn's provider session is real — the daemon pinned it onto the
// task row mid-flight and cancellation keeps it there — but nothing on the
// cancel path could hand it back: the resume lookups only considered
// 'completed' / 'failed' rows, and the chat-level resume pointer is written by
// CompleteTask / FailTask, neither of which runs for a cancelled task. These
// tests pin both halves, plus the guards that must survive them.

// TestClaimTask_ChatResumesCancelledTurnSession is the reported scenario at its
// worst: the FIRST turn of a chat is cancelled, so there is no earlier session
// to fall back to and the next turn used to start completely cold.
func TestClaimTask_ChatResumesCancelledTurnSession(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	ctx := context.Background()
	agentID, runtimeID, daemonID := createRuntimeGuardAgent(t, ctx)

	// A brand-new chat: no resume pointer of its own yet.
	var chatSessionID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO chat_session (workspace_id, agent_id, creator_id, title)
		VALUES ($1, $2, $3, 'cancelled first turn')
		RETURNING id
	`, testWorkspaceID, agentID, testUserID).Scan(&chatSessionID); err != nil {
		t.Fatalf("setup: create chat session: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(ctx, `DELETE FROM chat_session WHERE id = $1`, chatSessionID) })

	if _, err := testPool.Exec(ctx, `
		INSERT INTO agent_task_queue (
			agent_id, runtime_id, chat_session_id,
			status, priority, started_at, completed_at, session_id, work_dir
		)
		VALUES ($1, $2, $3, 'cancelled', 0, now(), now(), 'cancelled-turn-session', '/tmp/cancelled-turn-workdir')
	`, agentID, runtimeID, chatSessionID); err != nil {
		t.Fatalf("setup: create cancelled chat task: %v", err)
	}
	if _, err := testPool.Exec(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, chat_session_id, status, priority)
		VALUES ($1, $2, $3, 'queued', 0)
	`, agentID, runtimeID, chatSessionID); err != nil {
		t.Fatalf("setup: create follow-up chat task: %v", err)
	}

	task := claimTaskForRuntimeGuard(t, runtimeID, daemonID)
	if task.PriorSessionID != "cancelled-turn-session" {
		t.Fatalf("PriorSessionID = %q, want cancelled-turn-session (the cancelled turn's context must carry over)", task.PriorSessionID)
	}
	if task.PriorWorkDir != "/tmp/cancelled-turn-workdir" {
		t.Fatalf("PriorWorkDir = %q, want /tmp/cancelled-turn-workdir", task.PriorWorkDir)
	}
}

// TestClaimTask_ChatCancelledSessionStaysExcludedWhenRetired pins the guard the
// change must not weaken: a session some run deliberately abandoned stays
// unreachable even when a cancelled row still points at it.
func TestClaimTask_ChatCancelledSessionStaysExcludedWhenRetired(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	ctx := context.Background()
	agentID, runtimeID, daemonID := createRuntimeGuardAgent(t, ctx)

	var chatSessionID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO chat_session (workspace_id, agent_id, creator_id, title)
		VALUES ($1, $2, $3, 'cancelled retired turn')
		RETURNING id
	`, testWorkspaceID, agentID, testUserID).Scan(&chatSessionID); err != nil {
		t.Fatalf("setup: create chat session: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(ctx, `DELETE FROM chat_session WHERE id = $1`, chatSessionID) })

	if _, err := testPool.Exec(ctx, `
		INSERT INTO agent_task_queue (
			agent_id, runtime_id, chat_session_id,
			status, priority, started_at, completed_at, session_id, work_dir
		)
		VALUES ($1, $2, $3, 'cancelled', 0, now() - interval '5 minutes', now() - interval '4 minutes',
		        'retired-cancelled-session', '/tmp/retired-cancelled-workdir')
	`, agentID, runtimeID, chatSessionID); err != nil {
		t.Fatalf("setup: create cancelled chat task: %v", err)
	}
	// A later run resumed that session, could not use it, and retired it.
	if _, err := testPool.Exec(ctx, `
		INSERT INTO agent_task_queue (
			agent_id, runtime_id, chat_session_id,
			status, priority, started_at, completed_at, retired_session_id
		)
		VALUES ($1, $2, $3, 'completed', 0, now() - interval '2 minutes', now() - interval '1 minutes',
		        'retired-cancelled-session')
	`, agentID, runtimeID, chatSessionID); err != nil {
		t.Fatalf("setup: create retiring chat task: %v", err)
	}
	if _, err := testPool.Exec(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, chat_session_id, status, priority)
		VALUES ($1, $2, $3, 'queued', 0)
	`, agentID, runtimeID, chatSessionID); err != nil {
		t.Fatalf("setup: create follow-up chat task: %v", err)
	}

	task := claimTaskForRuntimeGuard(t, runtimeID, daemonID)
	if task.PriorSessionID != "" {
		t.Fatalf("PriorSessionID = %q, want empty (a retired session stays retired even when a cancelled row names it)", task.PriorSessionID)
	}
}

// TestClaimTask_IssueResumesCancelledTaskSession is the issue-side half: stop a
// run, comment again, and the follow-up keeps the stopped run's context.
func TestClaimTask_IssueResumesCancelledTaskSession(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	ctx := context.Background()
	agentID, runtimeID, daemonID := createRuntimeGuardAgent(t, ctx)

	var issueID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, title, status, priority, creator_id, creator_type, number, position)
		VALUES ($1, 'cancelled issue run fixture', 'in_progress', 'none', $2, 'member', 86340, 0)
		RETURNING id
	`, testWorkspaceID, testUserID).Scan(&issueID); err != nil {
		t.Fatalf("setup: create issue: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(ctx, `DELETE FROM issue WHERE id = $1`, issueID) })

	if _, err := testPool.Exec(ctx, `
		INSERT INTO agent_task_queue (
			agent_id, runtime_id, issue_id,
			status, priority, started_at, completed_at, session_id, work_dir
		)
		VALUES ($1, $2, $3, 'cancelled', 0, now(), now(), 'cancelled-issue-session', '/tmp/cancelled-issue-workdir')
	`, agentID, runtimeID, issueID); err != nil {
		t.Fatalf("setup: create cancelled issue task: %v", err)
	}
	if _, err := testPool.Exec(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority)
		VALUES ($1, $2, $3, 'queued', 0)
	`, agentID, runtimeID, issueID); err != nil {
		t.Fatalf("setup: create follow-up issue task: %v", err)
	}

	task := claimTaskForRuntimeGuard(t, runtimeID, daemonID)
	if task.PriorSessionID != "cancelled-issue-session" {
		t.Fatalf("PriorSessionID = %q, want cancelled-issue-session", task.PriorSessionID)
	}
	if task.PriorWorkDir != "/tmp/cancelled-issue-workdir" {
		t.Fatalf("PriorWorkDir = %q, want /tmp/cancelled-issue-workdir", task.PriorWorkDir)
	}
}

// TestCancelTask_AdvancesChatSessionResumePointer covers the pointer half. The
// claim handler reads chat_session.session_id FIRST, so on a chat that already
// has history a stale pointer would still rewind past the cancelled turn on any
// provider that mints a new session id per resume.
func TestCancelTask_AdvancesChatSessionResumePointer(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	ctx := context.Background()
	agentID, runtimeID, _ := createRuntimeGuardAgent(t, ctx)

	var chatSessionID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO chat_session (
			workspace_id, agent_id, creator_id, title,
			session_id, work_dir, runtime_id
		)
		VALUES ($1, $2, $3, 'cancel advances pointer', 'turn1-session', '/tmp/turn1-workdir', $4)
		RETURNING id
	`, testWorkspaceID, agentID, testUserID, runtimeID).Scan(&chatSessionID); err != nil {
		t.Fatalf("setup: create chat session: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(ctx, `DELETE FROM chat_session WHERE id = $1`, chatSessionID) })

	// Turn 2 is running and has already pinned its own session id.
	var taskID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (
			agent_id, runtime_id, chat_session_id,
			status, priority, started_at, session_id, work_dir
		)
		VALUES ($1, $2, $3, 'running', 0, now(), 'turn2-session', '/tmp/turn2-workdir')
		RETURNING id
	`, agentID, runtimeID, chatSessionID).Scan(&taskID); err != nil {
		t.Fatalf("setup: create running chat task: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE id = $1`, taskID) })

	if _, err := testHandler.TaskService.CancelTaskWithResult(ctx, parseUUID(taskID),
		service.CancelTaskOptions{ClientSupportsDraftRestore: true}); err != nil {
		t.Fatalf("cancel task: %v", err)
	}

	var sessionID, workDir, pointerRuntimeID string
	if err := testPool.QueryRow(ctx, `
		SELECT session_id, work_dir, runtime_id FROM chat_session WHERE id = $1
	`, chatSessionID).Scan(&sessionID, &workDir, &pointerRuntimeID); err != nil {
		t.Fatalf("read chat session pointer: %v", err)
	}
	if sessionID != "turn2-session" {
		t.Fatalf("chat_session.session_id = %q, want turn2-session (the cancelled turn owns the newest session)", sessionID)
	}
	if workDir != "/tmp/turn2-workdir" {
		t.Fatalf("chat_session.work_dir = %q, want /tmp/turn2-workdir", workDir)
	}
	if pointerRuntimeID != runtimeID {
		t.Fatalf("chat_session.runtime_id = %q, want %q (the claim guard only honours its own runtime's pointer)", pointerRuntimeID, runtimeID)
	}
}

// TestCancelTask_KeepsChatPointerWhenNoSessionEstablished guards the other
// direction: a turn cancelled before the backend revealed a session must leave
// the existing pointer alone rather than blanking the chat's memory.
func TestCancelTask_KeepsChatPointerWhenNoSessionEstablished(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	ctx := context.Background()
	agentID, runtimeID, _ := createRuntimeGuardAgent(t, ctx)

	var chatSessionID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO chat_session (
			workspace_id, agent_id, creator_id, title,
			session_id, work_dir, runtime_id
		)
		VALUES ($1, $2, $3, 'cancel keeps pointer', 'turn1-session', '/tmp/turn1-workdir', $4)
		RETURNING id
	`, testWorkspaceID, agentID, testUserID, runtimeID).Scan(&chatSessionID); err != nil {
		t.Fatalf("setup: create chat session: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(ctx, `DELETE FROM chat_session WHERE id = $1`, chatSessionID) })

	var taskID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, chat_session_id, status, priority, started_at)
		VALUES ($1, $2, $3, 'running', 0, now())
		RETURNING id
	`, agentID, runtimeID, chatSessionID).Scan(&taskID); err != nil {
		t.Fatalf("setup: create running chat task: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE id = $1`, taskID) })

	if _, err := testHandler.TaskService.CancelTaskWithResult(ctx, parseUUID(taskID),
		service.CancelTaskOptions{ClientSupportsDraftRestore: true}); err != nil {
		t.Fatalf("cancel task: %v", err)
	}

	var sessionID string
	if err := testPool.QueryRow(ctx, `
		SELECT session_id FROM chat_session WHERE id = $1
	`, chatSessionID).Scan(&sessionID); err != nil {
		t.Fatalf("read chat session pointer: %v", err)
	}
	if sessionID != "turn1-session" {
		t.Fatalf("chat_session.session_id = %q, want turn1-session (a session-less cancel must not touch the pointer)", sessionID)
	}
}

// TestCancelTask_PointerAdvanceIsAtomicWithStatusFlip is the adversarial case:
// a follow-up is already queued when the user cancels. If the status flip
// committed before the pointer advance, that follow-up could be claimed in the
// gap and resume the PREVIOUS turn's session — the exact failure this change
// exists to remove.
//
// The chat row is locked from another connection so the pointer write blocks
// mid-cancel; while it is blocked, no other connection may observe the task as
// cancelled.
func TestCancelTask_PointerAdvanceIsAtomicWithStatusFlip(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	ctx := context.Background()
	agentID, runtimeID, daemonID := createRuntimeGuardAgent(t, ctx)

	var chatSessionID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO chat_session (
			workspace_id, agent_id, creator_id, title,
			session_id, work_dir, runtime_id
		)
		VALUES ($1, $2, $3, 'cancel atomicity', 'turn1-session', '/tmp/turn1-workdir', $4)
		RETURNING id
	`, testWorkspaceID, agentID, testUserID, runtimeID).Scan(&chatSessionID); err != nil {
		t.Fatalf("setup: create chat session: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(ctx, `DELETE FROM chat_session WHERE id = $1`, chatSessionID) })

	var taskID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (
			agent_id, runtime_id, chat_session_id,
			status, priority, started_at, session_id, work_dir
		)
		VALUES ($1, $2, $3, 'running', 0, now(), 'turn2-session', '/tmp/turn2-workdir')
		RETURNING id
	`, agentID, runtimeID, chatSessionID).Scan(&taskID); err != nil {
		t.Fatalf("setup: create running chat task: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE id = $1`, taskID) })

	// The follow-up the user queued while the turn was still running.
	if _, err := testPool.Exec(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, chat_session_id, status, priority)
		VALUES ($1, $2, $3, 'queued', 0)
	`, agentID, runtimeID, chatSessionID); err != nil {
		t.Fatalf("setup: create queued follow-up: %v", err)
	}

	// Hold the chat row from another connection so the pointer write blocks.
	conn, err := testPool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire blocking conn: %v", err)
	}
	defer conn.Release()
	blockTx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin blocking tx: %v", err)
	}
	if _, err := blockTx.Exec(ctx, `SELECT id FROM chat_session WHERE id = $1 FOR UPDATE`, chatSessionID); err != nil {
		t.Fatalf("lock chat session: %v", err)
	}

	cancelDone := make(chan error, 1)
	go func() {
		_, err := testHandler.TaskService.CancelTaskWithResult(context.Background(), parseUUID(taskID),
			service.CancelTaskOptions{ClientSupportsDraftRestore: true})
		cancelDone <- err
	}()

	// While the pointer write is blocked, the cancellation must not be visible.
	time.Sleep(300 * time.Millisecond)
	if got := taskStatus(t, taskID); got == "cancelled" {
		t.Fatalf("task became cancelled while the pointer advance was still blocked: a follow-up claimed in this gap resumes the stale session")
	}

	if err := blockTx.Rollback(ctx); err != nil {
		t.Fatalf("release chat session lock: %v", err)
	}
	if err := <-cancelDone; err != nil {
		t.Fatalf("cancel task: %v", err)
	}

	var pointer string
	if err := testPool.QueryRow(ctx, `SELECT session_id FROM chat_session WHERE id = $1`, chatSessionID).Scan(&pointer); err != nil {
		t.Fatalf("read chat session pointer: %v", err)
	}
	if pointer != "turn2-session" {
		t.Fatalf("chat_session.session_id = %q, want turn2-session", pointer)
	}
	if got := taskStatus(t, taskID); got != "cancelled" {
		t.Fatalf("task status = %q, want cancelled", got)
	}

	// The queued follow-up now claims onto the cancelled turn's session.
	task := claimTaskForRuntimeGuard(t, runtimeID, daemonID)
	if task.PriorSessionID != "turn2-session" {
		t.Fatalf("PriorSessionID = %q, want turn2-session", task.PriorSessionID)
	}
}

// TestPinTaskSession_LateCancelledPinAdvancesChatPointer covers the other
// ordering: the cancel wins the race and finds no session to publish (a Codex
// pin is still waiting on its rollout), then the pin lands on the cancelled row.
// Filling the task row is not enough — an existing chat pointer shadows it, so
// the follow-up would still resume the previous turn.
func TestPinTaskSession_LateCancelledPinAdvancesChatPointer(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	ctx := context.Background()
	agentID, runtimeID, daemonID := createRuntimeGuardAgent(t, ctx)

	var chatSessionID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO chat_session (
			workspace_id, agent_id, creator_id, title,
			session_id, work_dir, runtime_id
		)
		VALUES ($1, $2, $3, 'late pin', 'turn1-session', '/tmp/turn1-workdir', $4)
		RETURNING id
	`, testWorkspaceID, agentID, testUserID, runtimeID).Scan(&chatSessionID); err != nil {
		t.Fatalf("setup: create chat session: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(ctx, `DELETE FROM chat_session WHERE id = $1`, chatSessionID) })

	// Cancelled before the backend revealed its session.
	var taskID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (
			agent_id, runtime_id, chat_session_id,
			status, priority, started_at, completed_at
		)
		VALUES ($1, $2, $3, 'cancelled', 0, now(), now())
		RETURNING id
	`, agentID, runtimeID, chatSessionID).Scan(&taskID); err != nil {
		t.Fatalf("setup: create cancelled chat task: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE id = $1`, taskID) })

	pinTaskSessionViaAPI(t, taskID, daemonID, "turn2-session", "/tmp/turn2-workdir")

	if _, err := testPool.Exec(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, chat_session_id, status, priority)
		VALUES ($1, $2, $3, 'queued', 0)
	`, agentID, runtimeID, chatSessionID); err != nil {
		t.Fatalf("setup: create follow-up chat task: %v", err)
	}

	task := claimTaskForRuntimeGuard(t, runtimeID, daemonID)
	if task.PriorSessionID != "turn2-session" {
		t.Fatalf("PriorSessionID = %q, want turn2-session (a late pin must not stay shadowed by the previous turn's pointer)", task.PriorSessionID)
	}
	if task.PriorWorkDir != "/tmp/turn2-workdir" {
		t.Fatalf("PriorWorkDir = %q, want /tmp/turn2-workdir", task.PriorWorkDir)
	}
}

// TestPinTaskSession_LateCancelledPinYieldsToNewerTurn is the guard on the
// path above: once a NEWER turn has recorded a session of its own, a straggler
// pin for the cancelled turn must not drag the conversation backwards.
func TestPinTaskSession_LateCancelledPinYieldsToNewerTurn(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	ctx := context.Background()
	agentID, runtimeID, daemonID := createRuntimeGuardAgent(t, ctx)

	var chatSessionID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO chat_session (
			workspace_id, agent_id, creator_id, title,
			session_id, work_dir, runtime_id
		)
		VALUES ($1, $2, $3, 'late pin yields', 'turn3-session', '/tmp/turn3-workdir', $4)
		RETURNING id
	`, testWorkspaceID, agentID, testUserID, runtimeID).Scan(&chatSessionID); err != nil {
		t.Fatalf("setup: create chat session: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(ctx, `DELETE FROM chat_session WHERE id = $1`, chatSessionID) })

	var cancelledID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (
			agent_id, runtime_id, chat_session_id,
			status, priority, created_at, started_at, completed_at
		)
		VALUES ($1, $2, $3, 'cancelled', 0, now() - interval '10 minutes',
		        now() - interval '10 minutes', now() - interval '9 minutes')
		RETURNING id
	`, agentID, runtimeID, chatSessionID).Scan(&cancelledID); err != nil {
		t.Fatalf("setup: create cancelled chat task: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE id = $1`, cancelledID) })

	// A newer turn already ran to completion and owns the pointer.
	if _, err := testPool.Exec(ctx, `
		INSERT INTO agent_task_queue (
			agent_id, runtime_id, chat_session_id,
			status, priority, created_at, started_at, completed_at, session_id, work_dir
		)
		VALUES ($1, $2, $3, 'completed', 0, now() - interval '2 minutes',
		        now() - interval '2 minutes', now() - interval '1 minutes',
		        'turn3-session', '/tmp/turn3-workdir')
	`, agentID, runtimeID, chatSessionID); err != nil {
		t.Fatalf("setup: create newer completed task: %v", err)
	}

	pinTaskSessionViaAPI(t, cancelledID, daemonID, "turn2-session", "/tmp/turn2-workdir")

	var pointer string
	if err := testPool.QueryRow(ctx, `SELECT session_id FROM chat_session WHERE id = $1`, chatSessionID).Scan(&pointer); err != nil {
		t.Fatalf("read chat session pointer: %v", err)
	}
	if pointer != "turn3-session" {
		t.Fatalf("chat_session.session_id = %q, want turn3-session (a straggler pin must not rewind a newer turn)", pointer)
	}
}

// TestPinTaskSession_PointerAdvanceIsAtomicWithPin is the pin-path mirror of
// TestCancelTask_PointerAdvanceIsAtomicWithStatusFlip: landing the session on
// the task row and advancing the chat pointer must be one commit, or a
// follow-up claimed in between still resumes the previous turn.
func TestPinTaskSession_PointerAdvanceIsAtomicWithPin(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	ctx := context.Background()
	agentID, runtimeID, daemonID := createRuntimeGuardAgent(t, ctx)

	var chatSessionID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO chat_session (
			workspace_id, agent_id, creator_id, title,
			session_id, work_dir, runtime_id
		)
		VALUES ($1, $2, $3, 'pin atomicity', 'turn1-session', '/tmp/turn1-workdir', $4)
		RETURNING id
	`, testWorkspaceID, agentID, testUserID, runtimeID).Scan(&chatSessionID); err != nil {
		t.Fatalf("setup: create chat session: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(ctx, `DELETE FROM chat_session WHERE id = $1`, chatSessionID) })

	var taskID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (
			agent_id, runtime_id, chat_session_id,
			status, priority, started_at, completed_at
		)
		VALUES ($1, $2, $3, 'cancelled', 0, now(), now())
		RETURNING id
	`, agentID, runtimeID, chatSessionID).Scan(&taskID); err != nil {
		t.Fatalf("setup: create cancelled chat task: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE id = $1`, taskID) })

	if _, err := testPool.Exec(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, chat_session_id, status, priority)
		VALUES ($1, $2, $3, 'queued', 0)
	`, agentID, runtimeID, chatSessionID); err != nil {
		t.Fatalf("setup: create queued follow-up: %v", err)
	}

	conn, err := testPool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire blocking conn: %v", err)
	}
	defer conn.Release()
	blockTx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin blocking tx: %v", err)
	}
	if _, err := blockTx.Exec(ctx, `SELECT id FROM chat_session WHERE id = $1 FOR UPDATE`, chatSessionID); err != nil {
		t.Fatalf("lock chat session: %v", err)
	}

	pinDone := make(chan int, 1)
	go func() {
		w := httptest.NewRecorder()
		req := newDaemonTokenRequest(http.MethodPost, "/api/daemon/tasks/"+taskID+"/session",
			map[string]any{"session_id": "turn2-session", "work_dir": "/tmp/turn2-workdir"},
			testWorkspaceID, daemonID)
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("taskId", taskID)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
		testHandler.PinTaskSession(w, req)
		pinDone <- w.Code
	}()

	// Blocked on the chat row: the task row must not be carrying the new
	// session yet, or a follow-up could claim the gap and resume turn1.
	time.Sleep(300 * time.Millisecond)
	var pinnedSoFar *string
	if err := testPool.QueryRow(ctx, `SELECT session_id FROM agent_task_queue WHERE id = $1`, taskID).Scan(&pinnedSoFar); err != nil {
		t.Fatalf("read task session: %v", err)
	}
	if pinnedSoFar != nil {
		t.Fatalf("task carried session %q while the pointer advance was still blocked", *pinnedSoFar)
	}

	if err := blockTx.Rollback(ctx); err != nil {
		t.Fatalf("release chat session lock: %v", err)
	}
	if code := <-pinDone; code != http.StatusNoContent {
		t.Fatalf("PinTaskSession: expected 204, got %d", code)
	}

	task := claimTaskForRuntimeGuard(t, runtimeID, daemonID)
	if task.PriorSessionID != "turn2-session" {
		t.Fatalf("PriorSessionID = %q, want turn2-session", task.PriorSessionID)
	}
}

// TestCancelTask_TakesChatSessionLockBeforeTask pins the lock order the repo
// documents (chat_session -> agent_task_queue, see LockChatSessionForTask).
// DeleteChatSession locks the session and then cascades into agent_task_queue;
// a cancel that took the task row first and only then reached for the session
// deadlocks against it — PostgreSQL aborts one side with 40P01 and runInTx has
// no deadlock retry.
//
// With the session held from another connection, a cancel must be waiting for
// the SESSION, having taken no lock on the task row yet — which a FOR UPDATE
// NOWAIT probe can prove.
func TestCancelTask_TakesChatSessionLockBeforeTask(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	ctx := context.Background()
	agentID, runtimeID, _ := createRuntimeGuardAgent(t, ctx)

	var chatSessionID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO chat_session (
			workspace_id, agent_id, creator_id, title,
			session_id, work_dir, runtime_id
		)
		VALUES ($1, $2, $3, 'cancel lock order', 'turn1-session', '/tmp/turn1-workdir', $4)
		RETURNING id
	`, testWorkspaceID, agentID, testUserID, runtimeID).Scan(&chatSessionID); err != nil {
		t.Fatalf("setup: create chat session: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(ctx, `DELETE FROM chat_session WHERE id = $1`, chatSessionID) })

	var taskID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (
			agent_id, runtime_id, chat_session_id,
			status, priority, started_at, session_id, work_dir
		)
		VALUES ($1, $2, $3, 'running', 0, now(), 'turn2-session', '/tmp/turn2-workdir')
		RETURNING id
	`, agentID, runtimeID, chatSessionID).Scan(&taskID); err != nil {
		t.Fatalf("setup: create running chat task: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE id = $1`, taskID) })

	// Stand in for DeleteChatSession's first statement.
	deleter, err := testPool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire deleter conn: %v", err)
	}
	defer deleter.Release()
	deleterTx, err := deleter.Begin(ctx)
	if err != nil {
		t.Fatalf("begin deleter tx: %v", err)
	}
	if _, err := deleterTx.Exec(ctx, `SELECT id FROM chat_session WHERE id = $1 FOR UPDATE`, chatSessionID); err != nil {
		t.Fatalf("lock chat session: %v", err)
	}

	cancelDone := make(chan error, 1)
	go func() {
		_, err := testHandler.TaskService.CancelTaskWithResult(context.Background(), parseUUID(taskID),
			service.CancelTaskOptions{ClientSupportsDraftRestore: true})
		cancelDone <- err
	}()
	time.Sleep(300 * time.Millisecond)

	// The deleter's next step would be cancelling the session's tasks. If the
	// cancel already holds that row, the two are in a cycle.
	probe, err := testPool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire probe conn: %v", err)
	}
	defer probe.Release()
	probeTx, err := probe.Begin(ctx)
	if err != nil {
		t.Fatalf("begin probe tx: %v", err)
	}
	var probed string
	if err := probeTx.QueryRow(ctx,
		`SELECT id FROM agent_task_queue WHERE id = $1 FOR UPDATE NOWAIT`, taskID,
	).Scan(&probed); err != nil {
		probeTx.Rollback(ctx)
		deleterTx.Rollback(ctx)
		<-cancelDone
		t.Fatalf("cancel holds the task row while waiting for the chat session — inverted lock order deadlocks against DeleteChatSession: %v", err)
	}
	if err := probeTx.Rollback(ctx); err != nil {
		t.Fatalf("release probe: %v", err)
	}

	if err := deleterTx.Rollback(ctx); err != nil {
		t.Fatalf("release chat session lock: %v", err)
	}
	if err := <-cancelDone; err != nil {
		t.Fatalf("cancel task: %v", err)
	}
	if got := taskStatus(t, taskID); got != "cancelled" {
		t.Fatalf("task status = %q, want cancelled", got)
	}
}

// TestCancelTask_ConcurrentWithChatSessionDelete runs the two paths against
// each other for real: whoever wins, neither may come back with a deadlock.
func TestCancelTask_ConcurrentWithChatSessionDelete(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	ctx := context.Background()
	agentID, runtimeID, _ := createRuntimeGuardAgent(t, ctx)

	for i := 0; i < 12; i++ {
		var chatSessionID string
		if err := testPool.QueryRow(ctx, `
			INSERT INTO chat_session (
				workspace_id, agent_id, creator_id, title,
				session_id, work_dir, runtime_id
			)
			VALUES ($1, $2, $3, 'cancel vs delete', 'turn1-session', '/tmp/turn1-workdir', $4)
			RETURNING id
		`, testWorkspaceID, agentID, testUserID, runtimeID).Scan(&chatSessionID); err != nil {
			t.Fatalf("setup: create chat session: %v", err)
		}

		var taskID string
		if err := testPool.QueryRow(ctx, `
			INSERT INTO agent_task_queue (
				agent_id, runtime_id, chat_session_id,
				status, priority, started_at, session_id, work_dir
			)
			VALUES ($1, $2, $3, 'running', 0, now(), 'turn2-session', '/tmp/turn2-workdir')
			RETURNING id
		`, agentID, runtimeID, chatSessionID).Scan(&taskID); err != nil {
			t.Fatalf("setup: create running chat task: %v", err)
		}

		start := make(chan struct{})
		cancelErr := make(chan error, 1)
		deleteErr := make(chan error, 1)
		go func() {
			<-start
			_, err := testHandler.TaskService.CancelTaskWithResult(context.Background(), parseUUID(taskID),
				service.CancelTaskOptions{ClientSupportsDraftRestore: true})
			cancelErr <- err
		}()
		go func() {
			<-start
			deleteErr <- deleteChatSessionLikeHandler(context.Background(), chatSessionID)
		}()
		close(start)

		if err := <-cancelErr; err != nil && isDeadlock(err) {
			t.Fatalf("iteration %d: cancel deadlocked against chat delete: %v", i, err)
		}
		if err := <-deleteErr; err != nil && isDeadlock(err) {
			t.Fatalf("iteration %d: chat delete deadlocked against cancel: %v", i, err)
		}

		testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE id = $1`, taskID)
		testPool.Exec(ctx, `DELETE FROM chat_session WHERE id = $1`, chatSessionID)
	}
}

// deleteChatSessionLikeHandler replays DeleteChatSession's locking protocol
// (lock the session, then cascade into agent_task_queue) so the concurrency
// test exercises the real order without going through HTTP auth.
func deleteChatSessionLikeHandler(ctx context.Context, chatSessionID string) error {
	tx, err := testHandler.TxStarter.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	qtx := testHandler.Queries.WithTx(tx)

	id := parseUUID(chatSessionID)
	if _, err := qtx.LockChatSessionForDelete(ctx, id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	}
	if _, err := qtx.CancelAgentTasksByChatSession(ctx, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func isDeadlock(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "40P01"
}

// pinTaskSessionViaAPI drives the real daemon endpoint so the test covers the
// handler wiring, not just the query.
func pinTaskSessionViaAPI(t *testing.T, taskID, daemonID, sessionID, workDir string) {
	t.Helper()

	w := httptest.NewRecorder()
	req := newDaemonTokenRequest(http.MethodPost, "/api/daemon/tasks/"+taskID+"/session",
		map[string]any{"session_id": sessionID, "work_dir": workDir}, testWorkspaceID, daemonID)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("taskId", taskID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	testHandler.PinTaskSession(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("PinTaskSession: expected 204, got %d: %s", w.Code, w.Body.String())
	}
}

// TestPinTaskSession_LateCancelledPin covers the mid-flight pin racing the
// cancel: it may fill an EMPTY session slot on the row it belongs to, but must
// never overwrite a session a terminal report already recorded.
func TestPinTaskSession_LateCancelledPin(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	ctx := context.Background()
	agentID, runtimeID, _ := createRuntimeGuardAgent(t, ctx)

	newTask := func(status, sessionID string) string {
		t.Helper()
		var id string
		var session any
		if sessionID != "" {
			session = sessionID
		}
		if err := testPool.QueryRow(ctx, `
			INSERT INTO agent_task_queue (
				agent_id, runtime_id, status, priority, started_at, completed_at, session_id
			)
			VALUES ($1, $2, $3, 0, now(), now(), $4)
			RETURNING id
		`, agentID, runtimeID, status, session).Scan(&id); err != nil {
			t.Fatalf("setup: create %s task: %v", status, err)
		}
		t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, id) })
		return id
	}
	readSession := func(taskID string) string {
		t.Helper()
		var sessionID *string
		if err := testPool.QueryRow(ctx, `SELECT session_id FROM agent_task_queue WHERE id = $1`, taskID).Scan(&sessionID); err != nil {
			t.Fatalf("read task session: %v", err)
		}
		if sessionID == nil {
			return ""
		}
		return *sessionID
	}

	pin := func(taskID, sessionID string) {
		t.Helper()
		if err := testHandler.Queries.UpdateAgentTaskSession(ctx, db.UpdateAgentTaskSessionParams{
			ID:        parseUUID(taskID),
			SessionID: pgtype.Text{String: sessionID, Valid: true},
		}); err != nil {
			t.Fatalf("pin session: %v", err)
		}
	}

	cancelledEmpty := newTask("cancelled", "")
	pin(cancelledEmpty, "late-pinned-session")
	if got := readSession(cancelledEmpty); got != "late-pinned-session" {
		t.Fatalf("cancelled task session_id = %q, want late-pinned-session", got)
	}

	cancelledPinned := newTask("cancelled", "pinned-session")
	pin(cancelledPinned, "straggler-session")
	if got := readSession(cancelledPinned); got != "pinned-session" {
		t.Fatalf("cancelled task session_id = %q, want pinned-session (an occupied slot is never overwritten)", got)
	}

	completed := newTask("completed", "reported-session")
	pin(completed, "straggler-session")
	if got := readSession(completed); got != "reported-session" {
		t.Fatalf("completed task session_id = %q, want reported-session (a terminal report must win)", got)
	}
}
