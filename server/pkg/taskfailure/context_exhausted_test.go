package taskfailure

import (
	"strings"
	"testing"
)

// TestContextExhaustedCompletion is the precision regression for GH #6402.
// The wordings marked "captured" were taken from the Claude Code binary /
// a live reproduction (resuming a saturated transcript against an endpoint
// returning the provider's 400 prompt-too-long shape), not from the issue
// report's paraphrase — the report said "a message like ...", which is exactly
// what a matcher must not be built on.
func TestContextExhaustedCompletion(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   bool
	}{
		// ── the real thing ──
		{
			name:   "captured: bare result when compaction is exhausted",
			output: "Prompt is too long",
			want:   true,
		},
		{
			name:   "captured: bare result with surrounding whitespace",
			output: "\n  Prompt is too long \n",
			want:   true,
		},
		{
			name:   "captured: single-exchange variant with token counts",
			output: "Prompt is too long · the request is ~274931 tokens (limit 200000) but this conversation is only ~1597 tokens — the rest is system prompt, tool definitions, and attachment content. A single-exchange conversation cannot be compacted; reduce attached files/tools or start with less context.",
			want:   true,
		},
		{
			name:   "captured: single-exchange variant without token counts",
			output: "Prompt is too long · this conversation is a single exchange and cannot be compacted — the request size comes mostly from system prompt, tool definitions, or attachments.",
			want:   true,
		},
		{
			name:   "captured: conversation-too-long thrown by the compaction path",
			output: "Conversation too long. Press esc twice to go up a few messages and try again.",
			want:   true,
		},
		{
			name:   "captured: compaction exhausted",
			output: "Compaction failed · conversation could not be reduced below the context limit",
			want:   true,
		},

		// ── near misses: each holds ONE half of a composite fingerprint ──
		{
			name: "the issue report's paraphrase is not a fingerprint",
			// GH #6402 wrote "a message like 'context too long, please run
			// /compact'". Neither half is safe alone, so the paraphrase itself
			// must not match — matching it would mean matching every agent
			// reply that suggests /compact.
			output: "context too long, please run /compact",
			want:   false,
		},
		{
			name:   "advice to compact is not a failure",
			output: "This session is getting big — run /compact before continuing.",
			want:   false,
		},
		{
			name:   "an agent quoting the phrase in a real answer",
			output: `Added a guard for the "Prompt is too long" case and pushed it.`,
			want:   false,
		},
		{
			name:   "a compaction failure with a different cause",
			output: "Compaction failed · attached media exceeds size limits",
			want:   false,
		},
		{
			name:   "empty output",
			output: "",
			want:   false,
		},
		{
			name:   "whitespace only",
			output: "   \n\t ",
			want:   false,
		},
		{
			name: "a long answer that discusses the exact wording",
			// The length cap is the backstop behind the composite match: a real
			// conclusion can legitimately contain every marker at once (this
			// review comment does), and it must still be delivered as a result.
			output: "I looked into why the agent stalled. Claude Code emits \"Prompt is too long\" when a single-exchange conversation cannot be compacted, and \"Conversation too long. Press esc twice to go up a few messages and try again.\" when the compaction path throws; the underlying failure is \"Compaction failed · conversation could not be reduced below the context limit\". I've added a classifier for all three and opened a PR — the daemon now retires the session instead of publishing the notice as an answer.",
			want:   false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ContextExhaustedCompletion(tc.output); got != tc.want {
				t.Errorf("ContextExhaustedCompletion(%q) = %v, want %v", tc.output, got, tc.want)
			}
		})
	}
}

// TestContextExhaustedCompletionLengthCap pins the boundary so a future edit
// to the cap has to be deliberate: the marker is identical on both sides and
// only the length decides.
func TestContextExhaustedCompletionLengthCap(t *testing.T) {
	base := "Prompt is too long · a single-exchange conversation cannot be compacted. "
	atCap := base + strings.Repeat("x", contextExhaustedOutputMaxLen-len(base))
	overCap := atCap + "x"

	if !ContextExhaustedCompletion(atCap) {
		t.Errorf("output of exactly %d bytes should classify", contextExhaustedOutputMaxLen)
	}
	if ContextExhaustedCompletion(overCap) {
		t.Errorf("output of %d bytes should be treated as a real answer", len(overCap))
	}
}

// TestTerminalReasonPromptTooLongClassifies pins the other half of the
// contract: the daemon quotes the structured terminal reason into the error it
// reports, and Classify must land that on the reason the resume blacklist
// covers. Without this the run would fail as agent_error.unknown — labelled
// wrong AND resume-safe by omission, which is the stuck (agent, issue) pair
// GH #6402 describes.
func TestTerminalReasonPromptTooLongClassifies(t *testing.T) {
	errMsg := "claude ended the turn with terminal_reason=" + TerminalReasonPromptTooLong +
		": the session's context window is exhausted and compaction could not recover it"
	if got := Classify(errMsg); got != ReasonAgentContextOverflow {
		t.Errorf("Classify(%q) = %q, want %q", errMsg, got, ReasonAgentContextOverflow)
	}
	// And with no provider prose attached at all.
	bare := "terminal_reason=" + TerminalReasonPromptTooLong
	if got := Classify(bare); got != ReasonAgentContextOverflow {
		t.Errorf("Classify(%q) = %q, want %q", bare, got, ReasonAgentContextOverflow)
	}
}

// TestNormalizeDaemonReasonUpgradesPromptTooLong: an older daemon that carries
// the structured check but not the taxonomy entry reports the catchall. The
// witness in the error text is unambiguous enough to upgrade it server-side,
// which is what retires the session without waiting for that host to update.
func TestNormalizeDaemonReasonUpgradesPromptTooLong(t *testing.T) {
	raw := "claude ended the turn with terminal_reason=" + TerminalReasonPromptTooLong + ": ..."
	if got := NormalizeDaemonReason(string(ReasonAgentUnknown), raw); got != ReasonAgentContextOverflow {
		t.Errorf("NormalizeDaemonReason = %q, want %q", got, ReasonAgentContextOverflow)
	}
	// A refined reason says more about what ended the run than a witness
	// appearing somewhere in the same blob, so it is left alone.
	if got := NormalizeDaemonReason(string(ReasonAgentProcessFailure), raw); got != ReasonAgentProcessFailure {
		t.Errorf("NormalizeDaemonReason overwrote a refined reason: got %q", got)
	}
}
