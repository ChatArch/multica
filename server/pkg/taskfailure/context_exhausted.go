package taskfailure

import "strings"

// TerminalReasonPromptTooLong is the value Claude Code writes into the
// stream-json result event's `terminal_reason` when the turn ended because the
// request no longer fits the model's context window — either the conversation
// was already over the limit on resume, or auto-compaction ran and could not
// get it back under.
//
// It is a structured enum value, not prose: the CLI carries it alongside
// image_error / model_error / max_turns / completed and friends, and it is
// emitted on the SUCCESS-subtype result frame. That matters because the frame's
// `is_error` flag answers a different question — the CLI derives it from
// whether the LAST message it rendered was an API-error message, while
// terminal_reason reports why the turn ended. The two are independent, and
// GH #6402 is what happens when only is_error is consulted: on Claude Code
// 2.1.220 a context-exhausted resume reported a clean success, so the platform
// published the CLI's "your context is full" copy as the agent's answer and
// kept the dead session pinned as the resume pointer.
//
// Verified against Claude Code 2.1.221 by resuming a saturated transcript
// against an endpoint returning the provider's 400 prompt-too-long shape. The
// captured terminal frame:
//
//	{"type":"result","subtype":"success","is_error":true,
//	 "terminal_reason":"prompt_too_long","api_error_status":400,
//	 "result":"Prompt is too long"}
//
// preceded by system/status frames reporting compact_result=failed,
// compact_error=exhausted.
const TerminalReasonPromptTooLong = "prompt_too_long"

// contextExhaustedOutputMaxLen caps how long a reported-successful output can
// be and still be re-read as a context-exhaustion notice. Every wording below
// is a terse one-liner the CLI emits INSTEAD of an answer; an agent that
// actually did the work and then discussed context limits writes far more than
// this. Same rationale and same value as poisonedOutputMaxLen in
// internal/daemon — a miss costs the pre-fix behaviour, a false positive turns
// a real result into a failure and retires a healthy session.
const contextExhaustedOutputMaxLen = 320

// ContextExhaustedCompletion reports whether an output the agent runtime
// reported as a SUCCESSFUL final answer is really the provider telling us the
// session's context window is full.
//
// This is the text-side counterpart to TerminalReasonPromptTooLong, and it
// exists for the two places the structured field is not available:
//
//   - Backends whose CLI has no equivalent structured field.
//   - The server's /complete boundary, where the caller may be an installed
//     daemon too old to carry the structured check. Daemons upgrade on their
//     own cadence, so a daemon-only fix reaches nobody until every host
//     updates — and one un-upgraded host means a permanently stuck (agent,
//     issue) pair, not just a mislabelled row (same argument as
//     NormalizeDaemonReason, MUL-5370).
//
// Matching is deliberately composite. The GitHub report paraphrased the CLI as
// "context too long, please run /compact", and neither half of that is safe on
// its own: "/compact" appears in ordinary Claude Code advice and "context too
// long" is a phrase an agent can legitimately write about its own run. Each
// clause below therefore pins a full, distinctive wording taken from the
// Claude Code binary itself rather than from the report:
//
//	"Prompt is too long"                                                    (bare, emitted when compaction fails)
//	"Prompt is too long · … A single-exchange conversation cannot be compacted; …"   (3 variants, all carrying "cannot be compacted")
//	"Conversation too long. Press esc twice to go up a few messages and try again."
//	"Compaction failed · conversation could not be reduced below the context limit"
//
// The bare form is matched by full-string equality, not substring, so a real
// answer that happens to quote the phrase cannot trip it.
func ContextExhaustedCompletion(output string) bool {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" || len(trimmed) > contextExhaustedOutputMaxLen {
		return false
	}
	lowered := strings.ToLower(trimmed)
	switch {
	case lowered == "prompt is too long":
		return true
	case strings.Contains(lowered, "prompt is too long") &&
		strings.Contains(lowered, "cannot be compacted"):
		return true
	case strings.Contains(lowered, "conversation too long") &&
		strings.Contains(lowered, "press esc twice"):
		return true
	case strings.Contains(lowered, "compaction failed") &&
		strings.Contains(lowered, "reduced below the context limit"):
		return true
	}
	return false
}
