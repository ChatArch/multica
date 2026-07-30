import { describe, expect, it } from "vitest";
import { InboxListSchema } from "./schemas";

/**
 * Wire-contract tests for GET /api/inbox.
 *
 * These exist because a compile-only check is not enough. MUL-5483 added the
 * `subtree_settled` inbox type and the mobile label map was updated so
 * `tsc` passed — but the server wrote a NUMBER into `details.child_count`, and
 * `details` is `z.record(z.string(), z.string())`. Since the endpoint parses an
 * ARRAY, one bad row fails the whole parse and `listInbox` falls back to
 * `EMPTY_INBOX_LIST` — the entire mobile inbox renders empty. Typechecking the
 * label map could never have caught that.
 *
 * The fixtures below mirror the shapes the Go notification path actually emits
 * (server/cmd/server/notification_listeners.go).
 */
describe("inbox wire contract", () => {
  it("parses a subtree_settled roll-up with the details the server sends", () => {
    const serverRow = {
      id: "inbox-1",
      workspace_id: "ws-1",
      recipient_type: "member",
      recipient_id: "user-1",
      type: "subtree_settled",
      severity: "info",
      issue_id: "issue-parent",
      title: "Refactor the subscription model",
      body: "Stage 1 is complete — 3 sub-issues finished.",
      actor_type: "agent",
      actor_id: "agent-1",
      read: false,
      archived: false,
      created_at: "2026-07-30T00:00:00Z",
      // Every value is a string. A number here drops the whole list.
      details: {
        parent_issue_id: "issue-parent",
        // barrier_scope is the stable "which barrier"; barrier_key adds a digest
        // of the children it covered so a membership change reopens it.
        barrier_scope: "1",
        barrier_key: "1:9f2c4ab71e05",
        child_count: "3",
        stage: "1",
      },
    };

    const parsed = InboxListSchema.safeParse([serverRow]);
    expect(parsed.success).toBe(true);
    expect(parsed.success && parsed.data[0]?.type).toBe("subtree_settled");
    expect(parsed.success && parsed.data[0]?.details?.child_count).toBe("3");
  });

  it("rejects a numeric details value, so a regression fails here and not in the UI", () => {
    const badRow = {
      id: "inbox-2",
      recipient_type: "member",
      type: "subtree_settled",
      details: { child_count: 3 },
    };

    expect(InboxListSchema.safeParse([badRow]).success).toBe(false);
  });

  it("keeps one malformed row from emptying the entire list observable", () => {
    // Documents the blast radius that made this a P1 rather than a cosmetic bug:
    // the schema is an array, so a single bad row invalidates every good one.
    const good = {
      id: "inbox-3",
      recipient_type: "member",
      type: "status_changed",
      details: { from: "todo", to: "in_review" },
    };
    const bad = { id: "inbox-4", recipient_type: "member", type: "subtree_settled", details: { child_count: 3 } };

    expect(InboxListSchema.safeParse([good]).success).toBe(true);
    expect(InboxListSchema.safeParse([good, bad]).success).toBe(false);
  });
});
