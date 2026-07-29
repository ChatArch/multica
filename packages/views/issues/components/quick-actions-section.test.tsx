import { describe, expect, it, vi, beforeEach } from "vitest";
import { fireEvent, screen, waitFor } from "@testing-library/react";
import type { QuickAction } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";
import { QuickActionsSection } from "./quick-actions-section";

// The section's contract (MUL-5465) is mostly about being HONEST with the
// user, so these tests target exactly that: which actions are offered, and
// what the toast claims happened afterwards. The `coalesced` case is the one
// worth locking — the DB allows only one pending task per (issue, agent), so a
// click can legitimately start NO new run, and reporting that as success is
// the bug this feature is most likely to ship with.

const runMock = vi.fn();
let listData: { quick_actions: QuickAction[]; sidebar_limit: number } | undefined;

vi.mock("@tanstack/react-query", () => ({
  useQuery: () => ({ data: listData, isLoading: false }),
}));

vi.mock("@multica/core/paths", () => ({
  useCurrentWorkspace: () => ({ id: "ws-1" }),
}));

vi.mock("@multica/core/quick-actions", () => ({
  runnableQuickActionsOptions: () => ({ queryKey: ["quick-actions"], queryFn: vi.fn() }),
  useRunQuickAction: () => ({ mutateAsync: runMock, isPending: false }),
}));

vi.mock("../../common/actor-avatar", () => ({
  ActorAvatar: () => <span data-testid="actor-avatar" />,
}));

const toastCalls: { kind: string; message: string }[] = [];
vi.mock("sonner", () => ({
  toast: {
    success: (m: string) => toastCalls.push({ kind: "success", message: m }),
    info: (m: string) => toastCalls.push({ kind: "info", message: m }),
    error: (m: string) => toastCalls.push({ kind: "error", message: m }),
  },
}));

function action(overrides: Partial<QuickAction> = {}): QuickAction {
  return {
    id: "qa-1",
    workspace_id: "ws-1",
    name: "Code Review",
    description: "",
    assignee_type: "agent",
    assignee_id: "agent-1",
    prompt: "review it",
    input_enabled: false,
    input_label: "",
    input_placeholder: "",
    input_required: false,
    position: 1,
    status: "active",
    last_used_at: null,
    use_count: 0,
    created_at: "",
    updated_at: "",
    visibility: "workspace",
    can_run: true,
    target_name: "Lambda",
    ...overrides,
  };
}

function outcome(status: string) {
  return {
    id: "c-1",
    trigger_outcomes: [
      { target_type: "agent", target_id: "agent-1", status, reason_code: status },
    ],
  };
}

describe("QuickActionsSection", () => {
  beforeEach(() => {
    runMock.mockReset();
    toastCalls.length = 0;
    listData = { quick_actions: [action()], sidebar_limit: 5 };
  });

  it("renders nothing when the workspace has no runnable actions", () => {
    listData = { quick_actions: [], sidebar_limit: 5 };
    const { container } = renderWithI18n(<QuickActionsSection issueId="i-1" />);
    expect(container).toBeEmptyDOMElement();
  });

  it("hides archived actions even if the server returned them", () => {
    listData = { quick_actions: [action({ status: "archived" })], sidebar_limit: 5 };
    const { container } = renderWithI18n(<QuickActionsSection issueId="i-1" />);
    expect(container).toBeEmptyDOMElement();
  });

  it("runs immediately on click when the action takes no input", async () => {
    runMock.mockResolvedValue(outcome("queued"));
    renderWithI18n(<QuickActionsSection issueId="i-1" />);

    fireEvent.click(screen.getByText("Code Review"));

    await waitFor(() => expect(runMock).toHaveBeenCalledWith({ quickActionId: "qa-1", input: undefined }));
    await waitFor(() => expect(toastCalls[0]?.kind).toBe("success"));
    expect(toastCalls[0]?.message).toContain("Lambda");
  });

  it("reports a coalesced run as queued-behind, never as a fresh start", async () => {
    // One pending task per (issue, agent) means the second click adds to the
    // existing run. Claiming "Lambda started working" here would be a lie the
    // user has to debug later.
    runMock.mockResolvedValue(outcome("coalesced"));
    renderWithI18n(<QuickActionsSection issueId="i-1" />);

    fireEvent.click(screen.getByText("Code Review"));

    await waitFor(() => expect(toastCalls).toHaveLength(1));
    expect(toastCalls[0]?.kind).toBe("info");
    expect(toastCalls[0]?.message.toLowerCase()).toContain("added to");
  });

  it("reports an offline target as deferred rather than started", async () => {
    runMock.mockResolvedValue(outcome("deferred"));
    renderWithI18n(<QuickActionsSection issueId="i-1" />);

    fireEvent.click(screen.getByText("Code Review"));

    await waitFor(() => expect(toastCalls).toHaveLength(1));
    expect(toastCalls[0]?.kind).toBe("info");
    expect(toastCalls[0]?.message.toLowerCase()).toContain("offline");
  });

  it("does not claim success for an unknown server status", async () => {
    // Server-driven enum: a value this build has never seen must not fall
    // through to the success branch.
    runMock.mockResolvedValue(outcome("some_future_status"));
    renderWithI18n(<QuickActionsSection issueId="i-1" />);

    fireEvent.click(screen.getByText("Code Review"));

    await waitFor(() => expect(toastCalls).toHaveLength(1));
    expect(toastCalls[0]?.kind).not.toBe("success");
  });

  it("opens an input field instead of running when the action asks for input", () => {
    listData = {
      quick_actions: [
        action({ input_enabled: true, input_label: "What should it focus on?" }),
      ],
      sidebar_limit: 5,
    };
    renderWithI18n(<QuickActionsSection issueId="i-1" />);

    fireEvent.click(screen.getByText("Code Review"));

    expect(screen.getByText("What should it focus on?")).toBeInTheDocument();
    expect(runMock).not.toHaveBeenCalled();
  });

  it("collapses everything past the server's sidebar limit behind More", () => {
    listData = {
      quick_actions: [
        action({ id: "a", name: "One" }),
        action({ id: "b", name: "Two" }),
        action({ id: "c", name: "Three" }),
      ],
      sidebar_limit: 2,
    };
    renderWithI18n(<QuickActionsSection issueId="i-1" />);

    expect(screen.getByText("One")).toBeInTheDocument();
    expect(screen.getByText("Two")).toBeInTheDocument();
    expect(screen.queryByText("Three")).not.toBeInTheDocument();

    fireEvent.click(screen.getByText(/Show 1 more/i));
    expect(screen.getByText("Three")).toBeInTheDocument();
  });
});
