package agent

import (
	"context"
	"log/slog"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/taskfailure"
)

// TestClaudeTerminalReasonFailure covers the mapping in isolation: only the one
// terminal reason that has been observed escaping as a success is recognised,
// and the message it produces has to classify as a context overflow.
func TestClaudeTerminalReasonFailure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		terminalReason string
		resultText     string
		wantFailure    bool
	}{
		{
			name:           "prompt_too_long is a failure",
			terminalReason: "prompt_too_long",
			resultText:     "Prompt is too long",
			wantFailure:    true,
		},
		{
			name:           "prompt_too_long with no prose",
			terminalReason: "prompt_too_long",
			wantFailure:    true,
		},
		{
			name:           "surrounding whitespace still matches",
			terminalReason: " prompt_too_long ",
			wantFailure:    true,
		},
		{
			name:           "a normal completion is untouched",
			terminalReason: "completed",
			resultText:     "Done — pushed the fix.",
		},
		{
			name: "an absent field is untouched",
			// Every backend and every CLI version that does not emit the field
			// keeps the pre-existing is_error contract.
			terminalReason: "",
			resultText:     "Done — pushed the fix.",
		},
		{
			name: "other terminal reasons are left to is_error",
			// These already arrive with is_error set; claiming them here would
			// second-guess a contract that works.
			terminalReason: "max_turns",
			resultText:     "Reached maximum number of turns (10)",
		},
		{
			name:           "api_error is left to is_error",
			terminalReason: "api_error",
			resultText:     "API Error: 500",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := claudeTerminalReasonFailure(tc.terminalReason, tc.resultText)
			if (got != "") != tc.wantFailure {
				t.Fatalf("claudeTerminalReasonFailure(%q, %q) = %q, wantFailure=%v",
					tc.terminalReason, tc.resultText, got, tc.wantFailure)
			}
			if !tc.wantFailure {
				return
			}
			if reason := taskfailure.Classify(got); reason != taskfailure.ReasonAgentContextOverflow {
				t.Fatalf("error %q classifies as %q, want %q", got, reason, taskfailure.ReasonAgentContextOverflow)
			}
			if tc.resultText != "" && !strings.Contains(got, tc.resultText) {
				t.Fatalf("the provider's own wording must survive for diagnosis; got %q", got)
			}
		})
	}
}

// TestClaudeExecuteFailsOnPromptTooLongDespiteCleanIsError is the end-to-end
// regression for GH #6402.
//
// The fixture replays the frames captured from Claude Code 2.1.221 when a
// saturated transcript is resumed — auto-compaction runs, reports
// compact_result=failed / compact_error=exhausted, and the turn ends with
// subtype "success" and terminal_reason "prompt_too_long" — with one change:
// is_error is false. That is the reporter's build (2.1.220), and it is
// structurally reachable on any build, because the CLI derives is_error from
// whether the LAST message it rendered was an API error while terminal_reason
// states why the turn stopped.
//
// Before the fix this run reported `completed` with the provider's notice as
// its answer, so the platform published "Prompt is too long" as the agent's
// reply and kept the dead session as the resume pointer — every later trigger
// resumed it and reproduced the same non-answer.
func TestClaudeExecuteFailsOnPromptTooLongDespiteCleanIsError(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fixture is POSIX-only")
	}

	fakePath := filepath.Join(t.TempDir(), "claude")
	script := "#!/bin/sh\n" +
		"IFS= read -r _\n" +
		`echo '{"type":"system","subtype":"init","session_id":"sess-full"}'` + "\n" +
		`echo '{"type":"system","subtype":"status","status":"compacting","session_id":"sess-full"}'` + "\n" +
		`echo '{"type":"system","subtype":"status","compact_result":"failed","compact_error":"exhausted","session_id":"sess-full"}'` + "\n" +
		`echo '{"type":"result","subtype":"success","is_error":false,"terminal_reason":"prompt_too_long","api_error_status":400,"session_id":"sess-full","result":"Prompt is too long"}'` + "\n"
	writeTestExecutable(t, fakePath, []byte(script))

	backend, err := New("claude", Config{
		ExecutablePath: fakePath,
		Env:            map[string]string{"IS_SANDBOX": "1"},
		Logger:         slog.Default(),
	})
	if err != nil {
		t.Fatalf("new claude backend: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, err := backend.Execute(ctx, "prompt", ExecOptions{
		ResumeSessionID: "sess-full",
		Timeout:         5 * time.Second,
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	go func() {
		for range session.Messages {
		}
	}()

	select {
	case result, ok := <-session.Result:
		if !ok {
			t.Fatal("result channel closed without a value")
		}
		if result.Status != "failed" {
			t.Fatalf("expected status=failed, got %q (output %q)", result.Status, result.Output)
		}
		// A failed run must publish no answer at all — the whole defect was the
		// provider's notice reaching the issue as the agent's conclusion.
		if result.Output != "" {
			t.Fatalf("expected no output on a failed run, got %q", result.Output)
		}
		if reason := taskfailure.Classify(result.Error); reason != taskfailure.ReasonAgentContextOverflow {
			t.Fatalf("error %q classifies as %q, want %q", result.Error, reason, taskfailure.ReasonAgentContextOverflow)
		}
		// The session id must still be reported. It is not a rejected resume —
		// the transcript loaded fine, it is simply full — and the daemon needs
		// the id to record which session the failure retires.
		if result.SessionID != "sess-full" {
			t.Fatalf("expected the session id to be reported, got %q", result.SessionID)
		}
		// A full session is not something a fresh-session retry of THIS task
		// should be triggered by: the platform retires the session and lets the
		// next task start clean, rather than replaying work that may already
		// have had side effects.
		if result.ResumeRejected {
			t.Fatal("a full context window is not a rejected resume")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for result")
	}
}

// The counterpart: a genuinely successful run that reports terminal_reason
// "completed" keeps completing. Guards against the new check swallowing normal
// traffic.
func TestClaudeExecuteKeepsCompletedTerminalReason(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fixture is POSIX-only")
	}

	fakePath := filepath.Join(t.TempDir(), "claude")
	script := "#!/bin/sh\n" +
		"IFS= read -r _\n" +
		`echo '{"type":"system","subtype":"init","session_id":"sess-ok"}'` + "\n" +
		`echo '{"type":"result","subtype":"success","is_error":false,"terminal_reason":"completed","session_id":"sess-ok","result":"Fixed the redirect and pushed."}'` + "\n"
	writeTestExecutable(t, fakePath, []byte(script))

	backend, err := New("claude", Config{
		ExecutablePath: fakePath,
		Env:            map[string]string{"IS_SANDBOX": "1"},
		Logger:         slog.Default(),
	})
	if err != nil {
		t.Fatalf("new claude backend: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, err := backend.Execute(ctx, "prompt", ExecOptions{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	go func() {
		for range session.Messages {
		}
	}()

	select {
	case result, ok := <-session.Result:
		if !ok {
			t.Fatal("result channel closed without a value")
		}
		if result.Status != "completed" {
			t.Fatalf("expected status=completed, got %q (error %q)", result.Status, result.Error)
		}
		if result.Output != "Fixed the redirect and pushed." {
			t.Fatalf("unexpected output %q", result.Output)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for result")
	}
}
