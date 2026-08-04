package daemon

import (
	"encoding/json"
	"log/slog"

	"github.com/multica-ai/multica/server/internal/piagent"
)

// decodePiRuntimeConfig validates the non-secret Pi model configuration. A
// malformed or partial blob fails soft: it is ignored and Pi keeps its native
// configuration, matching the behavior of the other provider-specific runtime
// config decoders.
func decodePiRuntimeConfig(raw json.RawMessage, logger *slog.Logger) (piagent.Config, bool) {
	cfg, configured, err := piagent.Decode(raw)
	if err != nil {
		logger.Warn("pi runtime_config: invalid; using native Pi configuration", "error", err)
		return piagent.Config{}, false
	}
	return cfg, configured
}
