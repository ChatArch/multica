import { describe, expect, it } from "vitest";
import {
  CHAT_VIRTUOSO_INITIAL_FIRST_ITEM_INDEX as BASE,
  nextChatFirstItemIndex,
  type ChatFirstItemIndexAnchor,
} from "./use-chat-first-item-index";

const sessionId = "session-1";

function rows(...ids: string[]) {
  return ids.map((id) => ({ id }));
}

/** Feed successive message lists through the reducer like React would. */
function run(lists: { id: string }[][], session: string | null = sessionId) {
  let anchor: ChatFirstItemIndexAnchor | null = null;
  return lists.map((messages) => {
    const next = nextChatFirstItemIndex(anchor, session, messages);
    anchor = next.anchor;
    return next.firstItemIndex;
  });
}

describe("nextChatFirstItemIndex", () => {
  it("starts at the base for the first non-empty render", () => {
    expect(run([rows("m1", "m2", "m3")])).toEqual([BASE]);
  });

  it("drops by exactly the number of rows prepended by a load-older page", () => {
    const [first, second] = run([
      rows("m11", "m12"),
      rows("m1", "m2", "m3", "m11", "m12"),
    ]);
    expect(first).toBe(BASE);
    expect(second).toBe(BASE - 3);
  });

  // Regression (MUL-5711): a turn appends two messages to the newest page, which
  // is a fixed-size window — two older messages spill into the next page and the
  // old "count everything but the newest page" derivation fell by 2 per turn,
  // telling react-virtuoso rows had been prepended when only appends happened.
  it("does not move when a turn appends and the page window slides", () => {
    const loaded = rows("m1", "m2", "m11", "m12");
    const afterTurn = rows("m1", "m2", "m11", "m12", "m13", "m14");
    expect(run([loaded, afterTurn])).toEqual([BASE, BASE]);
  });

  it("survives repeated turns without drifting", () => {
    const lists = [rows("m1", "m2")];
    for (let turn = 0; turn < 5; turn++) {
      const last = lists[lists.length - 1]!;
      lists.push([...last, { id: `u${turn}` }, { id: `a${turn}` }]);
    }
    expect(new Set(run(lists))).toEqual(new Set([BASE]));
  });

  // Regression (MUL-5711): the list stays mounted with zero messages while a
  // pending task holds it open. Re-basing to 0 there made the next render RAISE
  // the index by the whole base, which react-virtuoso reads as "drop that many
  // rows off the head".
  it("never raises the index when the list is momentarily empty", () => {
    const values = run([
      rows("m1", "m2", "m11"),
      rows("m1", "m2", "m11", "m12"),
      [],
      rows("m1", "m2", "m11", "m12", "m13"),
    ]);
    expect(values).toEqual([BASE, BASE, BASE, BASE]);
  });

  it("holds the last value when a session opens empty", () => {
    expect(run([[], rows("m1")])).toEqual([BASE, BASE]);
  });

  it("re-bases when the session changes", () => {
    let anchor: ChatFirstItemIndexAnchor | null = null;
    ({ anchor } = nextChatFirstItemIndex(anchor, sessionId, rows("m1", "m2")));
    ({ anchor } = nextChatFirstItemIndex(anchor, sessionId, rows("m0", "m1", "m2")));
    const other = nextChatFirstItemIndex(anchor, "session-2", rows("x1", "x2"));
    expect(other.firstItemIndex).toBe(BASE);
    expect(other.anchor).toMatchObject({ sessionId: "session-2", anchorId: "x1" });
  });

  it("re-anchors without moving when the anchored message is deleted", () => {
    const values = run([
      rows("m1", "m2", "m3"),
      rows("m0", "m1", "m2", "m3"), // prepended older page → BASE - 1
      rows("m2", "m3"), // m0/m1 gone (cancelled turn restored to the composer)
      rows("m2", "m3", "m4"),
    ]);
    expect(values).toEqual([BASE, BASE - 1, BASE - 1, BASE - 1]);
  });

  it("returns the base and no anchor without a session", () => {
    const result = nextChatFirstItemIndex(null, null, rows("m1"));
    expect(result).toEqual({ firstItemIndex: BASE, anchor: null });
  });

  it("keeps the anchor object stable when nothing moved", () => {
    const first = nextChatFirstItemIndex(null, sessionId, rows("m1", "m2"));
    const second = nextChatFirstItemIndex(first.anchor, sessionId, rows("m1", "m2", "m3"));
    expect(second.anchor).toBe(first.anchor);
  });
});
