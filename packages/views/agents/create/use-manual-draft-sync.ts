"use client";

import {
  useEffect,
  useRef,
  useState,
  type Dispatch,
  type SetStateAction,
} from "react";
import {
  fromStoredAgentDraft,
  manualDraftOwner,
  toStoredAgentDraft,
  useManualAgentDraftStore,
  type AgentDraft,
} from "@multica/core/agents";

/**
 * Keeps the manual creation form across navigation (#6287).
 *
 * The reported bug is a tab switch: the desktop shell mounts only the active
 * tab, so coming back is a remount and everything typed was gone. A route
 * change and a reload lose it the same way, so the fix has to be persistence,
 * not a guard on one exit.
 *
 * Restore runs before save is armed. A write fired while the form still held
 * its empty initial state would overwrite the stored draft with nothing —
 * turning "come back to my form" into "lose my form", which is the bug.
 */
export function useManualDraftSync(options: {
  duplicateId: string | null;
  draft: AgentDraft;
  setDraft: Dispatch<SetStateAction<AgentDraft>>;
  /** False until a duplicate has finished seeding; saving before then would
   *  persist the empty form the seed is about to replace. */
  ready: boolean;
}): void {
  const { duplicateId, draft, setDraft, ready } = options;
  const owner = manualDraftOwner(duplicateId);

  const stored = useManualAgentDraftStore((state) => state.draft);
  const setStored = useManualAgentDraftStore((state) => state.setDraft);
  const [restored, setRestored] = useState(false);
  const storedRef = useRef(stored);
  storedRef.current = stored;

  useEffect(() => {
    if (restored || !ready) return;
    const persisted = storedRef.current;
    // A draft belonging to a different flow is left alone, not adopted: a copy
    // of one agent must never seed a copy of another, or a blank form.
    if (persisted.owner === owner) {
      setDraft(fromStoredAgentDraft(persisted.draft, persisted.runtimeId));
    }
    setRestored(true);
  }, [owner, ready, restored, setDraft]);

  useEffect(() => {
    if (!restored) return;
    setStored({
      owner,
      runtimeId: draft.runtimeId,
      draft: toStoredAgentDraft(draft, null),
    });
  }, [draft, owner, restored, setStored]);
}
