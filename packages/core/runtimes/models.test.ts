import { beforeEach, describe, expect, it, vi } from "vitest";
import { resolveRuntimeModels, runtimeModelsOptions } from "./models";
import type { RuntimeModelListRequest } from "../types/agent";

const initiateListModels = vi.fn();
const getListModelsResult = vi.fn();

vi.mock("../api", () => ({
  api: {
    initiateListModels: (runtimeId: string) => initiateListModels(runtimeId),
    getListModelsResult: (runtimeId: string, requestId: string) =>
      getListModelsResult(runtimeId, requestId),
  },
}));

const catalog = [{ id: "claude-sonnet-4-6", label: "Claude Sonnet 4.6" }];

function request(
  overrides: Partial<RuntimeModelListRequest>,
): RuntimeModelListRequest {
  return {
    id: "req-1",
    runtime_id: "rt-1",
    status: "pending",
    supported: true,
    created_at: "2026-07-29T00:00:00Z",
    updated_at: "2026-07-29T00:00:00Z",
    ...overrides,
  };
}

beforeEach(() => {
  initiateListModels.mockReset();
  getListModelsResult.mockReset();
});

describe("resolveRuntimeModels", () => {
  // The server answers a warm runtime straight from its catalog cache
  // (MUL-5444). That response is already terminal, so discovery must resolve on
  // the POST alone — one round trip, no polling, no spinner.
  it("resolves from a cached completed response without polling", async () => {
    initiateListModels.mockResolvedValue(
      request({
        status: "completed",
        models: catalog,
        cached: true,
        cached_at: "2026-07-29T00:00:00Z",
      }),
    );

    const result = await resolveRuntimeModels("rt-1");

    expect(result).toEqual({ models: catalog, supported: true });
    expect(getListModelsResult).not.toHaveBeenCalled();
  });

  it("still polls a pending response until the daemon reports back", async () => {
    initiateListModels.mockResolvedValue(request({ status: "pending" }));
    getListModelsResult
      .mockResolvedValueOnce(request({ status: "running" }))
      .mockResolvedValueOnce(request({ status: "completed", models: catalog }));

    const result = await resolveRuntimeModels("rt-1");

    expect(result).toEqual({ models: catalog, supported: true });
    expect(getListModelsResult).toHaveBeenCalledTimes(2);
    expect(getListModelsResult).toHaveBeenLastCalledWith("rt-1", "req-1");
  });

  it("surfaces a failed discovery as an error", async () => {
    initiateListModels.mockResolvedValue(
      request({ status: "failed", error: "claude not installed" }),
    );

    await expect(resolveRuntimeModels("rt-1")).rejects.toThrow(
      "claude not installed",
    );
  });
});

describe("runtimeModelsOptions", () => {
  // Discovery is a round trip to the user's machine, so a revisited runtime
  // must render from cache instead of re-running it. Guards against a silent
  // regression back to the old 60s window with no gcTime.
  it("caches long enough to survive switching runtimes back and forth", () => {
    const options = runtimeModelsOptions("rt-1");
    expect(options.staleTime).toBeGreaterThanOrEqual(5 * 60_000);
    expect(options.gcTime).toBeGreaterThanOrEqual(options.staleTime as number);
    expect(options.queryKey).toEqual(["runtimes", "models", "rt-1"]);
    expect(options.enabled).toBe(true);
  });

  it("stays disabled without a runtime", () => {
    expect(runtimeModelsOptions(null).enabled).toBe(false);
  });
});
