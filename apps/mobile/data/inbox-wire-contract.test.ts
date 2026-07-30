import { describe, expect, it } from "vitest";
import { InboxListSchema } from "./schemas";

/**
 * Wire-contract tests for GET /api/inbox.
 *
 * These exist because a compile-only check is not enough. During MUL-5483 a new
 * inbox type was added and the mobile label map was updated so `tsc` passed —
 * but the server wrote a NUMBER into `details.child_count`, and `details` is
 * `z.record(z.string(), z.string())`. Since the endpoint parses an ARRAY, one bad
 * row fails the whole parse and `listInbox` falls back to `EMPTY_INBOX_LIST`:
 * the entire mobile inbox renders empty, not just that row. Typechecking the
 * label map could never have caught it.
 *
 * The rule this pins is general and outlives that feature: every value the Go
 * notification path puts in `details` is a string
 * (server/cmd/server/notification_listeners.go).
 */
describe("inbox wire contract", () => {
  it("parses a row with the details the server actually sends", () => {
    const serverRow = {
      id: "inbox-1",
      workspace_id: "ws-1",
      recipient_type: "member",
      recipient_id: "user-1",
      type: "status_changed",
      severity: "info",
      issue_id: "issue-1",
      title: "P0: delegated subscription rule",
      body: "",
      actor_type: "agent",
      actor_id: "agent-1",
      read: false,
      archived: false,
      created_at: "2026-07-30T00:00:00Z",
      // Every value is a string. A number here drops the whole list.
      details: { from: "in_progress", to: "in_review" },
    };

    const parsed = InboxListSchema.safeParse([serverRow]);
    expect(parsed.success).toBe(true);
    expect(parsed.success && parsed.data[0]?.type).toBe("status_changed");
    expect(parsed.success && parsed.data[0]?.details?.to).toBe("in_review");
  });

  it("rejects a numeric details value, so a regression fails here and not in the UI", () => {
    const badRow = {
      id: "inbox-2",
      recipient_type: "member",
      type: "status_changed",
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
    const bad = {
      id: "inbox-4",
      recipient_type: "member",
      type: "status_changed",
      details: { child_count: 3 },
    };

    expect(InboxListSchema.safeParse([good]).success).toBe(true);
    expect(InboxListSchema.safeParse([good, bad]).success).toBe(false);
  });

  it("renders an unknown server type instead of dropping the row", () => {
    // Mirrors the root CLAUDE.md API-compatibility rule and mobile's own
    // "render every inbox type, never silently drop a category" parity rule: a
    // type this build has never heard of must still parse.
    const future = {
      id: "inbox-5",
      recipient_type: "member",
      type: "some_future_type",
      details: { anything: "still a string" },
    };

    const parsed = InboxListSchema.safeParse([future]);
    expect(parsed.success).toBe(true);
  });
});
