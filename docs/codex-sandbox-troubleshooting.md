# Codex sandbox troubleshooting (macOS `no such host`)

This doc explains the failure mode that caused [MUL-963][mul-963] and the
matrix the daemon now follows when writing Codex's per-task `config.toml`.

[mul-963]: https://multica-api.copilothub.ai/issues/28c34ad2-102a-4f46-91ac-336ed78c5859

## Symptom fingerprint

| Error text                                                    | Likely cause                                                                    |
| ------------------------------------------------------------- | ------------------------------------------------------------------------------- |
| `dial tcp: lookup HOST: no such host`                         | **Codex Seatbelt sandbox blocking DNS** (macOS, `workspace-write` mode). |
| `dial tcp IP:PORT: connect: connection refused`               | Server/daemon not running on that port (app-level, not sandbox).                |
| `dial tcp IP:PORT: i/o timeout`                               | Container-level network policy or firewall (not Codex sandbox).                 |
| `x509: certificate signed by unknown authority`               | TLS/CA issue, unrelated.                                                        |

If you see `no such host` *inside a Codex session on macOS* but `curl https://multica-api.copilothub.ai` from a plain shell on the same machine works, you are hitting the Seatbelt bug below.

## Root cause

Upstream issue: [openai/codex#10390][codex-10390]. On macOS, Codex's Seatbelt
profile for `sandbox_mode = "workspace-write"` silently ignores the
`[sandbox_workspace_write] network_access = true` setting. The seatbelt
policy hard-codes `CODEX_SANDBOX_NETWORK_DISABLED=1`, which blocks DNS/UDP
syscalls. Go's `net.LookupHost` surfaces that as `no such host`.

Linux (Landlock) is **not** affected by this upstream bug — only macOS
Seatbelt. Linux nevertheless runs `danger-full-access` for a separate,
deliberate reason; see the decision matrix below.

[codex-10390]: https://github.com/openai/codex/issues/10390

## What the daemon does now

The daemon writes a *multica-managed* block into each task's
`$CODEX_HOME/config.toml`, delimited by `# BEGIN multica-managed` /
`# END multica-managed` markers. Anything outside the markers is left
untouched so users can still tune Codex behavior.

Decision matrix (see [`server/internal/daemon/execenv/codex_sandbox.go`](../server/internal/daemon/execenv/codex_sandbox.go)):

| Host OS   | Codex version                                    | Managed block emits                                                       |
| --------- | ------------------------------------------------ | ------------------------------------------------------------------------- |
| windows   | any                                              | `sandbox_mode = "danger-full-access"` — compatibility fallback (MUL-4957)  |
| linux / other non-darwin | any                               | `sandbox_mode = "danger-full-access"` (MUL-5578)                          |
| darwin    | ≥ `CodexDarwinNetworkAccessFixedVersion`         | `sandbox_mode = "workspace-write"` + `sandbox_workspace_write.network_access = true` (dotted-key form) |
| darwin    | older / unknown (current default)                | `sandbox_mode = "danger-full-access"` + warn-level log                     |

Only the two darwin rows are about the Seatbelt bug. The other two are
independent product decisions:

- **Linux (MUL-5578 / #6218):** tasks run with the daemon user's real `HOME`
  and full filesystem access. Enforcing `workspace-write` meant redirecting
  `HOME`/XDG per task and hand-maintaining a credential-path allowlist, which
  left every un-listed host CLI (`aws`, `kubectl`, `gcloud`, `glab`, `rclone`, …)
  unconfigured inside tasks. It also bought less than it looked: `workspace-write`
  restricts *writes* only, so credentials under the real `HOME` stayed readable
  either way. The task filesystem boundary is therefore the boundary the daemon
  itself runs inside — a VM, container, or dedicated Unix user.
- **Windows (MUL-4957):** Codex ships a native Windows sandbox
  (`windows.sandbox = "unelevated" | "elevated"`) but it is still experimental,
  so the daemon does not enable it by default. Without it Codex cannot enforce
  `workspace-write` on Windows — it downgrades to read-only and then rejects
  non-safe mutation commands "by policy", so e.g. `multica issue create` fails.
  A user who *has* opted into `windows.sandbox` keeps `workspace-write`, and an
  undecidable config fails closed; that logic lives in
  `codexSandboxPolicyForConfig` / `resolveWindowsSandboxState`.

The managed block is always hoisted to the top of `config.toml` and uses
TOML dotted-key syntax rather than a `[sandbox_workspace_write]` section
header. Both are load-bearing: if the block sat after a user table like
`[permissions.multica]`, a bare `sandbox_mode = "..."` line would be parsed
as `permissions.multica.sandbox_mode` and Codex would silently ignore it.

`CodexDarwinNetworkAccessFixedVersion` is an empty string today, meaning *no
known fixed release yet*. Bump it once a tagged Codex release includes the
upstream fix.

When the daemon falls back to `danger-full-access`, it logs at `WARN`:

```
codex sandbox: falling back to danger-full-access on macOS
  reason=codex on macOS: seatbelt ignores sandbox_workspace_write.network_access (openai/codex#10390) ...
  codex_version=0.121.0
  hint=upgrade Codex CLI (e.g. `brew upgrade codex` or `npm i -g @openai/codex`) ...
  config_path=/.../codex-home/config.toml
```

## Quick self-check commands

From the host shell (outside the sandbox):

```bash
# Is the Multica API reachable at all?
curl -sSf https://multica-api.copilothub.ai/healthz
```

From inside a Codex session (after the daemon writes its config):

```bash
multica issue list --limit 1 --output json >/dev/null && echo OK
```

If the host curl works but the Codex-session call fails with `no such host`,
the sandbox is the culprit; confirm the daemon picked the right policy by
looking at the managed block in `$CODEX_HOME/config.toml`.

## Options and trade-offs

- **A. Domain-scoped `permissions` profile** (tight): when the upstream
  `network_access` fix is available, prefer writing a `permissions.multica`
  profile that allows only `multica-api.copilothub.ai` and
  `multica-static.copilothub.ai`. Keeps filesystem sandbox intact.
- **B. `danger-full-access`** (current default on every platform except a
  fixed-version macOS): drops the whole Seatbelt profile. On macOS this is the
  simplest reliable workaround until the upstream fix is released; on
  Linux/Windows it is the deliberate baseline described above.
- **C. Upgrade Codex CLI**: `brew upgrade codex` or `npm i -g @openai/codex`.
  Once a release containing [openai/codex#10390][codex-10390] is installed,
  bump `CodexDarwinNetworkAccessFixedVersion` in `codex_sandbox.go` and
  option A/the workspace-write path takes over automatically.

## If you need to hand-verify

```bash
# Inspect the managed block the daemon wrote for a given task.
sed -n '/# BEGIN multica-managed/,/# END multica-managed/p' \
  ~/multica_workspaces/$WORKSPACE_ID/$TASK_SHORT/codex-home/config.toml
```

The block is idempotent — re-running a task rewrites it in place.
