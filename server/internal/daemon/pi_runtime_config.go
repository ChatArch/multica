package daemon

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"regexp"
	"strings"

	"github.com/multica-ai/multica/server/internal/daemon/execenv"
)

var piProviderNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

var supportedPiAPIs = map[string]struct{}{
	"openai-completions":   {},
	"openai-responses":     {},
	"anthropic-messages":   {},
	"google-generative-ai": {},
}

type piRuntimeConfigWire struct {
	Provider string `json:"provider"`
	API      string `json:"api"`
	BaseURL  string `json:"base_url"`
	Model    string `json:"model"`
}

// decodePiRuntimeConfig validates the non-secret Pi model configuration. A
// malformed or partial blob fails soft: it is ignored and Pi keeps its native
// configuration, matching the behavior of the other provider-specific runtime
// config decoders.
func decodePiRuntimeConfig(raw json.RawMessage, logger *slog.Logger) (execenv.PiRuntimeConfig, bool) {
	if len(raw) == 0 || string(raw) == "null" || string(raw) == "{}" {
		return execenv.PiRuntimeConfig{}, false
	}
	var wire piRuntimeConfigWire
	if err := json.Unmarshal(raw, &wire); err != nil {
		logger.Warn("pi runtime_config: invalid JSON; using native Pi configuration", "error", err)
		return execenv.PiRuntimeConfig{}, false
	}
	cfg := execenv.PiRuntimeConfig{
		Provider: strings.TrimSpace(wire.Provider),
		API:      strings.TrimSpace(wire.API),
		BaseURL:  strings.TrimSpace(wire.BaseURL),
		Model:    strings.TrimSpace(wire.Model),
	}
	if err := validatePiRuntimeConfig(cfg); err != nil {
		logger.Warn("pi runtime_config: invalid; using native Pi configuration", "error", err)
		return execenv.PiRuntimeConfig{}, false
	}
	return cfg, true
}

func validatePiRuntimeConfig(cfg execenv.PiRuntimeConfig) error {
	if cfg.Provider == "" || cfg.API == "" || cfg.BaseURL == "" || cfg.Model == "" {
		return fmt.Errorf("provider, api, base_url, and model are all required")
	}
	if !piProviderNamePattern.MatchString(cfg.Provider) {
		return fmt.Errorf("provider has an invalid format")
	}
	if _, ok := supportedPiAPIs[cfg.API]; !ok {
		return fmt.Errorf("unsupported api %q", cfg.API)
	}
	u, err := url.ParseRequestURI(cfg.BaseURL)
	if err != nil || u.Host == "" {
		return fmt.Errorf("base_url must be an absolute URL")
	}
	if u.User != nil {
		return fmt.Errorf("base_url must not contain credentials")
	}
	host := u.Hostname()
	if u.Scheme != "https" && !(u.Scheme == "http" && isLoopbackHost(host)) {
		return fmt.Errorf("base_url must use HTTPS (HTTP is allowed only for loopback)")
	}
	return nil
}

func isLoopbackHost(host string) bool {
	ip := net.ParseIP(host)
	return strings.EqualFold(host, "localhost") || (ip != nil && ip.IsLoopback())
}
