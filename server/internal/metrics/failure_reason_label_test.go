package metrics

import (
	"testing"

	"github.com/multica-ai/multica/server/pkg/taskfailure"
)

// NormalizeFailureReason falls back to free-text Classify() for any value that
// is not in taskfailure.AllReasons(), which silently relabels an unregistered
// platform-side reason as `agent_error.unknown` — the metric then blames the
// agent for a platform refusal, and the series is never pre-warmed.
//
// Pinning the WHOLE canonical set (rather than one reason) is deliberate: it
// makes forgetting to register the next new Reason a test failure instead of a
// monitoring mystery. This was a real miss for
// `mcp_config_daemon_outdated` (GitHub #6283).
func TestNormalizeFailureReasonKeepsEveryCanonicalReason(t *testing.T) {
	for _, reason := range taskfailure.AllReasons() {
		wire := reason.String()
		if got := NormalizeFailureReason(wire); got != wire {
			t.Errorf("NormalizeFailureReason(%q) = %q; every canonical reason must survive as itself (missing from allReasons?)", wire, got)
		}
	}
}

// The claim-time MCP refusal is platform-side; a regression here would put it
// back in the agent-error bucket.
func TestNormalizeFailureReasonMcpConfigDaemonOutdated(t *testing.T) {
	wire := taskfailure.ReasonMcpConfigDaemonOutdated.String()
	if got := NormalizeFailureReason(wire); got != wire {
		t.Fatalf("NormalizeFailureReason(%q) = %q, want it preserved", wire, got)
	}
	if taskfailure.ReasonMcpConfigDaemonOutdated.IsAgentError() {
		t.Fatal("the MCP daemon-upgrade refusal must classify as platform-side, not agent error")
	}
}

// Unknown values still degrade rather than leaking unbounded label cardinality.
func TestNormalizeFailureReasonUnknownStillDegrades(t *testing.T) {
	if got := NormalizeFailureReason("something-nobody-registered"); got == "something-nobody-registered" {
		t.Fatal("an unregistered value must not become its own label")
	}
}
