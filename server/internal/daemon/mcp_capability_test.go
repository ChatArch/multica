package daemon

import (
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

// The server refuses to hand a strictly-scoped MCP task to a daemon that does
// not advertise this capability, so a daemon that enforces the semantics but
// forgets to advertise them would silently stop running those tasks
// (GitHub #6283).
func TestDaemonAdvertisesAuthoritativeMcpCapability(t *testing.T) {
	caps := strings.Split(daemonClientCapabilities(), ",")
	for _, c := range caps {
		if c == protocol.DaemonCapabilityAuthoritativeMcpV1 {
			return
		}
	}
	t.Fatalf("daemon must advertise %q; got %v", protocol.DaemonCapabilityAuthoritativeMcpV1, caps)
}
