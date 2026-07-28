package agent

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// acpDeliverableProviders are the backends wired to acpDeliverableTracker. They
// all drive hermesClient over the same ACP stream, so one fixture pins all of
// them and a backend that forgets to wire the tracker fails here rather than in
// production.
var acpDeliverableProviders = []string{"hermes", "kimi", "kiro", "traecli", "grok", "qoder"}

// fakeACPDeliverableScript impersonates any of the ACP CLIs above. The
// handshake is the union of what they need — grok is the only one that requires
// an advertised auth method and an explicit `authenticate`, and the others
// never send it — and the turn shape is switched by env vars:
//
//	ACP_NARRATION       text emitted before the tool call
//	ACP_TOOL_TERMINATED turn ends on the tool call, with no text after it
//	ACP_DEFERRED_TOOL   tool_call frame carries no parameters, so hermesClient
//	                    defers MessageToolUse to the completed tool_call_update
func fakeACPDeliverableScript() string {
	return `#!/bin/sh
while IFS= read -r line; do
  id=$(printf '%s' "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
  case "$line" in
    *'"method":"initialize"'*)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":1,"authMethods":[{"id":"cached_token","name":"Cached login"}],"agentCapabilities":{"loadSession":true}}}\n' "$id"
      ;;
    *'"method":"authenticate"'*)
      printf '{"jsonrpc":"2.0","id":%s,"result":{}}\n' "$id"
      ;;
    *'"method":"session/new"'*)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"sessionId":"ses_deliverable"}}\n' "$id"
      ;;
    *'"method":"session/prompt"'*)
      printf '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"ses_deliverable","update":{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"thinking about it"}}}}\n'
      printf '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"ses_deliverable","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"%s"}}}}\n' "$ACP_NARRATION"
      if [ "$ACP_DEFERRED_TOOL" = "1" ]; then
        printf '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"ses_deliverable","update":{"sessionUpdate":"tool_call","toolCallId":"tc-1","name":"read_file","status":"pending"}}}\n'
      else
        printf '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"ses_deliverable","update":{"sessionUpdate":"tool_call","toolCallId":"tc-1","name":"read_file","status":"pending","parameters":{"path":"diagram.svg"}}}}\n'
      fi
      printf '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"ses_deliverable","update":{"sessionUpdate":"tool_call_update","toolCallId":"tc-1","name":"read_file","status":"completed","output":"<svg />"}}}\n'
      if [ "$ACP_TOOL_TERMINATED" != "1" ]; then
        printf '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"ses_deliverable","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"The diagram shows "}}}}\n'
        printf '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"ses_deliverable","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"the service architecture."}}}}\n'
      fi
      printf '{"jsonrpc":"2.0","id":%s,"result":{"stopReason":"end_turn"}}\n' "$id"
      exit 0
      ;;
  esac
done
`
}

// runFakeACPTurn executes one turn against the fixture above and returns the
// result together with the text chunks that reached the transcript.
func runFakeACPTurn(t *testing.T, provider string, env map[string]string) (Result, []string) {
	t.Helper()

	fakePath := filepath.Join(t.TempDir(), provider)
	writeTestExecutable(t, fakePath, []byte(fakeACPDeliverableScript()))

	backend, err := New(provider, Config{
		ExecutablePath: fakePath,
		Logger:         slog.Default(),
		Env:            env,
	})
	if err != nil {
		t.Fatalf("new %s backend: %v", provider, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	session, err := backend.Execute(ctx, "inspect this attachment", ExecOptions{Timeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("execute %s: %v", provider, err)
	}

	var streamed []string
	messagesDone := make(chan struct{})
	go func() {
		defer close(messagesDone)
		for msg := range session.Messages {
			if msg.Type == MessageText {
				streamed = append(streamed, msg.Content)
			}
		}
	}()

	select {
	case result, ok := <-session.Result:
		if !ok {
			t.Fatalf("%s: result channel closed without a value", provider)
		}
		<-messagesDone
		return result, streamed
	case <-time.After(20 * time.Second):
		t.Fatalf("%s: timeout waiting for result", provider)
		return Result{}, nil
	}
}

// Interim narration belongs to the transcript, not to Result.Output — the
// contract Result.Output documents in agent.go (GH #6006).
func TestACPBackendsDeliverFinalAnswerOnly(t *testing.T) {
	t.Parallel()

	const narration = "I will inspect the attachment."
	const want = "The diagram shows the service architecture."

	for _, provider := range acpDeliverableProviders {
		for _, tool := range []struct {
			name string
			env  map[string]string
		}{
			// hermesClient emits MessageToolUse at tool start when the frame
			// carries rawInput, and defers it to completion when it does not.
			// Both orderings must put the boundary before the final answer.
			{name: "tool use emitted at start", env: map[string]string{"ACP_NARRATION": narration}},
			{name: "tool use deferred until completion", env: map[string]string{"ACP_NARRATION": narration, "ACP_DEFERRED_TOOL": "1"}},
		} {
			t.Run(provider+"/"+tool.name, func(t *testing.T) {
				t.Parallel()

				result, streamed := runFakeACPTurn(t, provider, tool.env)

				if result.Status != "completed" {
					t.Fatalf("status = %q, want completed (error=%q)", result.Status, result.Error)
				}
				if result.Output != want {
					t.Errorf("Result.Output = %q, want %q", result.Output, want)
				}
				if got := strings.Join(streamed, "|"); got != narration+"|The diagram shows |the service architecture." {
					t.Errorf("transcript text = %q, want narration and final answer", got)
				}
			})
		}
	}
}

// A turn whose last event is a tool call has no trailing text block. Without
// the fallback Result.Output is empty, and the daemon treats an empty output as
// a valid tool-only completion — so the reply silently disappears.
func TestACPBackendsToolTerminatedTurnFallsBackToLatestTextBlock(t *testing.T) {
	t.Parallel()

	const want = "Done. Posted the summary to the issue."

	for _, provider := range acpDeliverableProviders {
		t.Run(provider, func(t *testing.T) {
			t.Parallel()

			result, streamed := runFakeACPTurn(t, provider, map[string]string{
				"ACP_NARRATION":       want,
				"ACP_TOOL_TERMINATED": "1",
			})

			if result.Status != "completed" {
				t.Fatalf("status = %q, want completed (error=%q)", result.Status, result.Error)
			}
			if result.Output != want {
				t.Errorf("Result.Output = %q, want fallback %q", result.Output, want)
			}
			if got := strings.Join(streamed, "|"); got != want {
				t.Errorf("transcript text = %q, want the single text chunk", got)
			}
		})
	}
}
