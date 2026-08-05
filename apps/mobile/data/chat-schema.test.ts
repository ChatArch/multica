import { describe, expect, it } from "vitest";
import { ChatPendingTaskSchema } from "./schemas";

describe("ChatPendingTaskSchema", () => {
  it("keeps the pending head and valid queue rows when one row is malformed", () => {
    const parsed = ChatPendingTaskSchema.parse({
      task_id: "task-active",
      status: "running",
      queued_tasks: [
        {
          task_id: "task-next",
          status: "queued",
          created_at: "2026-08-03T00:00:00Z",
        },
        { status: "queued" },
      ],
    });

    expect(parsed.task_id).toBe("task-active");
    expect(parsed.queued_tasks).toEqual([
      {
        task_id: "task-next",
        status: "queued",
        created_at: "2026-08-03T00:00:00Z",
      },
    ]);
  });
});
