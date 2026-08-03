import { describe, expect, it } from "vitest";
import { EMPTY_AGENT_DRAFT } from "./draft";
import {
  MANUAL_DRAFT_BLANK_OWNER,
  hasMeaningfulManualDraft,
  manualDraftOwner,
} from "./manual-draft-store";
import { toStoredAgentDraft } from "./stored-draft";

const manualDraft = (overrides: Partial<typeof EMPTY_AGENT_DRAFT> = {}) => ({
  owner: MANUAL_DRAFT_BLANK_OWNER,
  runtimeId: "runtime-1",
  draft: toStoredAgentDraft({ ...EMPTY_AGENT_DRAFT, ...overrides }, null),
});

describe("manual agent draft", () => {
  // A blank agent and a copy of agent X are different pieces of work, and a
  // copy of X is not a copy of Y. One slot would hand one flow's answers to
  // another.
  it("scopes a draft to what is being created", () => {
    expect(manualDraftOwner(null)).toBe(MANUAL_DRAFT_BLANK_OWNER);
    expect(manualDraftOwner("agent-1")).toBe("duplicate:agent-1");
    expect(manualDraftOwner("agent-1")).not.toBe(manualDraftOwner("agent-2"));
  });

  // The runtime is seeded automatically on every visit. Counting it as content
  // would make "you have unsaved work" true the moment the page opened.
  it("does not treat an auto-seeded runtime as work", () => {
    expect(hasMeaningfulManualDraft(manualDraft())).toBe(false);
  });

  it("treats anything the user actually entered as work", () => {
    expect(hasMeaningfulManualDraft(manualDraft({ name: "Release" }))).toBe(
      true,
    );
    expect(
      hasMeaningfulManualDraft(manualDraft({ instructions: "Be careful" })),
    ).toBe(true);
    expect(
      hasMeaningfulManualDraft(manualDraft({ skillIds: new Set(["skill-1"]) })),
    ).toBe(true);
    expect(hasMeaningfulManualDraft(manualDraft({ avatarUrl: "🚀" }))).toBe(
      true,
    );
  });
});
