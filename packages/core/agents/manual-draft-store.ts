"use client";

import { createDraftStore } from "../drafts/create-draft-store";
import type { StoredAgentDraft } from "../types";
import { EMPTY_AGENT_DRAFT } from "./draft";
import { toStoredAgentDraft } from "./stored-draft";

/**
 * The manual creation form, kept across navigation.
 *
 * The AI route stores its configuration server-side, keyed by the conversation
 * it belongs to. The manual route has no conversation and nothing on the server
 * to hang a draft off, so it persists locally — which is enough for what it has
 * to survive: a tab switch, a route change, a reload (#6287).
 *
 * `owner` scopes the draft to what is being created. A blank agent and a copy of
 * agent X are different pieces of work, and a copy of X is not a copy of Y; a
 * single slot would silently hand one flow's answers to another. A draft whose
 * owner does not match the current route is ignored rather than deleted — the
 * user may well come back to it.
 */
export interface ManualAgentDraft {
  /** `"blank"`, or `"duplicate:<agentId>"`. */
  owner: string;
  /** Runtime included here, unlike the AI route's draft: there is no carrier
   *  agent to be authoritative about it. */
  runtimeId: string;
  draft: StoredAgentDraft;
}

export const MANUAL_DRAFT_BLANK_OWNER = "blank";

export function manualDraftOwner(duplicateId: string | null): string {
  return duplicateId ? `duplicate:${duplicateId}` : MANUAL_DRAFT_BLANK_OWNER;
}

const EMPTY_MANUAL_AGENT_DRAFT: ManualAgentDraft = {
  owner: MANUAL_DRAFT_BLANK_OWNER,
  runtimeId: "",
  draft: toStoredAgentDraft(EMPTY_AGENT_DRAFT, null),
};

/**
 * Whether this draft is worth restoring. A runtime alone does not count: it is
 * seeded automatically for every visit, so treating it as content would make
 * "you have unsaved work" true the moment the page opened.
 */
export function hasMeaningfulManualDraft(value: ManualAgentDraft): boolean {
  const { draft } = value;
  return (
    draft.name.trim().length > 0 ||
    draft.description.trim().length > 0 ||
    draft.instructions.trim().length > 0 ||
    draft.skill_ids.length > 0 ||
    draft.member_ids.length > 0 ||
    draft.avatar_url !== null
  );
}

export const useManualAgentDraftStore = createDraftStore<ManualAgentDraft>({
  storageKey: "multica:agents:manual-draft",
  emptyData: EMPTY_MANUAL_AGENT_DRAFT,
  hasMeaningful: hasMeaningfulManualDraft,
});
