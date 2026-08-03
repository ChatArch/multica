package agent

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
)

// isWindows is split out so the env-name casing rule is testable as a value.
var isWindows = runtime.GOOS == "windows"

// ExecEnv is extra environment an agent CLI needs in order to run, produced by
// the same resolution step that produced its path.
//
// It exists because a pinned path is not always self-sufficient. A Volta-managed
// global package binary is typically a `#!/usr/bin/env node` script, and Volta
// supplies both the Node platform bound to that package and a NODE_PATH pointing
// at its shared global lib directory when it launches one (upstream
// volta-core/src/run/binary.rs: the DefaultBinary command carries the bound
// platform's PATH and `command.env("NODE_PATH", shared_module_path())`).
// Reproducing only the path would run the CLI under whatever Node the ambient
// environment happens to offer — or none at all.
//
// Every consumer that executes an agent CLI must apply the same ExecEnv, so the
// version the daemon gates on is produced under the environment the CLI actually
// runs in. The zero value is a no-op.
type ExecEnv struct {
	// PrefixPaths maps an environment variable name (PATH, NODE_PATH) to
	// directories prefixed onto its existing list-shaped value, highest
	// priority first.
	PrefixPaths map[string][]string
}

// IsZero reports whether e would change nothing.
func (e ExecEnv) IsZero() bool {
	for _, dirs := range e.PrefixPaths {
		if len(dirs) > 0 {
			return false
		}
	}
	return true
}

// sortedNames returns the variable names in deterministic order.
func (e ExecEnv) sortedNames() []string {
	names := make([]string, 0, len(e.PrefixPaths))
	for name, dirs := range e.PrefixPaths {
		if len(dirs) > 0 {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// CacheKey is a stable fingerprint, so a cache keyed on an executable also
// distinguishes the environment it is executed under.
func (e ExecEnv) CacheKey() string {
	if e.IsZero() {
		return ""
	}
	var b strings.Builder
	for _, name := range e.sortedNames() {
		b.WriteString(name)
		b.WriteByte('=')
		b.WriteString(strings.Join(e.PrefixPaths[name], string(os.PathListSeparator)))
		b.WriteByte(';')
	}
	return b.String()
}

// ApplyToEnviron returns environ with every prefix applied. environ is not
// modified.
func (e ExecEnv) ApplyToEnviron(environ []string) []string {
	if e.IsZero() {
		return environ
	}
	current := make(map[string]string, len(environ))
	order := make([]string, 0, len(environ))
	for _, kv := range environ {
		name, value, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		key := envKey(name)
		if _, seen := current[key]; !seen {
			order = append(order, name)
		}
		current[key] = value
	}
	// Track the original casing so an existing PATH/Path entry is replaced in
	// place rather than duplicated (Windows is case-insensitive here).
	nameFor := map[string]string{}
	for _, name := range order {
		nameFor[envKey(name)] = name
	}

	for _, name := range e.sortedNames() {
		key := envKey(name)
		joined := strings.Join(e.PrefixPaths[name], string(os.PathListSeparator))
		if existing := current[key]; existing != "" {
			current[key] = joined + string(os.PathListSeparator) + existing
		} else {
			current[key] = joined
		}
		if _, ok := nameFor[key]; !ok {
			nameFor[key] = name
			order = append(order, name)
		}
	}

	out := make([]string, 0, len(order))
	for _, name := range order {
		key := envKey(name)
		out = append(out, nameFor[key]+"="+current[key])
	}
	return out
}

// ApplyToMap applies every prefix to a name->value map, falling back to the
// process environment for a variable the map does not carry yet. Used for task
// environments, which are assembled as maps.
func (e ExecEnv) ApplyToMap(env map[string]string) {
	if e.IsZero() || env == nil {
		return
	}
	for _, name := range e.sortedNames() {
		joined := strings.Join(e.PrefixPaths[name], string(os.PathListSeparator))
		existing, ok := env[name]
		if !ok || existing == "" {
			existing = os.Getenv(name)
		}
		if existing == "" {
			env[name] = joined
			continue
		}
		env[name] = joined + string(os.PathListSeparator) + existing
	}
}

// command builds an exec.Cmd for path with this environment applied.
func (e ExecEnv) command(ctx context.Context, path string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, path, args...)
	if !e.IsZero() {
		cmd.Env = e.ApplyToEnviron(os.Environ())
	}
	return cmd
}

// envKey normalizes an environment variable name for lookup. Case-insensitive on
// Windows, exact elsewhere.
func envKey(name string) string {
	if isWindows {
		return strings.ToUpper(name)
	}
	return name
}
