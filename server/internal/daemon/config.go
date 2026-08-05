package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/mattn/go-shellwords"
	"golang.org/x/sync/singleflight"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/pkg/agent"
)

const (
	DefaultServerURL         = "ws://localhost:8080/ws"
	DefaultPollInterval      = 30 * time.Second
	DefaultHeartbeatInterval = 15 * time.Second
	// DefaultAgentTimeout is the optional absolute wall-clock cap on a single
	// agent run. 0 = no cap: a run is bounded only by the inactivity watchdogs
	// (DefaultAgentIdleWatchdog / DefaultAgentToolWatchdog), so a session that keeps emitting events is
	// never killed merely for running long (MUL-3064). Operators who want a
	// hard ceiling for cost/resource control can set MULTICA_AGENT_TIMEOUT.
	DefaultAgentTimeout                   = 0
	DefaultCodexSemanticInactivityTimeout = 10 * time.Minute
	DefaultCodexHandshakeTimeout          = 30 * time.Second
	// DefaultOpenCodeIdleWatchdog shortens the no-message budget for OpenCode
	// runs while they are not executing a tool. OpenCode streams text and tool
	// events incrementally, so a completely silent interval here covers both a
	// missing first model token and a stalled response stream. The generic
	// AgentIdleWatchdog remains the global enable/disable switch.
	DefaultOpenCodeIdleWatchdog = 10 * time.Minute
	// DefaultAgentIdleWatchdog is the per-task safety net that force-stops a
	// run when the backend has emitted no message for this long AND its
	// message queue is empty. Backends like Claude Code can hang indefinitely
	// on a stuck child process (e.g. `docker ps` against a frozen dockerd),
	// in which case `cmd.Wait()` never returns. With no wall-clock cap
	// (DefaultAgentTimeout = 0) such a run would otherwise sit at "running"
	// forever, so this watchdog is its sole liveness net. The previous 5 min default
	// killed legitimate long assistant outputs (e.g. RFC-length writeups)
	// where the model streams a single message for many minutes without any
	// daemon-visible activity — see MUL-2300. 30 min keeps the safety net for
	// truly stuck runs (dockerd hang) while leaving headroom for long writes.
	// Set MULTICA_AGENT_IDLE_WATCHDOG=0 to disable.
	DefaultAgentIdleWatchdog = 30 * time.Minute
	// DefaultAgentToolWatchdog bounds how long a single tool call may stay in
	// flight (tool_use emitted, no tool_result and no other message) before the
	// idle watchdog force-stops the run. The idle watchdog ignores its normal
	// window while a tool is in flight, because a real build/install/test
	// legitimately runs silently for many minutes — but with no wall-clock cap
	// (DefaultAgentTimeout = 0) a backend that emits tool_use and never the
	// matching tool_result would otherwise run forever. This is the backstop for
	// that stuck-tool case (MUL-3064). Set MULTICA_AGENT_TOOL_WATCHDOG=0 to
	// disable, in which case an in-flight tool never force-stops the run.
	DefaultAgentToolWatchdog              = 2 * time.Hour
	DefaultRuntimeName                    = "Local Agent"
	DefaultWorkspaceBootstrapSyncInterval = 30 * time.Second
	DefaultWorkspaceLegacySyncInterval    = 5 * time.Minute
	DefaultWorkspaceSyncInterval          = 30 * time.Minute
	DefaultWorkspaceSyncMaxBackoff        = 30 * time.Minute
	DefaultHealthPort                     = 19514
	DefaultMaxConcurrentTasks             = 20
	DefaultGCInterval                     = 2 * time.Hour
	DefaultGCTTL                          = 24 * time.Hour      // 1 day — AI-coding issues rarely stay open long
	DefaultGCOrphanTTL                    = 72 * time.Hour      // 3 days — orphans with no meta (crashes, pre-GC leftovers)
	DefaultGCArtifactTTL                  = 12 * time.Hour      // 12h — drop regenerable artifacts on completed but still-open issues
	DefaultGCCodexSessionTTL              = 14 * 24 * time.Hour // 14 days — reclaim per-issue Codex session stores untouched this long
	DefaultAutoUpdateCheckInterval        = 6 * time.Hour       // how often the daemon polls GitHub for a newer CLI release
)

// DefaultGCArtifactPatterns lists basename matches that the GC loop treats as
// regenerable build artifacts. Kept conservative: only directories that are
// always cheap to recreate (`pnpm install`, `next build`, `turbo build`). Things
// like `dist/`, `build/`, `.cache/` or `.venv/` may legitimately hold source or
// release output in some repos and are NOT included by default — set
// MULTICA_GC_ARTIFACT_PATTERNS to extend the list per deployment.
var DefaultGCArtifactPatterns = []string{"node_modules", ".next", ".turbo"}

// Config holds all daemon configuration.
type Config struct {
	ServerBaseURL                  string
	DaemonID                       string
	LegacyDaemonIDs                []string // historical daemon_ids this machine may have registered under; reported at register time so the server can merge old runtime rows
	DeviceName                     string
	RuntimeName                    string
	CLIVersion                     string                // multica CLI version (e.g. "0.1.13")
	LaunchedBy                     string                // "desktop" when spawned by the Electron app, empty for standalone
	Profile                        string                // profile name (empty = default)
	Agents                         map[string]AgentEntry // keyed by provider: claude, codebuddy, codex, copilot, opencode, openclaw, hermes, pi, cursor, kimi, kiro, antigravity, qoder, qoderclicn, traecli, grok, qwen
	WorkspacesRoot                 string                // base path for execution envs (default: ~/multica_workspaces)
	KeepEnvAfterTask               bool                  // preserve env after task for debugging
	HealthPort                     int                   // local HTTP port for health checks (default: 19514)
	MaxConcurrentTasks             int                   // max tasks running in parallel (default: 20)
	GCEnabled                      bool                  // enable periodic workspace garbage collection (default: true)
	GCInterval                     time.Duration         // how often the GC loop runs (default: 2h)
	GCTTL                          time.Duration         // clean dirs whose issue is done/cancelled and updated_at < now()-TTL (default: 24h)
	GCOrphanTTL                    time.Duration         // clean orphan dirs with no meta, or dirs whose issue gc-check returns 404, once they exceed this age (default: 72h). The 404 path uses the same TTL — a scoped-down token can't instantly wipe live workspaces.
	GCArtifactTTL                  time.Duration         // when a task has been completed for at least this long but its issue is still open, drop regenerable artifacts (default: 12h, set 0 to disable)
	GCArtifactPatterns             []string              // basename patterns whose subtrees are removed during artifact cleanup (default: node_modules, .next, .turbo)
	GCCodexSessionTTL              time.Duration         // reclaim a per-issue Codex session store (~/.codex/multica-sessions/<agent>/<issue>) untouched for at least this long, so a done/abandoned issue's conversation history does not accumulate forever (default: 14d, set 0 to disable)
	AutoUpdateEnabled              bool                  // periodically check for a newer CLI release and self-update when idle (default: true on Multica Cloud, false on self-host)
	AutoUpdateCheckInterval        time.Duration         // how often the auto-update loop polls for a new release (default: 6h)
	PollInterval                   time.Duration
	HeartbeatInterval              time.Duration
	AgentTimeout                   time.Duration
	CodexSemanticInactivityTimeout time.Duration
	CodexHandshakeTimeout          time.Duration
	OpenCodeIdleWatchdog           time.Duration // OpenCode-specific no-message window; 0 falls back to AgentIdleWatchdog and values above it cannot extend the global bound
	AgentIdleWatchdog              time.Duration // force-stop a run when the backend goes silent this long with an empty queue (0 = disabled)
	AgentToolWatchdog              time.Duration // force-stop a run when a single tool call stays in flight (silent) this long (0 = disabled); backstop for hung tools now that there is no wall-clock cap
	ClaudeArgs                     []string
	CodexArgs                      []string
	CodebuddyArgs                  []string
	QwenArgs                       []string

	// ProfileCommandOverrides maps a custom runtime profile_id -> the absolute
	// executable path to use for that profile on THIS machine (MUL-3284).
	// Sourced from the local CLI config (cli.CLIConfig.ProfileCommandOverrides),
	// written by `multica runtime profile set-path`. appendProfileRuntimes
	// prefers a matching, executable override over resolving the profile's
	// command_name on PATH. nil/empty means "always resolve via PATH".
	ProfileCommandOverrides map[string]string
}

// Overrides allows CLI flags to override environment variables and defaults.
// Zero values are ignored and the env/default value is used instead.
type Overrides struct {
	ServerURL         string
	WorkspacesRoot    string
	PollInterval      time.Duration
	HeartbeatInterval time.Duration
	// AgentTimeout is a pointer so an explicit `--agent-timeout 0` (no cap) is
	// distinguishable from "flag not passed". nil = use env/default.
	AgentTimeout                   *time.Duration
	CodexSemanticInactivityTimeout time.Duration
	CodexHandshakeTimeout          time.Duration
	MaxConcurrentTasks             int
	DaemonID                       string
	DeviceName                     string
	RuntimeName                    string
	Profile                        string // profile name (empty = default)
	HealthPort                     int    // health check port (0 = use default)
	// AllowNoAgents is reserved for read-only local configuration probes. Daemon
	// startup keeps the default false and still refuses to run with no agent CLI.
	AllowNoAgents bool
	// DisableAutoUpdate, when true, forces the auto-update poller off. There
	// is no symmetric "force on" override because the env/default already
	// resolves to enabled; the flag exists so users can opt out from the CLI.
	DisableAutoUpdate       bool
	AutoUpdateCheckInterval time.Duration // 0 = use env/default
}

// LoadConfig builds the daemon configuration from environment variables
// and optional CLI flag overrides.
func LoadConfig(overrides Overrides) (Config, error) {
	// Server URL: override > env > default
	rawServerURL := envOrDefault("MULTICA_SERVER_URL", DefaultServerURL)
	if overrides.ServerURL != "" {
		rawServerURL = overrides.ServerURL
	}
	serverBaseURL, err := NormalizeServerBaseURL(rawServerURL)
	if err != nil {
		return Config{}, err
	}

	// Apply backend overrides from the CLI config file (issue #3875).
	//
	// CLIConfig.Backends.OpenClaw lets users record "which OpenClaw on this
	// machine, and where its state lives" in a versioned, UI-editable file
	// instead of a launchctl env hack. We translate those fields into the
	// same env vars the rest of LoadConfig already honors:
	//
	//   - MULTICA_OPENCLAW_PATH: read by probe() via envOrDefault for the
	//     binary lookup; pre-existing path.
	//   - OPENCLAW_STATE_DIR:    OpenClaw's own env var; the daemon already
	//     forwards it to spawned children via mergeEnv (server/pkg/agent/...).
	//
	// Precedence is "env wins over config wins over default" — same shape
	// users already get with MULTICA_OPENCLAW_PATH today. We achieve it with
	// LookupEnv guards: if the user already exported the env var (in their
	// shell, via launchctl, or via the systemd unit), we leave it alone;
	// otherwise we Setenv from the config file. This keeps every downstream
	// consumer (probe, buildEnv, child processes) on the existing code path
	// without inventing a new plumbing channel.
	//
	// Errors loading CLIConfig are non-fatal: a missing or malformed config
	// file should not prevent daemon startup, since the daemon can still run
	// purely from env-var configuration. We log a warning and proceed with
	// no overrides.
	var profileCommandOverrides map[string]string
	if cliCfg, err := cli.LoadCLIConfigForProfile(overrides.Profile); err != nil {
		slog.Warn("could not load CLI config for backend overrides; proceeding without",
			"profile", overrides.Profile, "err", err)
	} else {
		if oc := openclawOverrideFrom(cliCfg); oc != nil {
			applyOpenclawOverride(oc)
		}
		// Per-machine custom-runtime command path overrides (MUL-3284).
		// Copy into our own map so later mutation of the loaded config can't
		// alias daemon state, and so an empty map normalizes to nil.
		if len(cliCfg.ProfileCommandOverrides) > 0 {
			profileCommandOverrides = make(map[string]string, len(cliCfg.ProfileCommandOverrides))
			for id, path := range cliCfg.ProfileCommandOverrides {
				if id == "" || strings.TrimSpace(path) == "" {
					continue
				}
				profileCommandOverrides[id] = path
			}
		}
	}

	// Discover installed agent CLIs. Extracted so the periodic workspace sync
	// can re-run the same discovery on a live daemon (MUL-5439).
	agents := probeAgentCLIs()
	if len(agents) == 0 && !overrides.AllowNoAgents {
		return Config{}, fmt.Errorf("no agent CLI found: install claude, codebuddy, codex, copilot, opencode, deveco, openclaw, hermes, pi, cursor-agent, kimi, kiro-cli, agy, qodercli, qoderclicn, traecli, grok, or qwen and ensure it is on PATH")
	}

	claudeArgs, err := shellArgsFromEnv("MULTICA_CLAUDE_ARGS")
	if err != nil {
		return Config{}, err
	}
	codexArgs, err := shellArgsFromEnv("MULTICA_CODEX_ARGS")
	if err != nil {
		return Config{}, err
	}
	codebuddyArgs, err := shellArgsFromEnv("MULTICA_CODEBUDDY_ARGS")
	if err != nil {
		return Config{}, err
	}
	qwenArgs, err := shellArgsFromEnv("MULTICA_QWEN_ARGS")
	if err != nil {
		return Config{}, err
	}

	// Host info
	host, err := os.Hostname()
	if err != nil || strings.TrimSpace(host) == "" {
		host = "local-machine"
	}

	// Durations: override > env > default
	pollInterval, err := durationFromEnv("MULTICA_DAEMON_POLL_INTERVAL", DefaultPollInterval)
	if err != nil {
		return Config{}, err
	}
	if overrides.PollInterval > 0 {
		pollInterval = overrides.PollInterval
	}

	heartbeatInterval, err := durationFromEnv("MULTICA_DAEMON_HEARTBEAT_INTERVAL", DefaultHeartbeatInterval)
	if err != nil {
		return Config{}, err
	}
	if overrides.HeartbeatInterval > 0 {
		heartbeatInterval = overrides.HeartbeatInterval
	}

	agentTimeout, err := durationFromEnv("MULTICA_AGENT_TIMEOUT", DefaultAgentTimeout)
	if err != nil {
		return Config{}, err
	}
	if overrides.AgentTimeout != nil {
		agentTimeout = *overrides.AgentTimeout
	}

	codexSemanticInactivityTimeout, err := durationFromEnv("MULTICA_CODEX_SEMANTIC_INACTIVITY_TIMEOUT", DefaultCodexSemanticInactivityTimeout)
	if err != nil {
		return Config{}, err
	}
	if overrides.CodexSemanticInactivityTimeout > 0 {
		codexSemanticInactivityTimeout = overrides.CodexSemanticInactivityTimeout
	}

	codexHandshakeTimeout, err := durationFromEnv("MULTICA_CODEX_HANDSHAKE_TIMEOUT", DefaultCodexHandshakeTimeout)
	if err != nil {
		return Config{}, err
	}
	if codexHandshakeTimeout <= 0 {
		codexHandshakeTimeout = DefaultCodexHandshakeTimeout
	}
	if overrides.CodexHandshakeTimeout > 0 {
		codexHandshakeTimeout = overrides.CodexHandshakeTimeout
	}

	// MULTICA_AGENT_IDLE_WATCHDOG=0 disables the per-task idle watchdog. We
	// route 0 through durationFromEnv so the operator can opt out without
	// patching the binary; any positive duration overrides DefaultAgentIdleWatchdog.
	agentIdleWatchdog, err := durationFromEnv("MULTICA_AGENT_IDLE_WATCHDOG", DefaultAgentIdleWatchdog)
	if err != nil {
		return Config{}, err
	}
	// MULTICA_OPENCODE_IDLE_WATCHDOG narrows the no-message window for
	// OpenCode's streamed model responses. Zero removes the provider-specific
	// override and falls back to MULTICA_AGENT_IDLE_WATCHDOG; positive values
	// cannot extend the global bound, and the global zero still disables the
	// whole mechanism.
	openCodeIdleWatchdog, err := durationFromEnv("MULTICA_OPENCODE_IDLE_WATCHDOG", DefaultOpenCodeIdleWatchdog)
	if err != nil {
		return Config{}, err
	}

	// MULTICA_AGENT_TOOL_WATCHDOG=0 disables the in-flight-tool backstop; any
	// positive duration overrides DefaultAgentToolWatchdog.
	agentToolWatchdog, err := durationFromEnv("MULTICA_AGENT_TOOL_WATCHDOG", DefaultAgentToolWatchdog)
	if err != nil {
		return Config{}, err
	}

	maxConcurrentTasks, err := intFromEnv("MULTICA_DAEMON_MAX_CONCURRENT_TASKS", DefaultMaxConcurrentTasks)
	if err != nil {
		return Config{}, err
	}
	if overrides.MaxConcurrentTasks > 0 {
		maxConcurrentTasks = overrides.MaxConcurrentTasks
	}

	// Profile
	profile := overrides.Profile

	// daemon_id resolution: override > env > persistent UUID on disk.
	// The persistent UUID is written once to `<profile-dir>/daemon.id` and
	// then reused forever so hostname drift (.local suffix, system rename,
	// mDNS state, profile switch) no longer mints a new runtime identity.
	// Callers may still pin a specific id via MULTICA_DAEMON_ID or the
	// override field (e.g. for tests or embedded environments).
	daemonID := strings.TrimSpace(os.Getenv("MULTICA_DAEMON_ID"))
	if overrides.DaemonID != "" {
		daemonID = overrides.DaemonID
	}
	if daemonID == "" {
		persisted, err := EnsureDaemonID(profile)
		if err != nil {
			return Config{}, fmt.Errorf("ensure daemon id: %w", err)
		}
		daemonID = persisted
	}
	// Historical daemon_ids derived from the current hostname/profile. The
	// server uses these at register time to merge any pre-UUID runtime rows
	// for this machine into the new UUID-keyed row and delete the stale ones.
	legacyDaemonIDs := LegacyDaemonIDs(host, profile)
	// Pre-change (#1220) daemon identity was stored per profile, which means
	// the same machine could end up with multiple leftover daemon.id files
	// — e.g. ~/.multica/daemon.id (default) plus ~/.multica/profiles/<x>/
	// daemon.id. Surface those UUIDs so the server can merge their runtime
	// rows into the canonical machine UUID. Fatal-free: a broken profiles
	// dir shouldn't block startup.
	if uuids, err := LegacyDaemonUUIDs(); err == nil {
		legacyDaemonIDs = append(legacyDaemonIDs, uuids...)
	}
	// Strip anything that collides with the resolved daemon_id (e.g. when
	// the user explicitly pins MULTICA_DAEMON_ID=<hostname>, or when the
	// canonical id was itself promoted from a pre-change profile file).
	legacyDaemonIDs = filterLegacyIDs(legacyDaemonIDs, daemonID)

	deviceName := envOrDefault("MULTICA_DAEMON_DEVICE_NAME", host)
	if overrides.DeviceName != "" {
		deviceName = overrides.DeviceName
	}

	runtimeName := envOrDefault("MULTICA_AGENT_RUNTIME_NAME", DefaultRuntimeName)
	if overrides.RuntimeName != "" {
		runtimeName = overrides.RuntimeName
	}

	// Workspaces root: override > env > default (~/multica_workspaces or ~/multica_workspaces_<profile>)
	workspacesRoot, err := ResolveWorkspacesRoot(profile, overrides.WorkspacesRoot)
	if err != nil {
		return Config{}, err
	}

	// Health port: override > default
	healthPort := DefaultHealthPort
	if overrides.HealthPort > 0 {
		healthPort = overrides.HealthPort
	}

	// Keep env after task: env > default (false)
	keepEnv := os.Getenv("MULTICA_KEEP_ENV_AFTER_TASK") == "true" || os.Getenv("MULTICA_KEEP_ENV_AFTER_TASK") == "1"

	// GC config: env > defaults
	gcEnabled := true
	if v := os.Getenv("MULTICA_GC_ENABLED"); v == "false" || v == "0" {
		gcEnabled = false
	}
	gcInterval, err := durationFromEnv("MULTICA_GC_INTERVAL", DefaultGCInterval)
	if err != nil {
		return Config{}, err
	}
	gcTTL, err := durationFromEnv("MULTICA_GC_TTL", DefaultGCTTL)
	if err != nil {
		return Config{}, err
	}
	gcOrphanTTL, err := durationFromEnv("MULTICA_GC_ORPHAN_TTL", DefaultGCOrphanTTL)
	if err != nil {
		return Config{}, err
	}
	gcArtifactTTL, err := durationFromEnv("MULTICA_GC_ARTIFACT_TTL", DefaultGCArtifactTTL)
	if err != nil {
		return Config{}, err
	}
	gcCodexSessionTTL, err := durationFromEnv("MULTICA_GC_CODEX_SESSION_TTL", DefaultGCCodexSessionTTL)
	if err != nil {
		return Config{}, err
	}
	gcArtifactPatterns := patternsFromEnv("MULTICA_GC_ARTIFACT_PATTERNS", DefaultGCArtifactPatterns)

	// Auto-update config: default -> env override -> CLI override.
	//
	// Default is opt-in on Multica Cloud (api.multica.ai) and opt-out for
	// self-hosted instances. Self-host operators frequently run a fork with
	// their own patches, and silently upgrading their daemon to an upstream
	// GitHub release would clobber that work; they also commonly stay on an
	// older server build, which a fresh CLI may no longer talk to. Keeping
	// auto-update off by default for self-host avoids both footguns (MUL-2381).
	// Operators on either side can flip the default with MULTICA_DAEMON_AUTO_UPDATE.
	autoUpdateEnabled := isOfficialCloudServer(serverBaseURL)
	if v := strings.TrimSpace(os.Getenv("MULTICA_DAEMON_AUTO_UPDATE")); v != "" {
		switch strings.ToLower(v) {
		case "false", "0", "no", "off":
			autoUpdateEnabled = false
		case "true", "1", "yes", "on":
			autoUpdateEnabled = true
		}
	}
	if overrides.DisableAutoUpdate {
		autoUpdateEnabled = false
	}
	autoUpdateInterval, err := durationFromEnv("MULTICA_DAEMON_AUTO_UPDATE_INTERVAL", DefaultAutoUpdateCheckInterval)
	if err != nil {
		return Config{}, err
	}
	if overrides.AutoUpdateCheckInterval > 0 {
		autoUpdateInterval = overrides.AutoUpdateCheckInterval
	}

	return Config{
		ServerBaseURL:                  serverBaseURL,
		DaemonID:                       daemonID,
		LegacyDaemonIDs:                legacyDaemonIDs,
		DeviceName:                     deviceName,
		RuntimeName:                    runtimeName,
		Profile:                        profile,
		Agents:                         agents,
		WorkspacesRoot:                 workspacesRoot,
		KeepEnvAfterTask:               keepEnv,
		GCEnabled:                      gcEnabled,
		GCInterval:                     gcInterval,
		GCTTL:                          gcTTL,
		GCOrphanTTL:                    gcOrphanTTL,
		GCArtifactTTL:                  gcArtifactTTL,
		GCArtifactPatterns:             gcArtifactPatterns,
		GCCodexSessionTTL:              gcCodexSessionTTL,
		AutoUpdateEnabled:              autoUpdateEnabled,
		AutoUpdateCheckInterval:        autoUpdateInterval,
		HealthPort:                     healthPort,
		MaxConcurrentTasks:             maxConcurrentTasks,
		PollInterval:                   pollInterval,
		HeartbeatInterval:              heartbeatInterval,
		AgentTimeout:                   agentTimeout,
		CodexSemanticInactivityTimeout: codexSemanticInactivityTimeout,
		CodexHandshakeTimeout:          codexHandshakeTimeout,
		OpenCodeIdleWatchdog:           openCodeIdleWatchdog,
		AgentIdleWatchdog:              agentIdleWatchdog,
		AgentToolWatchdog:              agentToolWatchdog,
		ClaudeArgs:                     claudeArgs,
		CodexArgs:                      codexArgs,
		CodebuddyArgs:                  codebuddyArgs,
		QwenArgs:                       qwenArgs,
		ProfileCommandOverrides:        profileCommandOverrides,
	}, nil
}

// officialCloudHost is the hostname of Multica's hosted cloud. It's the only
// origin we treat as "official" for the auto-update default — staging,
// preview, and any future *.multica.ai subdomains are deliberately excluded
// so they inherit the safer self-host default until explicitly opted in.
const officialCloudHost = "api.multica.ai"

// isOfficialCloudServer reports whether the resolved server base URL points
// at Multica's hosted cloud. Used to pick the auto-update default: cloud
// users run a server that publishes the matching CLI release, so opt-in
// self-update is safe; self-host users may run a fork or pin to an older
// server, so the default flips to off. Matching is host-only and
// case-insensitive — port and path are ignored.
func isOfficialCloudServer(baseURL string) bool {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Hostname(), officialCloudHost)
}

// NormalizeServerBaseURL converts a WebSocket or HTTP URL to a base HTTP URL.
func NormalizeServerBaseURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("invalid MULTICA_SERVER_URL: %w", err)
	}
	switch u.Scheme {
	case "ws":
		u.Scheme = "http"
	case "wss":
		u.Scheme = "https"
	case "http", "https":
	default:
		return "", fmt.Errorf("MULTICA_SERVER_URL must use ws, wss, http, or https")
	}
	if u.Path == "/ws" {
		u.Path = ""
	}
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""
	return strings.TrimRight(u.String(), "/"), nil
}

// ResolveWorkspacesRoot returns the absolute path that the daemon and CLI
// should treat as the workspaces root. Resolution order: explicit override >
// MULTICA_WORKSPACES_ROOT env > default ($HOME/multica_workspaces, or
// $HOME/multica_workspaces_<profile> for a named profile). Read-only callers
// (e.g. `multica daemon disk-usage`) use this directly so they pick the same
// directory the running daemon would have picked.
func ResolveWorkspacesRoot(profile, override string) (string, error) {
	root := strings.TrimSpace(os.Getenv("MULTICA_WORKSPACES_ROOT"))
	if override != "" {
		root = override
	}
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w (set MULTICA_WORKSPACES_ROOT to override)", err)
		}
		if profile != "" {
			root = filepath.Join(home, "multica_workspaces_"+profile)
		} else {
			root = filepath.Join(home, "multica_workspaces")
		}
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve absolute workspaces root: %w", err)
	}
	return abs, nil
}

// ArtifactPatternsFromEnv returns the configured artifact patternSet — the
// same list the GC loop consults when it runs the artifact-only cleanup. The
// disk-usage CLI uses this to make sure the "artifact size" it reports
// matches what the GC would actually reclaim.
func ArtifactPatternsFromEnv() []string {
	return patternsFromEnv("MULTICA_GC_ARTIFACT_PATTERNS", DefaultGCArtifactPatterns)
}

// patternsFromEnv reads a comma-separated list from env. Patterns containing
// path separators are silently dropped — the GC artifact cleanup only matches
// directory basenames, never paths, so a pattern like "foo/bar" is meaningless
// and accepting it would just be a footgun.
func patternsFromEnv(name string, defaults []string) []string {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		out := make([]string, len(defaults))
		copy(out, defaults)
		return out
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" || strings.ContainsAny(p, "/\\") {
			continue
		}
		out = append(out, p)
	}
	return out
}

func shellArgsFromEnv(name string) ([]string, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return nil, nil
	}
	args, err := shellwords.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid %s: %w", name, err)
	}
	return args, nil
}

// resolveAgentExecutablePath returns the concrete executable path the daemon
// should keep for an agent command. Bare command names are pinned to the path
// resolved during startup so later PATH changes cannot redirect task launches.
// When ~/.multica/hooks shadows a real agent binary, skip that hooks directory:
// previously generated hook wrappers can execute the same command name and
// recurse forever if the daemon records or launches the wrapper.
//
// The returned PATH entries must be prepended to the environment of BOTH the
// version probe and the task launch — see resolvedExecutable.
func resolveAgentExecutable(cmd string) (resolvedExecutable, error) {
	resolved, err := exec.LookPath(cmd)
	if err != nil {
		return resolvedExecutable{}, err
	}
	if strings.ContainsAny(cmd, "/\\") {
		// An operator-pinned absolute/relative path is taken literally: it is
		// not canonicalized today and must keep that contract.
		return resolvedExecutable{Path: resolved}, nil
	}
	if isInMulticaHooksDir(resolved) {
		if unshadowed, err := lookPathExcludingMulticaHooks(cmd); err == nil {
			return unshadowed, nil
		}
	}
	return canonicalExecutable(resolved), nil
}

// resolveAgentExecutablePath is the path-only form, for callers that do not
// launch the result (and for tests asserting the pinned path).
func resolveAgentExecutablePath(cmd string) (string, error) {
	resolved, err := resolveAgentExecutable(cmd)
	return resolved.Path, err
}

// agentExecutablePresent reports whether path currently resolves to a runnable
// executable, using the exact check the agent backends apply at launch
// (exec.LookPath). A pinned AgentEntry.Path that fails this has vanished from
// disk — typically because a version manager did an in-place upgrade and
// deleted the old versioned directory the path pointed into (MUL-4486).
func agentExecutablePresent(path string) bool {
	if path == "" {
		return false
	}
	_, err := exec.LookPath(path)
	return err == nil
}

// agentEntryLaunchable reports whether entry can be launched exactly as it
// stands: the executable resolves AND every directory its resolved environment
// depends on still exists.
//
// Checking only the executable was not enough. A Volta upgrade that prunes the
// old Node image leaves the CLI file in place, so a cached entry stayed
// "present" while its PATH pointed at a directory that no longer existed —
// every task then launched with a broken toolchain and nothing ever re-resolved.
// A rebind to a different Node is the same class of problem with nothing missing
// at all, which is why the resolution inputs are fingerprinted too.
func agentEntryLaunchable(entry AgentEntry) bool {
	if !agentExecutablePresent(entry.Path) {
		return false
	}
	if !execEnvDirsPresent(entry.Env) {
		return false
	}
	// Same reason as voltaResolutionUsable: a Volta rebind leaves every directory
	// in place, so only the config fingerprint reveals it.
	return resolutionStampValid(entry.EnvStampPath, entry.EnvStampHash)
}

// execEnvDirsPresent reports whether the PATH directories an ExecEnv depends on
// exist. NODE_PATH is advisory (Volta creates its shared dir lazily), so it is
// not required.
func execEnvDirsPresent(env agent.ExecEnv) bool {
	for name, dirs := range env.PrefixPaths {
		if name != "PATH" {
			continue
		}
		for _, dir := range dirs {
			if !dirExists(dir) {
				return false
			}
		}
	}
	return true
}

// reresolveAgentCommand re-runs the startup resolution for a single agent
// command name, returning the freshly resolved executable. It mirrors the
// probe() order in LoadConfig: exec.LookPath (with the ~/.multica/hooks
// exclusion preserved via resolveAgentExecutable) first, then the login
// shell fallback for a bare command name a GUI-launched daemon can't see on
// its own PATH. It is only called on the miss path — when a previously pinned
// path has disappeared — so the login-shell cost is paid rarely, never on a
// normal launch.
func reresolveAgentCommand(cmd string) (resolvedExecutable, bool) {
	if cmd == "" {
		return resolvedExecutable{}, false
	}
	if resolved, err := resolveAgentExecutable(cmd); err == nil {
		return resolved, true
	}
	// A bare command name the daemon's own PATH can't see: retry via the
	// user's login shell, exactly as the startup probe does for
	// fnm/nvm/native-installer prefixes. Absolute/relative overrides skip
	// this — an operator-pinned MULTICA_*_PATH that no longer exists should
	// stay a hard miss rather than silently resolve a different binary.
	if !strings.ContainsAny(cmd, "/\\") {
		if path, ok := resolveAgentsViaLoginShell([]string{cmd})[cmd]; ok {
			// Same reason as the probe's shell leg: the script preserves the
			// file name, so a Volta alias still needs the real resolution.
			return canonicalExecutable(path), true
		}
	}
	return resolvedExecutable{}, false
}

func lookPathExcludingMulticaHooks(cmd string) (resolvedExecutable, error) {
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" {
			dir = "."
		}
		if isMulticaHooksDir(dir) {
			continue
		}
		candidate := filepath.Join(dir, cmd)
		if isExecutableFile(candidate) {
			return canonicalExecutable(candidate), nil
		}
	}
	return resolvedExecutable{}, exec.ErrNotFound
}

func isInMulticaHooksDir(path string) bool {
	if path == "" {
		return false
	}
	return isMulticaHooksDir(filepath.Dir(path))
}

func isMulticaHooksDir(dir string) bool {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return false
	}
	return samePathDir(dir, filepath.Join(home, ".multica", "hooks"))
}

func samePathDir(a, b string) bool {
	absA, err := filepath.Abs(a)
	if err != nil {
		return false
	}
	absB, err := filepath.Abs(b)
	if err != nil {
		return false
	}
	absA = filepath.Clean(absA)
	absB = filepath.Clean(absB)
	if realA, err := filepath.EvalSymlinks(absA); err == nil {
		absA = realA
	}
	if realB, err := filepath.EvalSymlinks(absB); err == nil {
		absB = realB
	}
	return absA == absB
}

// resolvedExecutable is a pinned executable together with the environment it
// needs in order to run. The two travel as one result on purpose: a Volta-managed
// global package binary is typically a `#!/usr/bin/env node` script, and Volta
// supplies the Node platform bound to that package plus a NODE_PATH pointing at
// its shared global lib dir when it launches one (upstream
// volta-core/src/run/binary.rs). Pinning only the path would leave every consumer
// to find some node on its own — a different one per directory, or none at all
// under a GUI-stripped PATH — so the version the daemon gates on would not be
// produced under the environment the CLI actually runs in.
type resolvedExecutable struct {
	Path string
	Env  agent.ExecEnv
	// StampPath / StampHash fingerprint the file whose CONTENTS determined the
	// environment — Volta's bin config for this command. Existence checks alone
	// cannot see a rebind: `volta install pkg` against a newer Node rewrites the
	// config while the previous Node image often stays on disk for other tools, so
	// every directory still exists and a path-only validity check keeps serving the
	// superseded toolchain forever.
	StampPath string
	StampHash string
}

// voltaShimName is the basename of the single trampoline binary Volta installs
// and symlinks EVERY managed command to (claude, codex, pi, ...). Upstream names
// it "volta-shim[.exe]" (volta-layout/src/v1.rs, VoltaInstall.shim_executable),
// and on Unix each shim in $VOLTA_HOME/bin is a symlink to it
// (volta-core/src/shim.rs, unix create()).
const voltaShimName = "volta-shim"

var (
	// voltaResolveTimeout bounds a single `volta` invocation.
	voltaResolveTimeout = 5 * time.Second
	// voltaResolveWaitDelay is how long after the context fires we wait for a
	// wedged child's pipes to close. A hung shell can leave grandchildren
	// holding our stdout, and cmd.Output() blocks in Wait() until that pipe
	// closes, so the real worst case for one call is timeout + this.
	voltaResolveWaitDelay = time.Second
	// voltaResolveBudget caps the time one Volta install may cost inside a single
	// breaker window. Without it, N alias lookups against a wedged install each
	// paid the per-call timeout and the startup probe stalled for N × timeout.
	voltaResolveBudget = 8 * time.Second
	// voltaCommandMissCooldown is how long a single command's failure suppresses
	// retries of THAT command. Short, because such a miss is cheap to retest and a
	// user who installs the tool should be picked up promptly.
	voltaCommandMissCooldown = time.Minute
	// voltaFailureCooldown is how long an EXPENSIVE-failing install is left alone. It turns
	// "every command pays the timeout, every discovery tick" into "one command
	// pays it, once per cooldown". When it expires the budget resets too, so a
	// transient outage can never latch the breaker open permanently.
	voltaFailureCooldown = 5 * time.Minute
)

// canonicalExecutable resolves path to the concrete executable to pin plus the
// environment required to run it.
//
// Symlinks are collapsed so version-manager prefix dirs and other indirection
// settle onto one stable path we can pin.
//
// Volta needs a detour. It points every command it manages at one shared
// volta-shim and picks the tool from the name it was invoked as — upstream
// get_tool_name() takes file_name() of argv[0] (volta-core/src/run/mod.rs) — so
// resolving `~/.volta/bin/claude` to volta-shim throws away the only input that
// selects the tool. Upstream then refuses the call outright (get_executor:
// `Some("volta-shim") => Err(ErrorKind::RunShimDirectly)`) and volta-shim's main
// exits 126 for any Volta error, which is the symptom reported in #6183. Volta
// documents the same hazard from its own side in volta-cli/volta#579.
//
// So for a Volta alias we ask Volta itself for the concrete binary and pin that
// together with its execution context. Keeping the alias instead and letting the
// shim dispatch would break the {path, version} invariant the rest of the daemon
// relies on: the shim resolves per working directory, so the version verified at
// registration would not be the version a task executes.
//
// Failing to resolve is deliberate: we return the shim path, version detection
// fails as before, and the provider stays unregistered rather than being launched
// through a path whose environment we could not reproduce. MULTICA_*_PATH remains
// the manual override.
func canonicalExecutable(path string) resolvedExecutable {
	abs, err := filepath.Abs(path)
	if err != nil {
		return resolvedExecutable{Path: path}
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return resolvedExecutable{Path: abs}
	}
	if isVoltaShimPath(real) && !isVoltaShimPath(abs) {
		if resolved, ok := voltaResolve(abs, real, filepath.Base(abs)); ok {
			return resolved
		}
	}
	return resolvedExecutable{Path: real}
}

// canonicalExecutablePath is the path-only form of canonicalExecutable, for
// callers that pin a path without launching it.
func canonicalExecutablePath(path string) string {
	return canonicalExecutable(path).Path
}

// isVoltaShimPath reports whether path is Volta's shared shim. The accepted
// names are exact — an extension-insensitive match would also swallow
// neighbours like volta-shim.bak or volta-shim.wrapper, and this exception
// exists to be as narrow as possible.
func isVoltaShimPath(path string) bool {
	if path == "" {
		return false
	}
	switch filepath.Base(path) {
	case voltaShimName, voltaShimName + ".exe":
		return true
	}
	return false
}

// voltaHomeFromAlias derives $VOLTA_HOME from a shim alias path.
//
// This must not be read from the daemon's environment. A GUI-launched daemon does
// not inherit the user's VOLTA_HOME, and Volta locates all package data relative
// to it (falling back to ~/.volta), so an inherited-or-default value would make
// `volta` report on a different installation than the alias we actually found.
// The layout pins the answer instead: shims live in $VOLTA_HOME/bin
// (volta-layout: `"bin": shim_dir`), so the parent of the directory holding the
// alias is $VOLTA_HOME. Note this is the *home*, which for a Homebrew install is
// a different tree from where the volta binaries live.
//
// The symlink chain is walked one hop at a time rather than fully resolved,
// because only the link that points AT the shim is guaranteed to sit in
// $VOLTA_HOME/bin; an unrelated wrapper earlier in the chain would not.
func voltaHomeFromAlias(aliasPath string) (string, bool) {
	const maxHops = 16
	current := aliasPath
	for i := 0; i < maxHops; i++ {
		target, err := os.Readlink(current)
		if err != nil {
			return "", false
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(current), target)
		}
		if isVoltaShimPath(target) {
			shimDir := filepath.Dir(current)
			home := filepath.Dir(shimDir)
			if home == "" || home == shimDir {
				return "", false
			}
			return home, true
		}
		current = target
	}
	return "", false
}

// voltaBinConfig is the subset of $VOLTA_HOME/tools/user/bins/<name>.json the
// daemon needs. Volta records the platform a global package was installed
// against and uses exactly that when launching it (binary.rs
// DefaultBinary::from_config), so the *current default* toolchain is not a
// substitute: switching defaults must not change what a previously installed CLI
// runs under.
type voltaBinConfig struct {
	Platform struct {
		Node string  `json:"node"`
		Npm  *string `json:"npm"`
		Pnpm *string `json:"pnpm"`
		Yarn *string `json:"yarn"`
	} `json:"platform"`
}

// voltaPlatformDirs returns the PATH entries Volta puts in front for command, in
// Volta's own order.
//
// Upstream Image::bins() pushes npm, then pnpm, then yarn, and Node LAST —
// deliberately, "so that any custom version of npm will be earlier in the PATH"
// (volta-core/src/platform/image.rs). Reproducing only Node would leave an agent
// bound to a custom npm/pnpm/yarn unable to find it, or silently using the system
// one.
//
// The Node image is required: it is what makes an `#!/usr/bin/env node` package
// script runnable, and it anchors cache validity. A pinned package manager whose
// image is missing is skipped rather than failing the whole resolution — an absent
// directory on PATH has no effect on execution, and skipping it keeps validity
// checks honest about what actually has to exist.
func voltaPlatformDirs(voltaHome, command string) ([]string, string, bool) {
	configPath := voltaBinConfigPath(voltaHome, command)
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return nil, "", false
	}
	var cfg voltaBinConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, "", false
	}
	nodeVersion := strings.TrimSpace(cfg.Platform.Node)
	if nodeVersion == "" {
		return nil, "", false
	}

	var dirs []string
	for _, mgr := range []struct {
		name    string
		version *string
	}{
		{"npm", cfg.Platform.Npm},
		{"pnpm", cfg.Platform.Pnpm},
		{"yarn", cfg.Platform.Yarn},
	} {
		if mgr.version == nil || strings.TrimSpace(*mgr.version) == "" {
			continue
		}
		if dir, ok := voltaImageBinDir(voltaHome, mgr.name, strings.TrimSpace(*mgr.version)); ok {
			dirs = append(dirs, dir)
		}
	}
	nodeDir, ok := voltaImageBinDir(voltaHome, "node", nodeVersion)
	if !ok {
		return nil, "", false
	}
	return append(dirs, nodeDir), hashBytes(raw), true
}

// voltaBinConfigPath is where Volta records the platform bound to a command.
func voltaBinConfigPath(voltaHome, command string) string {
	return filepath.Join(voltaHome, "tools", "user", "bins", command+".json")
}

func hashBytes(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// resolutionStampValid reports whether the file that determined an environment
// still has the same contents. A missing stamp means "nothing to check".
func resolutionStampValid(stampPath, stampHash string) bool {
	if stampPath == "" {
		return true
	}
	raw, err := os.ReadFile(stampPath)
	if err != nil {
		return false
	}
	return hashBytes(raw) == stampHash
}

// voltaImageBinDir locates the executables for one toolchain image. Unix keeps
// them in <image>/bin; Windows puts them at the image root.
func voltaImageBinDir(voltaHome, tool, version string) (string, bool) {
	imageDir := filepath.Join(voltaHome, "tools", "image", tool, version)
	if binDir := filepath.Join(imageDir, "bin"); dirExists(binDir) {
		return binDir, true
	}
	if dirExists(imageDir) {
		return imageDir, true
	}
	return "", false
}

// voltaSharedLibDir is what Volta prefixes onto NODE_PATH so a global binary can
// `require` other global libs (binary.rs shared_module_path ->
// volta_home().shared_lib_root()).
func voltaSharedLibDir(voltaHome string) string {
	return filepath.Join(voltaHome, "tools", "shared")
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

type voltaInstallState struct {
	tools map[string]resolvedExecutable // command -> resolution
	// misses is a per-COMMAND negative cache. A command that simply is not
	// Volta-managed (no bin config) fails fast and cheaply, and must only stop
	// retries of ITSELF — it says nothing about the installation's health.
	misses map[string]time.Time
	// failureSpend is how much time failures have burned in the current window.
	// Only when it reaches voltaResolveBudget does the whole installation go into
	// cooldown, because that is the signal that querying Volta is EXPENSIVE (hung
	// or timing out), not merely unsuccessful.
	failureSpend time.Duration
	// installCooldownUntil suppresses every command for this installation. Set
	// only by an exhausted budget.
	installCooldownUntil time.Time
	// queryMu serializes this installation's queries so the budget is enforced per
	// installation rather than per command.
	queryMu sync.Mutex
}

var (
	voltaMu     sync.Mutex
	voltaStates = map[string]*voltaInstallState{}
	// voltaGroup coalesces identical concurrent resolutions.
	voltaGroup singleflight.Group
)

// voltaStateKey identifies one Volta *installation*. It must include the home: a
// Homebrew-style setup has several VOLTA_HOMEs sharing one volta-shim/volta pair,
// so keying on the binary alone would let home B serve home A's cached tool paths
// and environment. Symlinks are collapsed so two spellings of one home agree.
func voltaStateKey(voltaHome, voltaBin string) string {
	return canonicalDirKey(voltaHome) + "\x00" + canonicalDirKey(voltaBin)
}

func canonicalDirKey(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return filepath.Clean(path)
}

func voltaInstallStateFor(key string) *voltaInstallState {
	voltaMu.Lock()
	defer voltaMu.Unlock()
	state := voltaStates[key]
	if state == nil {
		state = &voltaInstallState{
			tools:  map[string]resolvedExecutable{},
			misses: map[string]time.Time{},
		}
		voltaStates[key] = state
	}
	return state
}

// voltaExecutable returns the `volta` binary that belongs to shimPath. Both are
// VoltaInstall entries, so they live in the same directory; we invoke it by
// absolute path because the daemon may not have Volta's bin dir on PATH.
func voltaExecutable(shimPath string) string {
	voltaBin := filepath.Join(filepath.Dir(shimPath), "volta")
	if runtime.GOOS == "windows" {
		voltaBin += ".exe"
	}
	return voltaBin
}

// voltaNonProjectDir is where `volta` is invoked. Volta resolves a *project*
// tool before the user default — upstream src/command/which.rs prefers
// session.project().find_bin() — by walking up from the working directory
// looking for a package.json. Inheriting the daemon's directory would therefore
// let a daemon started inside a JS project pin that project's node_modules
// binary as the machine-wide runtime. The filesystem root is the only directory
// with no ancestor that could hold a package.json.
func voltaNonProjectDir(voltaBin string) string {
	if runtime.GOOS == "windows" {
		if vol := filepath.VolumeName(voltaBin); vol != "" {
			return vol + string(os.PathSeparator)
		}
	}
	return string(os.PathSeparator)
}

// voltaResolve asks Volta for the concrete binary behind command and builds the
// execution context it needs. It returns false unless BOTH are available: a path
// without its environment is not reliably runnable, so a partial answer must
// never be cached or launched.
//
// Answers are revalidated by existence rather than a TTL: probeAgentCLIs re-runs
// on every discovery tick, and a steady-state daemon must not fork `volta`
// forever. When Volta replaces the binary or the Node image is removed, the entry
// is dropped and re-resolved, which dovetails with the MUL-4486 self-heal.
func voltaResolve(aliasPath, shimPath, command string) (resolvedExecutable, bool) {
	// command comes from a filesystem basename; keep it to the same bare-name
	// shape the shell resolver enforces so nothing flag-like reaches argv.
	if !isSafeAgentName(command) {
		return resolvedExecutable{}, false
	}
	voltaHome, ok := voltaHomeFromAlias(aliasPath)
	if !ok {
		return resolvedExecutable{}, false
	}
	voltaBin := voltaExecutable(shimPath)
	stateKey := voltaStateKey(voltaHome, voltaBin)
	state := voltaInstallStateFor(stateKey)

	if cached, ok := voltaCachedTool(state, command); ok {
		return cached, true
	}
	if !voltaBreakerAllows(state, command) {
		return resolvedExecutable{}, false
	}
	if !isExecutableFile(voltaBin) {
		voltaNoteFailure(state, command, 0)
		return resolvedExecutable{}, false
	}

	// Coalesce identical concurrent requests, then serialize per installation so
	// the budget below is an installation-wide bound rather than a per-command one.
	v, _, _ := voltaGroup.Do(stateKey+"\x00"+command, func() (any, error) {
		state.queryMu.Lock()
		defer state.queryMu.Unlock()

		// Re-check under the gate: a predecessor may have answered this command,
		// or burned the budget, while we waited.
		if cached, ok := voltaCachedTool(state, command); ok {
			return cached, nil
		}
		if !voltaBreakerAllows(state, command) {
			return resolvedExecutable{}, nil
		}

		started := time.Now()
		resolved, ok := voltaResolveUncached(voltaBin, voltaHome, command)
		if !ok {
			voltaNoteFailure(state, command, time.Since(started))
			return resolvedExecutable{}, nil
		}
		voltaNoteSuccess(state, command, resolved)
		return resolved, nil
	})
	resolved, _ := v.(resolvedExecutable)
	return resolved, resolved.Path != ""
}

// voltaCachedTool returns a cached resolution that is still runnable.
func voltaCachedTool(state *voltaInstallState, command string) (resolvedExecutable, bool) {
	voltaMu.Lock()
	defer voltaMu.Unlock()
	cached, ok := state.tools[command]
	if !ok {
		return resolvedExecutable{}, false
	}
	if voltaResolutionUsable(cached) {
		return cached, true
	}
	delete(state.tools, command)
	return resolvedExecutable{}, false
}

// voltaBreakerAllows reports whether command may be queried now.
//
// Two independent gates, deliberately:
//
//   - A per-command negative cache. Most failures are command-specific and cheap
//     (the command is simply not Volta-managed, so there is no bin config). Those
//     must not stop other commands: previously any failure set one shared flag and
//     a single fast miss froze every healthy command on the installation for the
//     full cooldown, which also made the spend budget unreachable dead code.
//   - An installation-wide cooldown, entered only when failures have actually
//     burned the time budget. That is the case worth suppressing globally, because
//     it means each further query costs a timeout.
func voltaBreakerAllows(state *voltaInstallState, command string) bool {
	voltaMu.Lock()
	defer voltaMu.Unlock()
	if !state.installCooldownUntil.IsZero() {
		if time.Now().Before(state.installCooldownUntil) {
			return false
		}
		// Cooldown served: start a fresh window.
		state.installCooldownUntil = time.Time{}
		state.failureSpend = 0
	}
	if last, ok := state.misses[command]; ok {
		if time.Since(last) < voltaCommandMissCooldown {
			return false
		}
		delete(state.misses, command)
	}
	return true
}

func voltaNoteFailure(state *voltaInstallState, command string, elapsed time.Duration) {
	voltaMu.Lock()
	defer voltaMu.Unlock()
	state.misses[command] = time.Now()
	state.failureSpend += elapsed
	if state.failureSpend >= voltaResolveBudget {
		// Expensive failures: suppress the whole installation for a cooldown so N
		// commands cannot each pay a timeout.
		state.installCooldownUntil = time.Now().Add(voltaFailureCooldown)
	}
}

// voltaNoteSuccess records a working resolution. A success proves the install is
// healthy, so it also clears the failure budget — and it deliberately does NOT
// add its own duration to it.
func voltaNoteSuccess(state *voltaInstallState, command string, resolved resolvedExecutable) {
	voltaMu.Lock()
	defer voltaMu.Unlock()
	delete(state.misses, command)
	state.failureSpend = 0
	state.installCooldownUntil = time.Time{}
	state.tools[command] = resolved
}

// voltaResolveUncached performs the actual queries. Both halves must succeed.
func voltaResolveUncached(voltaBin, voltaHome, command string) (resolvedExecutable, bool) {
	toolPath, ok := voltaWhich(voltaBin, voltaHome, command)
	if !ok {
		return resolvedExecutable{}, false
	}
	platformDirs, stampHash, ok := voltaPlatformDirs(voltaHome, command)
	if !ok {
		// Fail closed: without the bound platform we cannot reproduce the
		// environment Volta itself would use, so the version we verify would not
		// be the version that runs.
		return resolvedExecutable{}, false
	}
	return resolvedExecutable{
		Path: toolPath,
		Env: agent.ExecEnv{PrefixPaths: map[string][]string{
			"PATH":      platformDirs,
			"NODE_PATH": {voltaSharedLibDir(voltaHome)},
		}},
		StampPath: voltaBinConfigPath(voltaHome, command),
		StampHash: stampHash,
	}, true
}

// voltaResolutionUsable reports whether a cached resolution is still runnable —
// the tool AND every directory its environment depends on.
func voltaResolutionUsable(resolved resolvedExecutable) bool {
	if !isExecutableFile(resolved.Path) {
		return false
	}
	// A rebind rewrites the bin config; without this the old binding survives for
	// as long as its image happens to stay on disk.
	if !resolutionStampValid(resolved.StampPath, resolved.StampHash) {
		return false
	}
	for name, dirs := range resolved.Env.PrefixPaths {
		if name != "PATH" {
			// Only PATH entries have to exist to run; NODE_PATH is advisory and
			// its shared dir is created lazily by Volta.
			continue
		}
		for _, dir := range dirs {
			if !dirExists(dir) {
				return false
			}
		}
	}
	return true
}

// voltaWhich runs `volta which <command>` and returns the concrete path.
func voltaWhich(voltaBin, voltaHome, command string) (string, bool) {
	out, ok := runVolta(voltaBin, voltaHome, "which", command)
	if !ok {
		return "", false
	}
	// `volta which` prints nothing and exits non-zero when it cannot find the
	// binary, so anything that is not a single absolute runnable path is a miss
	// rather than something we should pin.
	if out == "" || strings.ContainsAny(out, "\r\n") || !filepath.IsAbs(out) {
		return "", false
	}
	if !isExecutableFile(out) {
		return "", false
	}
	return out, true
}

// runVolta invokes the Volta CLI and returns its trimmed stdout. VOLTA_HOME is
// set explicitly (never inherited) so the answer describes the installation the
// alias belongs to. A var so tests can exercise the caching/cooldown logic
// without a real subprocess, following the same seam pattern as
// resolveAgentsViaLoginShell and detectAgentVersion.
var runVolta = func(voltaBin, voltaHome string, args ...string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), voltaResolveTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, voltaBin, args...)
	cmd.Dir = voltaNonProjectDir(voltaBin)
	cmd.Env = envWithVoltaHome(os.Environ(), voltaHome)
	// Volta prints results on stdout and diagnostics on stderr; Output()
	// captures only stdout. WaitDelay keeps a hung child from outliving ctx.
	cmd.WaitDelay = voltaResolveWaitDelay
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(out)), true
}

// envWithVoltaHome returns environ with VOLTA_HOME replaced by home.
func envWithVoltaHome(environ []string, home string) []string {
	out := make([]string, 0, len(environ)+1)
	for _, kv := range environ {
		if name, _, ok := strings.Cut(kv, "="); ok && name == "VOLTA_HOME" {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "VOLTA_HOME="+home)
}

func isExecutableFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	return info.Mode()&0o111 != 0
}

// defaultAgentCommandNames lists the command names the agent probe loop tries
// before any MULTICA_*_PATH override is applied. Kept in sync with the
// `probe(...)` calls in LoadConfig — the shell-fallback resolver uses this
// list to pre-fetch canonical paths for every known agent in a single shell
// invocation, instead of paying the cost-per-miss.
var defaultAgentCommandNames = []string{
	"claude", "codex", "opencode", "deveco", "openclaw", "hermes",
	"pi", "cursor-agent", "copilot", "kimi", "kiro-cli", "codebuddy", "agy", "qodercli", "qoderclicn", "traecli", "grok", "qwen",
}

// codexDesktopAppBundlePaths returns candidate macOS app-bundle locations for
// the bundled Codex CLI. OpenAI relocated the Desktop app from Codex.app to
// ChatGPT.app (#5205). Candidates are ordered by install location first
// (system /Applications before user ~/Applications); within each location the
// new ChatGPT.app path is tried before the legacy Codex.app path, so updated
// installs win while older installs still resolve.
var codexDesktopAppBundlePaths = func() []string {
	paths := []string{
		"/Applications/ChatGPT.app/Contents/Resources/codex",
		"/Applications/Codex.app/Contents/Resources/codex",
	}
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths,
			filepath.Join(home, "Applications", "ChatGPT.app", "Contents", "Resources", "codex"),
			filepath.Join(home, "Applications", "Codex.app", "Contents", "Resources", "codex"),
		)
	}
	return paths
}

// loginShellResolveTimeout caps how long the daemon will wait for the user's
// login shell to print canonical agent paths. A broken rc file should not
// block startup — if the shell takes longer than this, we proceed without
// shell-resolved fallbacks and the daemon falls back to the same behaviour
// it had before this code was added.
const loginShellResolveTimeout = 3 * time.Second

// loginShellResolveWaitDelay is the hard cap that runs *after*
// loginShellResolveTimeout has elapsed and `CommandContext` has signalled the
// shell to exit. The context kills the shell process itself, but rc files in
// the wild routinely background things that inherit stdout (`nvm` shims,
// `direnv hook`, `eval $(starship init)`, plain `&`). Those survivors keep
// the stdout pipe open and `cmd.Output()` will block on EOF for as long as
// they live. Cmd.WaitDelay (Go 1.20+) forcibly closes the pipes and returns
// once this delay elapses, so the total daemon-startup penalty caused by a
// pathological rc file is bounded by `timeout + waitDelay`, not by however
// long the user's background processes happen to run.
const loginShellResolveWaitDelay = 2 * time.Second

// supportedLoginShells limits which interpreters we will invoke via
// `<shell> -ilc <script>`. Sticking to POSIX-compatible shells means the
// resolver script below works unchanged. Notably absent: fish (uses
// `command -s` and a different syntax for command substitution).
var supportedLoginShells = map[string]struct{}{
	"bash": {},
	"zsh":  {},
	"sh":   {},
	"dash": {},
	"ksh":  {},
}

// resolveAgentsViaLoginShell asks the user's login shell to print the canonical
// (symlink-resolved) absolute path to each name in `names`. It returns a map
// of name → path for whatever the shell could find, and an empty map if the
// shell is unavailable / unsupported / times out / produces no usable output.
//
// Why we need this:
//
// Daemon-style processes on macOS/Linux do not inherit the user's interactive
// PATH. `claude --version` working in Terminal.app is no guarantee that
// exec.LookPath("claude") will work from a binary spawned by Launchpad, the
// Electron app, or `launchctl`. The most common offenders are fnm/nvm/volta
// "multishell" prefix dirs (per-shell, ephemeral) and the Anthropic native
// installer (`~/.claude/local/`) — both leave their binaries on a path that
// only `.zshrc` knows about.
//
// Implementation notes:
//
//   - We invoke `$SHELL -ilc <script>` with both -i (interactive) and -l
//     (login) so we pick up PATH set in either ~/.zshrc / ~/.bashrc OR
//     ~/.zprofile / ~/.bash_profile. Real users put it in both places.
//   - The script resolves symlinks via `cd "$dirname" && pwd -P` while the
//     spawned shell is still alive. fnm/nvm "multishell" directories vanish
//     on shell exit, so the canonical path must be captured before stdout is
//     returned to Go — by then the original path is already gone.
//   - We only trust outputs that look like an absolute path AND still pass a
//     fresh exec.LookPath check from the daemon's vantage point. That filters
//     out aliases (`command -v` prints the alias definition for those, not a
//     path) and per-shell paths the shell happened not to fully canonicalise.
//   - Agent names are restricted to the bare set in defaultAgentCommandNames
//     (`[A-Za-z0-9._-]` only); we inline them into the script unquoted to
//     keep the script readable. Custom MULTICA_*_PATH values never reach this
//     resolver — those go through exec.LookPath directly.
//
// A var so tests can stub the fork without a real login shell.
var resolveAgentsViaLoginShell = func(names []string) map[string]string {
	out := map[string]string{}
	if len(names) == 0 {
		return out
	}
	shell := strings.TrimSpace(os.Getenv("SHELL"))
	if shell == "" {
		return out
	}
	if _, ok := supportedLoginShells[filepath.Base(shell)]; !ok {
		return out
	}

	safe := make([]string, 0, len(names))
	for _, n := range names {
		if isSafeAgentName(n) {
			safe = append(safe, n)
		}
	}
	if len(safe) == 0 {
		return out
	}

	ctx, cancel := context.WithTimeout(context.Background(), loginShellResolveTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, shell, "-ilc", buildLoginShellResolveScript(safe))
	cmd.WaitDelay = loginShellResolveWaitDelay
	raw, err := cmd.Output()
	if err != nil {
		return out
	}

	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			continue
		}
		name, path := parts[0], strings.TrimSpace(parts[1])
		if !filepath.IsAbs(path) {
			continue
		}
		// Final reality check: the path the shell gave us must still be
		// executable from the daemon's perspective right now. fnm
		// multishells are the motivating example — pwd -P inside the
		// helper shell can fail to break out of the per-session bin dir,
		// and we'd rather report "not found" than hand back a path that
		// vanishes between detection and execution.
		if _, err := exec.LookPath(path); err != nil {
			continue
		}
		out[name] = path
	}
	return out
}

// buildLoginShellResolveScript returns the shell script that resolveAgentsViaLoginShell
// runs inside `$SHELL -ilc`. The script:
//
//  1. iterates the provided command names,
//  2. strips any locally-defined alias and shell function with that name so
//     `command -v` reaches through to a real binary on PATH (see below),
//  3. uses POSIX `command -v` to find each one on the interactive PATH,
//  4. rejects results that are not absolute paths (defence in depth — if the
//     unalias/unset -f pair somehow didn't take effect, `command -v` would
//     still print the alias/function definition, and we'd rather drop it
//     than hand back garbage),
//  5. canonicalises the directory via `cd ... && pwd -P` so symlinked prefix
//     dirs (fnm/nvm/volta) collapse to stable paths,
//  6. if the resolved path lives in ~/.multica/hooks, searches the same
//     shell-expanded PATH for the first executable outside that hooks dir,
//  7. prints `<name>\t<canonical_path>` one entry per line for the caller.
//
// Why steps 2 is important — and why this PR's first revision missed #2512:
// the motivating case has `alias claude=...` in ~/.zshrc *and* fnm's real
// claude binary further down on PATH. With `-i` set, the alias loads, and
// `command -v claude` returns `claude: aliased to ...` (zsh) or `alias
// claude='...'` (bash) — neither starts with `/`, so step 4 drops them, and
// the loop never looks at PATH again. Unaliasing inside the same shell makes
// `command -v` fall back to the PATH search the daemon actually wants.
// Shell functions exhibit the same shadowing in bash/zsh, hence `unset -f`.
// Both calls are wrapped in `2>/dev/null` so the harmless "no such alias"
// error never reaches stderr.
//
// All input names are vetted by isSafeAgentName before they reach this
// function, so inlining them unquoted into the for-loop word list is safe.
func buildLoginShellResolveScript(names []string) string {
	var b strings.Builder
	b.WriteString("for n in")
	for _, n := range names {
		b.WriteByte(' ')
		b.WriteString(n)
	}
	b.WriteString("; do\n")
	b.WriteString("  unalias \"$n\" 2>/dev/null\n")
	b.WriteString("  unset -f \"$n\" 2>/dev/null\n")
	b.WriteString("  p=$(command -v \"$n\" 2>/dev/null) || continue\n")
	b.WriteString("  [ -n \"$p\" ] || continue\n")
	b.WriteString("  case \"$p\" in /*) ;; *) continue ;; esac\n")
	b.WriteString("  d=$(dirname \"$p\") && f=$(basename \"$p\") && c=$(cd \"$d\" 2>/dev/null && pwd -P) || continue\n")
	b.WriteString("  hc=\"\"\n")
	b.WriteString("  if [ -n \"${HOME:-}\" ]; then hd=\"$HOME/.multica/hooks\"; hc=$(cd \"$hd\" 2>/dev/null && pwd -P) || hc=\"\"; fi\n")
	b.WriteString("  if [ -n \"$hc\" ] && [ \"$c\" = \"$hc\" ]; then\n")
	b.WriteString("    oldIFS=$IFS; IFS=:\n")
	b.WriteString("    for d2 in $PATH; do\n")
	b.WriteString("      [ -n \"$d2\" ] || d2=.\n")
	b.WriteString("      c2=$(cd \"$d2\" 2>/dev/null && pwd -P) || continue\n")
	b.WriteString("      [ \"$c2\" = \"$hc\" ] && continue\n")
	b.WriteString("      if [ -f \"$c2/$n\" ] && [ -x \"$c2/$n\" ]; then c=\"$c2\"; f=\"$n\"; break; fi\n")
	b.WriteString("    done\n")
	b.WriteString("    IFS=$oldIFS\n")
	b.WriteString("  fi\n")
	b.WriteString("  printf '%s\\t%s\\n' \"$n\" \"$c/$f\"\n")
	b.WriteString("done\n")
	return b.String()
}

// isSafeAgentName checks that `s` is a bare command name composed only of
// characters that are safe to inline into a shell script (ASCII letters,
// digits, dot, dash, underscore). The agent names this daemon ships with all
// satisfy the predicate; it exists to guard against future drift, not to
// constrain operator-supplied paths (those never reach the shell resolver).
func isSafeAgentName(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == '.':
		default:
			return false
		}
	}
	return true
}

// openclawOverrideFrom returns the OpenClaw override block from a loaded
// CLIConfig, or nil when no override is configured. Centralized here so
// the LoadConfig path and tests share one navigation predicate over the
// nullable-pointer chain.
func openclawOverrideFrom(cfg cli.CLIConfig) *cli.OpenClawOverride {
	if cfg.Backends == nil {
		return nil
	}
	return cfg.Backends.OpenClaw
}

// applyOpenclawOverride translates the config-file overrides into process
// env vars, which the existing probe() / buildEnv code paths already honor.
// Env-set-by-user wins over config-set-by-file: we only Setenv when the var
// is not already present, preserving the back-compat contract documented
// on cli.OpenClawOverride.
//
// Side-effecting on os.Setenv is intentional and scoped:
//
//   - The two vars touched (MULTICA_OPENCLAW_PATH, OPENCLAW_STATE_DIR) are
//     OpenClaw-specific. Other backends do not read them; setting them in the
//     daemon process has no observable effect on, e.g., Claude Code or Codex
//     spawn behavior.
//   - LoadConfig runs once during daemon startup, before any backend Execute.
//     Concurrent reads of os.Environ() in spawned children see a stable view.
//   - We deliberately do not unset on later reload: the daemon's lifecycle is
//     "exit and respawn" (cmd_daemon.go), not in-process reconfigure.
func applyOpenclawOverride(oc *cli.OpenClawOverride) {
	if oc == nil {
		return
	}
	if oc.BinaryPath != "" {
		if _, set := os.LookupEnv("MULTICA_OPENCLAW_PATH"); !set {
			_ = os.Setenv("MULTICA_OPENCLAW_PATH", oc.BinaryPath)
		}
	}
	if oc.StateDir != "" {
		if _, set := os.LookupEnv("OPENCLAW_STATE_DIR"); !set {
			_ = os.Setenv("OPENCLAW_STATE_DIR", oc.StateDir)
		}
	}
}
