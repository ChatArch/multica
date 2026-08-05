import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { I18nProvider } from "@multica/core/i18n/react";
import { describe, expect, it, vi } from "vitest";
import enChat from "../../locales/en/chat.json";
import { ChatQueue } from "./chat-queue";

const TEST_RESOURCES = { en: { chat: enChat } };

function renderQueue(headStatus = "running") {
  const callbacks = {
    onSendNow: vi.fn<(taskId: string) => Promise<void>>().mockResolvedValue(),
    onEdit: vi.fn<(taskId: string) => Promise<void>>().mockResolvedValue(),
    onRemove: vi.fn<(taskId: string) => Promise<void>>().mockResolvedValue(),
    onClear: vi.fn<() => Promise<void>>().mockResolvedValue(),
  };
  const view = render(
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      <ChatQueue
        headStatus={headStatus}
        tasks={[
          {
            task_id: "task-2",
            status: "queued",
            content: "First follow-up",
            created_at: "2026-07-01T00:01:00Z",
          },
          {
            task_id: "task-3",
            status: "queued",
            content: "",
            created_at: "2026-07-01T00:02:00Z",
          },
        ]}
        {...callbacks}
      />
    </I18nProvider>,
  );
  return { ...callbacks, container: view.container };
}

describe("ChatQueue", () => {
  it("renders queued messages in order and falls back for empty content", () => {
    renderQueue();

    expect(screen.getByText("2 queued messages")).toBeInTheDocument();
    expect(screen.getByText("First follow-up")).toBeInTheDocument();
    expect(screen.getByText("Queued message")).toBeInTheDocument();
    expect(screen.getAllByLabelText("Send now")).toHaveLength(2);
    expect(screen.getAllByLabelText("Edit queued message")).toHaveLength(2);
    expect(screen.getAllByLabelText("Remove queued message")).toHaveLength(2);
    expect(screen.getByRole("button", { name: "Clear all" })).toBeInTheDocument();
  });

  it("runs send-now, edit, remove, and clear against the selected queue state", async () => {
    const actions = renderQueue();

    fireEvent.click(screen.getAllByLabelText("Send now")[1]!);
    await waitFor(() => expect(actions.onSendNow).toHaveBeenCalledWith("task-3"));
    fireEvent.click(screen.getAllByLabelText("Edit queued message")[0]!);
    await waitFor(() => expect(actions.onEdit).toHaveBeenCalledWith("task-2"));
    fireEvent.click(screen.getAllByLabelText("Remove queued message")[1]!);
    await waitFor(() => expect(actions.onRemove).toHaveBeenCalledWith("task-3"));
    fireEvent.click(screen.getByRole("button", { name: "Clear all" }));
    await waitFor(() => expect(actions.onClear).toHaveBeenCalledTimes(1));
  });

  it("disables send-now until the current positional head is claimable", () => {
    const actions = renderQueue("queued");

    const buttons = screen.getAllByRole("button", {
      name: "Send now is available after the current reply starts",
    });
    expect(buttons).toHaveLength(2);
    for (const button of buttons) expect(button).toBeDisabled();
    expect(actions.onSendNow).not.toHaveBeenCalled();
  });

  it("keeps long queues bounded and blocks duplicate actions while one is pending", async () => {
    let finishClear: (() => void) | undefined;
    const actions = renderQueue();
    actions.onClear.mockReturnValue(new Promise<void>((resolve) => {
      finishClear = resolve;
    }));

    const scroller = actions.container.querySelector(".overflow-y-auto");
    expect(scroller).toHaveClass("max-h-40");

    fireEvent.click(screen.getByRole("button", { name: "Clear all" }));
    await waitFor(() => {
      for (const button of screen.getAllByRole("button")) {
        expect(button).toBeDisabled();
      }
    });

    finishClear?.();
    await waitFor(() => {
      for (const button of screen.getAllByRole("button")) {
        expect(button).toBeEnabled();
      }
    });
    expect(actions.onClear).toHaveBeenCalledTimes(1);
  });
});
