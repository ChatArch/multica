/**
 * Issue Quick Actions (MUL-5465) — workspace-level presets for "who to call
 * and what to say" on an existing issue.
 *
 * Running one is not a separate dispatch path: the server renders the prompt,
 * posts a `quick_action` comment carrying the target's mention markup, and the
 * normal comment -> mention -> task trigger takes over. That is why the run
 * response is a `Comment` with `trigger_outcomes` rather than a bespoke shape.
 */

/**
 * The author's stated intent, chosen at creation.
 *
 * - `public`  — meant for the team. The server requires a target every member
 *   can invoke, so it is runnable by everyone by construction.
 * - `private` — meant for its creator only; any target is allowed, and the
 *   list endpoint returns it to nobody else.
 *
 * This is intent, NOT an authorization decision — the run endpoint always
 * re-checks invoke permission. Server-driven enum: switch with a `default`.
 */
export type QuickActionVisibility = "private" | "public";

export type QuickActionAssigneeType = "agent" | "squad";

export type QuickActionStatus = "active" | "archived";

export interface QuickAction {
  id: string;
  workspace_id: string;
  name: string;
  description: string;
  assignee_type: QuickActionAssigneeType | string;
  assignee_id: string;
  prompt: string;
  /** When true the prompt contains `{{input}}` and clicking opens one field. */
  input_enabled: boolean;
  input_label: string;
  input_placeholder: string;
  input_required: boolean;
  visibility: QuickActionVisibility | string;
  status: QuickActionStatus | string;
  last_used_at: string | null;
  use_count: number;
  created_by_id: string;
  created_at: string;
  updated_at: string;
  /** Display name of the bound agent or squad. Absent when it no longer resolves. */
  target_name?: string;
  /**
   * Whether the bound target is currently invocable by every workspace member.
   * Plain metadata, not a verdict — settings shows it beside the binding so a
   * `public` action pointing at a now-private agent reads as visibly wrong.
   */
  target_public: boolean;
  /** The bound agent or squad was archived or deleted. */
  target_missing: boolean;
}

export interface CreateQuickActionRequest {
  name: string;
  description?: string;
  assignee_type: QuickActionAssigneeType;
  assignee_id: string;
  prompt: string;
  input_enabled?: boolean;
  input_label?: string;
  input_placeholder?: string;
  input_required?: boolean;
  visibility?: QuickActionVisibility;
}

export interface UpdateQuickActionRequest {
  name?: string;
  description?: string;
  assignee_type?: QuickActionAssigneeType;
  assignee_id?: string;
  prompt?: string;
  input_enabled?: boolean;
  input_label?: string;
  input_placeholder?: string;
  input_required?: boolean;
  visibility?: QuickActionVisibility;
  status?: QuickActionStatus;
}

export interface ListQuickActionsResponse {
  quick_actions: QuickAction[];
}

/**
 * How many actions the issue sidebar shows before the rest collapse behind
 * "More". Scarcity here is structural: a list that renders all 30 stops being
 * a shortlist and becomes a menu nobody reads.
 */
export const QUICK_ACTION_SIDEBAR_LIMIT = 5;

/**
 * The closed set of prompt variables. Flat substitution only — no
 * conditionals, loops, or filters, by design: the agent already reads the
 * whole issue, so natural language is the control flow (MUL-5465 D5).
 */
export const QUICK_ACTION_VARIABLES = [
  "issue.title",
  "issue.identifier",
  "issue.url",
  "user.name",
  "date",
  "input",
] as const;

export type QuickActionVariable = (typeof QUICK_ACTION_VARIABLES)[number];

/** The variable that carries the runtime input. */
export const QUICK_ACTION_INPUT_VARIABLE = "input";

/** Matches `{{token}}`, tolerating inner whitespace, mirroring the server. */
export const QUICK_ACTION_VARIABLE_RE = /\{\{\s*([a-zA-Z0-9_.]+)\s*\}\}/g;

/**
 * Client-side mirror of the server's prompt validation so the settings form
 * can show the error inline instead of waiting for a 400. The server remains
 * the authority — this is an affordance, not a gate.
 */
export function findUnknownQuickActionVariables(prompt: string): string[] {
  const known = new Set<string>(QUICK_ACTION_VARIABLES);
  const unknown: string[] = [];
  for (const match of prompt.matchAll(QUICK_ACTION_VARIABLE_RE)) {
    const name = match[1];
    if (name && !known.has(name) && !unknown.includes(name)) unknown.push(name);
  }
  return unknown;
}

/** Whether a prompt references `{{input}}`. */
export function promptUsesQuickActionInput(prompt: string): boolean {
  for (const match of prompt.matchAll(QUICK_ACTION_VARIABLE_RE)) {
    if (match[1] === QUICK_ACTION_INPUT_VARIABLE) return true;
  }
  return false;
}
