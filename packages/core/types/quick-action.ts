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
 * Who can actually use an action. Derived server-side from the bound agent's
 * permission mode on every request — never stored, so it cannot drift out of
 * sync after someone flips an agent between private and public_to.
 *
 * Server-driven enum: switch on it with a `default` branch.
 */
export type QuickActionVisibility =
  | "workspace"
  | "restricted"
  | "owner_only"
  | "unavailable";

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
  position: number;
  status: QuickActionStatus | string;
  last_used_at: string | null;
  use_count: number;
  created_at: string;
  updated_at: string;
  /** Derived per request. Unknown values must fall through a `default`. */
  visibility: QuickActionVisibility | string;
  /**
   * The caller's own invoke verdict. The sidebar renders only `can_run`
   * actions — a button nobody can press is worse than no button.
   */
  can_run: boolean;
  /** Omitted when the caller cannot see the bound target at all. */
  target_name?: string;
  target_avatar_url?: string;
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
  status?: QuickActionStatus;
  position?: number;
}

export interface ListQuickActionsResponse {
  quick_actions: QuickAction[];
  /**
   * How many actions the sidebar shows before the rest collapse behind
   * "More". Server-driven so the number lives in one place, not three.
   */
  sidebar_limit: number;
}

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
