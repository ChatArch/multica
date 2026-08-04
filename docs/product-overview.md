# Multica Product Overview

> **About this document**
>
> The goal: **let anyone who has never written code fully understand, within 30 minutes, what features Multica has, where each one sits in the whole, and how one feature works with another.**
>
> The audience:
>
> - **New engineers, PMs, designers, and operators** — the first onboarding material
> - **Product storytelling** — the factual basis for explaining Multica externally
> - **Writers** — anyone writing UI copy, marketing copy, or help docs who needs to know what a term ("Skill", "Runtime", "Autopilot") means inside the product
> - **Anyone who needs to understand how a part relates to the whole before changing it**
>
> What it is **not**: developer documentation, an architecture decision record, or a sales script. It is **a summary of factual behaviour** — every statement maps to code, a schema, or an API.
>
> The document was generated from a systematic survey of the monorepo (server, apps, packages, migrations, daemon, CLI) with a data cutoff of 2026-04-21. Treat specifics as of that date unless noted. The provider list, the onboarding step sequence, and the table counts have been corrected since.
>
> 中文版本：[`product-overview.zh.md`](./product-overview.zh.md)。

---

## Contents

1. [What Multica is](#1-what-multica-is)
2. [Core concept dictionary](#2-core-concept-dictionary)
3. [Feature tour by module](#3-feature-tour-by-module)
   - 3.1 [Workspace](#31-workspace)
   - 3.2 [Issues](#32-issues)
   - 3.3 [Projects](#33-projects)
   - 3.4 [Agents](#34-agents)
   - 3.5 [Runtimes and the daemon](#35-runtimes-and-the-daemon)
   - 3.6 [Skills](#36-skills)
   - 3.7 [Autopilots](#37-autopilots)
   - 3.8 [Chat](#38-chat)
   - 3.9 [Inbox and notifications](#39-inbox-and-notifications)
   - 3.10 [Members, invitations, permissions](#310-members-invitations-permissions)
   - 3.11 [Search and the command palette](#311-search-and-the-command-palette)
   - 3.12 [Auth, login, onboarding](#312-auth-login-onboarding)
   - 3.13 [Settings and profile](#313-settings-and-profile)
   - 3.14 [The CLI](#314-the-cli)
4. [System architecture](#4-system-architecture)
5. [Product map: every route](#5-product-map-every-route)
6. [Web vs desktop](#6-web-vs-desktop)
7. [Appendix: key tables](#7-appendix-key-tables)

---

## 1. What Multica is

### One line

**Multica turns coding agents into real team members.**

Assign an issue to an agent the way you would assign it to a colleague. It picks the work up, writes the code, reports progress, and updates status — without you watching over it.

### The problem it solves

Pain points of using an AI coding agent the traditional way:

- Copy-pasting a prompt every single time
- Having to watch the terminal to see whether it finished
- No memory across tasks — every run starts from zero
- When several agents work at once, there is no single board showing the whole picture

What Multica does:

- Agents and people **share one task board** (the issue board)
- Agents **have profiles** — they appear in the assignee dropdown, speak in comments, and create issues of their own
- Multiple rounds on the same (agent, issue) pair **resume the session automatically** — prior context and working directory are preserved
- The **skill system** turns problems you have already solved into reusable capability
- **Autopilots** let agents start work on a schedule (bug triage every morning at 9, say)

### The positioning, in one sentence

> Multica is not an AI tool. It is a **task-management platform for humans and AI working together**, where agents are first-class citizens inside the same workflow as people.

### Deployment shapes

- **Cloud (Multica Cloud)**: officially hosted; agents execute through a daemon running on your own machine
- **Self-hosted**: the full backend can run on your own servers
- **Clients**: a Next.js web app and an Electron desktop app (near-identical experience; desktop adds multi-tab, a native tray, and auto-update)

### Supported coding agents

Multica **does not train models** and does not lock you to one vendor. It is a scheduler; the local daemon detects the following CLIs and wires them in:

Claude Code · Codex · CodeBuddy · GitHub Copilot CLI · OpenCode · OpenClaw · Hermes · Pi · Cursor Agent · Kimi · Kiro CLI · Antigravity · Qoder CLI · Trae CLI

Each agent can carry its own model, API key, environment variables, and MCP servers.

---

## 2. Core concept dictionary

**Understanding these terms is a prerequisite for understanding the product. Each definition maps strictly onto a database table.**

| Concept | Definition | Table |
|---|---|---|
| **User** | A human account. Can log in; belongs to multiple workspaces | `user` |
| **Workspace** | The container for everything. Issues, agents, projects, and skills are all isolated per workspace — the workspace/team concept from Linear or Notion | `workspace` |
| **Member** | A user's identity inside one workspace. The same user can hold different roles (owner/admin/member) in different workspaces | `member` |
| **Agent** | An AI worker that can be assigned work. Has a profile (name, avatar, description), a designated runtime and provider, and configurable prompt and skills | `agent` |
| **Runtime** | The **execution environment** an agent actually runs in — a user's local machine (via the daemon) or a cloud instance. **One runtime = one machine capable of running an agent** | `agent_runtime` |
| **Daemon** | A background process on the user's machine. Discovers installed coding CLIs, registers them as runtimes, then polls the server to claim work | (a process, not a table) |
| **Issue** | A unit of work — task, bug, feature. The central product object. Assignable to a person or an agent | `issue` |
| **Comment** | A reply under an issue. People and agents both post. `@`-mentioning an agent in a comment automatically triggers a new task for it | `comment` |
| **Task** | One run produced by an agent working an issue — essentially "one agent session". Executed through a queue | `agent_task_queue` |
| **Skill** | A reusable workspace-level document that gives an agent context on *how to do something*. On start-up the mounted skill content is injected into the working directory where the CLI can read it | `skill`, `skill_file`, `agent_skill` |
| **Project** | A higher-level home for issues, like a milestone or a release | `project` |
| **Autopilot** | A scheduled or triggered automation rule. Fires on cron or webhook, creates an issue, and assigns it to an agent | `autopilot`, `autopilot_trigger`, `autopilot_run` |
| **Chat** | A persistent multi-turn conversation between a user and an agent, not attached to an issue | `chat_session`, `chat_message` |
| **Inbox** | A personal notification centre. Mentions, assignments, and updates on subscribed issues land here | `inbox_item` |
| **Subscriber** | Who is following an issue. Being assigned, mentioned, or having commented subscribes you automatically. Subscribers receive inbox notifications | `issue_subscriber` |
| **Activity / timeline** | An audit record of every key action. The issue detail page's timeline is this table | `activity_log` |
| **Pin** | A personal sidebar shortcut that pins a frequently used issue or project | `pinned_item` |
| **Reaction** | An emoji reaction on an issue or comment, as in GitHub or Slack | `issue_reaction`, `comment_reaction` |
| **Attachment** | A file upload on an issue or comment; supports S3/CloudFront or local storage | `attachment` |
| **Personal access token (PAT)** | A user-level API token for the CLI and automation. `mul_` prefix | `personal_access_token` |
| **Daemon token** | A token scoped to one daemon in one workspace. `mdt_` prefix, narrower than a PAT | `daemon_token` |
| **Session resumption** | The next task for the same (agent, issue) pair reuses the previous CLI `session_id` and working directory, so conversation history and file state survive | `agent_task_queue.session_id`, `.work_dir` |
| **MCP (Model Context Protocol)** | Anthropic's protocol for letting an agent call external tools through a standard interface. Each agent can carry its own MCP server list | `agent.mcp_config` (JSONB) |
| **Workspace context** | A workspace-level system prompt for agents. Every agent in that workspace sees it | `workspace.context` |
| **Polymorphic actor** | The design pattern behind almost every "who did what" field: `actor_type` (`member`/`agent`) plus `actor_id`. It is why an agent can create issues, post comments, and be subscribed exactly like a person | pervasive |

---

## 3. Feature tour by module

### 3.1 Workspace

> **Role**: the container for everything. Multica's multi-tenancy boundary.

#### Features

- **Multiple workspaces**: a user can belong to many; each is fully isolated (issues, agents, skills, members are all separate).
- **Create**: a name is all that is required; the slug (the short ID used in URLs) is generated.
- **Switch**: a sidebar dropdown; on desktop each workspace has its own tab group.
- **Leave**: non-owner members can leave on their own.
- **Delete**: owner only. A hard delete with cascade.
- **Workspace settings**: name, slug, description, **workspace context** (a shared system prompt for every agent in the workspace), and the **repository list** (the allowlist of Git repository URLs agents may access).
- **Workspace avatar and issue prefix**: each workspace can define its own issue-number prefix (`ACME-42`).

#### Where it sits

A workspace is not a feature; it is **the coordinate system for every feature**. URLs are always `/{workspace-slug}/...` and API requests always carry `X-Workspace-Slug`. An issue, an agent, or a skill is meaningless outside a workspace.

#### Tables

`workspace`, `member`, `workspace_invitation`

---

### 3.2 Issues

> **Role**: Multica's core work object.

What Linear calls an Issue, Jira a Ticket, and GitHub an Issue — a unit of work. What makes Multica's version distinctive is that **an issue can be assigned to an agent on exactly the same footing as to a person**.

#### Core fields

- Title, description (Tiptap rich text), status, priority
- Number (auto-incrementing, with the workspace prefix)
- **Assignee (member or agent)**
- **Creator (member or agent)** — agents can create issues too
- Parent issue (for sub-tasks)
- Project
- Due date
- Labels (many-to-many)
- Dependencies (blocks / blocked by / related)
- Acceptance criteria (JSONB)
- Origin (if created by an autopilot, the originating run is recorded)

#### Views

- **List**: a table, filterable by status/priority/assignee/creator/project, sortable by name/priority/due date/manual position, with separate pagination for open and completed.
- **Board**: Kanban, one column per status, with drag-and-drop (dragging switches the view into manual-ordering mode).
- **My Issues**: a dedicated view with three scopes — assigned to me / created by me / owned by my agents.

#### Interactions

- **Quick create**: a single-line create in the sidebar, or a rich-text modal (drafts persist locally)
- **Bulk actions**: multi-select, then change status/priority/assignee or delete
- **Sub-issues**: the parent shows a completion ring for its children
- **Subscribe**: creator, assignee, and anyone mentioned subscribe automatically
- **Reactions**: emoji on issues and comments
- **Pin**: pin an issue to the sidebar shortcuts
- **Copy link / jump via `Cmd+K`**
- **Timeline**: every key action (status change, assignee change, comment) in chronological order, mixing `activity_log` and `comment` records

#### Comments and discussion

- Tiptap rich-text editor with `@` mentions for members and agents
- Nested replies (one level)
- Emoji reactions
- **`@agent` triggers a task**: mentioning an agent in a comment queues a new agent task to reply or act

#### Attachments

- Drag-and-drop or button upload
- Inline image preview
- Storage backend: S3/CloudFront or local disk (self-hosted)

#### Where it sits

The issue is **the carrier for every workflow**:

- Agents receive work by being assigned an issue
- Autopilots trigger agents by creating issues
- Comments append work via `@agent`
- Inbox notifications are generated around issues

#### Tables

`issue`, `comment`, `issue_label`, `issue_to_label`, `issue_dependency`, `issue_subscriber`, `issue_reaction`, `comment_reaction`, `attachment`, `activity_log`, `pinned_item`

---

### 3.3 Projects

> **Role**: a higher-level container for many issues — Linear's Project, Jira's Epic.

#### Features

- Title, description, icon (emoji or identifier)
- Status: `planned` / `in_progress` / `paused` / `completed` / `cancelled`
- Priority: urgent / high / medium / low / none
- **Lead**: a member or an agent (polymorphic, like an issue assignee)
- A detail page listing every issue in the project
- Project search

#### Where it sits

A project is one level above an issue. An issue need not belong to a project, but when it does, the project surfaces in list filters, sidebar navigation, and breadcrumbs.

#### Tables

`project`

---

### 3.4 Agents

> **Role**: the AI worker. Multica's most distinctive object.

An agent is not "an AI model" — it is **a configured worker identity**. It has a name, an avatar, a description, an instruction sheet (system prompt), a bound runtime, and mounted skills. In the UI it appears exactly where a person would: the assignee dropdown, comment authorship, subscriber lists.

#### Configuration

- **Basics**: name, description, avatar (generated)
- **Provider**: which underlying CLI it runs — Claude Code, Codex, CodeBuddy, GitHub Copilot CLI, OpenCode, OpenClaw, Hermes, Pi, Cursor Agent, Kimi, Kiro CLI, Antigravity, Qoder CLI, or Trae CLI
- **Runtime**: which runtime it is bound to (i.e. which machine it runs on)
- **Instructions**: the agent's system prompt ("You are a senior engineer…")
- **Custom env**: environment variables injected into the CLI process (`ANTHROPIC_API_KEY`, `ANTHROPIC_BASE_URL`, `CLAUDE_CODE_USE_BEDROCK`, …)
- **Custom args**: extra launch arguments for the CLI (`--model`, `--thinking`, …)
- **MCP config**: the Model Context Protocol server list that gives the agent extra tools
- **Max concurrent tasks**: how many tasks it may run at once
- **Skills**: associated skills (see 3.6)
- **Visibility**: `workspace` (visible workspace-wide) or `private` (creator only)

#### Status

- `idle` / `working` / `blocked` / `error` / `offline` — driven by runtime heartbeat
- Can be archived (soft delete)

#### Interactions

- Create, edit, and archive under **Settings → Agents**
- Select in an issue's assignee dropdown
- Trigger with `@agent` in a comment
- Talk to directly in the chat panel

#### Where it sits

The agent is Multica's centre of gravity. Nearly every feature exists to answer "how do I get an agent to do work":

- Issues trigger agents through assignment
- Skills equip agents through mounting
- Runtimes provide the environment agents run in
- Autopilots schedule agents to start on their own
- Chat provides the conversational surface

#### Tables

`agent`, `agent_skill`

---

### 3.5 Runtimes and the daemon

> **Role**: the physical or virtual machine where an agent actually runs.

This is the heart of Multica's **distributed execution architecture**: **agents do not run on the server, they run on the user's own machine.** The server only schedules work, syncs state, and stores data.

#### What the daemon is

The `multica` CLI starts a background process on the user's machine (launchd on macOS, systemd on Linux, a service-style process on Windows) which:

1. **Detects** coding CLIs installed on `$PATH` (`claude`, `codex`, `opencode`, `openclaw`, `hermes`, `pi`, `cursor-agent`, `kimi`, `kiro-cli`, `qodercli`, `qoderclicn`, `traecli`, `grok`)
2. **Registers** them with the server as a set of runtimes (one CLI = one runtime)
3. **Polls** the server every 3 seconds and claims any available task
4. **Heartbeats** every 15 seconds to report that it is alive
5. On claiming a task, **launches the agent CLI** in an isolated local working directory and **streams the agent's output back to the server** in real time
6. On completion, reports the result, token usage, session ID, and working directory (for the next resumption)

#### What the Runtimes page shows

Under **Settings → Runtimes**:

- Each runtime's name, provider (icon), owner (whose machine), status indicator (online/offline), and last-seen time
- Ping diagnostics: poke it manually and watch for a response
- Usage: recent token consumption
- Activity: task activity
- CLI install instructions (in self-hosted mode)
- Desktop only: a **local daemon card** showing this machine's daemon status with one-click restart

#### Runtime lifecycle

- **Register**: on start-up the daemon POSTs `/api/daemon/register` and receives a runtime ID
- **Online**: a heartbeat every 15 seconds
- **Offline**: if the server sees no heartbeat for 45 seconds, the runtime is marked offline (a background sweeper checks every 30 seconds)
- **Orphan recovery**: a task still dispatched after 5 minutes, or still running after 2.5 hours, is marked failed by the sweeper
- **Long-offline GC**: a runtime with no heartbeat for 7 days and no active agents is reclaimed

#### CLI and daemon

| Command | What it does |
|---|---|
| `multica setup` | One-shot setup: URL, login, start the daemon |
| `multica login` | Opens a browser for OAuth; saves a 90-day PAT to `~/.multica/config.json` |
| `multica login --token <pat>` | Headless login (SSH/CI) |
| `multica daemon start` | Starts the daemon in the background (PID to `~/.multica/daemon.pid`, logs to `~/.multica/daemon.log`) |
| `multica daemon stop` | Sends SIGTERM and shuts down gracefully (waits up to 30s for in-flight tasks) |
| `multica daemon status` | Prints daemon status, detected agents, watched workspaces |
| `multica daemon logs -f` | Follows the log |
| `multica daemon start --profile <name>` | Starts a separately configured daemon (for running staging and production side by side) |

#### Security boundary

- One **isolated working directory** per task: `~/multica_workspaces/{ws}/{task_short_id}/workdir/`
- Environment variables are **filtered** so an agent cannot overwrite the daemon's own auth variables (`MULTICA_TOKEN`, …)
- Repository access is **allowlisted**: agents may only check out repositories configured on the workspace
- Codex carries a **platform- and version-dependent sandbox policy**

#### Where it sits

Runtimes are the infrastructure that makes "assign a task to an agent" actually happen. Without a runtime, every agent is a shell. A user's first onboarding requires at least one runtime online, or agents have nowhere to work.

#### Tables

`agent_runtime`, `daemon_token`, `daemon_pairing_session` (deprecated), `daemon_connection` (deprecated), `runtime_usage`

---

### 3.6 Skills

> **Role**: a reusable document that teaches an agent a way of working.

A skill is a set of Markdown documents plus supporting files. It is **not code** and **not a prompt template** — it is **instructions for the agent CLI to read**.

#### Shape

```
skill
  ├─ name:         "react-patterns"
  ├─ description:  "Common React patterns and best practices"
  ├─ content:      "## Overview\n..."     # the main document
  └─ files:
      ├─ examples/hooks.md
      └─ examples/useState.jsx
```

#### How it works

1. **Create**: under **Settings → Skills**, or import from a URL (clawhub.ai, skills.sh, …)
2. **Mount**: tick the skills an agent should use
3. **Inject**: when the agent claims a task, the daemon writes the mounted skill content into the task working directory at each **provider's native location**:
   - Claude Code → `.claude/skills/{name}/SKILL.md`
   - Codex → `CODEX_HOME/skills/{name}/`
   - OpenCode → `.opencode/skills/{name}/SKILL.md`
   - Pi → `.pi/skills/{name}/SKILL.md`
   - Cursor → `.cursor/skills/{name}/SKILL.md`
   - GitHub Copilot → `.github/skills/{name}/SKILL.md`
   - Others → `.agent_context/skills/{name}/SKILL.md`
4. **Use**: the agent CLI discovers and reads those files by its own convention

> 💡 **Skills are static.** They are not AI-generated and do not change during execution. They are experience written by people. A future version might distil skills from task history; this one does not.

#### CLI

```bash
multica skill list
multica skill get <id>
multica skill create --title ...
multica skill import --url https://...
multica skill files upsert <skill-id> --path ...
```

#### Where it sits

Skills are what distinguish Multica from "write a long prompt every time". They let a team's expertise **settle into reusable components** that take effect simply by being attached to an agent — an SOP or playbook written for a colleague.

Architecturally, skills take no part in execution logic; they participate only in **context injection**. They appear exactly once in the task lifecycle: during environment preparation, before the daemon launches the CLI.

#### Tables

`skill`, `skill_file`, `agent_skill`

---

### 3.7 Autopilots

> **Role**: the scheduler that lets agents start work with nobody there to trigger them.

Autopilots solve recurrence. A lot of work is **periodic** — morning bug triage, weekly dependency audits, monthly security scans. Triggering by hand is tedious; an autopilot turns it into a rule.

#### Shape

```
autopilot
  ├─ title, description
  ├─ assignee:        <agent_id>          # which agent runs it
  ├─ execution_mode:  create_issue | run_only
  ├─ issue_title_template:  "Daily triage - {{date}}"
  ├─ concurrency_policy:    skip | queue | replace
  └─ triggers (many):
       ├─ kind:  schedule | webhook | api
       ├─ cron_expression
       ├─ timezone
       └─ webhook_token
```

#### Two execution modes

- **`create_issue` (default)**: on trigger, create a new issue (title rendered from `issue_title_template`), assign it to the agent, and run the normal agent task flow
- **`run_only`**: create the task directly with no issue — for work that should execute without leaving a ticket, such as an hourly status check

#### Three trigger kinds

- **Schedule (cron)**: a background scheduler scans `autopilot_trigger` every 30 seconds and fires anything due
- **Webhook**: a URL carrying a `webhook_token`; an external POST triggers the run
- **API / manual**: the "Run now" button in the UI, or `multica autopilot trigger <id>`

#### Concurrency policy

- `skip`: if the previous run of this autopilot has not finished, skip this one (deduplicate)
- `queue`: wait for the previous run to finish
- `replace`: abort the previous run and take its place

#### Run records

Every trigger writes a row in `autopilot_run`: `pending → issue_created → running → completed/failed/skipped`. The autopilot detail page shows the full history.

#### Built-in templates

Ready-made autopilots you can create in one click:

- Daily news digest (09:00 daily)
- PR review reminder (10:00 weekdays)
- Bug triage (09:00 weekdays)
- Weekly progress report (17:00 weekly)
- Dependency audit (10:00 weekly)
- Security scan (02:00 weekly)

#### Where it sits

Autopilots take Multica from "you assign, the agent does" to "the agent initiates work". With `run_only` you can even run scheduled work with no issue at all. The `origin_type=autopilot` and `origin_id` fields on an issue leave a trail back to the run that created it.

#### Tables

`autopilot`, `autopilot_trigger`, `autopilot_run`

---

### 3.8 Chat

> **Role**: a persistent multi-turn conversation with an agent, not attached to an issue.

Sometimes you do not want to open an issue just to say one thing to an agent. Chat is for that lightweight case — a ChatGPT-shaped interface, except you are talking to one of your workspace's agents.

#### Features

- **Create a session**: pick an agent and start
- **Message list**: Markdown rendering, syntax-highlighted code blocks
- **Send**: the message is queued as a task; the agent's response is written back as a message
- **Streaming**: pushed live over WebSocket
- **Unread tracking**: the `unread_since` field records the timestamp of the first unread message
- **Archive**: move an old session out of the active list
- **Session reuse**: successive turns in one chat session reuse the underlying CLI `session_id`, so conversation context survives

#### Chat vs issue comments

| | Chat | Issue comments |
|---|---|---|
| Context carrier | A standalone session (`chat_session`) | An issue |
| Visibility | Private between you and the agent | Visible to the whole workspace |
| Triggering the agent | Every user message triggers | Requires `@agent` |
| Purpose | Exploration, questions, one-off tasks | Work tied to an issue |

#### Where it sits

Chat fills the gap between "not formal enough to warrant an issue" and "still needs to persist". It is also the entry point that feels most like ordinary messaging.

#### Tables

`chat_session`, `chat_message`; execution still runs through `agent_task_queue` (distinguished by the `chat_session_id` field)

---

### 3.9 Inbox and notifications

> **Role**: each person's notification centre.

#### Shape

An `inbox_item` is an entry pushed to a specific recipient:

- `recipient_type` = `member` or `agent` (agents have inboxes too)
- `type` (e.g. `issue_assigned`, `comment_mention`, `task_completed`, `invitation_created`)
- `severity` (`action_required` / `attention` / `info`)
- The related issue, if any
- Read / archived state

#### What generates a notification

- An issue is assigned to you
- You are `@`-mentioned
- A subscribed issue changes status
- A subscribed issue gets a new comment
- A workspace invitation
- One of your agent's tasks completes or fails

#### Automatic subscription

The server's subscriber listener adds the following to `issue_subscriber` automatically:

- The issue creator
- The current assignee (kept in sync on change)
- Anyone `@`-mentioned in a comment
- Anyone who subscribed manually

#### UI

- **Inbox page**: two columns — a list on the left, issue detail on the right
- **Bulk actions**: mark all read / archive read only / archive notifications for completed issues
- **Badge**: unread count on the sidebar navigation
- **WebSocket push**: new entries arrive live (the `inbox:new` event is sent only to the target user)

#### Where it sits

The inbox is the "attention system": it lets users know what needs them without watching the board.

#### Tables

`inbox_item`, `issue_subscriber`

---

### 3.10 Members, invitations, permissions

#### Roles

| Role | Permissions |
|---|---|
| **Owner** | Everything; the only role that can delete the workspace |
| **Admin** | Manage members and settings; cannot delete the workspace or remove other admins |
| **Member** | Create issues, comment, self-assign, use agents |

#### Invitation flow

- An admin invites by email under **Settings → Members**
- The server creates a `workspace_invitation` record (7-day expiry)
- An email is sent (via the Resend integration; when unconfigured it prints to stderr)
- The invitee is notified: existing accounts see it in their personal inbox; new users get a signup link in the email
- Accept / decline / expire

#### UI

- Member list: avatar, email, role badge, action menu (change role, remove)
- Pending invitations: resend, revoke
- Invite acceptance page (`/invite/[id]`): workspace details with accept/decline

#### Desktop handling of invitation acceptance

On desktop the `multica://invite/{id}` deep link **does not go through routing**. It raises a `WindowOverlay` — the shared `InvitePage` view mounts inside a native window overlay so window dragging and other native behaviours keep working.

#### Where it sits

Member management is the precondition for all collaboration. Multica's twist: the membership system also covers agents. `assignee_type` exists precisely so members and agents can express "who can be assigned" through one API.

#### Tables

`member`, `workspace_invitation`

---

### 3.11 Search and the command palette

#### Command palette (`Cmd+K`)

The global search entry point, covering:

- **Issues** (by title, by number)
- **Projects** (by name)
- **Workspaces** (by name, for fast switching)
- **Navigation** (jump to settings, runtimes, skills, …)
- **Actions** (new issue, new project, toggle theme)
- **Recent issues** (recorded automatically)

#### List filtering

Issue lists, project lists, and the inbox each have local filter chips and a search input.

#### Full-text search

`GET /api/issues/search` searches issue titles, descriptions, and comment bodies, returning matching snippets.

> **There is no vector-based semantic search today.** The product is positioned as AI-native but does not use pgvector for search, and the schema does not enable the vector extension. A possible future extension.

#### Where it sits

`Cmd+K` is the main navigation route for keyboard-first users (Linear-style) and is faster than clicking through the sidebar.

---

### 3.12 Auth, login, onboarding

#### Login methods

- **Email verification code (magic-link style)**: enter an email, receive a 6-digit code, enter it
- **Google OAuth**: one-click sign-in
- **PAT (CLI)**: a token generated under Settings → API Tokens for CLI and scripting

#### Onboarding

Lives in `packages/views/onboarding/` and `apps/web/app/(auth)/onboarding/`.

The persisted sequence is three steps — `about_you` → `workspace` → `runtime` (see `packages/core/onboarding/step-order.ts`, the single source of truth). A `welcome` product intro precedes them but is deliberately not a persisted step and shows no progress indicator, because reading an intro is not progress toward setup.

Helper-agent creation is **not** part of the in-flow sequence. It happens after onboarding exits, in the workspace shell — see `packages/views/workspace/welcome-after-onboarding.tsx`.

#### Invitation acceptance (zero-workspace)

A new user who arrives through an invitation (and has no workspace yet) enters that workspace directly on acceptance, skipping onboarding.

#### Post-auth routing

- Signed in with at least one workspace: go to `/{slug}/issues`
- Signed in with no workspace: go to `/workspaces/new` or onboarding
- Not signed in: go to `/login`

#### Signup gating

The server supports:

- `ALLOW_SIGNUP=false` to close registration
- `ALLOWED_EMAILS` / `ALLOWED_EMAIL_DOMAINS` allowlists

#### Where it sits

Onboarding is the funnel that decides whether a new user ever gets an agent running. If any step is incomplete — especially a runtime never connecting — everything downstream is a shell.

#### Tables

`user`, `verification_code`, `personal_access_token`

---

### 3.13 Settings and profile

#### My Account tabs

- **Profile**: name, avatar (system-generated, not uploadable), email (read-only)
- **Appearance**: theme (light / dark / system)
- **API tokens**: create, view, revoke PATs; the full token is shown once at creation
- **Daemon** (desktop only): local daemon status, restart, launch-at-login toggle
- **Updates** (desktop only): current version, check for updates, auto-update toggle

#### Workspace tabs

- **General**: name, description, **workspace context** (the system-level prompt for agents)
- **Members**: see 3.10
- **Repositories**: GitHub integration, connected repository list, the agent allowlist
- **Agents / Runtimes / Skills / Autopilots**: each has its own page (reachable directly from the sidebar, with management tabs in settings as well)

#### Where it sits

Settings is where "configuration is the work" happens: an agent's prompt, a workspace's context, the repository allowlist, a skill's content. **The single most important sentence for operators and writers**: every setting a user configures here changes the context an agent actually reads at execution time.

---

### 3.14 The CLI

`multica` is not only the tool that starts the daemon — it is a full command-line surface. Many users prefer to move work forward in a terminal rather than the UI.

#### Workspaces and issues

```bash
multica workspace list | get | watch | unwatch
multica issue list | get | create | update | assign | status
multica issue comment list | add | delete
multica issue runs <id>                 # task execution history
multica issue run-messages <task-id>    # messages from one run
```

#### Agents, skills, autopilots, projects, repos

```bash
multica agent list | get | create | update | archive
multica skill list | get | create | update | delete | import | files upsert
multica autopilot list | get | create | update | trigger
multica autopilot trigger-add --cron "0 9 * * 1-5"
multica project list | get | create | update
multica repo list | add | update | delete
```

#### Runtimes

```bash
multica runtime list | usage | activity | update
```

#### Config and updates

```bash
multica config show | set server_url ...
multica auth status | logout
multica version | update
```

#### Where it sits

The CLI is how Multica expresses developer-friendliness — and it matters just as much to the agents themselves. **An agent executing a task calls `multica` to read and write issues, post comments, and look things up.** That is the CLI's role in an "agents as first-class citizens" architecture.

---

## 4. System architecture

```
┌─────────────────────┐        ┌────────────────────┐        ┌──────────────────┐
│  Next.js Web App    │        │  Electron Desktop  │        │  multica CLI     │
│  apps/web           │        │  apps/desktop      │        │  server/cmd/     │
└──────────┬──────────┘        └──────────┬─────────┘        └────────┬─────────┘
           │  HTTP + WebSocket            │                           │  HTTP
           │                              │                           │
           └──────────────┬───────────────┴───────────────┬───────────┘
                          │                               │
                          ▼                               ▼
              ┌─────────────────────────────────────────────────┐
              │               Go backend (server/)              │
              │  • Chi HTTP router  • gorilla/websocket hub     │
              │  • sqlc-generated queries                       │
              │  • In-process event bus                         │
              │  • Background workers (sweeper / scheduler)     │
              └──────────────────┬──────────────────────────────┘
                                 │
                                 ▼
                      ┌──────────────────────┐
                      │  PostgreSQL 17       │
                      │  + pgcrypto          │
                      └──────────────────────┘

                                 ▲
                                 │ HTTPS poll + heartbeat
                                 │
              ┌─────────────────────────────────────────────────┐
              │      Local daemon (on the user's machine)       │
              │  • Claims tasks every 3s  • Heartbeats every 15s│
              │  • Detects and launches agent CLI subprocesses  │
              │  • Prepares an isolated working directory       │
              └───────────────┬─────────────────────────────────┘
                              │ spawns
              ┌───────────────┼─────────────────────────────────┐
              ▼               ▼              ▼              ▼
         Claude Code      Codex         OpenCode      … other CLIs
         (subprocess)     (subprocess)  (subprocess)
```

### Layer responsibilities

| Layer | Owns | Does not own |
|---|---|---|
| **Web / desktop client** | UI, local client state (Zustand), server-state cache (TanStack Query), WebSocket subscriptions | Business rules, AI calls |
| **Server** | Persistence, permissions, task orchestration, event broadcast, autopilot scheduling, runtime health | Executing agents, calling LLMs |
| **Daemon** | Detecting and launching local CLIs, managing task working directories, streaming messages back, session resumption | Business decisions — it only runs what the server hands it |
| **Agent CLI (Claude Code etc.)** | Calling the LLM, executing tool calls, writing files, running tests | Knowing Multica's data model (all context comes back through `multica` CLI commands) |

### The realtime layer (WebSocket)

The server runs a WebSocket hub:

- **Auth**: a JWT or PAT in the URL parameters, plus `workspace_slug`
- **Room model**: one room per workspace; a workspace's events broadcast only to that room
- **Targeted push**: personal events such as `inbox:new` and `invitation:created` go through `SendToUser`
- **Heartbeat**: the server pings every 54 seconds; the client must pong within 60

**Every event type (roughly 60, for writers' reference):**

- `issue:created` / `issue:updated` / `issue:deleted`
- `comment:created` / `comment:updated` / `comment:deleted` / `reaction:added` / `issue_reaction:added`
- `agent:created` / `agent:status` / `agent:archived`
- `task:dispatch` / `task:progress` / `task:message` / `task:completed` / `task:failed` / `task:cancelled`
- `inbox:new` / `inbox:read` / `inbox:archived` / `inbox:batch-*`
- `workspace:updated` / `workspace:deleted` / `member:added` / `member:updated` / `member:removed`
- `invitation:created` / `invitation:accepted` / `invitation:declined` / `invitation:revoked`
- `chat:message` / `chat:done` / `chat:session_read`
- `skill:created` / `skill:updated` / `skill:deleted`
- `project:created` / `project:updated` / `project:deleted`
- `autopilot:created` / `autopilot:updated` / `autopilot:run_start` / `autopilot:run_done`
- `subscriber:added` / `activity:created`
- `daemon:heartbeat` / `daemon:register`

On receiving an event the client either patches its local cache directly (issues, comments, tasks — anything needing an instant update) or invalidates the corresponding query for a refetch (less critical data).

### Where the AI lives

**Multica never calls an LLM API directly.** Every LLM call happens inside an agent CLI subprocess (Claude Code calls the Anthropic API, Codex calls OpenAI, and so on).

What the server and daemon do:

1. Prepare the prompt (see `server/internal/daemon/prompt.go`)
2. Prepare environment variables (injecting `agent.custom_env`)
3. Prepare the working directory (injecting CLAUDE.md / AGENTS.md / skills / issue context)
4. Launch the CLI subprocess
5. Stream the CLI's stdout, classify the messages, and forward them

**This is why there is no large body of prompt-engineering code.** There are only a few templates (task prompt, chat prompt, comment-triggered prompt); the substance is agent instructions plus issue context plus skill files, and the actual LLM conversation is managed by the CLI itself.

### Background workers

The server starts three goroutines:

1. **Runtime sweeper** (every 30s): mark runtimes offline, recover orphaned tasks, GC long-offline runtimes
2. **Autopilot scheduler** (every 30s): scan cron triggers and dispatch anything due
3. **DB stats logger**: periodically log pgxpool connection-pool state

---

## 5. Product map: every route

### Public / auth

- `/` — home
- `/login` — sign in
- `/auth/callback` — OAuth callback
- `/workspaces/new` — create a workspace
- `/invite/[id]` — accept an invitation
- `/onboarding` — first-run setup

### Inside a workspace (`/{slug}/...`)

- `/issues` — issue list (board / list views)
- `/issues/[id]` — issue detail
- `/my-issues` — my issues (three scopes)
- `/projects` — project list
- `/projects/[id]` — project detail
- `/autopilots` — autopilot list
- `/autopilots/[id]` — autopilot detail
- `/agents` — agent list
- `/runtimes` — runtime list
- `/skills` — skill library
- `/inbox` — inbox
- `/settings` — settings (tabs: profile / appearance / tokens / workspace / members / repos / daemon / updates)

### Desktop only (not routes — `WindowOverlay`)

- **Create workspace overlay**
- **Invite accept overlay** (from the `multica://invite/{id}` deep link)
- **Onboarding overlay** (first run, or zero workspaces)

---

## 6. Web vs desktop

### Shared (nearly everything)

The real UI for every business page — issues, projects, autopilots, agents, runtimes, skills, inbox, settings, chat, login, onboarding — lives in `packages/views/`, and web and desktop use the same components.

### Web only

- Address bar plus browser back/forward
- Server-side rendering
- `/login` handles the OAuth callback on a localhost port (which is how CLI login works)

### Desktop only

- **Multi-tab**: one tab group per workspace, reorderable by drag
- **`WindowOverlay`**: invitation acceptance, workspace creation, and onboarding are native window layers rather than routes
- **Daemon integration**: restart the local daemon and read its status from settings
- **Local daemon runtime card**: the Runtimes page surfaces this machine's daemon automatically
- **Auto-update**: check, download, and install from `Settings → Updates`
- **Immersive mode**: full screen with the sidebar hidden
- **Deep links**: `multica://auth/callback?token=...` and `multica://invite/{id}`
- **Drag region**: the macOS traffic lights plus a 48px top drag strip (`h-12`) for moving the window
- **Workspace singleton**: `setCurrentWorkspace()` owns the global identity of the active workspace

### Why the two differ

The web has a URL bar, so an error state ("you do not have access to this workspace") is meaningfully a shareable URL. Desktop has no URL bar, and the same state would simply trap the user — so desktop **heals silently** by dropping the stale tab from the store. That difference drives several details:

- Web has a `NoAccessPage`; desktop does not
- Web has a `/workspaces/new` page; desktop makes it an overlay
- Web routes deep links directly; desktop converts them into a `WindowOverlay`

---

## 7. Appendix: key tables

Grouped by product domain, with the most important fields — for looking up "what is actually stored behind this feature".

### Identity / auth

- `user` — the base account (id, email, name, avatar_url)
- `verification_code` — email codes (code, expires_at, attempts)
- `personal_access_token` — user API tokens (token_hash, token_prefix, revoked)

### Workspace / membership

- `workspace` — the container (name, slug, description, context, settings, repos, issue_prefix, issue_counter)
- `member` — membership (role: owner/admin/member)
- `workspace_invitation` — invitations (invitee_email, status: pending/accepted/declined/expired)

### Agent / runtime / skill

- `agent` — the agent record (instructions, custom_env, custom_args, mcp_config, runtime_mode, visibility, status)
- `agent_runtime` — runtimes (daemon_id, provider, status: online/offline, last_seen_at)
- `agent_skill` — the n-n join mounting skills onto agents
- `skill` — the main skill document (name, description, content)
- `skill_file` — supporting files (path, content)
- `daemon_token` — daemon-level tokens
- `daemon_connection` / `daemon_pairing_session` — an earlier design (deprecated)

### Issue / collaboration

- `issue` — issues (status, priority, assignee_type+assignee_id, creator_type+creator_id, parent_issue_id, project_id, origin_type, origin_id, acceptance_criteria, due_date, position)
- `issue_label` / `issue_to_label` — labels
- `issue_dependency` — dependencies (blocks / blocked_by / related)
- `issue_subscriber` — subscribers (reason: creator/assignee/commenter/mentioned/manual)
- `issue_reaction` / `comment_reaction` — emoji reactions
- `comment` — comments (type: comment/status_change/progress_update/system; `parent_id` for threading)
- `attachment` — attachments

### Task execution

- `agent_task_queue` — the task record (status: queued/dispatched/running/completed/failed/cancelled, context, result, session_id, work_dir, trigger_comment_id, chat_session_id, autopilot_run_id)
- `task_message` — the per-run message stream (seq, type, tool, input, output)
- `task_usage` — token usage (input / output / cache_read / cache_write)

### Chat

- `chat_session` — sessions (unread_since, session_id, work_dir)
- `chat_message` — messages (role: user/assistant)

### Projects and organisation

- `project` — projects (status, priority, lead_type+lead_id, icon)
- `pinned_item` — sidebar pins (item_type, item_id, position)

### Automation

- `autopilot` — rules (assignee_id, execution_mode: create_issue/run_only, issue_title_template, concurrency_policy)
- `autopilot_trigger` — triggers (kind: schedule/webhook/api, cron_expression, timezone, next_run_at, webhook_token)
- `autopilot_run` — run records (status: pending/issue_created/running/skipped/completed/failed)

### Notification and audit

- `inbox_item` — inbox entries (recipient_type, type, severity, read, archived)
- `activity_log` — the audit log (actor_type: member/agent/system, action, details)
- `runtime_usage` — daily per-runtime token aggregates (for billing and capacity planning)

---

## Closing

Multica's design comes down to one sentence: **it extends "people collaborating on a board" into "people and AI agents collaborating on the same board".**

Every feature follows from that:

- So an agent can be assigned work like a person → polymorphic actors (`assignee_type`)
- So an agent can start work on its own → autopilots
- So an agent's way of working can be captured and reused → skills
- So agents execute in an environment the user controls → runtimes and the daemon
- So people are not buried in notifications → the inbox and automatic subscription
- So a conversation has continuity → session resumption

When you read a piece of copy, a UI module, or a table, place it back into that "humans + AI collaborating" coordinate system to understand where it sits.
