import { describe, expect, it } from "vitest";
import { EMPTY_AGENT_DRAFT } from "./draft";
import {
  MANUAL_DRAFT_BLANK_OWNER,
  manualDraftEntryHasContent,
  manualDraftOwner,
  setManualDraftEntry,
  type ManualAgentDrafts,
  type ManualDraftEntry,
} from "./manual-draft-store";
import { toStoredAgentDraft } from "./stored-draft";

const entry = (
  overrides: Partial<typeof EMPTY_AGENT_DRAFT> = {},
): ManualDraftEntry => ({
  runtimeId: "runtime-1",
  draft: toStoredAgentDraft({ ...EMPTY_AGENT_DRAFT, ...overrides }, null),
});

describe("manual agent drafts", () => {
  // A blank agent and a copy of agent X are different pieces of work, and a
  // copy of X is not a copy of Y.
  it("scopes a draft to what is being created", () => {
    expect(manualDraftOwner(null)).toBe(MANUAL_DRAFT_BLANK_OWNER);
    expect(manualDraftOwner("agent-1")).toBe("duplicate:agent-1");
    expect(manualDraftOwner("agent-1")).not.toBe(manualDraftOwner("agent-2"));
  });

  // The regression this keying exists for: with one shared slot, opening a
  // second flow overwrote the first before the user typed a character.
  it("leaves other owners' drafts alone", () => {
    const drafts: ManualAgentDrafts = setManualDraftEntry(
      { byOwner: {} },
      "duplicate:agent-A",
      entry({ name: "Half-finished copy of A" }),
    );

    const afterOpeningBlank = setManualDraftEntry(
      drafts,
      MANUAL_DRAFT_BLANK_OWNER,
      entry({ name: "Something else" }),
    );
    const afterOpeningB = setManualDraftEntry(
      afterOpeningBlank,
      "duplicate:agent-B",
      entry({ name: "Copy of B" }),
    );

    expect(afterOpeningB.byOwner["duplicate:agent-A"]?.draft.name).toBe(
      "Half-finished copy of A",
    );
    expect(Object.keys(afterOpeningB.byOwner).sort()).toEqual([
      MANUAL_DRAFT_BLANK_OWNER,
      "duplicate:agent-A",
      "duplicate:agent-B",
    ]);
  });

  // The runtime is seeded automatically on every visit. Counting it as content
  // would store a slot for a form nobody touched — and would grow a dead key
  // for every agent ever opened for duplication.
  it("stores nothing for a form the user has not touched", () => {
    const drafts = setManualDraftEntry({ byOwner: {} }, "blank", entry());
    expect(drafts.byOwner).toEqual({});
    expect(manualDraftEntryHasContent(entry())).toBe(false);
  });

  // Emptying the form removes its slot rather than parking a blank one.
  it("drops a slot once its content is gone", () => {
    const withContent = setManualDraftEntry(
      { byOwner: {} },
      "blank",
      entry({ name: "Typed" }),
    );
    expect(withContent.byOwner.blank).toBeDefined();

    const emptied = setManualDraftEntry({ ...withContent }, "blank", entry());
    expect(emptied.byOwner.blank).toBeUndefined();

    const discarded = setManualDraftEntry({ ...withContent }, "blank", null);
    expect(discarded.byOwner.blank).toBeUndefined();
  });

  it("treats anything the user actually entered as content", () => {
    expect(manualDraftEntryHasContent(entry({ name: "Release" }))).toBe(true);
    expect(
      manualDraftEntryHasContent(entry({ instructions: "Be careful" })),
    ).toBe(true);
    expect(
      manualDraftEntryHasContent(entry({ skillIds: new Set(["skill-1"]) })),
    ).toBe(true);
    expect(manualDraftEntryHasContent(entry({ avatarUrl: "🚀" }))).toBe(true);
  });
});
