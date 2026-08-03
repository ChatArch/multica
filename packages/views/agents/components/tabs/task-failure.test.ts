import { describe, expect, it } from "vitest";

import { failureReasonLabel } from "./task-failure";

// This is the copy the Usage page's Errors breakdown drills into: a top
// offender is only actionable if the runs it links to actually say why they
// failed.
describe("failureReasonLabel", () => {
  it("labels the refined agent_error.* reasons the backend has written since MUL-1949", () => {
    // Regression: this used to be a Record indexed with a cast to the old
    // 6-value coarse enum, so every one of these resolved to `undefined` and
    // the row rendered no reason at all.
    expect(failureReasonLabel("agent_error.provider_auth_or_access")).toBe(
      "Provider auth failed",
    );
    expect(failureReasonLabel("agent_error.provider_capacity_or_rate_limit")).toBe(
      "Rate limited by provider",
    );
    expect(failureReasonLabel("agent_error.context_overflow")).toBe(
      "Context window exceeded",
    );
    expect(failureReasonLabel("queued_expired")).toBe("Expired in queue");
  });

  it("still labels the pre-MUL-1949 coarse reasons on historical rows", () => {
    expect(failureReasonLabel("agent_error")).toBe("Agent execution error");
    expect(failureReasonLabel("runtime_offline")).toBe("Daemon offline");
    expect(failureReasonLabel("manual")).toBe("Cancelled by user");
  });

  it("falls back to the raw wire value for a reason this build predates", () => {
    // Truthful and searchable beats blank — the operator can paste it into a
    // log search.
    expect(failureReasonLabel("agent_error.from_a_newer_backend")).toBe(
      "agent_error.from_a_newer_backend",
    );
  });

  it("returns null when there is no reason to show", () => {
    expect(failureReasonLabel("")).toBeNull();
    expect(failureReasonLabel(null)).toBeNull();
    expect(failureReasonLabel(undefined)).toBeNull();
  });
});

// The agent activity list and the issue execution log surface failure_reason
// directly, so an unmapped reason renders the raw machine value
// `mcp_config_daemon_outdated` and the operator cannot tell what to do
// (GitHub #6283).
describe("failureReasonLabel for the MCP daemon-upgrade refusal", () => {
  it("names the operator action instead of leaking the wire value", () => {
    const label = failureReasonLabel("mcp_config_daemon_outdated");
    expect(label).not.toBe("mcp_config_daemon_outdated");
    expect(label).toBe("Daemon too old for this agent's MCP limits — upgrade it");
    // The point of the label is that it says what to do.
    expect(label).toMatch(/upgrade/i);
  });
});
