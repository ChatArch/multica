package agent

import (
	"strings"
	"sync"
	"testing"
)

func acpText(s string) Message { return Message{Type: MessageText, Content: s} }
func acpToolUse(name string) Message {
	return Message{Type: MessageToolUse, Tool: name, CallID: "tc-" + name}
}

func TestACPDeliverableTracker(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name            string
		stream          []Message
		wantDeliverable string
		wantComplete    string
	}{
		{
			name:            "no tool calls delivers the whole turn",
			stream:          []Message{acpText("The answer "), acpText("is 42.")},
			wantDeliverable: "The answer is 42.",
			wantComplete:    "The answer is 42.",
		},
		{
			name: "narration before a tool call is dropped from the deliverable",
			stream: []Message{
				acpText("I will inspect the attachment."),
				acpToolUse("read_file"),
				acpText("The diagram shows "),
				acpText("the service architecture."),
			},
			wantDeliverable: "The diagram shows the service architecture.",
			wantComplete:    "I will inspect the attachment.The diagram shows the service architecture.",
		},
		{
			// The shape that made replies silently empty: an agent told to
			// deliver through a CLI ends its turn on that tool call.
			name: "turn ending on a tool call falls back to the last block",
			stream: []Message{
				acpText("Done. Posted the summary to the issue."),
				acpToolUse("bash"),
			},
			wantDeliverable: "Done. Posted the summary to the issue.",
			wantComplete:    "Done. Posted the summary to the issue.",
		},
		{
			name: "the most recent non-blank block wins the fallback",
			stream: []Message{
				acpText("First I will read the file."),
				acpToolUse("read_file"),
				acpText("Now I will post the result."),
				acpToolUse("bash"),
			},
			wantDeliverable: "Now I will post the result.",
			wantComplete:    "First I will read the file.Now I will post the result.",
		},
		{
			// A blank trailing block must not shadow a real earlier answer.
			name: "a whitespace-only block does not become the fallback",
			stream: []Message{
				acpText("The migration is safe to apply."),
				acpToolUse("read_file"),
				acpText("\n  \n"),
				acpToolUse("bash"),
			},
			wantDeliverable: "The migration is safe to apply.",
			wantComplete:    "The migration is safe to apply.\n  \n",
		},
		{
			name: "thinking and tool results neither deliver nor end a block",
			stream: []Message{
				{Type: MessageThinking, Content: "let me think"},
				acpText("The answer "),
				{Type: MessageToolResult, CallID: "tc-1", Output: "some output"},
				{Type: MessageStatus, Status: "running"},
				acpText("is 42."),
			},
			wantDeliverable: "The answer is 42.",
			wantComplete:    "The answer is 42.",
		},
		{
			// promoteACPResultOnProviderError must still see the synthetic
			// give-up turn even though a tool boundary excluded it from the
			// deliverable.
			name: "a provider error before a tool boundary stays in the complete text",
			stream: []Message{
				acpText("API call failed after 3 retries: HTTP 429"),
				acpToolUse("read_file"),
				acpText("Recovered and finished."),
			},
			wantDeliverable: "Recovered and finished.",
			wantComplete:    "API call failed after 3 retries: HTTP 429Recovered and finished.",
		},
		{
			name:            "a turn with no text at all delivers nothing",
			stream:          []Message{acpToolUse("bash"), {Type: MessageToolResult, CallID: "tc-bash"}},
			wantDeliverable: "",
			wantComplete:    "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var tracker acpDeliverableTracker
			for _, msg := range tc.stream {
				tracker.observe(msg)
			}

			deliverable, complete := tracker.result()
			if deliverable != tc.wantDeliverable {
				t.Errorf("deliverable = %q, want %q", deliverable, tc.wantDeliverable)
			}
			if complete != tc.wantComplete {
				t.Errorf("complete = %q, want %q", complete, tc.wantComplete)
			}
		})
	}
}

// The streaming callback and the goroutine assembling Result run separately, so
// observe and result must not race. Meaningful under -race.
func TestACPDeliverableTrackerConcurrentAccess(t *testing.T) {
	t.Parallel()

	var tracker acpDeliverableTracker
	var wg sync.WaitGroup

	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			tracker.observe(acpText("chunk"))
			tracker.observe(acpToolUse("bash"))
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			tracker.result()
		}
	}()
	wg.Wait()

	deliverable, complete := tracker.result()
	if deliverable != "chunk" {
		t.Errorf("deliverable = %q, want the last completed block", deliverable)
	}
	if want := strings.Repeat("chunk", 200); complete != want {
		t.Errorf("complete length = %d, want %d", len(complete), len(want))
	}
}
