// @vitest-environment jsdom

import { useState } from "react";
import { act, cleanup, fireEvent, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { TimelineEntry } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";
import {
  ThreadNavPanel,
  matchesFilter,
  mentionsUser,
  threadDayGroup,
  type ThreadNavThread,
} from "./thread-nav-panel";

type OpenChange = (open: boolean, details: { reason: string }) => void;

const mockState = vi.hoisted(() => ({
  open: false,
  triggerProps: undefined as Record<string, unknown> | undefined,
  onOpenChange: undefined as ((open: boolean, details: { reason: string }) => void) | undefined,
}));

// ActorAvatar resolves workspace-scoped profile paths, which needs a route
// provider this component test has no reason to stand up. The row's identity
// rendering is not what is under test here.
vi.mock("../../common/actor-avatar", () => ({
  ActorAvatar: ({ actorId }: { actorId: string }) => (
    <span data-testid={`avatar-${actorId}`} />
  ),
}));

vi.mock("@multica/core/workspace/hooks", () => ({
  useActorName: () => ({
    getActorName: (_type: string, id: string) =>
      ({ "user-1": "Jiayuan", "agent-1": "Lambda" })[id] ?? "Unknown",
    getActorInitials: () => "XX",
    getActorAvatarUrl: () => null,
  }),
}));

// Base UI owns hover timing and focus trapping; those are its tests, not ours.
// The mock keeps the parts this component actually drives — the controlled
// `open` prop, the reason-carrying `onOpenChange`, and the content's keyboard
// and focus handlers — so the open/pin state machine is what gets exercised.
vi.mock("@multica/ui/components/ui/popover", async () => {
  const React = await vi.importActual<typeof import("react")>("react");
  return {
    Popover: ({
      children,
      open,
      onOpenChange,
    }: {
      children: React.ReactNode;
      open: boolean;
      onOpenChange: OpenChange;
    }) => {
      mockState.open = open;
      mockState.onOpenChange = onOpenChange;
      return <div data-testid="popover">{children}</div>;
    },
    // Base UI's trigger toggles on press and reports the reason; reproduce
    // exactly that so the component's own press handling is under test.
    PopoverTrigger: ({
      render,
      children,
      ...props
    }: {
      render: React.ReactElement;
      children: React.ReactNode;
    } & Record<string, unknown>) => {
      mockState.triggerProps = props;
      return React.cloneElement(
        render as React.ReactElement<Record<string, unknown>>,
        {
          onClick: () =>
            mockState.onOpenChange?.(!mockState.open, { reason: "trigger-press" }),
        },
        children,
      );
    },
    PopoverContent: ({
      children,
      onKeyDown,
      onFocusCapture,
    }: {
      children: React.ReactNode;
      onKeyDown?: React.KeyboardEventHandler;
      onFocusCapture?: React.FocusEventHandler;
    }) =>
      mockState.open ? (
        <div data-testid="panel" onKeyDown={onKeyDown} onFocusCapture={onFocusCapture}>
          {children}
        </div>
      ) : null,
  };
});

function comment(
  id: string,
  content: string,
  overrides: Partial<TimelineEntry> = {},
): TimelineEntry {
  return {
    type: "comment",
    id,
    actor_type: "member",
    actor_id: "user-1",
    created_at: new Date().toISOString(),
    content,
    ...overrides,
  };
}

function thread(
  id: string,
  content: string,
  overrides: Partial<ThreadNavThread> = {},
): ThreadNavThread {
  return {
    id,
    entry: comment(id, content),
    resolved: false,
    replyCount: 0,
    involvesMe: false,
    ...overrides,
  };
}

const THREADS: ThreadNavThread[] = [
  thread("t1", "Invite links require a workspace first"),
  thread("t2", "Move the required check into the service layer", { replyCount: 3 }),
  thread("t3", "Expiry: 7 days or 14 days?", { resolved: true }),
  thread("t4", "Mail template CTA collapses in Outlook", { involvesMe: true }),
];

/** Mirrors how issue-detail owns the panel's open/pinned pair. */
function Harness({
  threads = THREADS,
  onJump = vi.fn(),
  onHoverThread = vi.fn(),
}: {
  threads?: ThreadNavThread[];
  onJump?: (id: string) => void;
  onHoverThread?: (id: string | null) => void;
}) {
  const [state, setState] = useState({ open: false, pinned: false });
  return (
    <ThreadNavPanel
      threads={threads}
      scrollContainerEl={null}
      onJump={onJump}
      onHoverThread={onHoverThread}
      open={state.open}
      pinned={state.pinned}
      onOpenChange={(open, pinned) => setState({ open, pinned })}
    />
  );
}

/** Drive the state machine the way Base UI would, with a reason attached. */
function emit(open: boolean, reason: string) {
  act(() => {
    mockState.onOpenChange?.(open, { reason });
  });
}

beforeEach(() => {
  mockState.open = false;
  mockState.triggerProps = undefined;
  mockState.onOpenChange = undefined;
});

afterEach(cleanup);

describe("threadDayGroup", () => {
  const now = new Date("2026-08-05T12:00:00").getTime();

  it("buckets by local calendar day", () => {
    expect(threadDayGroup(new Date("2026-08-05T00:30:00").toISOString(), now)).toBe("today");
    expect(threadDayGroup(new Date("2026-08-04T23:30:00").toISOString(), now)).toBe("yesterday");
    expect(threadDayGroup(new Date("2026-08-03T23:30:00").toISOString(), now)).toBe("earlier");
  });

  it("treats an unparseable timestamp as earlier rather than throwing", () => {
    expect(threadDayGroup("not-a-date", now)).toBe("earlier");
  });
});

describe("matchesFilter", () => {
  it("splits on resolution and participation", () => {
    const open = thread("a", "x");
    const done = thread("b", "x", { resolved: true });
    const mine = thread("c", "x", { involvesMe: true });

    expect(matchesFilter(open, "all")).toBe(true);
    expect(matchesFilter(open, "unresolved")).toBe(true);
    expect(matchesFilter(done, "unresolved")).toBe(false);
    expect(matchesFilter(done, "resolved")).toBe(true);
    expect(matchesFilter(mine, "mine")).toBe(true);
    expect(matchesFilter(open, "mine")).toBe(false);
  });

  it("keeps every thread for an unknown filter instead of emptying the list", () => {
    expect(matchesFilter(thread("a", "x"), "sideways" as never)).toBe(true);
  });
});

describe("mentionsUser", () => {
  it("matches the markdown mention link form", () => {
    expect(mentionsUser("hey [@Jiayuan](mention://member/user-1) look", "user-1")).toBe(true);
  });

  it("does not match another member or an agent mention", () => {
    expect(mentionsUser("[@Wei](mention://member/user-2)", "user-1")).toBe(false);
    expect(mentionsUser("[@Lambda](mention://agent/user-1)", "user-1")).toBe(false);
  });

  it("is false for empty content or a missing user id", () => {
    expect(mentionsUser(undefined, "user-1")).toBe(false);
    expect(mentionsUser("[@x](mention://member/)", "")).toBe(false);
  });
});

describe("ThreadNavPanel", () => {
  it("renders nothing when the issue has no threads", () => {
    renderWithI18n(<Harness threads={[]} />);
    expect(screen.queryByRole("button")).toBeNull();
  });

  it("shows the thread count on the trigger", () => {
    renderWithI18n(<Harness />);
    expect(screen.getByRole("button", { name: /Comment threads \(4\)/ })).toBeTruthy();
  });

  it("wires the trigger to open on hover, not only on press", () => {
    renderWithI18n(<Harness />);
    expect(mockState.triggerProps?.openOnHover).toBe(true);
    expect(mockState.triggerProps?.delay).toBeGreaterThan(0);
    expect(mockState.triggerProps?.closeDelay).toBeGreaterThan(0);
  });

  it("lists every thread once open", () => {
    renderWithI18n(<Harness />);
    fireEvent.click(screen.getByRole("button", { name: /Comment threads/ }));
    expect(screen.getByText("Invite links require a workspace first")).toBeTruthy();
    expect(screen.getByText("Expiry: 7 days or 14 days?")).toBeTruthy();
  });

  it("focuses search when opened by a press", async () => {
    renderWithI18n(<Harness />);
    fireEvent.click(screen.getByRole("button", { name: /Comment threads/ }));
    const search = screen.getByPlaceholderText("Search threads or authors");
    await waitFor(() => expect(document.activeElement).toBe(search));
  });

  // The open model is the part of this design that differs from the original
  // "hover shows the list" request, so it carries the most test weight.
  describe("hover previews, press pins", () => {
    it("does not take focus when opened by hover", async () => {
      renderWithI18n(<Harness />);
      const before = document.activeElement;
      emit(true, "trigger-hover");
      expect(screen.getByTestId("panel")).toBeTruthy();
      // A hover preview over a half-written comment must leave the caret alone.
      await waitFor(() => {
        expect(document.activeElement).toBe(before);
      });
    });

    it("closes again when the pointer leaves an unpinned preview", () => {
      renderWithI18n(<Harness />);
      emit(true, "trigger-hover");
      emit(false, "trigger-hover");
      expect(screen.queryByTestId("panel")).toBeNull();
    });

    it("pins instead of closing when the trigger is pressed over a preview", async () => {
      renderWithI18n(<Harness />);
      emit(true, "trigger-hover");
      // Base UI reports a press on an open popover as a close request; over an
      // unpinned preview that means "keep this", not "dismiss".
      emit(false, "trigger-press");
      expect(screen.getByTestId("panel")).toBeTruthy();
      await waitFor(() =>
        expect(document.activeElement).toBe(
          screen.getByPlaceholderText("Search threads or authors"),
        ),
      );
    });

    it("survives the pointer leaving once pinned", () => {
      renderWithI18n(<Harness />);
      fireEvent.click(screen.getByRole("button", { name: /Comment threads/ }));
      emit(false, "trigger-hover");
      expect(screen.getByTestId("panel")).toBeTruthy();
    });

    it("closes a pinned panel on a second press, escape, or outside press", () => {
      const { unmount } = renderWithI18n(<Harness />);
      fireEvent.click(screen.getByRole("button", { name: /Comment threads/ }));
      fireEvent.click(screen.getByRole("button", { name: /Comment threads/ }));
      expect(screen.queryByTestId("panel")).toBeNull();
      unmount();

      for (const reason of ["escape-key", "outside-press"]) {
        renderWithI18n(<Harness />);
        fireEvent.click(screen.getByRole("button", { name: /Comment threads/ }));
        emit(false, reason);
        expect(screen.queryByTestId("panel")).toBeNull();
        cleanup();
      }
    });

    it("upgrades a preview to pinned when focus lands inside it", async () => {
      renderWithI18n(<Harness />);
      emit(true, "trigger-hover");
      fireEvent.focus(screen.getByTestId("panel"));
      // Now pinned: a pointer-out no longer closes it.
      emit(false, "trigger-hover");
      expect(screen.getByTestId("panel")).toBeTruthy();
      await waitFor(() =>
        expect(document.activeElement).toBe(
          screen.getByPlaceholderText("Search threads or authors"),
        ),
      );
    });
  });

  it("filters rows by the search query and reports the match count", () => {
    renderWithI18n(<Harness />);
    fireEvent.click(screen.getByRole("button", { name: /Comment threads/ }));
    fireEvent.change(screen.getByPlaceholderText("Search threads or authors"), {
      target: { value: "outlook" },
    });
    expect(screen.getByText("Mail template CTA collapses in Outlook")).toBeTruthy();
    expect(screen.queryByText("Invite links require a workspace first")).toBeNull();
    expect(screen.getByText("1 match")).toBeTruthy();
  });

  it("matches on the author name as well as the content", () => {
    renderWithI18n(<Harness />);
    fireEvent.click(screen.getByRole("button", { name: /Comment threads/ }));
    fireEvent.change(screen.getByPlaceholderText("Search threads or authors"), {
      target: { value: "jiayuan" },
    });
    expect(screen.getByText("4 matches")).toBeTruthy();
  });

  it("narrows to unresolved threads", () => {
    renderWithI18n(<Harness />);
    fireEvent.click(screen.getByRole("button", { name: /Comment threads/ }));
    fireEvent.click(screen.getByRole("button", { name: /^Unresolved/ }));
    expect(screen.queryByText("Expiry: 7 days or 14 days?")).toBeNull();
    expect(screen.getByText("Invite links require a workspace first")).toBeTruthy();
  });

  it("narrows to threads the reader is involved in", () => {
    renderWithI18n(<Harness />);
    fireEvent.click(screen.getByRole("button", { name: /Comment threads/ }));
    fireEvent.click(screen.getByRole("button", { name: /^@me/ }));
    expect(screen.getByText("Mail template CTA collapses in Outlook")).toBeTruthy();
    expect(screen.queryByText("Invite links require a workspace first")).toBeNull();
  });

  it("shows an empty state rather than a blank list when nothing matches", () => {
    renderWithI18n(<Harness />);
    fireEvent.click(screen.getByRole("button", { name: /Comment threads/ }));
    fireEvent.change(screen.getByPlaceholderText("Search threads or authors"), {
      target: { value: "zzzz" },
    });
    expect(screen.getByText("No threads match")).toBeTruthy();
  });

  it("jumps to the clicked thread and closes", () => {
    const onJump = vi.fn();
    renderWithI18n(<Harness onJump={onJump} />);
    fireEvent.click(screen.getByRole("button", { name: /Comment threads/ }));
    fireEvent.click(screen.getByText("Expiry: 7 days or 14 days?"));
    expect(onJump).toHaveBeenCalledWith("t3");
    expect(screen.queryByTestId("panel")).toBeNull();
  });

  it("moves the cursor with arrow keys and jumps on Enter", () => {
    const onJump = vi.fn();
    renderWithI18n(<Harness onJump={onJump} />);
    fireEvent.click(screen.getByRole("button", { name: /Comment threads/ }));
    const panel = screen.getByTestId("panel");
    fireEvent.keyDown(panel, { key: "ArrowDown" });
    fireEvent.keyDown(panel, { key: "Enter" });
    expect(onJump).toHaveBeenCalledWith("t2");
  });

  it("wraps the cursor at the top of the list", () => {
    const onJump = vi.fn();
    renderWithI18n(<Harness onJump={onJump} />);
    fireEvent.click(screen.getByRole("button", { name: /Comment threads/ }));
    const panel = screen.getByTestId("panel");
    fireEvent.keyDown(panel, { key: "ArrowUp" });
    fireEvent.keyDown(panel, { key: "Enter" });
    expect(onJump).toHaveBeenCalledWith("t4");
  });

  it("reports the hovered row so the rail can light the matching tick", () => {
    const onHoverThread = vi.fn();
    renderWithI18n(<Harness onHoverThread={onHoverThread} />);
    fireEvent.click(screen.getByRole("button", { name: /Comment threads/ }));
    const row = screen.getByText("Expiry: 7 days or 14 days?").closest("button")!;
    fireEvent.pointerEnter(row);
    expect(onHoverThread).toHaveBeenCalledWith("t3");
    fireEvent.pointerLeave(row);
    expect(onHoverThread).toHaveBeenCalledWith(null);
  });

  it("resets the query and filter between openings", () => {
    renderWithI18n(<Harness />);
    const trigger = screen.getByRole("button", { name: /Comment threads/ });
    fireEvent.click(trigger);
    fireEvent.change(screen.getByPlaceholderText("Search threads or authors"), {
      target: { value: "outlook" },
    });
    expect(screen.queryByText("Invite links require a workspace first")).toBeNull();
    fireEvent.click(trigger);
    fireEvent.click(trigger);
    expect(screen.getByText("Invite links require a workspace first")).toBeTruthy();
  });
});
