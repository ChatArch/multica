package agent

import (
	"strings"
	"sync"
)

// acpDeliverableTracker splits an ACP agent's text stream into the two strings
// a finished turn needs, because they are not the same string.
//
//   - Result.Output is "the final user-facing output selected by the backend"
//     (agent.go). It reaches channel replies and synthesized issue comments, so
//     it must hold the deliverable only — not the narration an agent emits
//     between tool calls (GH #6006).
//   - promoteACPResultOnProviderError needs the COMPLETE text, because the
//     synthetic "API call failed after N retries..." turn an adapter injects on
//     give-up can land before a tool boundary and would otherwise be invisible
//     to terminal-error detection.
//
// ACP gives no explicit final-answer marker the way Codex's app-server does
// (see codexDeliverableOutput), so the boundary is the ordered stream itself:
// consecutive MessageText chunks form the current block and a MessageToolUse
// starts a new one. That is a heuristic — replace it with an explicit marker if
// one of these protocols ever exposes a `phase: final_answer` equivalent.
//
// A turn whose last event is a tool call is common rather than exotic: an agent
// told to deliver its result through a CLI ends on that tool call, and whether
// it adds a closing sentence afterwards is up to the model. Such a turn has an
// empty trailing block, so the tracker falls back to the most recent non-empty
// one — matching codexDeliverableOutput's fallback to the latest agent message.
// Without that fallback the reply silently arrives empty, since the daemon
// treats an empty Output as a valid tool-only completion.
//
// The zero value is ready to use. All methods are safe for concurrent use: the
// streaming callback and the goroutine that assembles Result run separately.
type acpDeliverableTracker struct {
	mu sync.Mutex
	// complete is every text chunk of the turn, for provider-error detection.
	complete strings.Builder
	// block is the text since the most recent tool call — the deliverable
	// unless the turn ended on a tool call.
	block strings.Builder
	// lastBlock is the most recent non-blank completed block, used as the
	// fallback for a tool-terminated turn.
	lastBlock string
}

// observe folds one streamed message into the tracker. Non-text, non-tool-use
// messages (thinking, tool results, status) are ignored: they never belong to
// the deliverable and never end a text block.
func (t *acpDeliverableTracker) observe(msg Message) {
	t.mu.Lock()
	defer t.mu.Unlock()

	switch msg.Type {
	case MessageText:
		t.complete.WriteString(msg.Content)
		t.block.WriteString(msg.Content)
	case MessageToolUse:
		// Keep the block only if it carried real text; a blank one must not
		// shadow a genuine earlier answer.
		if block := t.block.String(); strings.TrimSpace(block) != "" {
			t.lastBlock = block
		}
		t.block.Reset()
	}
}

// result returns the deliverable for Result.Output and the complete text for
// promoteACPResultOnProviderError. Both are sampled under a single lock so they
// cannot disagree about where the stream ended.
func (t *acpDeliverableTracker) result() (deliverable, complete string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	deliverable = t.block.String()
	if strings.TrimSpace(deliverable) == "" {
		deliverable = t.lastBlock
	}
	return deliverable, t.complete.String()
}
