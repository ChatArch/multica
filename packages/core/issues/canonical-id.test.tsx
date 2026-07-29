/**
 * @vitest-environment jsdom
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { setApiInstance } from "../api";
import type { ApiClient } from "../api/client";
import type { Issue } from "../types";
import { issueKeys } from "./queries";
import { isIssueUuid, useCanonicalIssueId } from "./canonical-id";

const ISSUE_UUID = "cb240efb-154c-42a8-ae92-42b02676feca";

const issue = {
  id: ISSUE_UUID,
  workspace_id: "ws-1",
  number: 134,
  identifier: "TRS-134",
  title: "Example",
  status: "todo",
  priority: "medium",
} as Issue;

function createWrapper(qc: QueryClient) {
  return function Wrapper({ children }: { children: ReactNode }) {
    return <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
  };
}

describe("isIssueUuid", () => {
  it("distinguishes a UUID from an issue identifier", () => {
    expect(isIssueUuid(ISSUE_UUID)).toBe(true);
    expect(isIssueUuid(ISSUE_UUID.toUpperCase())).toBe(true);
    expect(isIssueUuid("TRS-134")).toBe(false);
    expect(isIssueUuid("trs-134")).toBe(false);
    expect(isIssueUuid("")).toBe(false);
  });
});

describe("useCanonicalIssueId", () => {
  let qc: QueryClient;
  let getIssue: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    getIssue = vi.fn(async () => issue);
    setApiInstance({ getIssue } as unknown as ApiClient);
  });

  afterEach(() => {
    qc.clear();
    vi.restoreAllMocks();
  });

  it("passes a UUID through without a request", () => {
    const { result } = renderHook(() => useCanonicalIssueId("ws-1", ISSUE_UUID), {
      wrapper: createWrapper(qc),
    });

    expect(result.current).toEqual({ canonicalId: ISSUE_UUID, isResolving: false });
    expect(getIssue).not.toHaveBeenCalled();
  });

  it("resolves an identifier to the issue UUID", async () => {
    const { result } = renderHook(() => useCanonicalIssueId("ws-1", "TRS-134"), {
      wrapper: createWrapper(qc),
    });

    expect(result.current.isResolving).toBe(true);
    await waitFor(() => expect(result.current.canonicalId).toBe(ISSUE_UUID));
    expect(result.current.isResolving).toBe(false);
    expect(getIssue).toHaveBeenCalledWith("TRS-134");
  });

  // The realtime updaters patch `issueKeys.detail(wsId, issue.id)` with the
  // UUID from the websocket payload. Seeding that entry is what lets the detail
  // view render from a UUID key — and what keeps an identifier URL from costing
  // a second round trip for a row we already hold.
  it("seeds the UUID-keyed detail entry with the resolved row", async () => {
    renderHook(() => useCanonicalIssueId("ws-1", "TRS-134"), {
      wrapper: createWrapper(qc),
    });

    await waitFor(() =>
      expect(qc.getQueryData(issueKeys.detail("ws-1", ISSUE_UUID))).toEqual(issue),
    );
    expect(getIssue).toHaveBeenCalledTimes(1);
  });

  it("does not clobber a realtime-patched entry with the resolution response", async () => {
    const patched = { ...issue, status: "done" } as Issue;
    qc.setQueryData(issueKeys.detail("ws-1", ISSUE_UUID), patched);

    renderHook(() => useCanonicalIssueId("ws-1", "TRS-134"), {
      wrapper: createWrapper(qc),
    });

    await waitFor(() => expect(getIssue).toHaveBeenCalled());
    expect(qc.getQueryData(issueKeys.detail("ws-1", ISSUE_UUID))).toEqual(patched);
  });

  it("stops resolving when the identifier does not exist", async () => {
    getIssue.mockRejectedValue(new Error("issue not found"));

    const { result } = renderHook(() => useCanonicalIssueId("ws-1", "ZZZ-134"), {
      wrapper: createWrapper(qc),
    });

    await waitFor(() => expect(result.current.isResolving).toBe(false));
    expect(result.current.canonicalId).toBeNull();
  });

  it("stays idle without a workspace id", () => {
    const { result } = renderHook(() => useCanonicalIssueId("", "TRS-134"), {
      wrapper: createWrapper(qc),
    });

    expect(result.current).toEqual({ canonicalId: null, isResolving: false });
    expect(getIssue).not.toHaveBeenCalled();
  });
});
